package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/browsersec/KubeBrowse/internal/guac"
	k8s2 "github.com/browsersec/KubeBrowse/internal/k8s"

	"github.com/browsersec/KubeBrowse/internal/httpx"
	redis2 "github.com/browsersec/KubeBrowse/internal/redis"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
)

// Struct for request body
type DeploySessionRequest struct {
	Height string `json:"height"`
	Width  string `json:"width"`
	Share  bool   `json:"share,omitempty"` // Added optional share field
}

// DeploySessionResponse is returned when a browser/office session is deployed.
type DeploySessionResponse struct {
	PodName      string `json:"podName"`
	FQDN         string `json:"fqdn"`
	ConnectionID string `json:"connection_id"`
	Status       string `json:"status"`
	Message      string `json:"message"`
}

// ConnectionResponse is returned for the connect endpoint.
type ConnectionResponse struct {
	WebsocketURL string `json:"websocket_url"`
	Status       string `json:"status"`
	Message      string `json:"message"`
}

// ErrorResponse represents a generic JSON error response.
type ErrorResponse struct {
	Error string `json:"error"`
}

// DeployOffice godoc
// @Summary New route for deploying and connecting to office pod with RDP credentials
// @Schemes
// @Description New route for deploying and connecting to office pod with RDP credentials
// @Tags sessions
// @Accept  json
// @Produce  json
// @Param request body DeploySessionRequest true "Session Deployment Request"
// @Success 201 {object} DeploySessionResponse
// @Failure 503 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/sessions/office [post]
func DeployOffice(c *httpx.Context, k8sClient *kubernetes.Clientset, k8sNamespace string, redisClient *redis.Client, tunnelStore *guac.ActiveTunnelStore, officeImage string) {

	if k8sClient == nil {
		c.JSON(http.StatusServiceUnavailable, httpx.H{
			"error": "Kubernetes client not initialized",
		})
		return
	}

	var reqBody DeploySessionRequest
	if err := c.ShouldBindJSON(&reqBody); err != nil {
		logrus.Errorf("Failed to bind request body: %v", err)
		c.JSON(http.StatusBadRequest, httpx.H{"error": err.Error()})
		return
	}

	// Generate a unique pod name
	podName := "office-" + uuid.New().String()[0:8]

	// Create an office sandbox pod
	pod, err := k8s2.CreateOfficeSandboxPod(k8sClient, k8sNamespace, podName, officeImage)
	if err != nil {
		logrus.Errorf("Failed to create office pod: %v", err)
		c.JSON(http.StatusInternalServerError, httpx.H{
			"error": fmt.Sprintf("Failed to create office pod: %v", err),
		})
		return
	}

	// Construct the FQDN
	fqdn := fmt.Sprintf("%s.sandbox-instances.browser-sandbox.svc.cluster.local", pod.Name)

	// Generate a unique connection ID
	connectionID := uuid.New().String()

	// Wait for pod readiness and RDP port
	err = k8s2.WaitForPodReadyAndRDP(k8sClient, k8sNamespace, pod.Name, fqdn, 120*time.Second)
	if err != nil {
		logrus.Errorf("Pod not ready: %v", err)
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Pod not ready for RDP connection"})

		return
	}
	podIP := pod.Status.PodIP
	if podIP == "" {
		logrus.Errorf("Pod IP is empty for connectionID: %s", connectionID)
		podIP = fqdn
	}
	// nsLookup fqdn
	ips, err := net.LookupIP(fqdn)
	if err == nil && len(ips) > 0 {
		podIP = ips[0].String()
	}
	logrus.Infof("Pod IP of connectionID: %s is %s", connectionID, podIP)

	// Store connection parameters in memory (in a real implementation, use a secure storage)
	params := url.Values{}
	params.Set("scheme", "rdp")
	params.Set("hostname", fqdn)
	params.Set("username", "rdpuser")
	params.Set("password", "money4band")
	params.Set("port", "3389")
	params.Set("security", "")
	params.Set("width", reqBody.Width)
	params.Set("height", reqBody.Height)
	params.Set("ignore-cert", "true")
	params.Set("uuid", connectionID)

	// Store the parameters in the tunnelStore store
	tunnelStore.StoreConnectionParams(connectionID, params)

	// Store session in Redis using the struct from internal/redis
	session := redis2.SessionData{
		PodName:      pod.Name,
		PodIP:        podIP,
		FQDN:         fqdn,
		ConnectionID: connectionID,
		ConnectionParams: map[string]string{
			"hostname":    fqdn,
			"ignore-cert": "true",
			"password":    "money4band",
			"port":        "3389",
			"scheme":      "rdp",
			"security":    "",
			"username":    "rdpuser",
			"height":      reqBody.Height,
			"width":       reqBody.Width,
			"uuid":        connectionID,
		},
		Share: reqBody.Share, // Include the share value
	}
	data, err := json.Marshal(session)
	if err != nil {
		logrus.Errorf("Failed to marshal session data: %v", err)
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Failed to marshal session data"})
		return
	}
	err = redisClient.Set(context.Background(), "session:"+connectionID, data, 0).Err()
	if err != nil {
		logrus.Errorf("Failed to store session in Redis: %v", err)
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Failed to store session in Redis"})
		return
	}

	// Return only the connection ID to the client
	c.JSON(http.StatusCreated, httpx.H{
		"podName":       pod.Name,
		"fqdn":          fqdn,
		"connection_id": connectionID,
		"status":        "creating",
		"message":       "Office pod deployed and connection parameters generated",
	})
}

