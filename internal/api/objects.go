package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/joshuadouch/halyard/internal/acme"
	"github.com/joshuadouch/halyard/internal/engine"
	"github.com/joshuadouch/halyard/internal/model"
	"github.com/joshuadouch/halyard/internal/render"
	"github.com/joshuadouch/halyard/internal/store"
)

// mutate runs fn and applies the result. If apply fails the previous state is
// restored, so a bad edit never sticks.
func (s *Server) mutate(w http.ResponseWriter, r *http.Request, msg string, withCerts bool, fn func(ctx context.Context) error) bool {
	if !s.Cfg.IsPrimary() {
		writeErr(w, 409, "this node is a standby; make changes on the primary")
		return false
	}
	ctx := r.Context()
	before, err := s.Store.TakeSnapshot(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return false
	}
	if err := fn(ctx); err != nil {
		code := 400
		if errors.Is(err, store.ErrNotFound) {
			code = 404
		}
		writeErr(w, code, err.Error())
		return false
	}
	if _, err := s.Engine.Apply(ctx, currentUser(r).Username, msg); err != nil {
		_ = s.Store.RestoreSnapshot(ctx, before, withCerts)
		var ve *engine.ValidationError
		if errors.As(err, &ve) {
			writeJSON(w, 422, map[string]string{"error": "HAProxy rejected the change; nothing was applied.", "output": ve.Output})
			return false
		}
		writeErr(w, 422, err.Error()+" (nothing was applied)")
		return false
	}
	return true
}

// VIPs ------------------------------------------------------------------

func (s *Server) listVIPs(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.VIPs(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	hosts, _ := s.Store.Hosts(r.Context())
	local := localAddrs()
	type row struct {
		model.VIP
		Hosts []string `json:"hosts"`
		Local bool     `json:"local"`
	}
	out := []row{}
	for _, x := range v {
		rw := row{VIP: x, Hosts: []string{}, Local: local[x.IP()]}
		for _, h := range hosts {
			if h.VIPID == x.ID && len(h.Hostnames) > 0 {
				rw.Hosts = append(rw.Hosts, h.Hostnames[0])
			}
		}
		out = append(out, rw)
	}
	writeJSON(w, 200, out)
}

func (s *Server) saveVIP(w http.ResponseWriter, r *http.Request) {
	var v model.VIP
	if err := readJSON(r, &v); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	id := r.PathValue("id")
	ok := s.mutate(w, r, "Saved VIP "+v.Address, false, func(ctx context.Context) error {
		if id != "" {
			var old model.VIP
			if err := s.Store.Get(ctx, store.KindVIP, id, &old); err != nil {
				return err
			}
			v.ID = id
		} else {
			v.ID = store.NewID()
		}
		if err := v.Validate(); err != nil {
			return err
		}
		all, err := s.Store.VIPs(ctx)
		if err != nil {
			return err
		}
		for _, o := range all {
			if o.ID == v.ID {
				continue
			}
			if o.VRID == v.VRID && o.Interface == v.Interface {
				return fmt.Errorf("VRRP ID %d is already used on %s", v.VRID, v.Interface)
			}
			if o.IP() == v.IP() {
				return fmt.Errorf("%s is already a VIP", v.IP())
			}
		}
		return s.Store.Put(ctx, store.KindVIP, v.ID, v)
	})
	if ok {
		writeJSON(w, 200, v)
	}
}

func (s *Server) deleteVIP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ok := s.mutate(w, r, "Deleted VIP", false, func(ctx context.Context) error {
		hosts, err := s.Store.Hosts(ctx)
		if err != nil {
			return err
		}
		for _, h := range hosts {
			if h.VIPID == id {
				return fmt.Errorf("host %s still uses this VIP", first(h.Hostnames))
			}
		}
		return s.Store.Delete(ctx, store.KindVIP, id)
	})
	if ok {
		writeJSON(w, 200, map[string]any{"ok": true})
	}
}

// Hosts -----------------------------------------------------------------

