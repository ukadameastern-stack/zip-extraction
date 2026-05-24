package health_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/health"
)

func TestGate_Defaults(t *testing.T) {
	g := health.NewGate()
	assert.False(t, g.Ready())
	g.SetReady(true)
	assert.True(t, g.Ready())
	g.SetReady(false)
	assert.False(t, g.Ready())
}

// pickPort returns a free TCP port. Used so test HTTP servers don't collide.
func pickPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().(*net.TCPAddr)
	require.NoError(t, l.Close())
	return addr.Port
}

func TestServer_LivenessAlwaysOK(t *testing.T) {
	gate := health.NewGate()
	reg := prometheus.NewRegistry()
	port := pickPort(t)
	srv := health.NewServer(port, gate, reg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = srv.Start(ctx) }()
	waitForListener(t, port)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz/live", port))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	cancel()
}

func TestServer_ReadinessGated(t *testing.T) {
	gate := health.NewGate()
	reg := prometheus.NewRegistry()
	port := pickPort(t)
	srv := health.NewServer(port, gate, reg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Start(ctx) }()
	waitForListener(t, port)

	// Initially not ready.
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz/ready", port))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	// Flip gate.
	gate.SetReady(true)

	resp, err = http.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz/ready", port))
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), "ok")
}

func TestServer_MetricsEndpoint(t *testing.T) {
	gate := health.NewGate()
	reg := prometheus.NewRegistry()
	// Register a sentinel collector so /metrics has something to expose.
	reg.MustRegister(prometheus.NewCounter(prometheus.CounterOpts{Name: "sentinel_total", Help: "x"}))

	port := pickPort(t)
	srv := health.NewServer(port, gate, reg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Start(ctx) }()
	waitForListener(t, port)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", port))
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), "sentinel_total")
}

func TestServer_ShutdownEndsStart(t *testing.T) {
	gate := health.NewGate()
	reg := prometheus.NewRegistry()
	port := pickPort(t)
	srv := health.NewServer(port, gate, reg)

	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { done <- srv.Start(ctx) }()
	waitForListener(t, port)

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Start did not return after ctx cancel")
	}
}

func TestServer_NilRegistryDefaultsGracefully(t *testing.T) {
	gate := health.NewGate()
	port := pickPort(t)
	srv := health.NewServer(port, gate, nil)
	require.NotNil(t, srv)
}

func waitForListener(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server on port %d did not start within deadline", port)
}
