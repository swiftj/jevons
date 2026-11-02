// Package proxy implements the loopback-only telemetry bridge HTTP server that
// exposes router status and on-demand routing to desktop extension mods.
package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/swiftj/jevons/internal/config"
	"github.com/swiftj/jevons/pkg/schema"
)

// Hardening limits and timeouts.
const (
	// MaxBodyBytes caps request bodies at 256 KiB.
	MaxBodyBytes = 256 << 10
	// RequestTimeout bounds every router call made on behalf of a request.
	RequestTimeout = 5 * time.Second
	// ShutdownTimeout bounds graceful shutdown after the context is cancelled.
	ShutdownTimeout = 5 * time.Second

	readHeaderTimeout = 2 * time.Second
	readTimeout       = 5 * time.Second
	writeTimeout      = 10 * time.Second
	idleTimeout       = 60 * time.Second

	contentTypeJSON = "application/json; charset=utf-8"
)

// Feed input limits.
const (
	// MaxSessionIDLen caps POST /session session_id length.
	MaxSessionIDLen = 128
	// MaxUsageTokens caps a single POST /usage report.
	MaxUsageTokens = 1_000_000_000
)

// ErrNonLoopback is returned by ListenAndServe when the listen address is not loopback.
var ErrNonLoopback = errors.New("proxy: listen address is not loopback")

// Feeds are optional sinks for client-reported session and usage data. A nil
// sink makes its endpoint answer 501.
type Feeds struct {
	Sessions schema.SessionTracker
	Usage    schema.UsageRecorder
	Clock    schema.Clock // nil → schema.SystemClock{}
}

// Server is the telemetry bridge HTTP server.
type Server struct {
	cfg     config.Config
	router  schema.Router
	logger  *slog.Logger
	feeds   Feeds
	handler http.Handler

	// onListen, when set, is called with the bound address once listening (tests).
	onListen func(net.Addr)
}

// NewServer builds a Server. A nil logger discards logs.
func NewServer(cfg config.Config, r schema.Router, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Server{cfg: cfg, router: r, logger: logger}
	s.handler = s.buildHandler()
	return s
}

// WithFeeds attaches session and usage sinks for POST /session and POST /usage.
// Call it before serving; it is not safe to call concurrently with requests.
func (s *Server) WithFeeds(f Feeds) *Server {
	if f.Clock == nil {
		f.Clock = schema.SystemClock{}
	}
	s.feeds = f
	return s
}

// Handler returns the fully wrapped HTTP handler (for httptest and embedding).
func (s *Server) Handler() http.Handler { return s.handler }

// ListenAndServe binds cfg.ListenAddr (loopback only) and serves until ctx is
// done, then shuts down gracefully within ShutdownTimeout. It returns nil on a
// clean shutdown.
func (s *Server) ListenAndServe(ctx context.Context) error {
	if err := checkListenAddr(s.cfg.ListenAddr); err != nil {
		return err
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("proxy: listen %s: %w", s.cfg.ListenAddr, err)
	}
	srv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	s.logger.Info("proxy listening", "addr", ln.Addr().String())
	if s.onListen != nil {
		s.onListen(ln.Addr())
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("proxy: serve: %w", err)
	case <-ctx.Done():
	}

	shutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		closeErr := srv.Close()
		return errors.Join(fmt.Errorf("proxy: shutdown: %w", err), closeErr)
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("proxy: serve: %w", err)
	}
	s.logger.Info("proxy stopped")
	return nil
}

// checkListenAddr refuses any listen address whose host is not loopback.
func checkListenAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%w: %q: %w", ErrNonLoopback, addr, err)
	}
	if !isLoopbackHost(host) {
		return fmt.Errorf("%w: %q", ErrNonLoopback, addr)
	}
	return nil
}

// isLoopbackHost reports whether host (without port, brackets optional) is an
// allowed loopback name.
func isLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	switch strings.ToLower(host) {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return false
}

// isLoopbackHostHeader reports whether a Host header value (with or without
// port) names loopback.
func isLoopbackHostHeader(h string) bool {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return isLoopbackHost(host)
	}
	return isLoopbackHost(h)
}

// isAllowedOrigin reports whether a CORS Origin is permitted: "null",
// file://, or an http(s) loopback origin.
func isAllowedOrigin(origin string) bool {
	if origin == "null" || strings.HasPrefix(origin, "file://") {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	return isLoopbackHost(u.Hostname())
}

func (s *Server) buildHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /status", s.handleStatus)
	mux.HandleFunc("POST /route", s.handleRoute)
	mux.HandleFunc("POST /escalate", s.handleEscalate)
	mux.HandleFunc("POST /session", s.handleSession)
	mux.HandleFunc("POST /usage", s.handleUsage)
	// Method-agnostic fallbacks produce JSON 405s with an Allow header.
	mux.HandleFunc("/healthz", methodNotAllowed("GET, HEAD"))
	mux.HandleFunc("/status", methodNotAllowed("GET, HEAD"))
	mux.HandleFunc("/route", methodNotAllowed("POST"))
	mux.HandleFunc("/escalate", methodNotAllowed("POST"))
	mux.HandleFunc("/session", methodNotAllowed("POST"))
	mux.HandleFunc("/usage", methodNotAllowed("POST"))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	return s.accessLog(guard(mux))
}