func (s *Server) listHosts(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.Hosts(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) getHost(w http.ResponseWriter, r *http.Request) {
	var h model.Host
	if err := s.Store.Get(r.Context(), store.KindHost, r.PathValue("id"), &h); err != nil {
		writeErr(w, 404, "host not found")
		return
	}
	writeJSON(w, 200, h)
}

func (s *Server) saveHost(w http.ResponseWriter, r *http.Request) {
	var h model.Host
	if err := readJSON(r, &h); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	id := r.PathValue("id")
	ok := s.mutate(w, r, "Saved host "+first(h.Hostnames), false, func(ctx context.Context) error {
		if id != "" {
			var old model.Host
			if err := s.Store.Get(ctx, store.KindHost, id, &old); err != nil {
				return err
			}
			h.ID = id
		} else {
			h.ID = store.NewID()
		}
		if err := h.Normalize(); err != nil {
			return err
		}
		var v model.VIP
		if err := s.Store.Get(ctx, store.KindVIP, h.VIPID, &v); err != nil {
			return errors.New("the selected VIP does not exist")
		}
		for _, rt := range h.Routes {
			var p model.Pool
			if err := s.Store.Get(ctx, store.KindPool, rt.PoolID, &p); err != nil {
				return errors.New("a route points at a pool that does not exist")
			}
		}
		if h.CertificateID != "" {
			var c model.Certificate
			if err := s.Store.Get(ctx, store.KindCert, h.CertificateID, &c); err != nil {
				return errors.New("the selected certificate does not exist")
			}
		}
		return s.Store.Put(ctx, store.KindHost, h.ID, h)
	})
	if ok {
		writeJSON(w, 200, h)
	}
}

func (s *Server) deleteHost(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ok := s.mutate(w, r, "Deleted host", false, func(ctx context.Context) error {
		return s.Store.Delete(ctx, store.KindHost, id)
	})
	if ok {
		writeJSON(w, 200, map[string]any{"ok": true})
	}
}

// Pools -----------------------------------------------------------------

func (s *Server) listPools(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.Pools(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) getPool(w http.ResponseWriter, r *http.Request) {
	var p model.Pool
	if err := s.Store.Get(r.Context(), store.KindPool, r.PathValue("id"), &p); err != nil {
		writeErr(w, 404, "pool not found")
		return
	}
	writeJSON(w, 200, p)
}

func (s *Server) savePool(w http.ResponseWriter, r *http.Request) {
	var p model.Pool
	if err := readJSON(r, &p); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	id := r.PathValue("id")
	ok := s.mutate(w, r, "Saved pool "+p.Name, false, func(ctx context.Context) error {
		if id != "" {
			var old model.Pool
			if err := s.Store.Get(ctx, store.KindPool, id, &old); err != nil {
				return err
			}
			p.ID = id
		} else {
			p.ID = store.NewID()
		}
		if err := p.Normalize(); err != nil {
			return err
		}
		if p.Health.Type == "http" {
			if _, err := render.ExpectToHAProxy(p.Health.ExpectStatus); err != nil {
				return err
			}
		}
		all, err := s.Store.Pools(ctx)
		if err != nil {
			return err
		}
		for _, o := range all {
			if o.ID != p.ID && strings.EqualFold(o.Name, p.Name) {
				return fmt.Errorf("a pool named %s already exists", p.Name)
			}
		}
		return s.Store.Put(ctx, store.KindPool, p.ID, p)
	})
	if ok {
		writeJSON(w, 200, p)
	}
}

func (s *Server) deletePool(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ok := s.mutate(w, r, "Deleted pool", false, func(ctx context.Context) error {
		hosts, err := s.Store.Hosts(ctx)
		if err != nil {
			return err
		}
		for _, h := range hosts {
			for _, rt := range h.Routes {
				if rt.PoolID == id {
					return fmt.Errorf("host %s still routes to this pool", first(h.Hostnames))
				}
			}
		}
		return s.Store.Delete(ctx, store.KindPool, id)
	})
	if ok {
		writeJSON(w, 200, map[string]any{"ok": true})
	}
}

// memberState drains, disables or re-enables a member immediately through the
// Runtime API and persists it so the state survives reloads.
func (s *Server) memberState(w http.ResponseWriter, r *http.Request) {
	var req struct {
		State string `json:"state"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if req.State != "ready" && req.State != "drain" && req.State != "maint" {
		writeErr(w, 400, "state must be ready, drain or maint")
		return
	}
	if !s.Cfg.IsPrimary() {
		writeErr(w, 409, "this node is a standby; make changes on the primary")
		return
	}
	ctx := r.Context()
	var p model.Pool
	if err := s.Store.Get(ctx, store.KindPool, r.PathValue("id"), &p); err != nil {
		writeErr(w, 404, "pool not found")
		return
	}
	name := r.PathValue("name")
	found := false
	for i := range p.Members {
		if p.Members[i].Name == name {
			p.Members[i].State = req.State
			found = true
		}
	}
	if !found {
		writeErr(w, 404, "member not found")
		return
	}
	if err := s.Store.Put(ctx, store.KindPool, p.ID, p); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// Runtime change first so it takes effect now; the apply makes it stick.
	be := render.BackendName(p.Name)
	rtErr := s.Engine.Runtime.SetServerState(be, render.ServerName(name), req.State)
	if _, err := s.Engine.Apply(ctx, currentUser(r).Username, fmt.Sprintf("Set %s/%s to %s", p.Name, name, req.State)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.Store.AddEvent(ctx, "info", "pool", fmt.Sprintf("%s set %s/%s to %s", currentUser(r).Username, p.Name, name, req.State))
	resp := map[string]any{"ok": true}
	if rtErr != nil {
		resp["runtime_warning"] = rtErr.Error()
	}
	writeJSON(w, 200, resp)
}

// Certificates ------------------------------------------------------------

func (s *Server) listCerts(w http.ResponseWriter, r *http.Request) {
	cs, err := s.Store.Certs(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	hosts, _ := s.Store.Hosts(r.Context())
	type row struct {
		model.Certificate
		Hosts   int  `json:"hosts"`
		Issuing bool `json:"issuing"`
	}
	out := []row{}
	for _, c := range cs {
		rw := row{Certificate: c.Public(), Issuing: s.Engine.Issuing(c.ID)}
		rw.CertPEM = ""
		for _, h := range hosts {
			if h.CertificateID == c.ID {
				rw.Hosts++
				continue
			}
			if h.CertificateID != "" {
				continue
			}
			for _, n := range h.Hostnames {
				hit := false
				for _, d := range c.Domains {
					if render.MatchWildcard(d, n) {
						hit = true
					}
				}
				if hit {
					rw.Hosts++
					break
				}
			}
		}
		out = append(out, rw)
	}
	writeJSON(w, 200, out)
}

func (s *Server) createCert(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domains []string `json:"domains"`
		KeyType string   `json:"key_type"`
		CertPEM string   `json:"cert_pem"`
		KeyPEM  string   `json:"key_pem"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	c := model.Certificate{ID: store.NewID(), UpdatedAt: time.Now().UTC()}
	if req.CertPEM != "" {
		notAfter, issuer, err := acme.ParseCert([]byte(req.CertPEM))
		if err != nil {
			writeErr(w, 400, "certificate: "+err.Error())
			return
		}
		if !strings.Contains(req.KeyPEM, "PRIVATE KEY") {
			writeErr(w, 400, "paste the private key in PEM form")
			return
		}
		domains, _ := acme.ParseCertDomains([]byte(req.CertPEM))
		c.Domains, c.Source, c.CertPEM, c.KeyPEM = domains, "upload", req.CertPEM, req.KeyPEM
		c.NotAfter, c.Issuer, c.Status = notAfter, issuer, "valid"
		if _, err := tlsPair(req.CertPEM, req.KeyPEM); err != nil {
			writeErr(w, 400, "certificate and key do not match: "+err.Error())
			return
		}
		ok := s.mutate(w, r, "Uploaded certificate "+first(domains), true, func(ctx context.Context) error {
			return s.Store.Put(ctx, store.KindCert, c.ID, c)
		})
		if ok {
			writeJSON(w, 200, c.Public())
		}
		return
	}
	if len(req.Domains) == 0 {
		writeErr(w, 400, "add at least one domain")
		return
	}
	for i, d := range req.Domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if !model.ValidHostname(d) || !strings.Contains(d, ".") {
			writeErr(w, 400, "invalid domain "+d)
			return
		}
		req.Domains[i] = d
	}
	if req.KeyType != "rsa2048" {
		req.KeyType = "ec256"
	}
	c.Domains, c.Source, c.KeyType, c.Status = req.Domains, "acme", req.KeyType, "pending"
	if err := s.Store.Put(r.Context(), store.KindCert, c.ID, c); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := s.Engine.IssueCertificate(c.ID); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, c.Public())
}

func (s *Server) renewCert(w http.ResponseWriter, r *http.Request) {
	var c model.Certificate
	if err := s.Store.Get(r.Context(), store.KindCert, r.PathValue("id"), &c); err != nil {
		writeErr(w, 404, "certificate not found")
		return
	}
	if c.Source != "acme" {
		writeErr(w, 400, "uploaded certificates are renewed by uploading a new one")
		return
	}
	if err := s.Engine.IssueCertificate(c.ID); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) deleteCert(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ok := s.mutate(w, r, "Deleted certificate", true, func(ctx context.Context) error {
		hosts, err := s.Store.Hosts(ctx)
		if err != nil {
			return err
		}
		for _, h := range hosts {
			if h.CertificateID == id {
				return fmt.Errorf("host %s is pinned to this certificate", first(h.Hostnames))
			}
		}
		return s.Store.Delete(ctx, store.KindCert, id)
	})
	if ok {
		writeJSON(w, 200, map[string]any{"ok": true})
	}
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}
