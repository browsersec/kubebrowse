package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/browsersec/KubeBrowse/internal/httpx"
	"github.com/go-redis/redis/v8"
	"github.com/sirupsen/logrus"
)

// WebSocketMetrics represents WebSocket performance metrics for a session
type WebSocketMetrics struct {
	SessionID string `json:"session_id"`
	Timestamp string `json:"timestamp"`

	// RTT statistics in milliseconds
	RTT struct {
		Samples int      `json:"samples"`
		Avg     *float64 `json:"avg,omitempty"`
		Min     *float64 `json:"min,omitempty"`
		Max     *float64 `json:"max,omitempty"`
		P50     *float64 `json:"p50,omitempty"`
		P95     *float64 `json:"p95,omitempty"`
		P99     *float64 `json:"p99,omitempty"`
	} `json:"rtt"`

	// Traffic statistics
	Traffic struct {
		MessagesSent     int64 `json:"messages_sent"`
		MessagesReceived int64 `json:"messages_received"`
		BytesSent        int64 `json:"bytes_sent"`
		BytesReceived    int64 `json:"bytes_received"`
		TotalBytes       int64 `json:"total_bytes"`
	} `json:"traffic"`

	// Timing information
	Timing struct {
		ConnectionStartTime *int64 `json:"connection_start_time,omitempty"`
		LastMessageTime     *int64 `json:"last_message_time,omitempty"`
		ConnectionDuration  *int64 `json:"connection_duration_ms,omitempty"`
	} `json:"timing"`

	Errors int `json:"errors"`
}

// MetricsStore stores WebSocket metrics for active sessions
type MetricsStore struct {
	mu      sync.RWMutex
	metrics map[string]*WebSocketMetrics
	history map[string][]*WebSocketMetrics // Historical snapshots per session
}

// NewMetricsStore creates a new metrics store
func NewMetricsStore() *MetricsStore {
	return &MetricsStore{
		metrics: make(map[string]*WebSocketMetrics),
		history: make(map[string][]*WebSocketMetrics),
	}
}

// Store stores metrics for a session
func (s *MetricsStore) Store(sessionID string, metrics *WebSocketMetrics) {
	s.mu.Lock()
	defer s.mu.Unlock()

	metrics.Timestamp = time.Now().Format(time.RFC3339)
	s.metrics[sessionID] = metrics

	// Keep history (max 100 snapshots per session)
	if s.history[sessionID] == nil {
		s.history[sessionID] = make([]*WebSocketMetrics, 0)
	}
	s.history[sessionID] = append(s.history[sessionID], metrics)
	if len(s.history[sessionID]) > 100 {
		s.history[sessionID] = s.history[sessionID][len(s.history[sessionID])-100:]
	}
}

// Get retrieves metrics for a session
func (s *MetricsStore) Get(sessionID string) (*WebSocketMetrics, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.metrics[sessionID]
	return m, ok
}

// GetHistory retrieves metrics history for a session
func (s *MetricsStore) GetHistory(sessionID string) []*WebSocketMetrics {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.history[sessionID]
}

// GetAll retrieves all current metrics
func (s *MetricsStore) GetAll() map[string]*WebSocketMetrics {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]*WebSocketMetrics)
	for k, v := range s.metrics {
		result[k] = v
	}
	return result
}

// Delete removes metrics for a session
func (s *MetricsStore) Delete(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.metrics, sessionID)
	delete(s.history, sessionID)
}

// Global metrics store instance
var GlobalMetricsStore = NewMetricsStore()

