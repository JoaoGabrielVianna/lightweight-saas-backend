package main

import (
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Representative HTTP measurement.
//
// The point is NOT to find the maximum this process can serve. It is to know
// the order of magnitude of each path so the rate-limit defaults are chosen
// against something real instead of against a feeling — and to know which paths
// are cheap enough that limiting them is about fairness rather than survival.
//
// Deliberately not a Go microbenchmark. A benchmark of the authenticator
// function would report nanoseconds and answer a question nobody asked: every
// number that matters here includes the HTTP server, the JSON, the middleware
// chain, the database round trip and — where the path reaches it — the provider.
//
// # The scenarios, and why each one
//
//	invalid bearer          the cheapest refusal. Bounds what a flood costs.
//	unknown credential      parser passes, database is consulted. The real
//	                        cost of an authentication attempt.
//	workspace mismatch      authenticated, then refused before the resolver.
//	                        Isolates the auth+authz chain from the provider.
//	operator read           the console's path, JWT validation included.
//	project read            the M2M read path, provider included.
//	project write           the M2M write path, provider included.
//
// The gap between "workspace mismatch" and "project read" is the provider's
// share of a request, and it is the number that decides whether the limits
// should be protecting the process or the provider.
type scenario struct {
	name string
	// send performs one request. Returning the status lets the report
	// distinguish work that was done from work that was refused, which matters
	// because a run that is mostly 429s is measuring the limiter, not the path.
	send func() (int, error)
}

func runBench(cfg *Config) int {
	c := cfg.Client()

	fmt.Printf("\033[1mlwprobe — HTTP measurement\033[0m\n")
	fmt.Printf("  url          %s\n", c.BaseURL)
	fmt.Printf("  workspace    %s\n", c.WorkspaceID)
	fmt.Printf("  concurrency  %d\n", cfg.BenchConcurrency)
	fmt.Printf("  requests     %d per scenario\n\n", cfg.BenchRequests)

	bogus := cfg.ClientWith("lw_sk_" + strings.Repeat("a", 16) + "_" + strings.Repeat("b", 52))
	garbage := cfg.ClientWith("not-a-token")

	// One user to read and to write, created up front so the write scenario
	// measures an update rather than a create-and-grow-the-realm.
	var writeTarget string
	if resp, err := c.CreateUser(fmt.Sprintf("bench-%d@example.test", time.Now().UnixNano()), "Bench", "Target"); err == nil {
		if id, err := userIDFrom(resp.Body); err == nil {
			writeTarget = id
			defer func() { _, _ = c.DeleteUser(writeTarget) }()
		}
	}

	scenarios := []scenario{
		{"invalid bearer (unparseable)", func() (int, error) {
			r, err := garbage.ListUsers()
			return statusOf(r, err)
		}},
		{"unknown credential (db lookup)", func() (int, error) {
			r, err := bogus.ListUsers()
			return statusOf(r, err)
		}},
		{"workspace mismatch (dies before provider)", func() (int, error) {
			r, err := c.DoForWorkspace("ws_00000000-0000-4000-8000-000000000000", http.MethodGet, "/users", nil)
			return statusOf(r, err)
		}},
		{"project read (provider involved)", func() (int, error) {
			r, err := c.ListUsers()
			return statusOf(r, err)
		}},
	}

	if writeTarget != "" {
		scenarios = append(scenarios, scenario{"project write (provider involved)", func() (int, error) {
			r, err := c.Do(http.MethodPatch, "/users/"+writeTarget, map[string]any{"first_name": "Bench"})
			return statusOf(r, err)
		}})
	}

	if token := envOr("LW_OPERATOR_TOKEN", ""); token != "" {
		op := NewClient(cfg.URL, cfg.WorkspaceID, token)
		scenarios = append(scenarios, scenario{"operator read (JWT validation)", func() (int, error) {
			r, err := op.DoRaw(http.MethodGet, "/v1/workspaces", nil)
			return statusOf(r, err)
		}})
	}

	fmt.Printf("  %-44s %7s %8s %8s %8s %8s   %s\n",
		"scenario", "req/s", "p50", "p95", "p99", "max", "statuses")
	fmt.Printf("  %s\n", strings.Repeat("─", 110))

	for _, s := range scenarios {
		measure(s, cfg.BenchConcurrency, cfg.BenchRequests).print()
		// Let the buckets refill between scenarios so one scenario's throttling
		// does not distort the next one's latencies.
		time.Sleep(3 * time.Second)
	}

	fmt.Printf("\n  Latencies include the full HTTP round trip from this process.\n")
	fmt.Printf("  A run dominated by 429s is measuring the limiter, not the path — raise\n")
	fmt.Printf("  RATE_LIMIT_EDGE_RPS / RATE_LIMIT_CREDENTIAL_RPS on the server to measure capacity.\n")
	return 0
}

func statusOf(r *Response, err error) (int, error) {
	if err != nil {
		return 0, err
	}
	return r.Status, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type result struct {
	name      string
	latencies []time.Duration
	statuses  map[int]int
	errors    int
	elapsed   time.Duration
}

func measure(s scenario, concurrency, total int) *result {
	res := &result{name: s.name, statuses: map[int]int{}}

	var mu sync.Mutex
	var wg sync.WaitGroup
	work := make(chan struct{}, total)
	for i := 0; i < total; i++ {
		work <- struct{}{}
	}
	close(work)

	start := time.Now()
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range work {
				t0 := time.Now()
				status, err := s.send()
				d := time.Since(t0)

				mu.Lock()
				res.latencies = append(res.latencies, d)
				if err != nil {
					res.errors++
				} else {
					res.statuses[status]++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	res.elapsed = time.Since(start)
	return res
}

func (r *result) print() {
	sort.Slice(r.latencies, func(i, j int) bool { return r.latencies[i] < r.latencies[j] })

	rps := float64(len(r.latencies)) / r.elapsed.Seconds()
	fmt.Printf("  %-44s %7.0f %8s %8s %8s %8s   %s\n",
		r.name, rps,
		ms(percentile(r.latencies, 0.50)),
		ms(percentile(r.latencies, 0.95)),
		ms(percentile(r.latencies, 0.99)),
		ms(percentile(r.latencies, 1.0)),
		r.statusSummary())
}

func (r *result) statusSummary() string {
	codes := make([]int, 0, len(r.statuses))
	for c := range r.statuses {
		codes = append(codes, c)
	}
	sort.Ints(codes)

	parts := make([]string, 0, len(codes)+1)
	for _, c := range codes {
		parts = append(parts, fmt.Sprintf("%d×%d", c, r.statuses[c]))
	}
	if r.errors > 0 {
		parts = append(parts, fmt.Sprintf("err×%d", r.errors))
	}
	return strings.Join(parts, " ")
}

// percentile uses nearest-rank on a sorted slice. Good enough for an
// order-of-magnitude reading, and it never interpolates a latency that was
// never observed.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(float64(len(sorted)-1) * p)
	return sorted[i]
}

func ms(d time.Duration) string {
	return fmt.Sprintf("%.1fms", float64(d.Microseconds())/1000)
}
