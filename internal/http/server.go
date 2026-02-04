package http

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
)

// Server wraps HTTP server with readiness checks.
type Server struct {
	server *http.Server
	mux    *http.ServeMux
	ready  atomic.Bool
	log    *slog.Logger
}

// New creates a new HTTP server.
func New(addr string, log *slog.Logger) *Server {
	mux := http.NewServeMux()
	s := &Server{
		mux: mux,
		server: &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
		log: log,
	}
	s.registerHealth()
	return s
}

// Handle registers a handler.
func (s *Server) Handle(pattern string, handler http.Handler) {
	s.mux.Handle(pattern, handler)
}

// SetReady updates readiness state.
func (s *Server) SetReady(ready bool) {
	s.ready.Store(ready)
}

// ListenAndServe starts the HTTP server.
func (s *Server) ListenAndServe() error {
	s.log.Info("HTTP server listening", "addr", s.server.Addr)
	return s.server.ListenAndServe()
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

func (s *Server) registerHealth() {
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	s.mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if !s.ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}
