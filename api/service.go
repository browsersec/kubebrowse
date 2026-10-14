package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/browsersec/KubeBrowse/internal/cleanup"
	guac2 "github.com/browsersec/KubeBrowse/internal/guac"
	"github.com/browsersec/KubeBrowse/internal/httpx"
	k8s2 "github.com/browsersec/KubeBrowse/internal/k8s"
	"github.com/browsersec/KubeBrowse/internal/logging"
	minio2 "github.com/browsersec/KubeBrowse/internal/minio"
	redis2 "github.com/browsersec/KubeBrowse/internal/redis"
	"github.com/go-redis/redis/v8"
	"github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// runtimeState contains dependencies that used to be initialized by the Gin
// main package. Encore owns the HTTP server; this state only owns application
// resources and is initialized on the first request.
type runtimeState struct {
	redisClient    *redis.Client
	k8sClient      *kubernetes.Clientset
	k8sNamespace   string
	guacdAddr      string
	clamavAddr     string
	minioClient    *minio2.MinioClient
	minioBucket    string
	browserImage   string
	officeImage    string
	tunnelStore    *guac2.ActiveTunnelStore
	servlet        *guac2.Server
	servletShared  *guac2.Server
	wsServer       *guac2.WebsocketServer
	wsServerShared *guac2.WebsocketServer
	cleanupService *cleanup.SessionCleanupService
	allowedOrigins map[string]struct{}
}

var (
	runtimeOnce sync.Once
	runtimeApp  *runtimeState
)

func application() *runtimeState {
	runtimeOnce.Do(func() {
		runtimeApp = initializeApplication()
	})
	return runtimeApp
}

func initializeApplication() *runtimeState {
	logging.Init(logrus.TraceLevel)

	state := &runtimeState{
		redisClient:    redis2.InitRedis(),
		k8sNamespace:   environmentOr("KUBERNETES_NAMESPACE", "browser-sandbox"),
		guacdAddr:      environmentOr("GUACD_ADDRESS", "0.0.0.0:4822"),
		clamavAddr:     environmentOr("CLAMAV_ADDRESS", "http://clamd-api.browser-sandbox.svc.cluster.local:3000"),
		minioBucket:    environmentOr("MINIO_BUCKET", "kubebrowse-files"),
		tunnelStore:    guac2.NewActiveTunnelStore(),
		allowedOrigins: allowedOrigins(),
	}

	state.browserImage = os.Getenv("BROWSER_IMAGE")
	if state.browserImage == "" {
		state.browserImage = k8s2.DefaultBrowserImage()
	}
	state.officeImage = os.Getenv("OFFICE_IMAGE")
	if state.officeImage == "" {
		state.officeImage = k8s2.DefaultOfficeImage()
	}

	state.k8sClient = newKubernetesClient()
	state.minioClient = newMinioClient(state.minioBucket)

	doConnect := func(request *http.Request) (guac2.Tunnel, error) {
		return DemoDoConnect(request, state.tunnelStore, state.redisClient, state.guacdAddr, state.cleanupService)
	}
	state.servlet = guac2.NewServer(doConnect)
	state.wsServer = guac2.NewWebsocketServer(doConnect)

	doSharedConnect := func(request *http.Request) (guac2.Tunnel, error) {
		return DoConnectShare(*request, state.tunnelStore, state.redisClient, state.guacdAddr, state.cleanupService)
	}
	state.servletShared = guac2.NewServer(doSharedConnect)
	state.wsServerShared = guac2.NewWebsocketServer(doSharedConnect)

	if state.k8sClient != nil {
		state.cleanupService = cleanup.NewSessionCleanupService(
			state.redisClient,
			state.k8sClient,
			state.k8sNamespace,
			state.tunnelStore,
			state.servlet,
		)
		state.cleanupService.Start()
	}

	state.wsServer.OnDisconnect = state.onDisconnect
	state.wsServer.CheckOrigin = state.isAllowedOrigin
	state.wsServerShared.CheckOrigin = state.isAllowedOrigin
	return state
}

func environmentOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func allowedOrigins() map[string]struct{} {
	origins := os.Getenv("CORS_ALLOWED_ORIGINS")
	if origins == "" {
		origins = os.Getenv("FRONTEND_URL")
	}
	if origins == "" {
		origins = "http://localhost:5173,http://localhost:3000,http://localhost:8080"
	}

	allowed := make(map[string]struct{})
	for _, origin := range strings.Split(origins, ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			allowed[origin] = struct{}{}
		}
	}
	return allowed
}

func newKubernetesClient() *kubernetes.Clientset {
	config, err := rest.InClusterConfig()
	if err != nil {
		kubeconfigPath := os.Getenv("KUBECONFIG")
		if kubeconfigPath == "" {
			if homeDir, homeErr := os.UserHomeDir(); homeErr == nil {
				kubeconfigPath = filepath.Join(homeDir, ".kube", "config")
			}
		}
		if kubeconfigPath != "" {
			config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		}
	}
	if err != nil || config == nil {
		logrus.Warnf("Kubernetes client is unavailable: %v", err)
		return nil
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		logrus.Warnf("Could not initialize Kubernetes client: %v", err)
		return nil
	}
	return client
}