// DeployBrowser godoc
// @Summary New route for deploying and connecting to browser pod with RDP credentials
// @Schemes
// @Description New route for deploying and connecting to browser pod with RDP credentials
// @Tags sessions
// @Accept  json
// @Produce  json
// @Param request body DeploySessionRequest true "Session Deployment Request"
// @Success 201 {object} DeploySessionResponse
// @Failure 503 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/sessions/browser [post]
func DeployBrowser(c *httpx.Context, k8sClient *kubernetes.Clientset, k8sNamespace string, redisClient *redis.Client, tunnelStore *guac.ActiveTunnelStore, browserImage string) {

	if k8sClient == nil {
		c.JSON(http.StatusServiceUnavailable, httpx.H{
			"error": "Kubernetes client not initialized",
		})
		return
	}

	var reqBody DeploySessionRequest
	if err := c.ShouldBindJSON(&reqBody); err != nil {
		logrus.Errorf("Failed to bind request body: %v", err)
		c.JSON(http.StatusBadRequest, httpx.H{"error": err.Error()})
		return
	}

	// Generate a unique pod name
	podName := "browser-" + uuid.New().String()[0:8]

	// Create an office sandbox pod
	pod, err := k8s2.CreateBrowserSandboxPod(k8sClient, k8sNamespace, podName, browserImage)
	if err != nil {
		logrus.Errorf("Failed to create office pod: %v", err)
		c.JSON(http.StatusInternalServerError, httpx.H{
			"error": fmt.Sprintf("Failed to create browser pod: %v", err),
		})
		return
	}

	// Construct the FQDN
	fqdn := fmt.Sprintf("%s.sandbox-instances.browser-sandbox.svc.cluster.local", pod.Name)

	// Generate a unique connection ID
	connectionID := uuid.New().String()

	// Wait for pod readiness and RDP port
	err = k8s2.WaitForPodReadyAndRDP(k8sClient, k8sNamespace, pod.Name, fqdn, 120*time.Second)
	if err != nil {
		logrus.Errorf("Pod not ready: %v", err)
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Pod not ready for RDP connection"})
		return
	}
	podIP := pod.Status.PodIP
	if podIP == "" {
		logrus.Errorf("Pod IP is empty for connectionID: %s", connectionID)
		podIP = fqdn
	}
	// nsLookup fqdn
	ips, err := net.LookupIP(fqdn)
	if err == nil && len(ips) > 0 {
		podIP = ips[0].String()
	}
	logrus.Infof("Pod IP of connectionID: %s is %s", connectionID, podIP)

	// Store connection parameters in memory (in a real implementation, use a secure storage)
	params := url.Values{}
	params.Set("scheme", "rdp")
	params.Set("hostname", fqdn)
	params.Set("username", "rdpuser")
	params.Set("password", "money4band")
	params.Set("port", "3389")
	params.Set("security", "")
	params.Set("width", reqBody.Width)
	params.Set("height", reqBody.Height)
	params.Set("ignore-cert", "true")
	params.Set("uuid", connectionID)

	// Store the parameters in the tunnelStore store
	tunnelStore.StoreConnectionParams(connectionID, params)

	// Store session in Redis using the struct from internal/redis
	session := redis2.SessionData{
		PodName:      pod.Name,
		PodIP:        podIP,
		FQDN:         fqdn,
		ConnectionID: connectionID,
		ConnectionParams: map[string]string{
			"hostname":    fqdn,
			"ignore-cert": "true",
			"password":    "money4band",
			"port":        "3389",
			"scheme":      "rdp",
			"security":    "",
			"username":    "rdpuser",
			"height":      reqBody.Height,
			"width":       reqBody.Width,
			"uuid":        connectionID,
		},
		Share: reqBody.Share, // Include the share value
	}
	data, err := json.Marshal(session)
	if err != nil {
		logrus.Errorf("Failed to marshal session data: %v", err)
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Failed to marshal session data"})
		return
	}
	err = redisClient.Set(context.Background(), "session:"+connectionID, data, 0).Err()
	if err != nil {
		logrus.Errorf("Failed to store session in Redis: %v", err)
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Failed to store session in Redis"})
		return
	}

	// Return only the connection ID to the client
	c.JSON(http.StatusCreated, httpx.H{
		"podName":       pod.Name,
		"fqdn":          fqdn,
		"connection_id": connectionID,
		"status":        "creating",
		"message":       "Browser pod deployed and connection parameters generated",
	})
}

