package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/browsersec/KubeBrowse/internal/cleanup"
	"github.com/browsersec/KubeBrowse/internal/httpx"
	redis2 "github.com/browsersec/KubeBrowse/internal/redis"
	"github.com/go-redis/redis/v8"
	"github.com/sirupsen/logrus"
)

type ExtendSessionRequest struct {
	ExtensionMinutes int `json:"extension_minutes" binding:"required,min=1,max=10"`
}

type SessionTimeResponse struct {
	SessionID    string `json:"session_id"`
	TimeLeft     string `json:"time_left"`
	CanExtend    bool   `json:"can_extend"`
	TotalSeconds int64  `json:"total_seconds"`
}

type ExtendSessionResponse struct {
	SessionID       string `json:"session_id"`
	Extended        bool   `json:"extended"`
	NewTimeLeft     string `json:"new_time_left"`
	ExtensionAmount string `json:"extension_amount"`
	Message         string `json:"message"`
}

// HandlerExtendSession handles session timeout extension requests
func HandlerExtendSession(c *httpx.Context, redisClient *redis.Client, cleanupService *cleanup.SessionCleanupService) {
	connectionID := c.Param("connectionID")
	if connectionID == "" {
		c.JSON(http.StatusBadRequest, httpx.H{
			"error": "Connection ID is required",
		})
		return
	}

	var req ExtendSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, httpx.H{
			"error": fmt.Sprintf("Invalid request: %v", err),
		})
		return
	}

	// Check if session can be extended
	canExtend, timeLeft, err := redis2.CanExtendSession(redisClient, connectionID)
	if err != nil {
		logrus.Errorf("Error checking if session %s can be extended: %v", connectionID, err)
		c.JSON(http.StatusNotFound, httpx.H{
			"error": "Session not found",
		})
		return
	}

	if !canExtend {
		var message string
		if timeLeft <= 0 {
			message = "Session has already expired"
		} else {
			message = fmt.Sprintf("Session can only be extended within the last 2 minutes. Time remaining: %v", timeLeft)
		}

		c.JSON(http.StatusForbidden, ExtendSessionResponse{
			SessionID: connectionID,
			Extended:  false,
			Message:   message,
		})
		return
	}

	// Extend the session
	extensionDuration := time.Duration(req.ExtensionMinutes) * time.Minute
	err = redis2.ExtendSession(redisClient, connectionID, extensionDuration)
	if err != nil {
		logrus.Errorf("Error extending session %s: %v", connectionID, err)
		c.JSON(http.StatusInternalServerError, httpx.H{
			"error": fmt.Sprintf("Failed to extend session: %v", err),
		})
		return
	}

	// If cleanup service is available, we could potentially refresh the monitoring
	// but the current implementation should handle this automatically

	// Get new time left after extension
	newTimeLeft, err := redis2.GetSessionTimeLeft(redisClient, connectionID)
	if err != nil {
		logrus.Warnf("Error getting updated time left for session %s: %v", connectionID, err)
		newTimeLeft = timeLeft + extensionDuration // More accurate fallback
	}

	logrus.Infof("Successfully extended session %s by %d minutes", connectionID, req.ExtensionMinutes)

	c.JSON(http.StatusOK, ExtendSessionResponse{
		SessionID:       connectionID,
		Extended:        true,
		NewTimeLeft:     newTimeLeft.String(),
		ExtensionAmount: extensionDuration.String(),
		Message:         fmt.Sprintf("Session extended by %d minutes", req.ExtensionMinutes),
	})
}

// HandlerGetSessionTimeLeft returns the remaining time for a session
func HandlerGetSessionTimeLeft(c *httpx.Context, redisClient *redis.Client) {
	connectionID := c.Param("connectionID")
	if connectionID == "" {
		c.JSON(http.StatusBadRequest, httpx.H{
			"error": "Connection ID is required",
		})
		return
	}

	timeLeft, err := redis2.GetSessionTimeLeft(redisClient, connectionID)
	if err != nil {
		logrus.Errorf("Error getting time left for session %s: %v", connectionID, err)
		c.JSON(http.StatusNotFound, httpx.H{
			"error": "Session not found or expired",
		})
		return
	}

	// Check if session can be extended
	canExtend, _, err := redis2.CanExtendSession(redisClient, connectionID)
	if err != nil {
		logrus.Warnf("Error checking if session %s can be extended: %v", connectionID, err)
		canExtend = false
	}

	c.JSON(http.StatusOK, SessionTimeResponse{
		SessionID:    connectionID,
		TimeLeft:     timeLeft.String(),
		CanExtend:    canExtend,
		TotalSeconds: int64(timeLeft.Seconds()),
	})
}

// HandlerRegisterSession registers a new session for cleanup monitoring
func HandlerRegisterSession(c *httpx.Context, redisClient *redis.Client, cleanupService *cleanup.SessionCleanupService) {
	sessionID := c.Param("connectionID")
	podName := c.Query("pod_name")
	userID := c.Query("user_id")

	if sessionID == "" || podName == "" || userID == "" {
		c.JSON(http.StatusBadRequest, httpx.H{
			"error": "Session ID, pod name, and user ID are required",
		})
		return
	}

	// Verify session exists in Redis
	_, err := redis2.GetSessionTimeLeft(redisClient, sessionID)
	if err != nil {
		c.JSON(http.StatusNotFound, httpx.H{
			"error": "Session not found",
		})
		return
	}

	cleanupService.RegisterSession(sessionID, podName, userID)

	c.JSON(http.StatusOK, httpx.H{
		"message":    "Session registered for cleanup monitoring",
		"session_id": sessionID,
		"pod_name":   podName,
	})
}

// HandlerUnregisterSession removes a session from cleanup monitoring
func HandlerUnregisterSession(c *httpx.Context, cleanupService *cleanup.SessionCleanupService) {
	sessionID := c.Param("connectionID")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, httpx.H{
			"error": "Session ID is required",
		})
		return
	}

	cleanupService.UnregisterSession(sessionID)

	c.JSON(http.StatusOK, httpx.H{
		"message":    "Session unregistered from cleanup monitoring",
		"session_id": sessionID,
	})
}
