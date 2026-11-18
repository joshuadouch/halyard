// Package api serves the Halyard REST API and the embedded web UI.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/joshuadouch/halyard/internal/auth"
	"github.com/joshuadouch/halyard/internal/config"
	"github.com/joshuadouch/halyard/internal/engine"
	"github.com/joshuadouch/halyard/internal/model"
	"github.com/joshuadouch/halyard/internal/store"
)

// Server is the HTTP API.
type Server struct {
	Cfg    *config.Config
	Store  *store.Store
	Engine *engine.Engine
	Log    *slog.Logger
	UI     fs.FS
	Access *auth.AccessVerifier
	// Version is shown in the UI.
	Version string

	limiter *loginLimiter
}

type ctxKey int

const userKey ctxKey = 1

const sessionCookie = "halyard_session"

// Handler builds the router.
func (s *Server) Handler() http.Handler {
	if s.Access == nil {
		s.Access = &auth.AccessVerifier{}
	}
	s.limiter = newLoginLimiter()
	mux := http.NewServeMux()
	// Public endpoints.
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"ok": true}) })
	mux.HandleFunc("GET /api/setup", s.getSetup)
	mux.HandleFunc("POST /api/setup", s.postSetup)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)
	mux.HandleFunc("POST /api/cluster/sync", s.clusterSync)

	// Authenticated endpoints.
	a := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireAuth(h)) }
	a("GET /api/me", s.me)
	a("POST /api/me/password", s.changePassword)
	a("POST /api/me/totp", s.totpSetup)
	a("POST /api/me/totp/enable", s.totpEnable)
	a("POST /api/me/totp/disable", s.totpDisable)
	a("GET /api/users", s.listUsers)
	a("POST /api/users", s.createUser)
	a("DELETE /api/users/{id}", s.deleteUser)

	a("GET /api/dashboard", s.dashboard)
	a("GET /api/stats", s.stats)
	a("GET /api/events", s.events)
	a("GET /api/nodes", s.nodes)
	a("POST /api/cluster/sync-now", s.syncNow)

	a("GET /api/vips", s.listVIPs)
	a("POST /api/vips", s.saveVIP)
	a("PUT /api/vips/{id}", s.saveVIP)
	a("DELETE /api/vips/{id}", s.deleteVIP)

	a("GET /api/hosts", s.listHosts)
	a("GET /api/hosts/{id}", s.getHost)
	a("POST /api/hosts", s.saveHost)
	a("PUT /api/hosts/{id}", s.saveHost)
	a("DELETE /api/hosts/{id}", s.deleteHost)

	a("GET /api/pools", s.listPools)
	a("GET /api/pools/{id}", s.getPool)
	a("POST /api/pools", s.savePool)
	a("PUT /api/pools/{id}", s.savePool)
	a("DELETE /api/pools/{id}", s.deletePool)
	a("POST /api/pools/{id}/members/{name}/state", s.memberState)

	a("GET /api/certs", s.listCerts)
	a("POST /api/certs", s.createCert)
	a("POST /api/certs/{id}/renew", s.renewCert)
	a("DELETE /api/certs/{id}", s.deleteCert)

	a("GET /api/cloudflare", s.getCloudflare)
	a("PUT /api/cloudflare", s.putCloudflare)
	a("POST /api/cloudflare/tunnel", s.cfTunnel)
	a("POST /api/cloudflare/reconcile", s.cfReconcile)
	a("POST /api/cloudflare/aop", s.cfAOP)
	a("POST /api/cloudflare/access-app", s.cfAccessApp)

	a("GET /api/revisions", s.listRevisions)
	a("GET /api/revisions/{id}", s.getRevision)
	a("POST /api/revisions/{id}/rollback", s.rollback)
	a("GET /api/preview", s.preview)
	a("POST /api/apply", s.apply)

	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, 404, "no such endpoint")
	}))
	if s.UI != nil {
		mux.Handle("/", spaHandler(s.UI))
	}
	return securityHeaders(mux)
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'")
		h.ServeHTTP(w, r)
	})
}

func spaHandler(ui fs.FS) http.Handler {
	files := http.FileServer(http.FS(ui))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if f, err := ui.Open(p); err == nil {
				st, _ := f.Stat()
				f.Close()
				if st != nil && !st.IsDir() {
					if strings.HasPrefix(p, "assets/") {
						w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
					}
					files.ServeHTTP(w, r)
					return
				}
			}
		}
		b, err := fs.ReadFile(ui, "index.html")
		if err != nil {
			http.Error(w, "UI not built", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
	})
}

// requireAuth accepts a session cookie or a Cloudflare Access JWT. Mutating
// requests also need the X-Halyard header, which browsers will not send
// cross-site without a CORS preflight we never allow.
func (s *Server) requireAuth(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := s.authenticate(r)
		if err != nil {
			writeErr(w, 401, "sign in required")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("X-Halyard") != "1" {
			writeErr(w, 403, "missing X-Halyard header")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	})
}

func (s *Server) authenticate(r *http.Request) (model.User, error) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if u, err := s.Store.SessionUser(r.Context(), c.Value); err == nil {
			return u, nil
		}
	}
	tok := r.Header.Get("Cf-Access-Jwt-Assertion")
	if tok == "" {
		if c, err := r.Cookie("CF_Authorization"); err == nil {
			tok = c.Value
		}
	}
	if tok == "" {
		return model.User{}, errors.New("no credentials")
	}
	cf, err := s.Store.Cloudflare(r.Context())
	if err != nil || cf.AccessTeam == "" || cf.AccessAUD == "" {
		return model.User{}, errors.New("access not configured")
	}
	email, err := s.Access.Verify(r.Context(), cf.AccessTeam, cf.AccessAUD, tok)
	if err != nil {
		return model.User{}, err
	}
	if u, err := s.Store.UserByEmail(r.Context(), email); err == nil {
		return u, nil
	}
	for _, allowed := range cf.AccessEmails {
		if strings.EqualFold(allowed, email) {
			u := model.User{ID: store.NewID(), Username: strings.ToLower(email), Email: strings.ToLower(email)}
			if err := s.saveUser(r.Context(), u); err != nil {
				return model.User{}, err
			}
			s.Store.AddEvent(r.Context(), "info", "auth", "Created user "+u.Email+" from Cloudflare Access")
			return u, nil
		}
	}
	return model.User{}, errors.New("access email is not a Halyard user")
}

func currentUser(r *http.Request) model.User {
	u, _ := r.Context().Value(userKey).(model.User)
	return u
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errors.New("invalid JSON: " + err.Error())
	}
	return nil
}

// syncNow pushes config and users to every peer immediately (primary only).
func (s *Server) syncNow(w http.ResponseWriter, r *http.Request) {
	if !s.Cfg.IsPrimary() {
		writeErr(w, 409, "only the primary pushes config")
		return
	}
	if len(s.Cfg.Cluster.Peers) == 0 {
		writeErr(w, 400, "no peers are configured in /etc/halyard/halyard.yaml")
		return
	}
	s.Engine.SyncPeers(r.Context())
	writeJSON(w, 200, s.Engine.PeerStatuses())
}

func (s *Server) clusterSync(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.Engine.ReceiveSync(r.Context(), body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// loginLimiter slows down password guessing per client IP.
type loginLimiter struct {
	fails map[string][]time.Time
}

func newLoginLimiter() *loginLimiter { return &loginLimiter{fails: map[string][]time.Time{}} }