// HandlerConnectionID godoc
// @Summary Get WebSocket connection info for a session
// @Description Returns the websocket URL and status for a given connection ID
// @Tags sessions
// @Produce json
// @Param connectionID path string true "Connection ID"
// @Success 200 {object} ConnectionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/sessions/{connectionID}/connect [get]
func HandlerConnectionID(c *httpx.Context, tunnelStore *guac.ActiveTunnelStore, redisClient *redis.Client) {

	connectionID := c.Param("connectionID")

	if connectionID == "" {
		c.JSON(http.StatusBadRequest, httpx.H{"error": "Connection ID is required"})
		return
	}

	// Check if the connectionID is valid in redis
	_, err := redisClient.Get(context.Background(), "session:"+connectionID).Result()
	if err == redis.Nil {
		c.JSON(http.StatusNotFound, httpx.H{"error": "Session not found"})
		return
	} else if err != nil {
		logrus.Errorf("Failed to get session data for %s: %v", connectionID, err)
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Failed to get session data"})
		return
	}

	// Get stored parameters
	_, exists := tunnelStore.GetConnectionParams(connectionID)
	if !exists {
		c.JSON(http.StatusNotFound, httpx.H{"error": "Connection parameters not found"})
		return
	}

	// Construct the websocket URL with only the connection ID
	wsURL := fmt.Sprintf("/websocket-tunnel?uuid=%s", connectionID)

	c.JSON(http.StatusOK, httpx.H{
		"websocket_url": wsURL,
		"status":        "ready",
		"message":       "Connection parameters retrieved successfully",
	})
}

// HandlerShareSession godoc
// @Summary Enable sharing for a session
// @Description Enables sharing for the given connection ID and returns the shared websocket URL
// @Tags sessions
// @Produce json
// @Param connectionID path string true "Connection ID"
// @Success 200 {object} ConnectionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/sessions/{connectionID}/share [get]
func HandlerShareSession(c *httpx.Context, tunnelStore *guac.ActiveTunnelStore, redisClient *redis.Client) {
	connectionID := c.Param("connectionID")
	if connectionID == "" {
		c.JSON(http.StatusBadRequest, httpx.H{"error": "Connection ID is required"})
		return
	}

	ctx := context.Background()
	sessionKey := fmt.Sprintf("session:%s", connectionID)

	// Get current session data and TTL
	sessionJSON, err := redisClient.Get(ctx, sessionKey).Result()
	if err != nil {
		logrus.Errorf("Failed to get session data for %s: %v", connectionID, err)
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Failed to get session data"})
		return
	}

	// Get current TTL before modifying the session
	currentTTL, err := redisClient.TTL(ctx, sessionKey).Result()
	if err != nil {
		logrus.Errorf("Failed to get TTL for session %s: %v", connectionID, err)
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Failed to get session TTL"})
		return
	}

	// If TTL is -1 (no expiration) or -2 (key doesn't exist), handle appropriately
	if currentTTL == -2*time.Second {
		logrus.Errorf("Session %s does not exist", connectionID)
		c.JSON(http.StatusNotFound, httpx.H{"error": "Session not found"})
		return
	}
	if currentTTL == -1*time.Second {
		// Session has no expiration, set a default timeout
		currentTTL = 10 * time.Minute
		logrus.Warnf("Session %s has no TTL, setting default 10 minutes", connectionID)
	}

	// Parse session data
	var sessionData redis2.SessionData
	err = json.Unmarshal([]byte(sessionJSON), &sessionData)
	if err != nil {
		logrus.Errorf("Failed to unmarshal session data for %s: %v", connectionID, err)
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Failed to unmarshal session data"})
		return
	}

	if sessionData.Share {
		logrus.Warnf("Session %s is already shared, skipping update", connectionID)
		c.JSON(http.StatusOK, httpx.H{
			"message":       "Session is already shared",
			"status":        "ready",
			"websocket_url": fmt.Sprintf("/websocket-tunnel/share?uuid=%s", connectionID),
		})
		return
	}

	// Update the share flag
	sessionData.Share = true
	logrus.Infof("Enabling sharing for session %s (TTL: %v)", connectionID, currentTTL.Round(time.Second))

	// Marshal updated session data
	updatedData, err := json.Marshal(sessionData)
	if err != nil {
		logrus.Errorf("Failed to marshal updated session data for %s: %v", connectionID, err)
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Failed to marshal session data"})
		return
	}

	// Update Redis with preserved TTL
	err = redisClient.Set(ctx, sessionKey, updatedData, currentTTL).Err()
	if err != nil {
		logrus.Errorf("Failed to update session data for %s: %v", connectionID, err)
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Failed to update session data"})
		return
	}

	logrus.Infof("Successfully enabled sharing for session %s with preserved TTL %v", connectionID, currentTTL.Round(time.Second))

	// Check if connection parameters exist
	_, exists := tunnelStore.GetConnectionParams(connectionID)
	if !exists {
		logrus.Errorf("Connection parameters not found for %s", connectionID)
		c.JSON(http.StatusNotFound, httpx.H{"error": "Connection parameters not found"})
		return
	}

	// Construct the websocket URL with only the connection ID
	wsURL := fmt.Sprintf("/websocket-tunnel/share?uuid=%s", connectionID)

	c.JSON(http.StatusOK, httpx.H{
		"websocket_url": wsURL,
		"status":        "ready",
		"message":       "Session sharing enabled successfully",
	})
}

