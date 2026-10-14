package api

import (
	"context"

	"encoding/json"
	"net/http"

	"fmt"

	guac2 "github.com/browsersec/KubeBrowse/internal/guac"
	"github.com/browsersec/KubeBrowse/internal/httpx"
	"github.com/browsersec/KubeBrowse/internal/k8s"
	redis2 "github.com/browsersec/KubeBrowse/internal/redis"
	"github.com/go-redis/redis/v8"
	"github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
)

// Endpoint to stop a specific WebSocket session
func HandlerStopWSSession(c *httpx.Context, redisClient *redis.Client, k8sClient *kubernetes.Clientset, server *guac2.Server) {
	connectionID := c.Param("connectionID")

	if err := stopWSSession(connectionID, redisClient, k8sClient, server); err != nil {
		logrus.Errorf("Failed to stop WebSocket session: %v", err)
		c.JSON(http.StatusInternalServerError, httpx.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, httpx.H{"message": fmt.Sprintf("Session %s stopped and pod deleted.", connectionID)})
}

// StopWSSession is an exported version of stopWSSession that can be used by other packages
func StopWSSession(connectionID string, redisClient *redis.Client, k8sClient *kubernetes.Clientset, server *guac2.Server) error {
	return stopWSSession(connectionID, redisClient, k8sClient, server)
}

func stopWSSession(connectionID string, redisClient *redis.Client, k8sClient *kubernetes.Clientset, server *guac2.Server) error {

	if connectionID == "" {
		return fmt.Errorf("connection ID is required")
	}
	val, err := redisClient.Get(context.Background(), "session:"+connectionID).Result()
	if err != nil {
		return fmt.Errorf("session not found: %w", err)
	}
	var session redis2.SessionData
	err = json.Unmarshal([]byte(val), &session)
	if err != nil {
		logrus.Errorf("Failed to unmarshal session data: %v", err)
		return fmt.Errorf("failed to unmarshal session data: %w", err)
	}

	// Deregister the tunnel if it exists
	tunnel, err := server.GetTunnelByUUID(session.TunnelConnectionID)

	if err == nil {
		// Tunnel found, deregister and close it
		server.DeregisterTunnel(tunnel)
		if closeErr := tunnel.Close(); closeErr != nil {
			logrus.Warnf("Error closing tunnel %s: %v", connectionID, closeErr)
		} else {
			logrus.Infof("Successfully closed tunnel for session %s", connectionID)
		}
	}

	// Delete the pod
	err = k8s.DeletePod(k8sClient, session.PodName)
	if err != nil {
		logrus.Errorf("Failed to delete pod: %v", err)
	}

	// Delete session from Redis
	redisClient.Del(context.Background(), "session:"+connectionID)
	return nil

}

// Session management handler
func HandlerSession(c *httpx.Context, activeTunnels *guac2.ActiveTunnelStore) {
	c.Header("Content-Type", "application/json")

	// sessions.RLock() // Old store lock
	// defer sessions.RUnlock() // Old store unlock

	type ConnInfo struct {
		Uuid string `json:"uuid"`
		// Num  int    `json:"num"` // We don't have a 'Num' equivalent directly, can show count if needed
	}

	// connIds := make([]*ConnIds, len(sessions.ConnIds)) // Old way
	allIDs := activeTunnels.GetAllIDs()
	connInfos := make([]*ConnInfo, len(allIDs))

	// i := 0 // Old way
	// for id, num := range sessions.ConnIds { // Old way
	// 	connIds[i] = &ConnIds{ // Old way
	// 		Uuid: id, // Old way
	// 		Num:  num, // Old way
	// 	}
	// 	i++ // Old way
	// }
	for i, id := range allIDs {
		connInfos[i] = &ConnInfo{Uuid: id}
	}

	// c.JSON(http.StatusOK, connIds) // Old way
	c.JSON(http.StatusOK, httpx.H{
		"active_sessions": len(connInfos),
		"connection_ids":  connInfos,
	})

}
