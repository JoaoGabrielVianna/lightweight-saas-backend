package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// Pins the F1 closure: per-IP token bucket on /admin/*. The middleware must
// admit traffic up to `burst`, reject the next request with 429, and admit
// requests from a different IP independently.

func TestRateLimitPerIP_AllowsBurstThenRejects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Tight settings so the test doesn't have to wait for refill.
	r.Use(RateLimitPerIP(1, 3))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	send := func() int {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = "203.0.113.1:1234"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}

	// Burst of 3 must pass.
	for i := 1; i <= 3; i++ {
		if code := send(); code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200 (within burst)", i, code)
		}
	}
	// 4th request from the same IP must be rejected.
	if code := send(); code != http.StatusTooManyRequests {
		t.Fatalf("request 4: status = %d, want 429 (burst exhausted)", code)
	}
}

func TestRateLimitPerIP_IsolatesByIP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RateLimitPerIP(1, 1))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	sendFrom := func(remote string) int {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = remote
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}

	// Exhaust IP-A.
	if code := sendFrom("203.0.113.1:1"); code != http.StatusOK {
		t.Fatalf("first request from A: status = %d, want 200", code)
	}
	if code := sendFrom("203.0.113.1:2"); code != http.StatusTooManyRequests {
		t.Fatalf("second request from A: status = %d, want 429", code)
	}
	// IP-B has its own bucket — must still admit.
	if code := sendFrom("203.0.113.2:1"); code != http.StatusOK {
		t.Fatalf("first request from B: status = %d, want 200 (separate bucket)", code)
	}
}

func TestRateLimitPerIP_RespectsXForwardedFor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RateLimitPerIP(1, 1))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	send := func(xff string) int {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = "10.0.0.1:1" // loopback proxy
		req.Header.Set("X-Forwarded-For", xff)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}

	// Behind a proxy, two distinct client IPs in XFF must be bucketed
	// separately even though RemoteAddr is identical.
	if code := send("198.51.100.1"); code != http.StatusOK {
		t.Fatalf("first XFF=A: status = %d, want 200", code)
	}
	if code := send("198.51.100.1, 10.0.0.1"); code != http.StatusTooManyRequests {
		t.Fatalf("second XFF=A: status = %d, want 429 (same client through proxy)", code)
	}
	if code := send("198.51.100.2"); code != http.StatusOK {
		t.Fatalf("first XFF=B: status = %d, want 200 (distinct client through same proxy)", code)
	}
}

func TestRateLimitPerIP_429HasJSONBodyAndRetryAfter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RateLimitPerIP(1, 1))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	send := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = "203.0.113.9:1"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	_ = send() // burn the first token
	w := send()
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("Retry-After header missing on 429")
	}
	if !strings.Contains(w.Body.String(), "rate limit") {
		t.Errorf("body = %q, want JSON error mentioning rate limit", w.Body.String())
	}
}

func TestClientIP_PrefersXForwardedForLeftmost(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	var seen string
	r.GET("/x", func(c *gin.Context) {
		seen = clientIP(c)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.RemoteAddr = "10.0.0.1:1"
	req.Header.Set("X-Forwarded-For", "198.51.100.5, 10.0.0.1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if seen != "198.51.100.5" {
		t.Errorf("clientIP = %q, want leftmost XFF entry", seen)
	}
}

func TestClientIP_FallsBackToRemoteAddr(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	var seen string
	r.GET("/x", func(c *gin.Context) {
		seen = clientIP(c)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.RemoteAddr = "203.0.113.99:5555"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if seen != "203.0.113.99" {
		t.Errorf("clientIP = %q, want stripped RemoteAddr IP", seen)
	}
}