// HandlerReportWebSocketMetrics handles POST /sessions/:connectionID/metrics
// Frontend clients report their WebSocket metrics to this endpoint
// @Summary Report WebSocket metrics for a session
// @Description Frontend clients report WebSocket RTT and traffic metrics
// @Tags metrics
// @Accept json
// @Produce json
// @Param connectionID path string true "Connection/Session ID"
// @Param metrics body WebSocketMetrics true "WebSocket Metrics"
// @Success 200 {object} ErrorResponse
// @Failure 400 {object} ErrorResponse
// @Router /sessions/{connectionID}/metrics [post]
func HandlerReportWebSocketMetrics(c *httpx.Context, redisClient *redis.Client) {
	connectionID := c.Param("connectionID")
	if connectionID == "" {
		c.JSON(http.StatusBadRequest, httpx.H{"error": "Connection ID is required"})
		return
	}

	var metrics WebSocketMetrics
	if err := c.ShouldBindJSON(&metrics); err != nil {
		logrus.Errorf("Failed to bind metrics request: %v", err)
		c.JSON(http.StatusBadRequest, httpx.H{"error": "Invalid metrics data"})
		return
	}

	metrics.SessionID = connectionID
	metrics.Timestamp = time.Now().Format(time.RFC3339)

	// Store in memory
	GlobalMetricsStore.Store(connectionID, &metrics)

	// Also store in Redis for persistence (with 1-hour TTL)
	if redisClient != nil {
		metricsJSON, err := json.Marshal(metrics)
		if err == nil {
			key := "metrics:websocket:" + connectionID
			err = redisClient.Set(context.Background(), key, metricsJSON, time.Hour).Err()
			if err != nil {
				logrus.Warnf("Failed to store metrics in Redis: %v", err)
			}
		}
	}

	logrus.Debugf("Received WebSocket metrics for session %s: RTT samples=%d, msgs sent=%d, msgs recv=%d",
		connectionID, metrics.RTT.Samples, metrics.Traffic.MessagesSent, metrics.Traffic.MessagesReceived)

	c.JSON(http.StatusOK, httpx.H{
		"message":    "Metrics recorded",
		"session_id": connectionID,
	})
}

// HandlerGetWebSocketMetrics handles GET /sessions/:connectionID/metrics
// Returns WebSocket metrics for a specific session
// @Summary Get WebSocket metrics for a session
// @Description Retrieve WebSocket RTT and traffic metrics for a session
// @Tags metrics
// @Produce json
// @Param connectionID path string true "Connection/Session ID"
// @Success 200 {object} WebSocketMetrics
// @Failure 404 {object} ErrorResponse
// @Router /sessions/{connectionID}/metrics [get]
func HandlerGetWebSocketMetrics(c *httpx.Context, redisClient *redis.Client) {
	connectionID := c.Param("connectionID")
	if connectionID == "" {
		c.JSON(http.StatusBadRequest, httpx.H{"error": "Connection ID is required"})
		return
	}

	// Try to get from memory first
	metrics, found := GlobalMetricsStore.Get(connectionID)
	if found {
		c.JSON(http.StatusOK, metrics)
		return
	}

	// Try Redis if not in memory
	if redisClient != nil {
		key := "metrics:websocket:" + connectionID
		metricsJSON, err := redisClient.Get(context.Background(), key).Result()
		if err == nil {
			var metrics WebSocketMetrics
			if err := json.Unmarshal([]byte(metricsJSON), &metrics); err == nil {
				c.JSON(http.StatusOK, metrics)
				return
			}
		}
	}

	c.JSON(http.StatusNotFound, httpx.H{"error": "Metrics not found for session"})
}

// HandlerGetWebSocketMetricsHistory handles GET /sessions/:connectionID/metrics/history
// Returns historical WebSocket metrics for a session
// @Summary Get WebSocket metrics history for a session
// @Description Retrieve historical WebSocket RTT and traffic metrics snapshots
// @Tags metrics
// @Produce json
// @Param connectionID path string true "Connection/Session ID"
// @Success 200 {array} WebSocketMetrics
// @Failure 404 {object} ErrorResponse
// @Router /sessions/{connectionID}/metrics/history [get]
func HandlerGetWebSocketMetricsHistory(c *httpx.Context) {
	connectionID := c.Param("connectionID")
	if connectionID == "" {
		c.JSON(http.StatusBadRequest, httpx.H{"error": "Connection ID is required"})
		return
	}

	history := GlobalMetricsStore.GetHistory(connectionID)
	if len(history) == 0 {
		c.JSON(http.StatusNotFound, httpx.H{"error": "No metrics history found for session"})
		return
	}

	c.JSON(http.StatusOK, history)
}

