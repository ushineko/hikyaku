package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/ushineko/hikyaku/internal/paths"
)

// Handler answers one request. ctx is cancelled when the client goes away.
type Handler interface {
	Handle(ctx context.Context, req Request) Response
}

// RunningError is returned by Listen when another service holds the lock.
type RunningError struct{ PID int }

func (e *RunningError) Error() string {
	if e.PID > 0 {
		return fmt.Sprintf("hikyaku is already running (pid %d)", e.PID)
	}
	return "hikyaku is already running"
}

// Server is a listening socket that only one service may hold.
type Server struct {
	ln   net.Listener
	lock *os.File
	path string
	log  *slog.Logger
	wg   sync.WaitGroup
}

// Listen takes the single-instance lock in the socket's directory, then
// listens on socketPath with mode 0600. The directory is created 0700, and
// one that exists with looser permissions or another owner is refused.
func Listen(socketPath string, log *slog.Logger) (*Server, error) {
	dir := filepath.Dir(socketPath)
	if err := paths.PrivateDir(dir); err != nil {
		return nil, err
	}
	lock, err := takeLock(filepath.Join(dir, "serve.lock"))
	if err != nil {
		return nil, err
	}
	// The lock is ours, so a socket file left here is a dead service's.
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = lock.Close()
		return nil, fmt.Errorf("remove stale socket: %w", err)
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", socketPath)
	if err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("listen on %s: %w", socketPath, err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = ln.Close()
		_ = lock.Close()
		return nil, fmt.Errorf("restrict socket: %w", err)
	}
	return &Server{ln: ln, lock: lock, path: socketPath, log: log}, nil
}

func takeLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		b := make([]byte, 32)
		n, _ := f.ReadAt(b, 0)
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			pid, _ := strconv.Atoi(strings.TrimSpace(string(b[:n])))
			return nil, &RunningError{PID: pid}
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	if err := f.Truncate(0); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write lock: %w", err)
	}
	if _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write lock: %w", err)
	}
	return f, nil
}

// Serve accepts connections until ctx is cancelled, then closes the socket,
// waits for open requests to finish and releases the lock.
func (s *Server) Serve(ctx context.Context, h Handler) error {
	go func() {
		<-ctx.Done()
		_ = s.ln.Close()
	}()
	defer s.release()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		s.wg.Go(func() { s.serveConn(ctx, conn, h) })
	}
}

func (s *Server) release() {
	s.wg.Wait()
	_ = os.Remove(s.path)
	_ = s.lock.Close()
}

func (s *Server) serveConn(ctx context.Context, conn net.Conn, h Handler) {
	defer func() { _ = conn.Close() }()
	if uid, err := peerUID(conn); err != nil || uid != os.Getuid() {
		// File modes already keep other accounts out; this is the second lock.
		s.log.Warn("refused a connection from another account", "uid", uid, "err", err)
		return
	}
	r := bufio.NewReaderSize(conn, 64<<10)
	line, err := readLine(r)
	if err != nil {
		s.write(conn, Fail(CodeBadRequest, err.Error()))
		return
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		s.write(conn, Fail(CodeBadRequest, "request is not JSON"))
		return
	}
	// A client that closes its end has gone away; nothing more is read
	// from it, so a read that returns means the request can be abandoned.
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		_, _ = r.ReadByte()
		cancel()
	}()
	s.write(conn, h.Handle(reqCtx, req))
}

func (s *Server) write(conn net.Conn, resp Response) {
	b, err := json.Marshal(resp)
	if err != nil {
		s.log.Error("encode response", "err", err)
		return
	}
	if _, err := conn.Write(append(b, '\n')); err != nil {
		s.log.Debug("write response", "err", err)
	}
}

func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return nil, fmt.Errorf("read request: %w", err)
		}
		buf = append(buf, chunk...)
		if len(buf) > maxLine {
			return nil, errors.New("request too large")
		}
		if !isPrefix {
			return buf, nil
		}
	}
}

func peerUID(conn net.Conn) (int, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return -1, errors.New("not a unix connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return -1, fmt.Errorf("peer credentials: %w", err)
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return -1, fmt.Errorf("peer credentials: %w", err)
	}
	if credErr != nil {
		return -1, fmt.Errorf("peer credentials: %w", credErr)
	}
	return int(cred.Uid), nil
}
