package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"
)

// Process lifecycle.
//
// # What was wrong
//
// `router.Run(":8080")` — gin's convenience wrapper — builds an http.Server with
// zero timeouts and no handle on it ([TD-013]). Two consequences, both
// operational:
//
//	no shutdown  SIGTERM killed the process mid-request. Every deploy, every
//	             restart, every `docker compose down` dropped whatever was in
//	             flight, and a request that had already written to Keycloak was
//	             dropped after the write and before the response.
//	no timeouts  a client that opens a connection and never sends a request
//	             holds a goroutine and a file descriptor until it disconnects.
//	             Ten thousand of them is a denial of service that costs the
//	             attacker nothing.
//
// # The sequence, and why the order is the whole design
//
//	signal
//	  ↓ mark NOT READY            load balancer stops sending new work
//	  ↓ (drain delay)             …but it has not noticed yet
//	  ↓ stop accepting            listener closes
//	  ↓ in-flight finish          bounded by ShutdownTimeout
//	  ↓ close the database
//	  ↓ exit
//
// Marking not-ready BEFORE closing the listener is the part that matters and
// the part most implementations skip. A load balancer learns about readiness by
// polling, so between the signal and its next probe it is still routing traffic
// here. Closing the listener first turns that window into connection refusals;
// answering 503 on readiness while still SERVING turns it into a clean
// hand-off. The drain delay exists to make the window long enough to be noticed.
//
// [TD-013]: docs/TECH_DEBT.md#td-013

// HTTP server timeouts.
//
// Every value below is derived from what this API actually does, not copied
// from a template. The measurements are from Slice 8, taken over HTTP against a
// real Keycloak: a project read p99 was ~116ms and a write p99 ~292ms.
const (
	// readHeaderTimeout is the defence against a Slowloris client that opens a
	// connection and dribbles headers forever. Nothing legitimate needs more
	// than a moment to send request headers, so this can be aggressive; it is
	// the single most valuable of these timeouts and the one Go leaves unset.
	readHeaderTimeout = 10 * time.Second

	// readTimeout covers headers plus body. Bodies here are small JSON
	// documents — the largest is an email-template update — so a slow body is
	// a slow client, not a large one.
	readTimeout = 30 * time.Second

	// writeTimeout is the one that can break working requests if set carelessly,
	// because the clock covers the HANDLER as well as the write. A handler that
	// waits on Keycloak is inside this budget.
	//
	// 60s against a ~300ms p99 write is two orders of magnitude of headroom.
	// That is deliberate: the p99 was measured against a local Keycloak, and a
	// production one may be across a network, cold, or garbage-collecting. This
	// timeout exists to bound a stuck request, not to enforce a latency
	// objective — there is no value in it being tight, and real cost if it is.
	writeTimeout = 60 * time.Second

	// idleTimeout closes kept-alive connections that have gone quiet, so a
	// pool of idle clients does not accumulate goroutines. Longer than
	// readTimeout so an idle keep-alive is not mistaken for a slow request.
	idleTimeout = 120 * time.Second

	// maxHeaderBytes caps header size. Go's default is 1MB, which is generous
	// for an API whose largest legitimate header is a JWT. 64KB fits any token
	// this validates plus a proxy's worth of forwarding headers.
	maxHeaderBytes = 64 << 10

	// drainDelay is how long the process keeps serving normally after marking
	// itself not-ready, giving a load balancer time to observe the change.
	//
	// Compose's healthcheck interval is 10s and a typical ingress polls every
	// 2–5s, so this cannot cover every configuration; it covers the common one
	// and turns a guaranteed window of refused connections into a probable
	// clean hand-off. It is deliberately NOT configurable: an operator who
	// needs a different value needs a different probe interval, and one knob
	// implying control over the other would be misleading.
	drainDelay = 3 * time.Second
)

// Start runs the server until a termination signal arrives, then drains.
//
// It blocks. The error is non-nil only when the server failed to serve at all —
// a port already in use, most often. A clean shutdown returns nil, including
// one that hit the timeout with requests still running, because that is a
// bounded shutdown working as designed rather than a failure to start.
func (s *Server) Start(port string) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return s.serve(ctx, ":"+port)
}

// serve is Start with the trigger injected, so a test can drive the whole
// sequence by cancelling a context instead of signalling a process.
//
// The address is a parameter and the resolved one is published on s.addr,
// which lets a test listen on :0 and discover the port. Without that, a test
// would have to guess a free port and would be flaky on a busy machine.
func (s *Server) serve(ctx context.Context, addr string) error {
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           s.router,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.setAddr(listener.Addr().String())

	serveErr := make(chan error, 1)
	go func() {
		// ErrServerClosed is what Shutdown causes; it is the success path.
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	log.Info("listening on " + listener.Addr().String() +
		" (drain " + s.shutdownTimeout().String() + ")")
	s.markStarted()

	select {
	case err := <-serveErr:
		// Failed before any signal — a bind error, or the listener died.
		return err
	case <-ctx.Done():
	}

	return s.drain(httpServer, serveErr)
}

// drain performs the ordered shutdown.
func (s *Server) drain(httpServer *http.Server, serveErr <-chan error) error {
	log.Info("shutdown signal received")

	// 1. Stop being advertised as ready. Nothing else changes yet: requests
	//    still arrive and are still served normally.
	s.ready.beginShutdown()
	log.Info("readiness now reports 503; draining for " + drainDelay.String() +
		" before closing the listener")

	// 2. Give a load balancer time to notice. Interruptible: a second signal
	//    from an impatient operator skips straight to the close.
	select {
	case <-time.After(drainDelay):
	case <-secondSignal():
		log.Warn("second signal received; skipping the drain delay")
	}

	// 3. Close the listener and let in-flight requests finish, bounded.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout())
	defer cancel()

	err := httpServer.Shutdown(shutdownCtx)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		// Requests were still running when the budget ran out. Reported, not
		// hidden: it means either the timeout is too short for this workload or
		// a handler is stuck, and both are worth an operator seeing.
		log.Warn("shutdown timeout of " + s.shutdownTimeout().String() +
			" elapsed with requests still in flight; exiting anyway")
	case err != nil:
		log.Warn("shutdown: " + err.Error())
	default:
		log.Info("all in-flight requests completed")
	}

	// Serve() has now returned. Drain its result so the goroutine cannot leak.
	<-serveErr

	s.closeResources()
	log.Info("shutdown complete")
	return nil
}

// secondSignal returns a channel that fires if another termination signal
// arrives during the drain.
//
// An operator pressing Ctrl-C twice means "stop waiting", and a process that
// ignores the second one feels hung — which is how people learn to reach for
// SIGKILL, losing the drain entirely.
func secondSignal() <-chan struct{} {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	out := make(chan struct{})
	go func() {
		<-ctx.Done()
		stop()
		close(out)
	}()
	return out
}

// closeResources releases what the process holds open.
//
// Only the database today. It runs AFTER the HTTP shutdown returns, which is
// the ordering that matters: closing the pool while a handler still holds a
// transaction would turn a graceful drain into a set of failed requests.
func (s *Server) closeResources() {
	if s.db == nil {
		return
	}
	sqlDB, err := s.db.DB()
	if err != nil {
		log.Warn("could not reach the database handle to close it: " + err.Error())
		return
	}
	if err := sqlDB.Close(); err != nil {
		log.Warn("closing the database: " + err.Error())
		return
	}
	log.Info("database connections closed")
}

func (s *Server) shutdownTimeout() time.Duration {
	if s.cfg == nil {
		return 20 * time.Second
	}
	return s.cfg.ShutdownTimeout()
}
