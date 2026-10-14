package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/browsersec/KubeBrowse/internal/cleanup"
	guac2 "github.com/browsersec/KubeBrowse/internal/guac"
	redis2 "github.com/browsersec/KubeBrowse/internal/redis"
	"github.com/go-redis/redis/v8"
	uuid2 "github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

var SESSION_TIMEOUT int

func init() {
	timeoutStr := os.Getenv("POD_SESSION_TIMEOUT")
	timeout, err := strconv.Atoi(timeoutStr)
	if err != nil || timeout <= 0 {
		SESSION_TIMEOUT = 10 // default to 10 minutes if not set or invalid
		logrus.Warnf("Invalid or unset POD_SESSION_TIMEOUT environment variable, defaulting to %d minutes", SESSION_TIMEOUT)
	} else {
		SESSION_TIMEOUT = timeout
	}
}

// DemoDoConnect creates the tunnel to the remote machine (via guacd)
// Now accepts ActiveTunnelStore to register the tunnel
func DemoDoConnect(request *http.Request, tunnelStore *guac2.ActiveTunnelStore, redisClient *redis.Client, guacdAddr string, cleanupService *cleanup.SessionCleanupService) (guac2.Tunnel, error) {
	config := guac2.NewGuacamoleConfiguration()
	var query url.Values
	uuid := request.URL.Query().Get("uuid")
	var session redis2.SessionData

	if uuid != "" {
		val, err := redisClient.Get(context.Background(), "session:"+uuid).Result()
		if err != nil {
			logrus.Errorf("Failed to get session from Redis for UUID %s: %v", uuid, err)
			return nil, fmt.Errorf("session not found")
		}
		err = json.Unmarshal([]byte(val), &session)
		logrus.Debugf("Retrieved session data for UUID %s: %+v", uuid, session)
		if err != nil {
			logrus.Errorf("Failed to unmarshal session data for UUID %s: %v", uuid, err)
			return nil, fmt.Errorf("failed to unmarshal session data")
		}

		// Initialize timeout fields if they don't exist (for backward compatibility)
		if session.CreatedAt.IsZero() {
			session.CreatedAt = time.Now()
			// TODO: Set a sensible default timeout duration if not set
			session.TimeoutDuration = time.Duration(SESSION_TIMEOUT) * time.Minute
		}

		if session.ExpireAt.IsZero() {
			// First time: set absolute expiration
			session.ExpireAt = time.Now().Add(session.TimeoutDuration)
		}

		query = url.Values{}
		for k, v := range session.ConnectionParams {
			query.Set(k, v)
		}

		// Check if this is a reconnection and clear the reconnect key
		ctx := context.Background()
		reconnectKey := fmt.Sprintf("reconnect:%s", uuid)
		exists, err := redisClient.Exists(ctx, reconnectKey).Result()
		if err == nil && exists > 0 {
			// This is a reconnection - clear the reconnect key to signal successful reconnection
			err = redisClient.Del(ctx, reconnectKey).Err()
			if err != nil {
				logrus.Errorf("Failed to clear reconnect key for %s: %v", uuid, err)
			} else {
				logrus.Infof("User reconnected to session %s, cleared reconnection window", uuid)
			}

			// Reset disconnection count but preserve existing timeout
			session.DisconnectionCount = 0

			// Get the current TTL from Redis (most accurate remaining time)
			sessionKey := fmt.Sprintf("session:%s", uuid)
			currentTTL, err := redisClient.TTL(ctx, sessionKey).Result()
			if err != nil || currentTTL <= 0 {
				// Fallback to calculating from ExpireAt if TTL fails
				if !session.ExpireAt.IsZero() {
					currentTTL = time.Until(session.ExpireAt)
					logrus.Warnf("Could not get TTL from Redis for session %s, calculated from ExpireAt: %v", uuid, currentTTL)
				} else {
					// Last resort fallback
					currentTTL = 10 * time.Minute
					logrus.Warnf("Could not determine remaining time for session %s, using default 10 minutes", uuid)
				}
			}

			// Ensure we don't use negative TTL
			if currentTTL <= 0 {
				currentTTL = 1 * time.Minute // Give at least 1 minute for reconnection
				logrus.Warnf("Session %s had expired or negative TTL, giving 1 minute grace period", uuid)
			}

			// Update session in Redis with the preserved TTL
			err = redis2.SetSessionData(redisClient, uuid, &session, currentTTL)
			if err != nil {
				logrus.Errorf("Failed to update session data for reconnection %s: %v", uuid, err)
			} else {
				logrus.Infof("Preserved session %s timeout (%v remaining) after reconnection", uuid, currentTTL.Round(time.Second))
			}
		} else {
			// Not a reconnection - use normal timeout logic
			remaining := redis2.GetRemainingTTL(&session)
			if remaining <= 0 {
				remaining = session.TimeoutDuration
			}
			err = redis2.SetSessionData(redisClient, uuid, &session, remaining)
			if err != nil {
				logrus.Errorf("Failed to update session data for %s: %v", uuid, err)
			}
		}
	} else {
		query = request.URL.Query()
	}

	// Check if we have stored parameters for this connection
	if uuid := query.Get("uuid"); uuid != "" {
		if storedParams, exists := tunnelStore.GetConnectionParams(uuid); exists {
			logrus.Debugf("Using stored parameters for UUID %s", uuid)
			// Use stored parameters instead of query parameters
			query = storedParams
		} else {
			logrus.Debugf("No stored parameters found for UUID %s", uuid)
		}
	}

	config.Protocol = query.Get("scheme")
	config.Parameters = map[string]string{}
	for k, v := range query {
		config.Parameters[k] = v[0]
	}

	var err error
	if query.Get("width") != "" {
		config.OptimalScreenWidth, err = strconv.Atoi(query.Get("width"))
		if err != nil || config.OptimalScreenWidth == 0 {
			logrus.Errorf("Invalid width value '%s': %v", query.Get("width"), err)
			config.OptimalScreenWidth = 800
		}
	}
	if query.Get("height") != "" {
		config.OptimalScreenHeight, err = strconv.Atoi(query.Get("height"))
		if err != nil || config.OptimalScreenHeight == 0 {
			logrus.Errorf("Invalid height value '%s': %v", query.Get("height"), err)
			config.OptimalScreenHeight = 600
		}
	}
	config.AudioMimetypes = []string{"audio/L16", "rate=44100", "channels=2"}

	logrus.Debugf("Attempting to connect to guacd at %s", guacdAddr)
	addr, err := net.ResolveTCPAddr("tcp", guacdAddr)
	if err != nil {
		logrus.Errorf("Failed to resolve guacd address %s: %v", guacdAddr, err)
		return nil, err
	}

	// Set connection timeout
	dialer := net.Dialer{
		Timeout: 60 * time.Second,
	}
	logrus.Debugf("Attempting to establish TCP connection to guacd at %s with timeout %v", addr.String(), dialer.Timeout)
	conn, err := dialer.Dial("tcp", addr.String())
	if err != nil {
		logrus.Errorf("Failed to connect to guacd at %s: %v", addr.String(), err)
		return nil, err
	}

	stream := guac2.NewStream(conn, guac2.SocketTimeout)
	logrus.Debugf("TCP connection established, created new stream with timeout %v", guac2.SocketTimeout)

	logrus.Debug("Successfully connected to guacd")
	if request.URL.Query().Get("uuid") != "" {
		config.ConnectionID = request.URL.Query().Get("uuid")
	}

	sanitisedCfg := config
	sanitisedCfg.Parameters = make(map[string]string, len(config.Parameters))
	for k, v := range config.Parameters {
		sanitisedCfg.Parameters[k] = v
	}

	if session.Share {
		sanitisedCfg.Parameters["password"] = "********"
	} else {
		sanitisedCfg.ConnectionID = ""
	}

	logrus.Debugf("Starting handshake with config: %#v", sanitisedCfg)

	// err = stream.Handshake(config)
	// if err != nil {
	//     logrus.Errorf("Handshake failed: %v", err)
	//     return nil, err
	// }

	// Add context with timeout for handshake
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	// Create a channel to handle the handshake
	handshakeDone := make(chan error, 1)
	go func() {
		handshakeDone <- stream.Handshake(config)
	}()

	// Wait for handshake with timeout
	select {
	case err := <-handshakeDone:
		if err != nil {
			logrus.Errorf("Handshake failed: %v. Connection details - Local: %s, Remote: %s",
				err,
				conn.LocalAddr().String(),
				conn.RemoteAddr().String())
			return nil, err
		}
	case <-ctx.Done():
		logrus.Errorf("Handshake timed out after 40 seconds. Connection details - Local: %s, Remote: %s",
			conn.LocalAddr().String(),
			conn.RemoteAddr().String())
		return nil, fmt.Errorf("handshake timed out: %v", ctx.Err())
	}

	logrus.Debug("Handshake completed successfully")

	tunnel := guac2.NewSimpleTunnel(stream)

	// Register the tunnel with its ConnectionID after handshake
	if tunnel != nil && tunnel.ConnectionID() != "" {
		// Add tunnel to the store
		tunnelStore.Add(tunnel.ConnectionID(), tunnel, nil)
		logrus.Debugf("Tunnel %s successfully added to active store", tunnel.ConnectionID())

		// Store connection parameters for future sharing
		if len(query) > 0 {
			tunnelStore.StoreConnectionParams(tunnel.ConnectionID(), query)
			logrus.Debugf("Stored connection parameters for future sharing of %s", tunnel.ConnectionID())
		}

		// Update the Redis session with the tunnel's ConnectionID as TunnelConnectionID
		if uuid != "" {
			err = redis2.UpdateSessionWithTunnelInfo(redisClient, uuid, tunnel.ConnectionID(), session.Share)
			if err != nil {
				logrus.Warnf("Failed to update session with tunnel ConnectionID: %v", err)
			} else {
				logrus.Infof("Updated session %s with tunnel ConnectionID %s", uuid, tunnel.ConnectionID())
			}
		}

		// Register session with cleanup service when Kubernetes integration is
		// available. Encore can run outside the target cluster, where the
		// cleanup service is intentionally unavailable.
		if cleanupService != nil {
			cleanupService.RegisterSession(
				session.ConnectionID,
				session.PodName,
				tunnel.ConnectionID(), // Pass the tunnel's ConnectionID
			)
		}

	} else if tunnel != nil {
		logrus.Warnf("Tunnel created but ConnectionID is empty. Not adding to store. UUID: %s", tunnel.GetUUID())
	} else {
		logrus.Error("Failed to create tunnel - tunnel is nil")
	}

	return tunnel, nil
}

