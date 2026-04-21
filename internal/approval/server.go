package approval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Decider renders an approval UI for chatID and blocks until the operator
// decides or the context is cancelled. Implementations must be safe for
// concurrent calls from different chats.
type Decider interface {
	Decide(ctx context.Context, req Request) Response
}

// Server listens on a unix-domain socket for hook connections. Each
// connection is a one-shot JSON request followed by a one-shot JSON reply.
type Server struct {
	path    string
	decider Decider
	logger  *slog.Logger

	mu   sync.Mutex
	ln   net.Listener
	wg   sync.WaitGroup
	done chan struct{}
}

// NewServer constructs a Server. Call Start to begin listening.
func NewServer(socketPath string, d Decider, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		path:    socketPath,
		decider: d,
		logger:  logger,
		done:    make(chan struct{}),
	}
}

// Start removes any stale socket, ensures the parent dir exists, binds the
// unix socket, and starts the accept loop. Non-blocking.
func (s *Server) Start() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("approval: mkdir socket dir: %w", err)
	}
	// Stale socket from a previous crash — remove before binding.
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("approval: remove stale socket: %w", err)
	}

	ln, err := net.Listen("unix", s.path)
	if err != nil {
		return fmt.Errorf("approval: listen %s: %w", s.path, err)
	}
	// World-writable so hook processes (possibly different uid) can connect.
	// The socket lives in a dir the bot controls; this is a local IPC gate,
	// not an auth boundary.
	_ = os.Chmod(s.path, 0o666)

	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()

	s.wg.Add(1)
	go s.acceptLoop(ln)
	s.logger.Info("approval server listening", "socket", s.path)
	return nil
}

// Stop closes the listener, waits for in-flight handlers to finish, and
// removes the socket file. Safe to call multiple times.
func (s *Server) Stop() {
	s.mu.Lock()
	ln := s.ln
	s.ln = nil
	s.mu.Unlock()

	if ln != nil {
		_ = ln.Close()
	}
	close(s.done)
	s.wg.Wait()
	_ = os.Remove(s.path)
}

func (s *Server) acceptLoop(ln net.Listener) {
	defer s.wg.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
			}
			s.logger.Warn("approval accept failed", "err", err)
			// Transient error — small backoff to avoid spin.
			time.Sleep(50 * time.Millisecond)
			continue
		}
		s.wg.Add(1)
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()

	// Hook processes may take as long as the operator takes to click. The
	// CLI enforces its own timeout via the hook config; we set a generous
	// ceiling here so that a hung connection eventually frees resources.
	_ = conn.SetDeadline(time.Now().Add(15 * time.Minute))

	dec := json.NewDecoder(conn)
	var req Request
	if err := dec.Decode(&req); err != nil {
		s.logger.Warn("approval decode request failed", "err", err)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Abort in-flight approvals on server shutdown.
	go func() {
		select {
		case <-s.done:
			cancel()
		case <-ctx.Done():
		}
	}()

	resp := s.decider.Decide(ctx, req)
	if resp.Decision != DecisionAllow && resp.Decision != DecisionDeny {
		// Paranoia: never return an unknown decision to the hook.
		resp = Response{Decision: DecisionDeny, Reason: "server returned unknown decision"}
	}

	enc := json.NewEncoder(conn)
	if err := enc.Encode(resp); err != nil {
		s.logger.Warn("approval encode response failed", "err", err)
	}
}
