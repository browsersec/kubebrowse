package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	redis2 "github.com/browsersec/KubeBrowse/internal/redis"
)

func TestPreservedSessionTTL(t *testing.T) {
	deadline := time.Now().Add(5 * time.Minute)
	session := &redis2.SessionData{ExpireAt: deadline}

	for _, test := range []struct {
		name       string
		ttl        time.Duration
		err        error
		wantUsable bool
	}{
		{name: "redis ttl", ttl: time.Minute, wantUsable: true},
		{name: "redis failure uses expiration", err: errors.New("redis unavailable"), wantUsable: true},
		{name: "expired session", err: errors.New("redis unavailable"), ttl: 0, wantUsable: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			current := session
			if test.name == "expired session" {
				current = &redis2.SessionData{ExpireAt: time.Now().Add(-time.Minute)}
			}
			ttl, usable := preservedSessionTTL(current, test.ttl, test.err)
			if usable != test.wantUsable {
				t.Fatalf("usable = %t, want %t", usable, test.wantUsable)
			}
			if usable && ttl <= 0 {
				t.Fatalf("ttl = %v, want positive", ttl)
			}
		})
	}
}

func TestHandleCORS(t *testing.T) {
	state := &runtimeState{allowedOrigins: map[string]struct{}{"https://app.example": {}}}

	allowed := httptest.NewRecorder()
	allowedRequest := httptest.NewRequest(http.MethodOptions, "/tunnel", nil)
	allowedRequest.Header.Set("Origin", "https://app.example")
	if !state.handleCORS(allowed, allowedRequest) {
		t.Fatal("allowed preflight was not handled")
	}
	if allowed.Code != http.StatusNoContent {
		t.Fatalf("allowed status = %d, want %d", allowed.Code, http.StatusNoContent)
	}
	if got := allowed.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example" {
		t.Fatalf("allow origin = %q", got)
	}

	disallowed := httptest.NewRecorder()
	disallowedRequest := httptest.NewRequest(http.MethodOptions, "/tunnel", nil)
	disallowedRequest.Header.Set("Origin", "https://attacker.example")
	if !state.handleCORS(disallowed, disallowedRequest) {
		t.Fatal("disallowed preflight was not handled")
	}
	if disallowed.Code != http.StatusForbidden {
		t.Fatalf("disallowed status = %d, want %d", disallowed.Code, http.StatusForbidden)
	}
	if got := disallowed.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("disallowed origin unexpectedly allowed: %q", got)
	}
}