func DoConnectShare(request http.Request, tunnelStore *guac2.ActiveTunnelStore, redisClient *redis.Client, guacdAddr string, cleanupService *cleanup.SessionCleanupService) (guac2.Tunnel, error) {
	config := guac2.ExistingGuacamoleConfiguration()
	var query url.Values
	uuid := request.URL.Query().Get("uuid")
	var storedConnectionID string
	var exists bool

	// Check if UUID is provided
	if uuid == "" {
		logrus.Warn("No UUID provided for shared connection")
		return nil, fmt.Errorf("no UUID provided")
	}

	// Get the stored connection ID
	if storedConnectionID, exists = redis2.GetTunnelConnectionID(redisClient, uuid); !exists {
		logrus.Debugf("No stored connection ID found for UUID %s", uuid)
		return nil, fmt.Errorf("no stored connection ID found for UUID %s", uuid)
	}

	// Validate that the connection ID still exists in the tunnel store
	if _, exists = tunnelStore.Get(storedConnectionID); !exists {
		logrus.Warnf("Shared connection %s no longer exists in tunnel store", storedConnectionID)
		return nil, fmt.Errorf("shared connection is no longer available")
	}

	// Get stored connection parameters
	query, exists = tunnelStore.GetConnectionParams(uuid)
	if !exists {
		logrus.Debugf("No stored parameters found for UUID %s", uuid)
		return nil, fmt.Errorf("no stored parameters found for UUID %s", uuid)
	}

	logrus.Debugf("Found existing connection ID %s for UUID %s", storedConnectionID, uuid)

	config.Protocol = "rdp"
	config.Parameters = map[string]string{}
	for k, v := range query {
		if k == "uuid" {
			config.Parameters[k] = uuid2.New().String()
			continue
		}
		config.Parameters[k] = v[0]
	}

	// Validate essential parameters
	if config.Protocol == "" {
		logrus.Error("Protocol not specified in connection parameters")
		return nil, fmt.Errorf("protocol not specified")
	}

	if query.Get("width") != "" {
		config.OptimalScreenWidth, _ = strconv.Atoi(query.Get("width"))
		if config.OptimalScreenWidth == 0 {
			config.OptimalScreenWidth = 800 // Default width
		}
	}
	if query.Get("height") != "" {
		config.OptimalScreenHeight, _ = strconv.Atoi(query.Get("height"))
		if config.OptimalScreenHeight == 0 {
			config.OptimalScreenHeight = 600 // Default height
		}
	}

	config.AudioMimetypes = []string{"audio/L16", "rate=44100", "channels=2"}

	logrus.Debugf("Attempting to connect to shared guacd at %s", guacdAddr)
	addr, err := net.ResolveTCPAddr("tcp", guacdAddr)
	if err != nil {
		logrus.Errorf("Failed to resolve guacd address: %v", err)
		return nil, err
	}

	// Set connection timeout
	dialer := net.Dialer{
		Timeout: 60 * time.Second,
	}
	logrus.Debugf("Attempting to establish TCP connection to guacd at %s with timeout %v", addr.String(), dialer.Timeout)

	conn, err := dialer.Dial("tcp", addr.String())
	if err != nil {
		logrus.Errorf("Failed to establish TCP connection to guacd: %v", err)
		return nil, err
	}

	stream := guac2.NewStream(conn, guac2.SocketTimeout)
	logrus.Debugf("TCP connection established, created new stream with timeout %v", guac2.SocketTimeout)

	logrus.Debug("Successfully connected to guacd for shared connection")

	// Set the connection ID for joining existing session
	config.ConnectionID = storedConnectionID

	logrus.Debugf("Starting handshake for shared connection with ID: %s", config.ConnectionID)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	// Create a channel to handle the handshake
	handshakeDone := make(chan error, 1)
	go func() {
		err := stream.Handshake(config)
		if err != nil {
			logrus.Errorf("Handshake error details: %v", err)
			// If handshake fails, the shared connection might be invalid
			if storedConnectionID != "" {
				logrus.Warnf("Shared connection %s appears to be invalid, removing from store", storedConnectionID)
				// tunnelStore.Remove(storedConnectionID)
			}
		}
		handshakeDone <- err
	}()

	select {
	case err := <-handshakeDone:
		if err != nil {
			err := conn.Close()
			if err != nil {
				logrus.Errorf("Failed to close connection after handshake error: %v", err)
			}
			logrus.Errorf("Failed to complete handshake for shared connection: %v", err)
			return nil, fmt.Errorf("failed to join shared connection: %v", err)
		}
		logrus.Debug("Handshake completed successfully for shared connection")
	case <-ctx.Done():
		err := conn.Close()
		if err != nil {
			logrus.Errorf("Failed to close connection after handshake error: %v", err)
		}
		logrus.Warn("Handshake timed out for shared connection")
		return nil, fmt.Errorf("handshake timed out for shared connection")
	}

	// Create the tunnel
	tunnel := guac2.NewSimpleTunnel(stream)

	if tunnel == nil {
		err := conn.Close()
		if err != nil {
			logrus.Errorf("Failed to close connection after creating shared tunnel: %v", err)
		}
		logrus.Error("Failed to create shared tunnel")
		return nil, fmt.Errorf("failed to create shared tunnel")
	}

	logrus.Infof("Successfully created shared tunnel for connection ID: %s", storedConnectionID)

	return tunnel, nil
}
