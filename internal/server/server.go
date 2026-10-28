package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aliAljaffer/chat-gateway/internal/config"
	"github.com/aliAljaffer/chat-gateway/internal/opencode"
	"github.com/aliAljaffer/chat-gateway/internal/store"
	"github.com/aliAljaffer/chat-gateway/internal/web"
)

var (
	errBodyTooLarge = errors.New("body is too large")
	errBadJSON      = errors.New("body must be a single JSON object")
)

type Server struct {
	cfg     config.Config
	store   *store.Store
	oc      *opencode.Client
	log     *slog.Logger
	access  *accessVerifier
	limiter *rateLimiter
	assets  http.Handler
	index   []byte
}

func New(cfg config.Config, st *store.Store, oc *opencode.Client, log *slog.Logger) (*Server, error) {
	assets := web.FS()
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg:     cfg,
		store:   st,
		oc:      oc,
		log:     log,
		access:  newAccessVerifier(cfg.CFTeamDomain, cfg.CFAudience, cfg.OwnerEmail),
		limiter: newRateLimiter(cfg.RatePerMinute),
		assets:  http.StripPrefix("/assets/", http.FileServer(http.FS(assets))),
		index:   index,
	}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.ready)

	mux.HandleFunc("POST /v1/chat", s.withApp(s.apiChat))
	mux.HandleFunc("POST /v1/chat/stream", s.withApp(s.apiChatStream))

	mux.HandleFunc("GET /api/me", s.withUI(s.me))
	mux.HandleFunc("GET /api/apps", s.withUI(s.listApps))
	mux.HandleFunc("POST /api/apps", s.withUI(s.createApp))
	mux.HandleFunc("DELETE /api/apps/{id}", s.withUI(s.deleteApp))
	mux.HandleFunc("GET /api/conversations", s.withUI(s.listConversations))
	mux.HandleFunc("POST /api/conversations", s.withUI(s.createConversation))
	mux.HandleFunc("DELETE /api/conversations/{id}", s.withUI(s.deleteConversation))
	mux.HandleFunc("GET /api/conversations/{id}/messages", s.withUI(s.listMessages))
	mux.HandleFunc("POST /api/conversations/{id}/messages", s.withUI(s.sendMessage))

	mux.HandleFunc("GET /", s.static)
	return s.recoverer(s.logger(s.security(mux)))
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	if err := s.oc.Health(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "assistant unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" || r.URL.Path == "/index.html" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(s.index)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/assets/") {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		s.assets.ServeHTTP(w, r)
		return
	}
	http.NotFound(w, r)
}

func (s *Server) withApp(h func(http.ResponseWriter, *http.Request, store.App)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := bearerToken(r)
		if key == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		app, err := s.store.AppByKey(r.Context(), key)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			s.log.Error("app lookup failed", "error", err)
			writeError(w, http.StatusInternalServerError, "could not read the app")
			return
		}
		if !s.limiter.allow(app.ID) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		h(w, r, app)
	}
}

func (s *Server) withUI(h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
			writeError(w, http.StatusForbidden, "cross-site request refused")
			return
		}
		email, err := s.uiUser(r)
		if err != nil {
			switch {
			case errors.Is(err, errUINotConfigured):
				writeError(w, http.StatusServiceUnavailable, "ui auth is not configured")
			case errors.Is(err, errUnauthorized):
				writeError(w, http.StatusUnauthorized, "unauthorized")
			default:
				s.log.Error("ui auth failed", "error", err)
				writeError(w, http.StatusServiceUnavailable, "ui auth is unavailable")
			}
			return
		}
		if !s.limiter.allow("ui:" + email) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		h(w, r, email)
	}
}

func (s *Server) uiUser(r *http.Request) (string, error) {
	if s.cfg.DevUI {
		if email := strings.TrimSpace(r.Header.Get("X-Chat-Dev-Email")); email != "" {
			return strings.ToLower(email), nil
		}
		return "dev@localhost", nil
	}
	if s.access.issuer == "" || s.access.audience == "" {
		return "", errUINotConfigured
	}
	raw := strings.TrimSpace(r.Header.Get("Cf-Access-Jwt-Assertion"))
	if raw == "" {
		return "", errUnauthorized
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	return s.access.verify(ctx, raw)
}

func (s *Server) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		s.log.Info("request", "method", r.Method, "path", r.URL.Path,
			"status", sw.status, "duration_ms", time.Since(started).Milliseconds())
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("panic", "path", r.URL.Path, "value", v)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	host := r.Host
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); forwarded != "" {
		host = forwarded
	}
	return origin == "https://"+host || origin == "http://"+host
}

func readBody(r *http.Request, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errBodyTooLarge
	}
	return data, nil
}

func decodeJSON(data []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errBadJSON
	}
	if dec.More() {
		return errBadJSON
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
