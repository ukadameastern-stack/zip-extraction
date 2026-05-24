// Package health implements the operational HTTP server per Q3 of application
// design: a single port serving /healthz/live, /healthz/ready, and /metrics.
package health

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Gate is the readiness gate consumed by /healthz/ready.
type Gate struct {
	ready atomic.Bool
}

// NewGate constructs a Gate that starts not-ready.
func NewGate() *Gate { return &Gate{} }

// SetReady flips the gate. Threadsafe.
func (g *Gate) SetReady(v bool) { g.ready.Store(v) }

// Ready returns the current readiness state.
func (g *Gate) Ready() bool { return g.ready.Load() }

// Server is the operational HTTP server.
type Server struct {
	port    int
	gate    *Gate
	httpSrv *http.Server
}

// NewServer constructs a Server. The Prometheus handler is registered against
// the supplied registerer (or the default if nil).
func NewServer(port int, gate *Gate, reg prometheus.Gatherer) *Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz/live", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/healthz/ready", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !gate.Ready() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"not-ready"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	if reg == nil {
		reg = prometheus.DefaultGatherer
	}
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	return &Server{
		port: port,
		gate: gate,
		httpSrv: &http.Server{
			Addr:              fmt.Sprintf(":%d", port),
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      15 * time.Second,
			IdleTimeout:       30 * time.Second,
		},
	}
}

// Start runs ListenAndServe on a goroutine and returns when ctx is cancelled
// or the server exits. Always returns nil on graceful shutdown.
func (s *Server) Start(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		err := s.httpSrv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.httpSrv.Shutdown(shutdownCtx)
		<-errCh
		return nil
	case err := <-errCh:
		return err
	}
}

// Shutdown initiates a graceful shutdown.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpSrv.Shutdown(ctx)
}
