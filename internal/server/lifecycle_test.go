package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/config"
	"github.com/gin-gonic/gin"
)

// Graceful shutdown, exercised against a real listener.
//
// These start an actual HTTP server on a real port and talk to it over TCP.
// A test that called Shutdown on an http.Server and asserted it returned would
// prove the standard library works; what has to be proven here is the ORDER —
// readiness drops first, in-flight requests are allowed to finish, and the
// process stops accepting afterwards. Only a running server can show that.
//
// The trigger is a cancelled context rather than a signal. `serve` takes the
// context precisely so a test can drive the whole sequence without signalling
// anything: sending SIGTERM inside `go test` would hit the test binary, and
// through it whatever runs the test binary. TestSignal_TriggersDrain covers the
// signal wiring itself, on a process the test creates.

// testServer starts a server on an ephemeral port and returns its base URL plus
// a function that triggers the drain and waits for it.
// withoutDatabase makes a test server report an unreachable dependency, so a
// test can assert that liveness and readiness diverge.
func withoutDatabase(s *Server) { s.ready.ping = nil }

func testServer(t *testing.T, handler func(*gin.Engine), opts ...func(*Server)) (base string, shutdown func() error) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	s := &Server{
		router:  gin.New(),
		cfg:     &config.Config{ShutdownTimeoutSeconds: 5},
		ready:   newReadyState(nil),
		started: make(chan struct{}),
	}
	// A database that answers. The lifecycle assertions are about ORDERING —
	// when readiness flips relative to the listener closing — and standing up
	// PostgreSQL to prove that would make the ordering test depend on a
	// service. The database check itself is covered by its own tests.
	s.ready.ping = func(context.Context) error { return nil }
	for _, opt := range opts {
		opt(s)
	}

	s.router.GET("/health/live", livenessHandler)
	s.router.GET("/health/ready", readinessHandler(s.ready))
	handler(s.router)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.serve(ctx, "127.0.0.1:0") }()

	select {
	case <-s.Started():
	case err := <-done:
		t.Fatalf("server exited before it started listening: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start listening within 5s")
	}

	stopped := false
	shutdown = func() error {
		if stopped {
			return nil
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(30 * time.Second):
			t.Fatal("server did not finish draining within 30s")
			return nil
		}
	}
	t.Cleanup(func() { _ = shutdown() })

	return "http://" + s.Addr(), shutdown
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return 0, err.Error()
	}
	defer func() { _ = resp.Body.Close() }()

	buf := make([]byte, 2048)
	n, _ := resp.Body.Read(buf)
	return resp.StatusCode, string(buf[:n])
}