// guard applies the DNS-rebinding Host check, CORS policy, and body cap.
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentTypeJSON)
		if !isLoopbackHostHeader(r.Host) {
			writeError(w, http.StatusMisdirectedRequest, "host not allowed")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if !isAllowedOrigin(origin) {
				writeError(w, http.StatusForbidden, "origin not allowed")
				return
			}
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
			if r.Method == http.MethodOptions {
				h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Content-Type")
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the response status for access logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	if sr.status == 0 {
		sr.status = code
	}
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	if sr.status == 0 {
		sr.status = http.StatusOK
	}
	return sr.ResponseWriter.Write(b)
}

// accessLog logs method, path, status, and latency — never bodies.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		s.logger.LogAttrs(r.Context(), slog.LevelInfo, "http request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", status),
			slog.Duration("latency", time.Since(start)),
		)
	})
}

func methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", allow)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), RequestTimeout)
	defer cancel()
	writeJSON(w, http.StatusOK, s.router.Status(ctx))
}

func (s *Server) handleRoute(w http.ResponseWriter, r *http.Request) {
	var req schema.RouteRequest
	if status, msg := decodeBody(r, &req); status != 0 {
		writeError(w, status, msg)
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeError(w, http.StatusBadRequest, "empty prompt")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), RequestTimeout)
	defer cancel()
	d, err := s.router.Route(ctx, req)
	if err != nil {
		s.routerError(w, r, "route", err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// escalateRequest is the POST /escalate body.
type escalateRequest struct {
	Decision schema.RoutingDecision `json:"decision"`
	Cause    string                 `json:"cause"`
}

func (s *Server) handleEscalate(w http.ResponseWriter, r *http.Request) {
	var req escalateRequest
	if status, msg := decodeBody(r, &req); status != 0 {
		writeError(w, status, msg)
		return
	}
	if err := req.Decision.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid decision")
		return
	}
	cause := strings.TrimSpace(req.Cause)
	if cause == "" {
		cause = "unspecified"
	}
	ctx, cancel := context.WithTimeout(r.Context(), RequestTimeout)
	defer cancel()
	d, err := s.router.Escalate(ctx, req.Decision, errors.New(cause))
	if err != nil {
		s.routerError(w, r, "escalate", err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// handleSession records session activity so the router's cache-warmth rule
// can see it. A zero or future last_activity is replaced with the current time.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if s.feeds.Sessions == nil {
		writeError(w, http.StatusNotImplemented, "session feed not configured")
		return
	}
	var st schema.SessionState
	if status, msg := decodeBody(r, &st); status != 0 {
		writeError(w, status, msg)
		return
	}
	switch {
	case strings.TrimSpace(st.SessionID) == "" || len(st.SessionID) > MaxSessionIDLen:
		writeError(w, http.StatusBadRequest, "invalid session_id")
		return
	case strings.TrimSpace(st.Model) == "":
		writeError(w, http.StatusBadRequest, "empty model")
		return
	case st.CachedTokens < 0:
		writeError(w, http.StatusBadRequest, "negative cached_tokens")
		return
	}
	now := s.feeds.Clock.Now()
	if st.LastActivity.IsZero() || st.LastActivity.After(now) {
		st.LastActivity = now
	}
	s.feeds.Sessions.Touch(st)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// usageReport is the POST /usage body.
type usageReport struct {
	Tokens int64 `json:"tokens"`
}

// handleUsage records consumed tokens for the quota throttle windows.
func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	if s.feeds.Usage == nil {
		writeError(w, http.StatusNotImplemented, "usage feed not configured")
		return
	}
	var rep usageReport
	if status, msg := decodeBody(r, &rep); status != 0 {
		writeError(w, status, msg)
		return
	}
	if rep.Tokens <= 0 || rep.Tokens > MaxUsageTokens {
		writeError(w, http.StatusBadRequest, "tokens out of range")
		return
	}
	s.feeds.Usage.Add(rep.Tokens)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// routerError maps a router error to 502/500 with a generic body. The error
// text itself is never logged or returned since it may embed prompt content.
func (s *Server) routerError(w http.ResponseWriter, r *http.Request, op string, err error) {
	status, msg := http.StatusInternalServerError, "internal error"
	switch {
	case errors.Is(err, schema.ErrClassifierUnavailable):
		status, msg = http.StatusBadGateway, "classifier unavailable"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, schema.ErrTimeout):
		msg = "router timeout"
	}
	s.logger.LogAttrs(r.Context(), slog.LevelWarn, "router error",
		slog.String("op", op), slog.Int("status", status), slog.String("kind", msg))
	writeError(w, status, msg)
}

// decodeBody reads and strictly decodes a JSON body. It returns a non-zero
// status and message on failure.
func decodeBody(r *http.Request, dst any) (int, string) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return http.StatusRequestEntityTooLarge, "request body too large"
		}
		return http.StatusBadRequest, "cannot read body"
	}
	if len(data) == 0 {
		return http.StatusBadRequest, "empty body"
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return http.StatusBadRequest, "malformed JSON"
	}
	return 0, ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		status = http.StatusInternalServerError
		data = []byte(`{"error":"encode failure"}`)
	}
	w.Header().Set("Content-Type", contentTypeJSON)
	w.WriteHeader(status)
	// A failed write means the client went away; nothing useful to do.
	_, _ = w.Write(append(data, '\n'))
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
