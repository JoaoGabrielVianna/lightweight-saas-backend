package lightweight_test

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	lightweight "github.com/JoaoGabrielVianna/lightweight-saas-backend/sdk/go"
)

// TestClient_IsSafeForConcurrentUse.
//
// A backend shares one client across every request it serves, so "safe for
// concurrent use" is not a nicety — it is the only way the type is ever used in
// production. Run this package's tests with -race for the assertion to mean
// anything; without it, a data race here is invisible and intermittent.
//
// The shape of the bug this guards against is specific: per-call state kept on
// the client (a reused http.Request, a shared buffer, a "last response" field
// added for debugging) works perfectly in every sequential test and corrupts one
// request in a thousand under load.
func TestClient_IsSafeForConcurrentUse(t *testing.T) {
	const goroutines = 100

	client, ts := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Every response is distinguishable, so a request that picked up
		// another's state produces a wrong VALUE rather than merely a race the
		// detector might miss.
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", testRequestID)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"users":[{"id":"` + r.URL.Query().Get("search") +
			`","username":"u","email":"e","first_name":"","last_name":"",` +
			`"enabled":true,"email_verified":false,"created_at":"2026-08-10T14:03:11Z"}],` +
			`"first":0,"max":20,"count":1}`))
	})

	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	mismatches := make(chan string, goroutines)

	for i := range goroutines {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()

			marker := "goroutine-" + strings.Repeat("x", n%7) + itoa(n)
			page, err := client.Users.List(testContext(t), lightweight.UserListOptions{Search: marker})
			if err != nil {
				errs <- err
				return
			}
			if len(page.Users) != 1 || page.Users[0].ID != marker {
				mismatches <- marker
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	close(mismatches)

	for err := range errs {
		t.Errorf("concurrent call failed: %v", err)
	}
	for marker := range mismatches {
		t.Errorf("the response for %q did not match its request; per-call state is being shared", marker)
	}
	if n := ts.count(); n != goroutines {
		t.Errorf("%d requests reached the server, want %d", n, goroutines)
	}
}

// TestClient_ConcurrentUseAcrossEveryService — the services share one client and
// one http.Client, so the mix matters as much as the volume.
func TestClient_ConcurrentUseAcrossEveryService(t *testing.T) {
	client, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", testRequestID)
		switch {
		case strings.HasSuffix(r.URL.Path, "/roles"):
			_, _ = w.Write([]byte(`{"roles":[],"count":0}`))
		case strings.HasSuffix(r.URL.Path, "/sessions"):
			_, _ = w.Write([]byte(`{"sessions":[],"count":0}`))
		case strings.HasSuffix(r.URL.Path, "/invitations"):
			_, _ = w.Write([]byte(`{"invitations":[],"count":0}`))
		case strings.HasSuffix(r.URL.Path, "/audit"):
			_, _ = w.Write([]byte(`{"items":[],"pagination":{"count":0,"limit":50}}`))
		default:
			_, _ = w.Write([]byte(`{"users":[],"first":0,"max":20,"count":0}`))
		}
	})

	calls := []func() error{
		func() error { _, err := client.Users.List(testContext(t), lightweight.UserListOptions{}); return err },
		func() error { _, err := client.Roles.List(testContext(t)); return err },
		func() error { _, err := client.Sessions.List(testContext(t)); return err },
		func() error { _, err := client.Invitations.List(testContext(t)); return err },
		func() error { _, err := client.Audit.List(testContext(t), lightweight.AuditListOptions{}); return err },
	}

	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i := range 100 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if err := calls[n%len(calls)](); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent call failed: %v", err)
	}
}

// itoa avoids importing strconv into a file that otherwise needs nothing from it.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
