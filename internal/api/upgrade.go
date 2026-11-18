package api

import (
	"io"
	"net/http"
)

func (s *Server) updateInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.Engine.UpgradeInfo(r.Context(), r.URL.Query().Get("refresh") == "1"))
}

func (s *Server) startUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target string `json:"target"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if req.Target == "" {
		tag, _, err := s.Engine.LatestRelease(r.Context())
		if err != nil {
			writeErr(w, 502, "could not check the latest release: "+err.Error())
			return
		}
		req.Target = tag
	}
	if err := s.Engine.StartUpgrade(req.Target, currentUser(r).Username); err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, s.Engine.UpgradeInfo(r.Context()))
}

// clusterUpgrade is called by the primary; the body is sealed with the cluster secret.
func (s *Server) clusterUpgrade(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.Engine.ReceiveUpgrade(body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
