package app_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/app"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/extraction"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/health"
	mylog "github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/log"
)

type fakeQueue struct {
	runCalled atomic.Bool
	runErr    error
}

func (f *fakeQueue) Run(ctx context.Context, _ func(ctx context.Context, msg extraction.ClaimCheck) (bool, error)) error {
	f.runCalled.Store(true)
	if f.runErr != nil {
		return f.runErr
	}
	<-ctx.Done()
	return nil
}

type fakeHTTPServer struct {
	startCalls    atomic.Int32
	shutdownCalls atomic.Int32
	startErr      error
}

func (f *fakeHTTPServer) Start(ctx context.Context) error {
	f.startCalls.Add(1)
	if f.startErr != nil {
		return f.startErr
	}
	<-ctx.Done()
	return nil
}

func (f *fakeHTTPServer) Shutdown(ctx context.Context) error {
	f.shutdownCalls.Add(1)
	return nil
}

func TestRun_NoStartupProbe_BecomesReady(t *testing.T) {
	gate := health.NewGate()
	q := &fakeQueue{}
	srv := &fakeHTTPServer{}
	a := app.New(app.Config{GracefulShutdownTimeoutSec: 1}, app.Dependencies{
		Logger: mylog.NewDiscardLogger(),
		Queue:  q, HTTPServer: srv, HealthGate: gate,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	require.Eventually(t, gate.Ready, 2*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return q.runCalled.Load() }, 2*time.Second, 10*time.Millisecond)

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
	assert.False(t, gate.Ready(), "readiness flipped to false during drain")
	assert.GreaterOrEqual(t, srv.shutdownCalls.Load(), int32(1))
}

func TestRun_StartupProbeFailure(t *testing.T) {
	gate := health.NewGate()
	a := app.New(app.Config{GracefulShutdownTimeoutSec: 1}, app.Dependencies{
		Logger:     mylog.NewDiscardLogger(),
		Queue:      &fakeQueue{},
		HTTPServer: &fakeHTTPServer{},
		HealthGate: gate,
		Startup:    func(ctx context.Context) error { return errors.New("aws not reachable") },
	})
	err := a.Run(context.Background())
	require.Error(t, err)
	assert.False(t, gate.Ready())
}

func TestRun_StartupProbeSuccess(t *testing.T) {
	gate := health.NewGate()
	q := &fakeQueue{}
	a := app.New(app.Config{GracefulShutdownTimeoutSec: 1}, app.Dependencies{
		Logger:     mylog.NewDiscardLogger(),
		Queue:      q,
		HTTPServer: &fakeHTTPServer{},
		HealthGate: gate,
		Startup:    func(ctx context.Context) error { return nil },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	require.Eventually(t, gate.Ready, 2*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestRun_HTTPStartupFailure(t *testing.T) {
	gate := health.NewGate()
	q := &fakeQueue{}
	srv := &fakeHTTPServer{startErr: errors.New("port in use")}
	a := app.New(app.Config{GracefulShutdownTimeoutSec: 1}, app.Dependencies{
		Logger: mylog.NewDiscardLogger(),
		Queue:  q, HTTPServer: srv, HealthGate: gate,
	})
	err := a.Run(context.Background())
	require.Error(t, err)
}
