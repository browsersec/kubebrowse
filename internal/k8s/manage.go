package k8s

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/browsersec/KubeBrowse/internal/httpx"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/lib/pq"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Config contains application configuration
type Config struct {
	PostgresHost     string
	PostgresPort     string
	PostgresUser     string
	PostgresPassword string
	PostgresDB       string
	RedisHost        string
	RedisPort        string
	GuacdHost        string
	GuacdPort        string
	K8sNamespace     string
	BrowserImage     string
	OfficeImage      string
	SessionTimeout   time.Duration
}

// KubeBrowseManager manages sandbox environments
type KubeBrowseManager struct {
	Config    Config
	DB        *sql.DB
	Redis     *redis.Client
	K8sClient *kubernetes.Clientset
}

// Session represents a user browser session
type Session struct {
	ID            string    `json:"id"`
	UserID        string    `json:"user_id"`
	PodName       string    `json:"pod_name"`
	Status        string    `json:"status"`
	Type          string    `json:"type"` // "browser" or "office"
	CreatedAt     time.Time `json:"created_at"`
	LastActiveAt  time.Time `json:"last_active_at"`
	ConnectionURI string    `json:"connection_uri"`
}

// NewManager creates a new KubeBrowse manager
func NewManager() (*KubeBrowseManager, error) {
	manager := &KubeBrowseManager{
		Config: Config{
			PostgresHost:     getEnv("POSTGRES_HOST", "localhost"),
			PostgresPort:     getEnv("POSTGRES_PORT", "5432"),
			PostgresUser:     getEnv("POSTGRES_USER", "postgres"),
			PostgresPassword: getEnv("POSTGRES_PASSWORD", "postgres"),
			PostgresDB:       getEnv("POSTGRES_DB", "sandbox_db"),
			RedisHost:        getEnv("REDIS_HOST", "localhost"),
			RedisPort:        getEnv("REDIS_PORT", "6379"),
			GuacdHost:        getEnv("GUACD_HOST", "localhost"),
			GuacdPort:        getEnv("GUACD_PORT", "4822"),
			K8sNamespace:     getEnv("KUBERNETES_NAMESPACE", "browser-sandbox"),
			BrowserImage:     getEnv("BROWSER_IMAGE", DefaultBrowserImage()),
			OfficeImage:      getEnv("OFFICE_IMAGE", DefaultOfficeImage()),
			SessionTimeout:   getDurationEnv("SESSION_TIMEOUT", 10*time.Minute),
		},
	}

	// Connect to PostgreSQL
	connStr := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		manager.Config.PostgresHost,
		manager.Config.PostgresPort,
		manager.Config.PostgresUser,
		manager.Config.PostgresPassword,
		manager.Config.PostgresDB)

	var err error
	manager.DB, err = sql.Open("postgres", connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// Connect to Redis
	manager.Redis = redis.NewClient(&redis.Options{
		Addr: fmt.Sprintf("%s:%s", manager.Config.RedisHost, manager.Config.RedisPort),
	})

	// Connect to Kubernetes
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes config: %w", err)
	}

	manager.K8sClient, err = kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	// Initialize database tables
	if err := manager.initializeDB(); err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	return manager, nil
}

// Initialize database tables
func (m *KubeBrowseManager) initializeDB() error {
	// Create users table
	_, err := m.DB.Exec(`
        CREATE TABLE IF NOT EXISTS users (
            id VARCHAR(36) PRIMARY KEY,
            username VARCHAR(255) UNIQUE NOT NULL,
            password_hash VARCHAR(255) NOT NULL,
            email VARCHAR(255) UNIQUE NOT NULL,
            created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
        )
    `)
	if err != nil {
		return err
	}

	// Create sessions table
	_, err = m.DB.Exec(`
        CREATE TABLE IF NOT EXISTS sessions (
            id VARCHAR(36) PRIMARY KEY,
            user_id VARCHAR(36) NOT NULL REFERENCES users(id),
            pod_name VARCHAR(255),
            status VARCHAR(50) NOT NULL,
            type VARCHAR(50) NOT NULL,
            created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
            last_active_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
            connection_uri TEXT,
            CONSTRAINT unique_active_session UNIQUE (user_id, status)
                WHERE status = 'active'
        )
    `)
	if err != nil {
		return err
	}

	// File scans table updated to not reference MinIO
	_, err = m.DB.Exec(`
        CREATE TABLE IF NOT EXISTS file_scans (
            id VARCHAR(36) PRIMARY KEY,
            session_id VARCHAR(36) NOT NULL REFERENCES sessions(id),
            filename VARCHAR(255) NOT NULL,
            storage_path VARCHAR(255) NOT NULL,
            scan_status VARCHAR(50) NOT NULL,
            scan_result JSONB,
            created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
        )
    `)

	return err
}

