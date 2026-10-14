package api

import (
	"net/http"
	"strings"

	"github.com/browsersec/KubeBrowse/internal/httpx"
)

func connectionID(path, suffix string) string {
	_, value, found := strings.Cut(path, "/sessions/")
	if !found {
		return ""
	}
	value = strings.TrimSuffix(value, suffix)
	return strings.Trim(value, "/")
}

// ListSessions returns the active Guacamole tunnel IDs.
//
//encore:api public raw method=GET path=/sessions
func ListSessions(w http.ResponseWriter, r *http.Request) {
	state := application()
	state.handle(w, r, nil, func(context *httpx.Context) {
		HandlerSession(context, state.tunnelStore)
	})
}

// DeployBrowserSession creates a browser sandbox session.
//
//encore:api public raw method=POST path=/api/v1/sessions/browser
func DeployBrowserSession(w http.ResponseWriter, r *http.Request) {
	state := application()
	state.handle(w, r, nil, func(context *httpx.Context) {
		DeployBrowser(context, state.k8sClient, state.k8sNamespace, state.redisClient, state.tunnelStore, state.browserImage)
	})
}

// DeployOfficeSession creates an office sandbox session.
//
//encore:api public raw method=POST path=/api/v1/sessions/office
func DeployOfficeSession(w http.ResponseWriter, r *http.Request) {
	state := application()
	state.handle(w, r, nil, func(context *httpx.Context) {
		DeployOffice(context, state.k8sClient, state.k8sNamespace, state.redisClient, state.tunnelStore, state.officeImage)
	})
}

// GetConnection returns a Guacamole WebSocket URL for a session.
//
//encore:api public raw method=GET path=/api/v1/sessions/:connectionID/connect
func GetConnection(w http.ResponseWriter, r *http.Request) {
	state := application()
	id := connectionID(r.URL.Path, "/connect")
	state.handle(w, r, map[string]string{"connectionID": id}, func(context *httpx.Context) {
		HandlerConnectionID(context, state.tunnelStore, state.redisClient)
	})
}

// ShareConnection enables sharing for a session.
//
//encore:api public raw method=GET path=/api/v1/sessions/:connectionID/share
func ShareConnection(w http.ResponseWriter, r *http.Request) {
	state := application()
	id := connectionID(r.URL.Path, "/share")
	state.handle(w, r, map[string]string{"connectionID": id}, func(context *httpx.Context) {
		HandlerShareSession(context, state.tunnelStore, state.redisClient)
	})
}

// CreateBrowserPod creates a browser pod without a tunnel session.
//
//encore:api public raw method=POST path=/api/v1/pods/browser
func CreateBrowserPod(w http.ResponseWriter, r *http.Request) {
	state := application()
	state.handle(w, r, nil, func(context *httpx.Context) {
		HandlerBrowserPod(context, state.tunnelStore, state.k8sClient, state.k8sNamespace, state.browserImage)
	})
}

// CreateOfficePod creates an office pod without a tunnel session.
//
//encore:api public raw method=POST path=/api/v1/pods/office
func CreateOfficePod(w http.ResponseWriter, r *http.Request) {
	state := application()
	state.handle(w, r, nil, func(context *httpx.Context) {
		HandlerOfficePod(context, state.tunnelStore, state.k8sClient, state.k8sNamespace, state.officeImage)
	})
}

// StopSession closes a running session.
//
//encore:api public raw method=DELETE path=/sessions/:connectionID/stop
func StopSession(w http.ResponseWriter, r *http.Request) {
	state := application()
	id := connectionID(r.URL.Path, "/stop")
	state.handle(w, r, map[string]string{"connectionID": id}, func(context *httpx.Context) {
		HandlerStopWSSession(context, state.redisClient, state.k8sClient, state.servlet)
	})
}

// ExtendSession extends the timeout for a session.
//
//encore:api public raw method=POST path=/sessions/:connectionID/extend
func ExtendSession(w http.ResponseWriter, r *http.Request) {
	state := application()
	id := connectionID(r.URL.Path, "/extend")
	state.handle(w, r, map[string]string{"connectionID": id}, func(context *httpx.Context) {
		HandlerExtendSession(context, state.redisClient, state.cleanupService)
	})
}

// GetSessionTimeLeft returns remaining session time.
//
//encore:api public raw method=GET path=/sessions/:connectionID/time-left
func GetSessionTimeLeft(w http.ResponseWriter, r *http.Request) {
	state := application()
	id := connectionID(r.URL.Path, "/time-left")
	state.handle(w, r, map[string]string{"connectionID": id}, func(context *httpx.Context) {
		HandlerGetSessionTimeLeft(context, state.redisClient)
	})
}