func newMinioClient(bucket string) *minio2.MinioClient {
	accessKey := os.Getenv("MINIO_ACCESS_KEY")
	secretKey := os.Getenv("MINIO_SECRET_KEY")
	if accessKey == "" || secretKey == "" {
		logrus.Warn("MinIO credentials are not configured; uploads will bypass MinIO")
		return nil
	}
	client, err := minio2.NewMinioClient(environmentOr("MINIO_ENDPOINT", "minio.browser-sandbox.svc.cluster.local:9000"), accessKey, secretKey, false)
	if err != nil {
		logrus.Warnf("Could not initialize MinIO client: %v", err)
		return nil
	}
	if err := client.CreateBucket(context.Background(), bucket, "us-east-1"); err != nil {
		logrus.Warnf("Could not initialize MinIO bucket %q: %v", bucket, err)
	}
	return client
}

func (state *runtimeState) onDisconnect(connectionID string, request *http.Request, tunnel guac2.Tunnel) {
	uuid := request.URL.Query().Get("uuid")
	if uuid == "" || state.k8sClient == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := redis2.GetSessionDataWithContext(ctx, state.redisClient, uuid)
	if err != nil {
		logrus.Warnf("Could not get disconnected session %s: %v", uuid, err)
		return
	}
	reportedTTL, ttlErr := redis2.GetCurrentSessionTTL(ctx, state.redisClient, uuid)
	ttl, ok := preservedSessionTTL(session, reportedTTL, ttlErr)
	if !ok {
		logrus.Warnf("Not recording disconnect for expired session %s", uuid)
		return
	}
	session.DisconnectionCount++
	if err := redis2.SetSessionDataWithContext(ctx, state.redisClient, uuid, session, ttl); err != nil {
		logrus.Warnf("Could not update disconnected session %s: %v", uuid, err)
	}

	reconnectKey := fmt.Sprintf("reconnect:%s", uuid)
	if err := state.redisClient.Set(ctx, reconnectKey, "pending", 2*time.Minute).Err(); err != nil {
		logrus.Warnf("Could not create reconnect window for %s: %v", uuid, err)
		return
	}
	podName := session.PodName
	go func() {
		time.Sleep(2 * time.Minute)
		background, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), 30*time.Second)
		defer cancel()
		exists, err := state.redisClient.Exists(background, reconnectKey).Result()
		if err != nil || exists == 0 {
			return
		}
		if err := k8s2.DeletePodGrace(state.k8sClient, state.k8sNamespace, podName); err != nil {
			logrus.Warnf("Could not delete disconnected pod %s: %v", podName, err)
		}
		state.tunnelStore.Delete(connectionID, request, tunnel)
		state.redisClient.Del(background, "session:"+uuid, reconnectKey)
	}()
}

func preservedSessionTTL(session *redis2.SessionData, ttl time.Duration, err error) (time.Duration, bool) {
	if err == nil && ttl > 0 {
		return ttl, true
	}
	if session.ExpireAt.IsZero() {
		return 0, false
	}
	ttl = time.Until(session.ExpireAt)
	return ttl, ttl > 0
}

func (state *runtimeState) isAllowedOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if origin == "" {
		return true
	}
	_, allowed := state.allowedOrigins[origin]
	return allowed
}

// handleCORS writes CORS headers for approved browser origins. It returns true
// when it has completed an OPTIONS request and the endpoint must not continue.
func (state *runtimeState) handleCORS(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	if !state.isAllowedOrigin(r) {
		if r.Method == http.MethodOptions {
			http.Error(w, "origin is not allowed", http.StatusForbidden)
			return true
		}
		return false
	}

	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Vary", "Origin")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Origin, Content-Length, Content-Type, Authorization")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	return false
}

func (state *runtimeState) handle(w http.ResponseWriter, r *http.Request, params map[string]string, handler httpx.HandlerFunc) {
	if state.handleCORS(w, r) {
		return
	}

	context := httpx.NewContext(w, r, params)
	started := time.Now()
	defer func() {
		if recovered := recover(); recovered != nil {
			logrus.Errorf("panic handling %s %s: %v\n%s", r.Method, r.URL.Path, recovered, debug.Stack())
			if context.Writer.Status() == http.StatusOK {
				context.JSON(http.StatusInternalServerError, httpx.H{"error": "Internal server error"})
			}
		}
		logrus.WithFields(logrus.Fields{
			"method":  r.Method,
			"path":    r.URL.Path,
			"status":  context.Writer.Status(),
			"latency": time.Since(started).String(),
			"client":  context.ClientIP(),
		}).Info("handled request")
	}()
	handler(context)
}