// RegisterUser handles user registration
func (m *KubeBrowseManager) RegisterUser(c *httpx.Context) {
	var user struct {
		Username string `json:"username" binding:"required"`
		Password string `json:"password" binding:"required"`
		Email    string `json:"email" binding:"required,email"`
	}

	if err := c.ShouldBindJSON(&user); err != nil {
		c.JSON(http.StatusBadRequest, httpx.H{"error": err.Error()})
		return
	}

	// Hash password (in a real app, use bcrypt)
	// passwordHash := hashPassword(user.Password)

	// For simplicity, we're not hashing the password in this example
	passwordHash := user.Password

	// Generate UUID for user
	userID := uuid.New().String()

	// Insert user into database
	_, err := m.DB.Exec(
		"INSERT INTO users (id, username, password_hash, email) VALUES ($1, $2, $3, $4)",
		userID, user.Username, passwordHash, user.Email,
	)
	if err != nil {
		// Check for unique constraint violation
		if pqErr, ok := err.(*pq.Error); ok && pqErr.Code == "23505" {
			c.JSON(http.StatusConflict, httpx.H{"error": "Username or email already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Failed to create user"})
		return
	}

	c.JSON(http.StatusCreated, httpx.H{"id": userID, "username": user.Username})
}

// LoginUser handles user login
func (m *KubeBrowseManager) LoginUser(c *httpx.Context) {
	var credentials struct {
		Username string `json:"username" binding:"required"`
		Password string `json:"password" binding:"required"`
	}

	if err := c.ShouldBindJSON(&credentials); err != nil {
		c.JSON(http.StatusBadRequest, httpx.H{"error": err.Error()})
		return
	}

	// Get user from database
	var user struct {
		ID           string
		PasswordHash string
	}
	err := m.DB.QueryRow(
		"SELECT id, password_hash FROM users WHERE username = $1",
		credentials.Username,
	).Scan(&user.ID, &user.PasswordHash)

	if err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusUnauthorized, httpx.H{"error": "Invalid credentials"})
			return
		}
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Database error"})
		return
	}

	// Verify password (in a real app, use bcrypt)
	if user.PasswordHash != credentials.Password {
		c.JSON(http.StatusUnauthorized, httpx.H{"error": "Invalid credentials"})
		return
	}

	// Generate JWT token (in a real app)
	token := "dummy-token-" + user.ID

	c.JSON(http.StatusOK, httpx.H{"token": token, "user_id": user.ID})
}

// CreateSession creates a new browser or office session
func (m *KubeBrowseManager) CreateSession(c *httpx.Context) {
	userID := c.GetString("userID")
	if userID == "" {
		c.JSON(http.StatusBadRequest, httpx.H{"error": "User ID not found"})
		return
	}

	var sessionRequest struct {
		Type string `json:"type" binding:"required,oneof=browser office"`
	}

	if err := c.ShouldBindJSON(&sessionRequest); err != nil {
		c.JSON(http.StatusBadRequest, httpx.H{"error": err.Error()})
		return
	}

	// Generate a session ID
	sessionID := uuid.New().String()

	var pod *corev1.Pod
	var err error

	// Create the appropriate pod based on session type
	if sessionRequest.Type == "browser" {
		pod, err = CreateBrowserSandboxPod(m.K8sClient, m.Config.K8sNamespace, userID, m.Config.BrowserImage)
	} else {
		pod, err = CreateOfficeSandboxPod(m.K8sClient, m.Config.K8sNamespace, userID, m.Config.OfficeImage)
	}

	if err != nil {
		log.Printf("Error creating pod: %v", err)
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Failed to create sandbox environment"})
		return
	}

	// Create connection URI based on pod name and service
	connectionURI := fmt.Sprintf("/guac/?id=%s&type=%s", pod.Name, sessionRequest.Type)

	// Create a new session in the database
	_, err = m.DB.Exec(
		"INSERT INTO sessions (id, user_id, pod_name, status, type, connection_uri) VALUES ($1, $2, $3, $4, $5, $6)",
		sessionID, userID, pod.Name, "creating", sessionRequest.Type, connectionURI,
	)
	if err != nil {
		log.Printf("Database error creating session: %v", err)
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Failed to create session"})
		return
	}

	// Return session information
	session := Session{
		ID:            sessionID,
		UserID:        userID,
		PodName:       pod.Name,
		Status:        "creating",
		Type:          sessionRequest.Type,
		CreatedAt:     time.Now(),
		LastActiveAt:  time.Now(),
		ConnectionURI: connectionURI,
	}

	c.JSON(http.StatusCreated, session)
}

