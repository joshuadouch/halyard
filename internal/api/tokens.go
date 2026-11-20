package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/joshuadouch/halyard/internal/model"
	"github.com/joshuadouch/halyard/internal/store"
)

// API tokens let other programs (like Berth) use the API with
// "Authorization: Bearer hlt_...". Only a hash of each token is kept.

const kvAPITokens = "api_tokens"

type apiToken struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Hash     string    `json:"hash"`
	Prefix   string    `json:"prefix"` // first characters, to recognise it
	Created  time.Time `json:"created"`
	Author   string    `json:"author"`
	LastUsed time.Time `json:"last_used"`
}

var tokenMu sync.Mutex

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

func (s *Server) loadTokens(ctx context.Context) []apiToken {
	var ts []apiToken
	_ = s.Store.GetKV(ctx, kvAPITokens, &ts)
	return ts
}

// tokenUser authenticates a bearer token. The returned user is not stored;
// its name shows in config history as "api:<token name>".
func (s *Server) tokenUser(r *http.Request) (model.User, bool, error) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer hlt_") {
		return model.User{}, false, nil
	}
	want := hashToken(strings.TrimPrefix(h, "Bearer "))
	tokenMu.Lock()
	defer tokenMu.Unlock()
	ts := s.loadTokens(r.Context())
	for i, t := range ts {
		if subtle.ConstantTimeCompare([]byte(t.Hash), []byte(want)) == 1 {
			if time.Since(t.LastUsed) > time.Minute {
				ts[i].LastUsed = time.Now().UTC()
				_ = s.Store.SetKV(r.Context(), kvAPITokens, ts)
			}
			return model.User{ID: "token:" + t.ID, Username: "api:" + t.Name}, true, nil
		}
	}
	return model.User{}, true, errors.New("unknown API token")
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	ts := s.loadTokens(r.Context())
	out := make([]apiToken, 0, len(ts))
	for _, t := range ts {
		t.Hash = ""
		out = append(out, t)
	}
	writeJSON(w, 200, out)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(currentUser(r).ID, "token:") {
		writeErr(w, 403, "API tokens cannot create tokens")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 64 {
		writeErr(w, 400, "give the token a name of up to 64 characters, like berth")
		return
	}
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	tok := "hlt_" + hex.EncodeToString(b)
	t := apiToken{ID: store.NewID(), Name: req.Name, Hash: hashToken(tok), Prefix: tok[:10], Created: time.Now().UTC(), Author: currentUser(r).Username}
	tokenMu.Lock()
	ts := append(s.loadTokens(r.Context()), t)
	err := s.Store.SetKV(r.Context(), kvAPITokens, ts)
	tokenMu.Unlock()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.Store.AddEvent(r.Context(), "info", "auth", currentUser(r).Username+" created API token "+t.Name)
	t.Hash = ""
	writeJSON(w, 200, map[string]any{"token": tok, "info": t})
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tokenMu.Lock()
	ts := s.loadTokens(r.Context())
	var keep []apiToken
	name := ""
	for _, t := range ts {
		if t.ID == id {
			name = t.Name
			continue
		}
		keep = append(keep, t)
	}
	var err error
	if name != "" {
		err = s.Store.SetKV(r.Context(), kvAPITokens, keep)
	}
	tokenMu.Unlock()
	if name == "" {
		writeErr(w, 404, "token not found")
		return
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.Store.AddEvent(r.Context(), "info", "auth", currentUser(r).Username+" revoked API token "+name)
	writeJSON(w, 200, map[string]any{"ok": true})
}