// HandlerBrowserPod godoc
// @Summary Create a raw browser sandbox pod
// @Description Creates a browser sandbox pod directly without generating session connection parameters
// @Tags pods
// @Accept json
// @Produce json
// @Success 201 {object} map[string]interface{}
// @Failure 503 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/pods/browser [post]
func HandlerBrowserPod(c *httpx.Context, tunnelStore *guac.ActiveTunnelStore, k8sClient *kubernetes.Clientset, k8sNamespace, browserImage string) {
	if k8sClient == nil {
		c.JSON(http.StatusServiceUnavailable, httpx.H{
			"error": "Kubernetes client not initialized",
		})
		return
	}

	// Generate a dummy user ID for testing
	userID := "test-" + uuid.New().String()[0:8]

	// Create a browser sandbox pod
	pod, err := k8s2.CreateBrowserSandboxPod(k8sClient, k8sNamespace, userID+"-browser", browserImage)
	if err != nil {
		logrus.Errorf("Failed to create browser pod: %v", err)
		c.JSON(http.StatusInternalServerError, httpx.H{
			"error": fmt.Sprintf("Failed to create browser pod: %v", err),
		})
		return
	}

	// Connection URI for the pod (simplified version)
	connectionURI := fmt.Sprintf("/guac/?id=%s&type=browser", pod.Name)

	c.JSON(http.StatusCreated, httpx.H{
		"podName":       pod.Name,
		"namespace":     pod.Namespace,
		"status":        "creating",
		"connectionURI": connectionURI,
		"podIP":         pod.Status.PodIP,
		"message":       "Browser sandbox pod created successfully",
	})
}

// HandlerOfficePod godoc
// @Summary Create a raw office sandbox pod
// @Description Creates an office sandbox pod directly without generating session connection parameters
// @Tags pods
// @Accept json
// @Produce json
// @Success 201 {object} map[string]interface{}
// @Failure 503 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/pods/office [post]
func HandlerOfficePod(c *httpx.Context, tunnelStore *guac.ActiveTunnelStore, k8sClient *kubernetes.Clientset, k8sNamespace, officeImage string) {
	if k8sClient == nil {
		c.JSON(http.StatusServiceUnavailable, httpx.H{
			"error": "Kubernetes client not initialized",
		})
		return
	}

	// Generate a dummy user ID for testing
	userID := "test-" + uuid.New().String()[0:8]

	// Create an office sandbox pod
	pod, err := k8s2.CreateOfficeSandboxPod(k8sClient, k8sNamespace, userID+"-office", officeImage)
	if err != nil {
		logrus.Errorf("Failed to create office pod: %v", err)
		c.JSON(http.StatusInternalServerError, httpx.H{
			"error": fmt.Sprintf("Failed to create office pod: %v", err),
		})
		return
	}

	// Connection URI for the pod (simplified version)
	connectionURI := fmt.Sprintf("/guac/?id=%s&type=office", pod.Name)

	c.JSON(http.StatusCreated, httpx.H{
		"podName":       pod.Name,
		"namespace":     pod.Namespace,
		"status":        "creating",
		"connectionURI": connectionURI,
		"podIP":         pod.Status.PodIP,
		"message":       "Office sandbox pod created successfully",
	})

}
