package ipc_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ushineko/hikyaku/internal/ipc"
	"github.com/ushineko/hikyaku/internal/paths"
)

var quiet = slog.New(slog.DiscardHandler)

type echo struct{}

func (echo) Handle(ctx context.Context, req ipc.Request) ipc.Response {
	if req.Op == "wait" {
		<-ctx.Done()
		return ipc.Response{}
	}
	return ipc.Response{OK: true, Handle: req.Text}
}

func socketIn(t *testing.T) string {
	t.Helper()
	// Unix socket paths are limited to about a hundred bytes; t.TempDir()
	// under a long test name can exceed that.
	dir, err := os.MkdirTemp("", "hk")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "run", "hikyaku.sock")
}

func serve(t *testing.T, path string) (cancel func()) {
	t.Helper()
	srv, err := ipc.Listen(path, quiet)
	require.NoError(t, err)
	ctx, c := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx, echo{}); close(done) }()
	return func() { c(); <-done }
}

// R2: the socket is 0600 in a 0700 directory, and a second service is told
// the first one's PID.
func TestOneServicePrivatelyListening(t *testing.T) {
	path := socketIn(t)
	stop := serve(t, path)
	defer stop()

	di, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), di.Mode().Perm())
	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())

	_, err = ipc.Listen(path, quiet)
	var running *ipc.RunningError
	require.ErrorAs(t, err, &running)
	assert.Equal(t, os.Getpid(), running.PID)

	resp, err := ipc.Call(context.Background(), path, ipc.Request{Op: "x", Text: "hello"})
	require.NoError(t, err, "the first service still answers")
	assert.Equal(t, "hello", resp.Handle)
}

func TestTheLockIsReleasedOnStop(t *testing.T) {
	path := socketIn(t)
	serve(t, path)()
	_, err := os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist, "the socket file goes with the service")
	serve(t, path)()
}

func TestADirectoryOthersCanEnterIsRefused(t *testing.T) {
	path := socketIn(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.Chmod(filepath.Dir(path), 0o755)) //nolint:gosec // the test makes it unsafe on purpose
	_, err := ipc.Listen(path, quiet)
	require.ErrorIs(t, err, paths.ErrUnsafeDir)
}

// R9: no service means an answer within a second, not a hang.
func TestNoServiceIsReportedQuickly(t *testing.T) {
	start := time.Now()
	_, err := ipc.Call(context.Background(), socketIn(t), ipc.Request{Op: ipc.OpStatus})
	require.ErrorIs(t, err, ipc.ErrNoService)
	assert.Less(t, time.Since(start), time.Second)
}

// A client that gives up cancels the service's side of the request.
func TestAClientGoingAwayCancelsTheRequest(t *testing.T) {
	path := socketIn(t)
	stop := serve(t, path)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := ipc.Call(ctx, path, ipc.Request{Op: "wait"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded))
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the abandoned request kept the service from stopping")
	}
}