// ReportWebSocketMetrics stores client-reported WebSocket metrics.
//
//encore:api public raw method=POST path=/sessions/:connectionID/metrics
func ReportWebSocketMetrics(w http.ResponseWriter, r *http.Request) {
	state := application()
	id := connectionID(r.URL.Path, "/metrics")
	state.handle(w, r, map[string]string{"connectionID": id}, func(context *httpx.Context) {
		HandlerReportWebSocketMetrics(context, state.redisClient)
	})
}

// GetWebSocketMetrics returns the latest metrics for a session.
//
//encore:api public raw method=GET path=/sessions/:connectionID/metrics
func GetWebSocketMetrics(w http.ResponseWriter, r *http.Request) {
	state := application()
	id := connectionID(r.URL.Path, "/metrics")
	state.handle(w, r, map[string]string{"connectionID": id}, func(context *httpx.Context) {
		HandlerGetWebSocketMetrics(context, state.redisClient)
	})
}

// GetWebSocketMetricsHistory returns historic session metrics.
//
//encore:api public raw method=GET path=/sessions/:connectionID/metrics/history
func GetWebSocketMetricsHistory(w http.ResponseWriter, r *http.Request) {
	state := application()
	id := connectionID(r.URL.Path, "/metrics/history")
	state.handle(w, r, map[string]string{"connectionID": id}, HandlerGetWebSocketMetricsHistory)
}

// UploadFile accepts a multipart file for a running session.
//
//encore:api public raw method=POST path=/sessions/:connectionID/upload
func UploadFile(w http.ResponseWriter, r *http.Request) {
	state := application()
	id := connectionID(r.URL.Path, "/upload")
	state.handle(w, r, map[string]string{"connectionID": id}, func(context *httpx.Context) {
		if state.minioClient == nil {
			HandlerUploadFileWithoutMinio(context, state.redisClient, state.k8sClient, state.clamavAddr, 10)
			return
		}
		HandlerUploadFile(context, state.redisClient, state.k8sClient, state.minioClient.Client, state.minioBucket, state.clamavAddr, 10)
	})
}

// ListWebSocketMetrics returns metrics for every active session.
//
//encore:api public raw method=GET path=/metrics/websocket
func ListWebSocketMetrics(w http.ResponseWriter, r *http.Request) {
	state := application()
	state.handle(w, r, nil, HandlerGetAllWebSocketMetrics)
}

// GetWebSocketMetricsSummary returns aggregate WebSocket metrics.
//
//encore:api public raw method=GET path=/metrics/websocket/summary
func GetWebSocketMetricsSummary(w http.ResponseWriter, r *http.Request) {
	state := application()
	state.handle(w, r, nil, HandlerGetWebSocketMetricsSummary)
}

// Tunnel handles Guacamole HTTP tunnel traffic.
//
//encore:api public raw path=/tunnel
func Tunnel(w http.ResponseWriter, r *http.Request) {
	state := application()
	if state.handleCORS(w, r) {
		return
	}
	state.servlet.ServeHTTP(w, r)
}

// TunnelPath handles Guacamole HTTP tunnel subpaths.
//
//encore:api public raw path=/tunnel/*path
func TunnelPath(w http.ResponseWriter, r *http.Request) {
	state := application()
	if state.handleCORS(w, r) {
		return
	}
	state.servlet.ServeHTTP(w, r)
}

// WebsocketTunnel upgrades the regular Guacamole display WebSocket.
//
//encore:api public raw method=GET path=/websocket-tunnel
func WebsocketTunnel(w http.ResponseWriter, r *http.Request) {
	application().wsServer.ServeHTTP(w, r)
}

// SharedTunnel handles shared Guacamole HTTP tunnel traffic.
//
//encore:api public raw path=/shared-tunnel
func SharedTunnel(w http.ResponseWriter, r *http.Request) {
	state := application()
	if state.handleCORS(w, r) {
		return
	}
	state.servletShared.ServeHTTP(w, r)
}

// SharedTunnelPath handles shared Guacamole HTTP tunnel subpaths.
//
//encore:api public raw path=/shared-tunnel/*path
func SharedTunnelPath(w http.ResponseWriter, r *http.Request) {
	state := application()
	if state.handleCORS(w, r) {
		return
	}
	state.servletShared.ServeHTTP(w, r)
}

// SharedWebsocketTunnel upgrades the shared display WebSocket.
//
//encore:api public raw method=GET path=/websocket-tunnel/share
func SharedWebsocketTunnel(w http.ResponseWriter, r *http.Request) {
	application().wsServerShared.ServeHTTP(w, r)
}