// TestLifecycle_ServesAndDrains is the required end-to-end shutdown proof.
//
// It covers, in one run, every step the mission lists: a server is listening, a
// slow request is in flight, shutdown begins, readiness reports 503 while the
// slow request is STILL running, the slow request completes with its real
// response, and the process finishes within the bound.
func TestLifecycle_ServesAndDrains(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})

	base, shutdown := testServer(t, func(r *gin.Engine) {
		r.GET("/slow", func(c *gin.Context) {
			close(requestStarted)
			<-releaseRequest
			c.JSON(http.StatusOK, gin.H{"finished": true})
		})
	})

	// Ready before anything happens.
	if code, body := get(t, base+"/health/ready"); code != http.StatusOK {
		t.Fatalf("readiness before shutdown = %d (%s), want 200", code, body)
	}

	// A request that will still be running when the signal arrives.
	type result struct {
		code int
		body string
	}
	slow := make(chan result, 1)
	go func() {
		code, body := get(t, base+"/slow")
		slow <- result{code, body}
	}()
	<-requestStarted

	// Begin the drain, and let it run concurrently: the assertions below have
	// to happen WHILE it is in progress.
	drained := make(chan error, 1)
	go func() { drained <- shutdown() }()

	// Readiness must flip to 503 well before the process exits. This is the
	// property that lets a load balancer take the instance out of rotation
	// rather than have connections refused.
	deadline := time.Now().Add(3 * time.Second)
	var lastCode int
	for time.Now().Before(deadline) {
		code, _ := get(t, base+"/health/ready")
		lastCode = code
		if code == http.StatusServiceUnavailable {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if lastCode != http.StatusServiceUnavailable {
		t.Errorf("readiness during drain = %d, want 503 — a load balancer would keep "+
			"routing traffic to an instance that is going away", lastCode)
	}

	// Liveness must stay 200: the process is alive and draining correctly, and
	// reporting otherwise would invite an orchestrator to kill it mid-drain.
	if code, _ := get(t, base+"/health/live"); code != http.StatusOK {
		t.Errorf("liveness during drain = %d, want 200 — a draining process is not a dead one", code)
	}

	// Release the in-flight request. It must complete with its real answer.
	close(releaseRequest)
	select {
	case got := <-slow:
		if got.code != http.StatusOK {
			t.Errorf("in-flight request finished with %d (%s), want 200 — it was cut off",
				got.code, got.body)
		}
		if !strings.Contains(got.body, "finished") {
			t.Errorf("in-flight response body = %q, want the handler's real answer", got.body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the in-flight request never completed")
	}

	if err := <-drained; err != nil {
		t.Errorf("drain returned an error: %v", err)
	}
}

// TestLifecycle_StopsAcceptingAfterTheDrainDelay — once the listener closes, a
// NEW connection must be refused rather than served or silently accepted.
func TestLifecycle_StopsAcceptingAfterTheDrainDelay(t *testing.T) {
	base, shutdown := testServer(t, func(r *gin.Engine) {
		r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	})

	if code, _ := get(t, base+"/x"); code != http.StatusOK {
		t.Fatalf("request before shutdown = %d, want 200", code)
	}

	if err := shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	// The listener is closed. A new request must fail at the transport, which
	// get() reports as code 0.
	if code, _ := get(t, base+"/x"); code != 0 {
		t.Errorf("a new request after shutdown got %d; the listener is still accepting", code)
	}
}

// TestLifecycle_ExitsWithinTheBoundWhenAHandlerHangs.
//
// The timeout is what makes the drain bounded rather than a promise. A handler
// that never returns must not produce a process that never exits — that is the
// failure that gets a service SIGKILLed, losing every other in-flight request
// with it.
func TestLifecycle_ExitsWithinTheBoundWhenAHandlerHangs(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hung := make(chan struct{})
	t.Cleanup(func() { close(hung) })

	s := &Server{
		router: gin.New(),
		// One second, so the test asserts the bound rather than waiting for it.
		cfg:     &config.Config{ShutdownTimeoutSeconds: 1},
		ready:   newReadyState(nil),
		started: make(chan struct{}),
	}
	started := make(chan struct{})
	s.router.GET("/hang", func(c *gin.Context) {
		close(started)
		<-hung
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.serve(ctx, "127.0.0.1:0") }()
	<-s.Started()

	go func() {
		client := &http.Client{Timeout: 30 * time.Second}
		if resp, err := client.Get("http://" + s.Addr() + "/hang"); err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-started

	begin := time.Now()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("drain returned an error: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the process did not exit; a hung handler produced an unbounded shutdown")
	}

	// drainDelay + the 1s timeout, with slack for a loaded CI runner. The
	// upper bound is what matters: without the timeout this would never finish.
	if elapsed := time.Since(begin); elapsed > drainDelay+8*time.Second {
		t.Errorf("shutdown took %v with a 1s timeout; the bound is not being enforced", elapsed)
	}
}

// TestReadiness_ReportsWhichCheckFailed — an operator looking at a deployment
// that will not come up needs to know which dependency is the problem, and they
// may not have the process's logs.
func TestReadiness_ReportsWhichCheckFailed(t *testing.T) {
	state := newReadyState(nil) // no database wired

	ok, report := state.check(context.Background())
	if ok {
		t.Fatal("readiness passed with no database")
	}
	if report.Status != "not ready" {
		t.Errorf("status = %q, want \"not ready\"", report.Status)
	}
	if report.Checks["database"] == "" {
		t.Error("the report does not name the database check")
	}
	if report.Checks["accepting"] != "ok" {
		t.Errorf("accepting = %q, want ok when no shutdown has begun", report.Checks["accepting"])
	}
}

// TestReadiness_ReportsNothingSensitive — the probe is unauthenticated, because
// a kubelet has no credentials. That makes its body a public surface.
func TestReadiness_ReportsNothingSensitive(t *testing.T) {
	state := newReadyState(nil)
	_, report := state.check(context.Background())

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := strings.ToLower(string(encoded))

	// Values that would be genuinely useful to an attacker mapping the
	// installation, and that a careless "include the error" would leak.
	for _, forbidden := range []string{
		"postgres://", "password", "secret", "keycloak", "realm", "token", "sslmode",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the readiness body contains %q: %s", forbidden, encoded)
		}
	}
}

// TestReadiness_IgnoresWorkspaceProviderHealth is the multi-tenant rule, and it
// is a rule about what readiness must NOT look at.
//
// If readiness consulted workspace connections, one tenant's Keycloak going
// down would take this instance out of rotation and every other tenant with it.
// The blast radius of a broken provider must be exactly one workspace.
//
// Asserted structurally, over the checks the probe actually reports, because
// the failure would arrive as a future edit that adds a check here — and a test
// asserting "a broken provider still yields 200" would pass just as well
// against an implementation that had no providers to break.
func TestReadiness_IgnoresWorkspaceProviderHealth(t *testing.T) {
	state := newReadyState(nil)
	_, report := state.check(context.Background())

	allowed := map[string]bool{"accepting": true, "database": true}
	for name := range report.Checks {
		if !allowed[name] {
			t.Errorf("readiness reports a check named %q.\n"+
				"  Readiness may only consult GLOBAL dependencies. A per-workspace or "+
				"per-connection\n  check here means one tenant's Keycloak can take the "+
				"whole instance out of rotation.\n"+
				"  This covers master-key coverage too: a missing historical key strands "+
				"the\n  connections sealed under it and nothing else, so it belongs in the "+
				"boot log,\n  the lightweight_secret_key_version_rows gauge and `secrets "+
				"status` — not here.\n  See keycensus.go.", name)
		}
	}
}

// TestReadiness_FlipsBeforeAnythingElse — the ordering, at unit level, so a
// refactor that moves beginShutdown after the listener close fails here as well
// as in the end-to-end test.
func TestReadiness_FlipsBeforeAnythingElse(t *testing.T) {
	state := newReadyState(nil)
	if state.isShuttingDown() {
		t.Fatal("a fresh state reports shutting down")
	}

	state.beginShutdown()

	if !state.isShuttingDown() {
		t.Error("beginShutdown did not take effect")
	}
	_, report := state.check(context.Background())
	if report.Checks["accepting"] != "draining" {
		t.Errorf("accepting = %q after beginShutdown, want \"draining\"", report.Checks["accepting"])
	}
}

// TestLiveness_DoesNoWork — liveness must not be able to fail for a reason a
// restart cannot fix, so it must not touch a dependency. Proven by giving it a
// server with no database at all: if it consulted one, this would panic or fail.
func TestLiveness_DoesNoWork(t *testing.T) {
	base, _ := testServer(t, func(r *gin.Engine) {}, withoutDatabase)

	for i := 0; i < 3; i++ {
		code, body := get(t, base+"/health/live")
		if code != http.StatusOK {
			t.Fatalf("liveness = %d (%s), want 200 with no dependencies wired", code, body)
		}
	}

	// And readiness on the same server must NOT pass, which is what shows the
	// two are actually different checks rather than one aliased twice.
	if code, _ := get(t, base+"/health/ready"); code != http.StatusServiceUnavailable {
		t.Errorf("readiness = %d with no database, want 503 — liveness and readiness "+
			"are answering the same question", code)
	}
}
