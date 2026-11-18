package api

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/joshuadouch/halyard/internal/auth"
	"github.com/joshuadouch/halyard/internal/model"
	"github.com/joshuadouch/halyard/internal/store"
)

var limiterMu sync.Mutex

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

func (l *loginLimiter) blocked(ip string) bool {
	limiterMu.Lock()
	defer limiterMu.Unlock()
	cut := time.Now().Add(-15 * time.Minute)
	var keep []time.Time
	for _, t := range l.fails[ip] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	l.fails[ip] = keep
	return len(keep) >= 10
}

func (l *loginLimiter) fail(ip string) {
	limiterMu.Lock()
	defer limiterMu.Unlock()
	l.fails[ip] = append(l.fails[ip], time.Now())
}

func (s *Server) setSession(w http.ResponseWriter, r *http.Request, u model.User) error {
	tok := auth.RandomToken(32)
	if err := s.Store.CreateSession(r.Context(), tok, u.ID, 7*24*time.Hour); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteStrictMode, MaxAge: 7 * 24 * 3600})
	return nil
}

func (s *Server) getSetup(w http.ResponseWriter, r *http.Request) {
	n, err := s.Store.CountUsers(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	cf, _ := s.Store.Cloudflare(r.Context())
	writeJSON(w, 200, map[string]any{
		"needs_setup": n == 0, "version": s.Version, "node": s.Cfg.Node.Name,
		"access_enabled": cf.AccessTeam != "" && cf.AccessAUD != "",
	})
}

func (s *Server) postSetup(w http.ResponseWriter, r *http.Request) {
	if !s.Cfg.IsPrimary() {
		writeErr(w, 409, "This is a standby node. Create the admin on the primary; accounts sync here within 5 minutes.")
		return
	}
	n, err := s.Store.CountUsers(r.Context())
	if err != nil || n > 0 {
		writeErr(w, 403, "setup is already complete")
		return
	}
	var req struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	u, err := newUser(req.Username, req.Email, req.Password)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.saveUser(r.Context(), u); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.Store.AddEvent(r.Context(), "info", "auth", "Initial admin "+u.Username+" created")
	_ = s.setSession(w, r, u)
	writeJSON(w, 200, u)
}

func newUser(username, email, password string) (model.User, error) {
	username = strings.TrimSpace(username)
	if len(username) < 2 || len(username) > 64 {
		return model.User{}, errString("username must be 2-64 characters")
	}
	email = strings.TrimSpace(email)
	if email != "" && !strings.Contains(email, "@") {
		return model.User{}, errString("email looks invalid")
	}
	u := model.User{ID: store.NewID(), Username: username, Email: strings.ToLower(email)}
	if password != "" {
		h, err := auth.HashPassword(password)
		if err != nil {
			return u, err
		}
		u.PasswordHash = h
	} else if email == "" {
		return u, errString("set a password, or an email for Cloudflare Access sign-in")
	}
	return u, nil
}

type errString string

func (e errString) Error() string { return string(e) }

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.limiter.blocked(ip) {
		writeErr(w, 429, "too many failed attempts; try again in 15 minutes")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		TOTP     string `json:"totp"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	u, err := s.Store.UserByName(r.Context(), strings.TrimSpace(req.Username))
	if err != nil || !auth.CheckPassword(u.PasswordHash, req.Password) {
		s.limiter.fail(ip)
		writeErr(w, 401, "wrong username or password")
		return
	}
	if u.TOTPEnabled {
		if req.TOTP == "" {
			writeJSON(w, 401, map[string]any{"error": "enter your authenticator code", "totp_required": true})
			return
		}
		if !auth.CheckTOTP(u.TOTPSecret, req.TOTP) {
			s.limiter.fail(ip)
			writeJSON(w, 401, map[string]any{"error": "wrong authenticator code", "totp_required": true})
			return
		}
	}
	if err := s.setSession(w, r, u); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, u)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.Store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true})
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	writeJSON(w, 200, map[string]any{
		"user": u, "node": s.Cfg.Node.Name, "role": s.Cfg.Node.Role, "version": s.Version,
		"has_password": u.PasswordHash != "",
	})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	u := currentUser(r)
	if u.PasswordHash != "" && !auth.CheckPassword(u.PasswordHash, req.Current) {
		writeErr(w, 400, "current password is wrong")
		return
	}
	h, err := auth.HashPassword(req.New)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	u.PasswordHash = h
	if err := s.saveUser(r.Context(), u); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) totpSetup(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if u.TOTPEnabled {
		writeErr(w, 400, "two-factor is already on")
		return
	}
	u.TOTPSecret = auth.NewTOTPSecret()
	if err := s.saveUser(r.Context(), u); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"secret": u.TOTPSecret, "uri": auth.TOTPURI(u.TOTPSecret, u.Username)})
}

func (s *Server) totpEnable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	u := currentUser(r)
	if u.TOTPSecret == "" || !auth.CheckTOTP(u.TOTPSecret, req.Code) {
		writeErr(w, 400, "that code is not valid")
		return
	}
	u.TOTPEnabled = true
	if err := s.saveUser(r.Context(), u); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) totpDisable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	u := currentUser(r)
	if !u.TOTPEnabled || !auth.CheckTOTP(u.TOTPSecret, req.Code) {
		writeErr(w, 400, "that code is not valid")
		return
	}
	u.TOTPEnabled, u.TOTPSecret = false, ""
	if err := s.saveUser(r.Context(), u); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	us, err := s.Store.Users(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, us)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	u, err := newUser(req.Username, req.Email, req.Password)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if _, err := s.Store.UserByName(r.Context(), u.Username); err == nil {
		writeErr(w, 400, "username is taken")
		return
	}
	if err := s.saveUser(r.Context(), u); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.Store.AddEvent(r.Context(), "info", "auth", currentUser(r).Username+" added user "+u.Username)
	writeJSON(w, 200, u)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == currentUser(r).ID {
		writeErr(w, 400, "you cannot delete yourself")
		return
	}
	if err := s.removeUser(r.Context(), id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// saveUser and removeUser push account changes to standby nodes, so the same
// logins work everywhere.
func (s *Server) saveUser(ctx context.Context, u model.User) error {
	if err := s.Store.SaveUser(ctx, u); err != nil {
		return err
	}
	s.usersChanged()
	return nil
}

func (s *Server) removeUser(ctx context.Context, id string) error {
	if err := s.Store.DeleteUser(ctx, id); err != nil {
		return err
	}
	s.usersChanged()
	return nil
}

func (s *Server) usersChanged() {
	if s.Engine != nil && s.Cfg.IsPrimary() {
		go s.Engine.SyncPeers(context.Background())
	}
}
