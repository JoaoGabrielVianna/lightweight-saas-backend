package server

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/config"
	"github.com/gin-gonic/gin"
)

// The signal half of graceful shutdown.
//
// TestLifecycle_ServesAndDrains proves the DRAIN, driven by a cancelled
// context. What it cannot prove is that a real SIGTERM reaches that code — the
// wiring between signal.NotifyContext and the drain — and that is the half that
// matters in production, because it is what `docker stop`, a rolling deploy and
// a systemd restart all send.
//
// Signals are process-wide. Raising one inside `go test` would deliver it to
// the test binary, and through it to whatever is running the test binary. So
// this re-executes the test binary as a CHILD and signals only that child,
// which the test created and owns. Nothing else in the environment is touched.
//
// It reuses the subprocess pattern already in this repository
// (config.TestLoadConfig_FatalOnMissingRequired) rather than inventing a
// second one.

const signalChildEnv = "LSB_SIGNAL_CHILD"

// TestSignal_SIGTERMDrainsAndExitsCleanly.
func TestSignal_SIGTERMDrainsAndExitsCleanly(t *testing.T) {
	if os.Getenv(signalChildEnv) == "1" {
		runSignalChild()
		return
	}
	runSignalParent(t, syscall.SIGTERM)
}

// TestSignal_SIGINTDrainsAndExitsCleanly — Ctrl-C in a terminal, which is how a
// developer stops the process and therefore the path they will notice first if
// it regresses.
func TestSignal_SIGINTDrainsAndExitsCleanly(t *testing.T) {
	if os.Getenv(signalChildEnv) == "1" {
		runSignalChild()
		return
	}
	runSignalParent(t, syscall.SIGINT)
}

// runSignalChild is the server under test. It starts on an ephemeral port,
// prints the address so the parent can reach it, and blocks in Start — which
// installs the real signal handler.
func runSignalChild() {
	gin.SetMode(gin.TestMode)

	s := &Server{
		router:  gin.New(),
		cfg:     &config.Config{ShutdownTimeoutSeconds: 5},
		ready:   newReadyState(nil),
		started: make(chan struct{}),
	}
	s.ready.ping = func(context.Context) error { return nil }
	s.router.GET("/health/ready", readinessHandler(s.ready))
	s.router.GET("/slow", func(c *gin.Context) {
		// Long enough that the parent can signal while this is in flight, and
		// short enough to finish inside the 5s drain budget.
		time.Sleep(1500 * time.Millisecond)
		c.JSON(http.StatusOK, gin.H{"finished": true})
	})

	go func() {
		<-s.Started()
		// The parent reads this line to learn where to connect.
		//nolint:forbidigo // the child's stdout IS the channel to the parent
		os.Stdout.WriteString("LISTENING " + s.Addr() + "\n") //nolint:errcheck
	}()

	if err := s.Start("0"); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func runSignalParent(t *testing.T, sig syscall.Signal) {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(), signalChildEnv+"=1")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}

	// Always reap the child, including on a failed assertion. Kill is safe
	// here: it only ever targets the process this test created.
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()

	addr := waitForListening(t, stdout)
	base := "http://" + addr

	if code, _ := get(t, base+"/health/ready"); code != http.StatusOK {
		t.Fatalf("child readiness = %d before the signal, want 200", code)
	}

	// Put a request in flight, then signal while it is still running.
	var wg sync.WaitGroup
	slowCode := 0
	wg.Add(1)
	go func() {
		defer wg.Done()
		slowCode, _ = get(t, base+"/slow")
	}()
	time.Sleep(300 * time.Millisecond)

	if err := cmd.Process.Signal(sig); err != nil {
		t.Fatalf("signal child: %v", err)
	}

	// Readiness must report 503 while the child is still serving.
	sawDraining := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if code, _ := get(t, base+"/health/ready"); code == http.StatusServiceUnavailable {
			sawDraining = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sawDraining {
		t.Errorf("readiness never reported 503 after %v; a load balancer would keep "+
			"sending traffic to a process that is exiting", sig)
	}

	wg.Wait()
	if slowCode != http.StatusOK {
		t.Errorf("the in-flight request finished with %d, want 200 — %v cut it off "+
			"instead of draining", slowCode, sig)
	}

	// And the process must actually exit, cleanly, within the bound.
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	select {
	case err := <-exited:
		if err != nil {
			t.Errorf("child exited with %v after %v; a graceful stop is exit 0.\nstderr:\n%s",
				err, sig, stderr.String())
		}
		cmd.Process = nil // reaped; stop the deferred Kill
	case <-time.After(20 * time.Second):
		t.Fatalf("child did not exit within 20s of %v — the signal is not wired to the drain", sig)
	}
}

// waitForListening reads the child's stdout until it announces its address.
//
// Reading the address rather than agreeing on a port in advance: a fixed port
// makes the test fail on a machine that happens to be using it, and picking one
// by binding and releasing races anything else starting at the same moment.
func waitForListening(t *testing.T, stdout io.Reader) string {
	t.Helper()

	type line struct{ text string }
	lines := make(chan line)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- line{scanner.Text()}
		}
		close(lines)
	}()

	deadline := time.After(30 * time.Second)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatal("child stdout closed before it announced an address")
			}
			if addr, found := strings.CutPrefix(l.text, "LISTENING "); found {
				return strings.TrimSpace(addr)
			}
		case <-deadline:
			t.Fatal("child never announced an address within 30s")
		}
	}
}
