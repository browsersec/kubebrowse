package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"sync"
	"time"

	"github.com/browsersec/KubeBrowse/internal/httpx"
	redis2 "github.com/browsersec/KubeBrowse/internal/redis"
	"github.com/go-redis/redis/v8"
	"github.com/minio/minio-go/v7"
	"github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
)

const maxUploadBytes = int64(100 << 20) // 100 MiB

// UploadResult represents the result of each upload operation
type UploadResult struct {
	Service string      `json:"service"`
	Success bool        `json:"success"`
	Error   string      `json:"error,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}

// MultiUploadResponse represents the aggregated response
type MultiUploadResponse struct {
	Success bool           `json:"success"`
	Results []UploadResult `json:"results"`
	Message string         `json:"message"`
}

// FileBuffer holds file data for concurrent uploads
type FileBuffer struct {
	Data     []byte
	Filename string
	Size     int64
}

func getFQDNURL(connectionID string, redisClient *redis.Client) (string, error) {
	val, err := redisClient.Get(context.Background(), "session:"+connectionID).Result()
	if err != nil {
		return "", fmt.Errorf("session not found")
	}

	var session redis2.SessionData
	err = json.Unmarshal([]byte(val), &session)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal session data")
	}

	fqdn := session.FQDN
	url := fmt.Sprintf("http://%s:%d/%s", fqdn, 8080, "upload")
	logrus.Infof("File URL: %s", url)
	return url, nil
}

// HandlerUploadFile handles file uploads to multiple destinations concurrently
func HandlerUploadFile(c *httpx.Context, redisClient *redis.Client, k8sClient *kubernetes.Clientset, minioClient *minio.Client, minioBucket string, clamavurl string, timeout time.Duration) {
	start := time.Now()

	// Get connection URL
	url, err := getFQDNURL(c.Param("connectionID"), redisClient)
	if err != nil {
		c.JSON(http.StatusBadRequest, httpx.H{"error": err.Error()})
		return
	}

	if url == "" {
		c.JSON(http.StatusBadRequest, httpx.H{"error": "Invalid connection ID"})
		return
	}

	// Get uploaded file
	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, httpx.H{"error": "file is required"})
		return
	}

	if fileHeader.Size <= 0 || fileHeader.Size > maxUploadBytes {
		c.JSON(http.StatusRequestEntityTooLarge, httpx.H{"error": "file too large"})
		return
	}

	// Read file once into memory buffer
	fileBuffer, err := readFileToBuffer(fileHeader)
	if err != nil {
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "failed to read file: " + err.Error()})
		return
	}

	// Create context with timeout for all operations
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Perform concurrent uploads
	results := performConcurrentUploads(ctx, fileBuffer, url, minioClient, minioBucket, clamavurl, timeout)

	// Determine overall success
	overallSuccess := true
	for _, result := range results {
		if !result.Success {
			overallSuccess = false
			break
		}
	}

	statusCode := http.StatusOK
	if !overallSuccess {
		statusCode = http.StatusMultiStatus // 207
	}

	response := MultiUploadResponse{
		Success: overallSuccess,
		Results: results,
		Message: fmt.Sprintf("Upload completed in %v", time.Since(start)),
	}

	c.JSON(statusCode, response)
}

// readFileToBuffer reads the uploaded file into a buffer for concurrent use
func readFileToBuffer(fileHeader *multipart.FileHeader) (*FileBuffer, error) {
	srcFile, err := fileHeader.Open()
	if err != nil {
		return nil, fmt.Errorf("cannot open uploaded file: %w", err)
	}
	defer func() {
		if err := srcFile.Close(); err != nil {
			logrus.Errorf("Failed to close source file: %v", err)
		}
	}()

	// Read entire file into memory
	data, err := io.ReadAll(srcFile)
	if err != nil {
		return nil, fmt.Errorf("cannot read file data: %w", err)
	}

	return &FileBuffer{
		Data:     data,
		Filename: fileHeader.Filename,
		Size:     fileHeader.Size,
	}, nil
}

// HandlerUploadFileWithoutMinio handles file uploads without MinIO storage
func HandlerUploadFileWithoutMinio(c *httpx.Context, redisClient *redis.Client, k8sClient *kubernetes.Clientset, clamavurl string, timeout time.Duration) {
	start := time.Now()

	// Get connection URL
	url, err := getFQDNURL(c.Param("connectionID"), redisClient)
	if err != nil {
		c.JSON(http.StatusBadRequest, httpx.H{"error": err.Error()})
		return
	}

	if url == "" {
		c.JSON(http.StatusBadRequest, httpx.H{"error": "Invalid connection ID"})
		return
	}

	// Get uploaded file
	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, httpx.H{"error": "file is required"})
		return
	}

	if fileHeader.Size <= 0 || fileHeader.Size > maxUploadBytes {
		c.JSON(http.StatusRequestEntityTooLarge, httpx.H{"error": "file too large"})
		return
	}

	// Read file once into memory buffer
	fileBuffer, err := readFileToBuffer(fileHeader)
	if err != nil {
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "failed to read file: " + err.Error()})
		return
	}

	// Create context with timeout for all operations
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Perform limited uploads (only to container and ClamAV)
	results := make([]UploadResult, 2)

	// Upload to Office/Browser container (primary function)
	results[0] = uploadOfficeContainer(ctx, fileBuffer, url)

	// Try ClamAV scan if URL is provided
	if clamavurl != "" {
		// Use a shorter timeout for ClamAV scan
		scanCtx, scanCancel := context.WithTimeout(ctx, timeout*time.Second)
		defer scanCancel()

		results[1] = uploadToClamAV(scanCtx, fileBuffer, clamavurl, timeout*time.Second)
	} else {
		results[1] = UploadResult{
			Service: "clamav",
			Success: false,
			Error:   "ClamAV service address not configured",
		}
	}

	// Determine overall success - only consider container upload for success
	overallSuccess := results[0].Success

	statusCode := http.StatusOK
	if !overallSuccess {
		statusCode = http.StatusMultiStatus // 207
	}

	response := MultiUploadResponse{
		Success: overallSuccess,
		Results: results,
		Message: fmt.Sprintf("Upload completed in %v", time.Since(start)),
	}

	c.JSON(statusCode, response)
}

// performConcurrentUploads executes all uploads concurrently
func performConcurrentUploads(ctx context.Context, fileBuffer *FileBuffer, officePodUrl string, minioClient *minio.Client, minioBucket string, clamavAddr string, timeout time.Duration) []UploadResult {
	var wg sync.WaitGroup
	results := make([]UploadResult, 3)
	mutex := &sync.Mutex{}

	// Upload to Docker container - this is the primary function
	wg.Add(1)
	go func() {
		defer wg.Done()
		result := uploadOfficeContainer(ctx, fileBuffer, officePodUrl)
		mutex.Lock()
		results[0] = result
		mutex.Unlock()
	}()

	// Upload to ClamAV - make this optional with its own timeout
	if clamavAddr != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Create a shorter context for ClamAV to prevent blocking other operations
			scanCtx, scanCancel := context.WithTimeout(ctx, timeout*time.Second)
			defer scanCancel()

			result := uploadToClamAV(scanCtx, fileBuffer, clamavAddr, timeout*time.Second)
			mutex.Lock()
			results[1] = result
			mutex.Unlock()
		}()
	} else {
		results[1] = UploadResult{Service: "clamav", Success: false, Error: "ClamAV service address not configured"}
	}

	// Upload to MinIO - make sure client and bucket are valid
	if minioClient != nil && minioBucket != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := uploadToMinIO(ctx, fileBuffer, minioClient, minioBucket)
			mutex.Lock()
			results[2] = result
			mutex.Unlock()
		}()
	} else {
		results[2] = UploadResult{Service: "minio", Success: false, Error: "MinIO client or bucket not configured"}
	}

	wg.Wait()
	return results
}

// uploadOfficeContainer uploads file to a office container
func uploadOfficeContainer(ctx context.Context, fileBuffer *FileBuffer, url string) UploadResult {
	// Create multipart form data
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add file
	part, err := writer.CreateFormFile("file", fileBuffer.Filename)
	if err != nil {
		return UploadResult{Service: "file_upload", Success: false, Error: "failed to create form file: " + err.Error()}
	}

	if _, err := part.Write(fileBuffer.Data); err != nil {
		return UploadResult{Service: "file_upload", Success: false, Error: "failed to write file data: " + err.Error()}
	}

	// Add openNow field
	if err := writer.WriteField("openNow", "true"); err != nil {
		return UploadResult{Service: "file_upload", Success: false, Error: "failed to write openNow field: " + err.Error()}
	}

	if err := writer.Close(); err != nil {
		return UploadResult{Service: "file_upload", Success: false, Error: "failed to close writer: " + err.Error()}
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &buf)
	if err != nil {
		return UploadResult{Service: "file_upload", Success: false, Error: "failed to create request: " + err.Error()}
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Send request
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return UploadResult{Service: "file_upload", Success: false, Error: "request failed: " + err.Error()}
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			logrus.Errorf("Failed to close response body: %v", err)
		}
	}()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return UploadResult{Service: "file_upload", Success: true, Data: map[string]interface{}{"status_code": resp.StatusCode}}
	}

	return UploadResult{Service: "file_upload", Success: false, Error: fmt.Sprintf("HTTP %d", resp.StatusCode)}
}

// ClamAVScanResult represents the structure of ClamAV scan result
type ClamAVScanResult struct {
	Success bool `json:"success"`
	Data    struct {
		Result []struct {
			Name       string   `json:"name"`
			IsInfected bool     `json:"is_infected"`
			Viruses    []string `json:"viruses,omitempty"`
		} `json:"result"`
	} `json:"data"`
}

// uploadToClamAV uploads file to ClamAV for virus scanning
func uploadToClamAV(ctx context.Context, fileBuffer *FileBuffer, clamavurl string, timeout time.Duration) UploadResult {
	// Create multipart form payload
	payload := &bytes.Buffer{}
	writer := multipart.NewWriter(payload)

	// Create form file part with "FILES" as the field name
	part, err := writer.CreateFormFile("FILES", fileBuffer.Filename)
	if err != nil {
		return UploadResult{Service: "clamav", Success: false, Error: "failed to create form file: " + err.Error()}
	}

	// Write file data to the form part
	if _, err := part.Write(fileBuffer.Data); err != nil {
		return UploadResult{Service: "clamav", Success: false, Error: "failed to write file data: " + err.Error()}
	}

	// Close the writer
	if err := writer.Close(); err != nil {
		return UploadResult{Service: "clamav", Success: false, Error: "failed to close writer: " + err.Error()}
	}

	// Create scan URL
	scanURL := clamavurl + "/api/v1/scan"

	logrus.Infof("Sending scan request to ClamAV at %s", scanURL)

	// Create request with context
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, scanURL, payload)
	if err != nil {
		return UploadResult{Service: "clamav", Success: false, Error: "failed to create request: " + err.Error()}
	}

	// Set proper headers for multipart/form-data
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Send request with timeout - use a shorter timeout to prevent blocking other uploads
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		// Log the error but don't treat ClamAV failure as critical
		logrus.Warnf("ClamAV scan failed: %v", err)
		return UploadResult{
			Service: "clamav",
			Success: false,
			Error:   "scan request failed: " + err.Error(),
			Data: map[string]interface{}{
				"endpoint": scanURL,
				"status":   "File upload succeeded but virus scan was skipped",
			},
		}
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			logrus.Errorf("Failed to close ClamAV response body: %v", err)
		}
	}()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return UploadResult{Service: "clamav", Success: false, Error: "failed to read response: " + err.Error()}
	}

	// Parse the ClamAV specific response format
	var scanResult ClamAVScanResult
	var responseData map[string]interface{}

	// Try to parse as structured ClamAV response first
	err = json.Unmarshal(body, &scanResult)
	if err != nil {
		// If parsing as ClamAV format fails, fallback to generic JSON parsing
		if jsonErr := json.Unmarshal(body, &responseData); jsonErr != nil {
			// If all JSON parsing fails, treat as plain text
			responseStr := string(body)
			responseData = map[string]interface{}{
				"raw_response": responseStr,
			}
		}
	} else {
		// Successfully parsed as ClamAV response, convert to map for consistent return format
		responseData = map[string]interface{}{
			"success": scanResult.Success,
			"data": map[string]interface{}{
				"result": scanResult.Data.Result,
			},
		}

		// Check if any viruses were detected
		infected := false
		detectedViruses := []string{}

		for _, result := range scanResult.Data.Result {
			if result.IsInfected {
				infected = true
				detectedViruses = append(detectedViruses, result.Viruses...)
			}
		}

		responseData["infected"] = infected
		if infected {
			responseData["viruses"] = detectedViruses
		}
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return UploadResult{
			Service: "clamav",
			Success: true,
			Data: map[string]interface{}{
				"status_code": resp.StatusCode,
				"response":    responseData,
				"endpoint":    scanURL,
			},
		}
	}

	return UploadResult{
		Service: "clamav",
		Success: false,
		Error:   fmt.Sprintf("scan failed with HTTP %d", resp.StatusCode),
		Data: map[string]interface{}{
			"status_code": resp.StatusCode,
			"response":    responseData,
			"endpoint":    scanURL,
		},
	}
}

// uploadToMinIO uploads file to MinIO S3 bucket
func uploadToMinIO(ctx context.Context, fileBuffer *FileBuffer, minioClient *minio.Client, bucket string) UploadResult {
	// Generate object name with timestamp to avoid conflicts
	objectName := fmt.Sprintf("%d_%s", time.Now().Unix(), fileBuffer.Filename)

	// Upload to MinIO
	info, err := minioClient.PutObject(
		ctx,
		bucket,
		objectName,
		bytes.NewReader(fileBuffer.Data),
		fileBuffer.Size,
		minio.PutObjectOptions{
			ContentType: "application/octet-stream",
		},
	)

	if err != nil {
		return UploadResult{Service: "minio", Success: false, Error: "upload failed: " + err.Error()}
	}

	return UploadResult{
		Service: "minio",
		Success: true,
		Data: map[string]interface{}{
			"bucket":      bucket,
			"object_name": objectName,
			"size":        info.Size,
			"etag":        info.ETag,
		},
	}
}