// HandlerGetAllWebSocketMetrics handles GET /metrics/websocket
// Returns all current WebSocket metrics for all sessions
// @Summary Get all WebSocket metrics
// @Description Retrieve WebSocket metrics for all active sessions (for benchmarking)
// @Tags metrics
// @Produce json
// @Success 200 {object} map[string]WebSocketMetrics
// @Router /metrics/websocket [get]
func HandlerGetAllWebSocketMetrics(c *httpx.Context) {
	allMetrics := GlobalMetricsStore.GetAll()
	c.JSON(http.StatusOK, allMetrics)
}

// WebSocketMetricsSummary provides aggregate metrics across all sessions
type WebSocketMetricsSummary struct {
	TotalSessions     int      `json:"total_sessions"`
	TotalRTTSamples   int      `json:"total_rtt_samples"`
	AvgRTT            *float64 `json:"avg_rtt_ms,omitempty"`
	MinRTT            *float64 `json:"min_rtt_ms,omitempty"`
	MaxRTT            *float64 `json:"max_rtt_ms,omitempty"`
	P95RTT            *float64 `json:"p95_rtt_ms,omitempty"`
	TotalBytesSent    int64    `json:"total_bytes_sent"`
	TotalBytesRecv    int64    `json:"total_bytes_received"`
	TotalMessagesSent int64    `json:"total_messages_sent"`
	TotalMessagesRecv int64    `json:"total_messages_received"`
	Timestamp         string   `json:"timestamp"`
}

// HandlerGetWebSocketMetricsSummary handles GET /metrics/websocket/summary
// Returns aggregated WebSocket metrics summary
// @Summary Get WebSocket metrics summary
// @Description Retrieve aggregated WebSocket metrics across all sessions
// @Tags metrics
// @Produce json
// @Success 200 {object} WebSocketMetricsSummary
// @Router /metrics/websocket/summary [get]
func HandlerGetWebSocketMetricsSummary(c *httpx.Context) {
	allMetrics := GlobalMetricsStore.GetAll()

	summary := WebSocketMetricsSummary{
		TotalSessions: len(allMetrics),
		Timestamp:     time.Now().Format(time.RFC3339),
	}

	var allRTTs []float64
	var sumRTT float64

	for _, m := range allMetrics {
		summary.TotalMessagesSent += m.Traffic.MessagesSent
		summary.TotalMessagesRecv += m.Traffic.MessagesReceived
		summary.TotalBytesSent += m.Traffic.BytesSent
		summary.TotalBytesRecv += m.Traffic.BytesReceived
		summary.TotalRTTSamples += m.RTT.Samples

		// Collect RTT values for aggregate stats
		if m.RTT.Avg != nil {
			allRTTs = append(allRTTs, *m.RTT.Avg)
			sumRTT += *m.RTT.Avg
		}
		if m.RTT.Min != nil {
			if summary.MinRTT == nil || *m.RTT.Min < *summary.MinRTT {
				summary.MinRTT = m.RTT.Min
			}
		}
		if m.RTT.Max != nil {
			if summary.MaxRTT == nil || *m.RTT.Max > *summary.MaxRTT {
				summary.MaxRTT = m.RTT.Max
			}
		}
	}

	// Calculate aggregate averages
	if len(allRTTs) > 0 {
		avg := sumRTT / float64(len(allRTTs))
		summary.AvgRTT = &avg

		// Sort for P95
		// Simple P95 calculation (average of top 5% removed)
		if len(allRTTs) >= 20 {
			// Sort RTTs
			sorted := make([]float64, len(allRTTs))
			copy(sorted, allRTTs)
			for i := 0; i < len(sorted); i++ {
				for j := i + 1; j < len(sorted); j++ {
					if sorted[i] > sorted[j] {
						sorted[i], sorted[j] = sorted[j], sorted[i]
					}
				}
			}
			p95Idx := int(float64(len(sorted)) * 0.95)
			if p95Idx >= len(sorted) {
				p95Idx = len(sorted) - 1
			}
			p95 := sorted[p95Idx]
			summary.P95RTT = &p95
		}
	}

	c.JSON(http.StatusOK, summary)
}