// GetSession returns details for a specific session
func (m *KubeBrowseManager) GetSession(c *httpx.Context) {
	sessionID := c.Param("id")
	userID := c.GetString("userID")

	var session Session
	err := m.DB.QueryRow(
		"SELECT id, user_id, pod_name, status, type, created_at, last_active_at, connection_uri FROM sessions WHERE id = $1 AND user_id = $2",
		sessionID, userID,
	).Scan(&session.ID, &session.UserID, &session.PodName, &session.Status, &session.Type, &session.CreatedAt, &session.LastActiveAt, &session.ConnectionURI)

	if err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, httpx.H{"error": "Session not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Database error"})
		return
	}

	c.JSON(http.StatusOK, session)
}

// ExtendSession extends the timeout of a session
func (m *KubeBrowseManager) ExtendSession(c *httpx.Context) {
	sessionID := c.Param("id")
	userID := c.GetString("userID")

	result, err := m.DB.Exec(
		"UPDATE sessions SET last_active_at = NOW() WHERE id = $1 AND user_id = $2 AND status = 'active'",
		sessionID, userID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Database error"})
		return
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		c.JSON(http.StatusNotFound, httpx.H{"error": "Active session not found"})
		return
	}

	c.JSON(http.StatusOK, httpx.H{"message": "Session extended"})
}

// CloseSession terminates a session and its pod
func (m *KubeBrowseManager) CloseSession(c *httpx.Context) {
	sessionID := c.Param("id")
	userID := c.GetString("userID")

	// Get pod name from the session
	var podName string
	err := m.DB.QueryRow(
		"SELECT pod_name FROM sessions WHERE id = $1 AND user_id = $2",
		sessionID, userID,
	).Scan(&podName)

	if err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, httpx.H{"error": "Session not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Database error"})
		return
	}

	// Update session status
	_, err = m.DB.Exec(
		"UPDATE sessions SET status = 'closed' WHERE id = $1",
		sessionID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, httpx.H{"error": "Database error"})
		return
	}

	// Delete the pod
	err = m.K8sClient.CoreV1().Pods(m.Config.K8sNamespace).Delete(
		context.Background(),
		podName,
		metav1.DeleteOptions{},
	)
	if err != nil {
		log.Printf("Error deleting pod %s: %v", podName, err)
		// Still return success to the client as the session is marked as closed
	}

	c.JSON(http.StatusOK, httpx.H{"message": "Session closed"})
}

// Helper functions for environment variables
func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func getDurationEnv(key string, fallback time.Duration) time.Duration {
	if value, ok := os.LookupEnv(key); ok {
		if duration, err := time.ParseDuration(value); err == nil {
			return duration
		}
	}
	return fallback
}

// DefaultBrowserImage returns the default browser sandbox image for the
// current runtime architecture (latest-amd64 or latest-arm64).
func DefaultBrowserImage() string {
	return fmt.Sprintf("ghcr.io/browsersec/rdp-chromium:latest-%s", runtime.GOARCH)
}

// DefaultOfficeImage returns the default office sandbox image for the
// current runtime architecture (latest-amd64 or latest-arm64).
func DefaultOfficeImage() string {
	return fmt.Sprintf("ghcr.io/browsersec/rdp-onlyoffice-lxde:latest-%s", runtime.GOARCH)
}
