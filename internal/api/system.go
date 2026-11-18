package api

import (
	"crypto/tls"
	"errors"
	"github.com/joshuadouch/halyard/internal/render"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joshuadouch/halyard/internal/cloudflare"
	"github.com/joshuadouch/halyard/internal/engine"
	"github.com/joshuadouch/halyard/internal/haproxy"
	"github.com/joshuadouch/halyard/internal/model"
)

func tlsPair(certPEM, keyPEM string) (tls.Certificate, error) {
	return tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
}

func localAddrs() map[string]bool {
	m := map[string]bool{}
	if as, err := net.InterfaceAddrs(); err == nil {
		for _, a := range as {
			if n, ok := a.(*net.IPNet); ok {
				m[n.IP.String()] = true
			}
		}
	}
	return m
}

// PoolStatus summarises one backend from HAProxy stats.
type PoolStatus struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Up       int            `json:"up"`
	Total    int            `json:"total"`
	Rate     int            `json:"rate"`
	Sessions int            `json:"sessions"`
	Resp5xx  int64          `json:"resp_5xx"`
	RespAll  int64          `json:"resp_total"`
	AvgMS    int            `json:"avg_ms"`
	Members  []haproxy.Stat `json:"members"`
	Status   string         `json:"status"` // healthy, degraded, down, unknown
}

func (s *Server) poolStatuses(pools []model.Pool) ([]PoolStatus, bool) {
	stats, err := s.Engine.Runtime.ShowStat()
	return s.poolStatusesFrom(pools, stats, err == nil)
}

// poolStatusesFrom summarises pools from stats already read; live says
// whether HAProxy answered.
func (s *Server) poolStatusesFrom(pools []model.Pool, stats []haproxy.Stat, live bool) ([]PoolStatus, bool) {
	byBE := map[string][]haproxy.Stat{}
	for _, st := range stats {
		byBE[st.Proxy] = append(byBE[st.Proxy], st)
	}
	var out []PoolStatus
	for _, p := range pools {
		ps := PoolStatus{ID: p.ID, Name: p.Name, Status: "unknown", Members: []haproxy.Stat{}}
		for _, st := range byBE[render.BackendName(p.Name)] {
			switch st.Server {
			case "BACKEND":
				ps.Rate, ps.Sessions, ps.Resp5xx, ps.RespAll, ps.AvgMS = st.Rate, st.CurSessions, st.Resp5xx, st.RespTotal, st.AvgTotalMS
			case "FRONTEND":
			default:
				ps.Members = append(ps.Members, st)
				ps.Total++
				if strings.HasPrefix(st.Status, "UP") || st.Status == "no check" {
					ps.Up++
				}
			}
		}
		if live {
			activeTotal := 0
			for _, m := range p.Members {
				if !m.Backup && m.State == "ready" {
					activeTotal++
				}
			}
			upActive := 0
			for _, st := range ps.Members {
				for _, m := range p.Members {
					if render.ServerName(m.Name) == st.Server && !m.Backup && m.State == "ready" && (strings.HasPrefix(st.Status, "UP") || st.Status == "no check") {
						upActive++
					}
				}
			}
			switch {
			case activeTotal == 0 || upActive == 0:
				ps.Status = "down"
			case upActive < activeTotal:
				ps.Status = "degraded"
			default:
				ps.Status = "healthy"
			}
			if len(p.Members) == 0 {
				ps.Status = "empty"
			}
		}
		out = append(out, ps)
	}
	return out, live
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pools, _ := s.Store.Pools(ctx)
	hosts, _ := s.Store.Hosts(ctx)
	vips, _ := s.Store.VIPs(ctx)
	certs, _ := s.Store.Certs(ctx)
	events, _ := s.Store.Events(ctx, 12)
	statuses, live := s.poolStatuses(pools)
	reqRate, sessions := 0, 0
	if stats, err := s.Engine.Runtime.ShowStat(); err == nil {
		for _, st := range stats {
			if st.Server == "FRONTEND" && strings.HasPrefix(st.Proxy, "fe_") {
				reqRate += st.ReqRate
				if st.ReqRate == 0 {
					reqRate += st.Rate
				}
				sessions += st.CurSessions
			}
		}
	}
	var up, total int
	var r5xx, rall int64
	for _, p := range statuses {
		up += p.Up
		total += p.Total
		r5xx += p.Resp5xx
		rall += p.RespAll
	}
	expiring := 0
	for _, c := range certs {
		if c.CertPEM != "" && c.NotAfter.Sub(nowUTC()).Hours() < 14*24 {
			expiring++
		}
	}
	local := localAddrs()
	type vipRow struct {
		model.VIP
		Hosts int  `json:"hosts"`
		Local bool `json:"local"`
	}
	vr := []vipRow{}
	for _, v := range vips {
		row := vipRow{VIP: v, Local: local[v.IP()]}
		for _, h := range hosts {
			if h.VIPID == v.ID {
				row.Hosts++
			}
		}
		vr = append(vr, row)
	}
	last, _ := s.Store.Revisions(ctx, 1)
	writeJSON(w, 200, map[string]any{
		"live": live, "req_rate": reqRate, "sessions": sessions, "members_up": up, "members_total": total,
		"resp_5xx": r5xx, "resp_total": rall, "hosts": len(hosts), "pools": statuses, "vips": vr,
		"certs": len(certs), "certs_expiring": expiring, "events": events, "last_revision": last,
		"node": s.Cfg.Node.Name, "role": s.Cfg.Node.Role,
	})
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	pools, _ := s.Store.Pools(r.Context())
	st, live := s.poolStatuses(pools)
	writeJSON(w, 200, map[string]any{"live": live, "pools": st})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if n <= 0 || n > 500 {
		n = 100
	}
	ev, err := s.Store.Events(r.Context(), n)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, ev)
}

func (s *Server) nodes(w http.ResponseWriter, r *http.Request) {
	vips, _ := s.Store.VIPs(r.Context())
	local := localAddrs()
	var held []string
	for _, v := range vips {
		if local[v.IP()] {
			held = append(held, v.Address)
		}
	}
	_, hapErr := os.Stat(s.Cfg.HAProxy.Socket)
	writeJSON(w, 200, map[string]any{
		"self": map[string]any{
			"name": s.Cfg.Node.Name, "role": s.Cfg.Node.Role, "address": s.Cfg.Node.Address,
			"vips_held": held, "haproxy_socket": hapErr == nil, "keepalived": s.Cfg.Keepalived.Enabled,
		},
		"peers": s.Engine.PeerStatuses(),
	})
}

// Cloudflare ----------------------------------------------------------------

type cfView struct {
	model.CloudflareSettings
	TokenSet bool                    `json:"token_set"`
	Zones    []cloudflare.Zone       `json:"zones"`
	Tunnel   *cloudflare.Tunnel      `json:"tunnel"`
	Report   engine.CloudflareReport `json:"report"`
	Error    string                  `json:"error"`
	AOP      map[string]bool         `json:"aop"`
}

func (s *Server) getCloudflare(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cf, err := s.Store.Cloudflare(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	v := cfView{CloudflareSettings: cf, TokenSet: cf.APIToken != "", Report: s.Engine.LastCloudflareReport(), AOP: map[string]bool{}}
	v.APIToken = ""
	// light=1 skips the Cloudflare API (pages that only need settings).
	if cf.APIToken != "" && r.URL.Query().Get("light") != "1" {
		st := s.Engine.CloudflareStatus(ctx, r.URL.Query().Get("refresh") == "1")
		v.Zones, v.AOP, v.Tunnel, v.Error = st.Zones, st.AOP, st.Tunnel, st.Error
	}
	writeJSON(w, 200, v)
}

func (s *Server) putCloudflare(w http.ResponseWriter, r *http.Request) {
	if !s.Cfg.IsPrimary() {
		writeErr(w, 409, "this node is a standby; make changes on the primary")
		return
	}
	var req model.CloudflareSettings
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx := r.Context()
	cur, err := s.Store.Cloudflare(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// Server-managed fields are kept.
	req.TunnelID, req.TunnelName, req.AOPZones, req.AccessAppID, req.LastReconcile = cur.TunnelID, cur.TunnelName, cur.AOPZones, cur.AccessAppID, cur.LastReconcile
	if req.APIToken == "" {
		req.APIToken = cur.APIToken
	}
	if req.APIToken != "" && req.APIToken != cur.APIToken {
		cl := cloudflare.New(req.APIToken)
		if err := cl.VerifyToken(ctx, req.AccountID); err != nil {
			writeErr(w, 400, "token check failed: "+err.Error())
			return
		}
		if req.AccountID == "" {
			if zones, err := cl.Zones(ctx); err == nil && len(zones) > 0 {
				req.AccountID = zones[0].Account.ID
			}
		}
	}
	if req.PublicIP != "" && net.ParseIP(req.PublicIP) == nil {
		writeErr(w, 400, "public IP is not an IP address")
		return
	}
	if req.UIHostname != "" && !model.ValidHostname(req.UIHostname) {
		writeErr(w, 400, "UI hostname is invalid")
		return
	}
	if err := s.Store.SetCloudflare(ctx, req); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.Engine.InvalidateCloudflareStatus()
	if _, err := s.Engine.Apply(ctx, currentUser(r).Username, "Cloudflare settings changed"); err != nil {
		_ = s.Store.SetCloudflare(ctx, cur)
		writeErr(w, 422, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) cfTunnel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.Engine.EnsureTunnel(r.Context(), req.Name); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.Engine.InvalidateCloudflareStatus()
	rep, err := s.Engine.ReconcileCloudflare(r.Context(), false)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": true, "warning": err.Error(), "report": rep})
		return
	}
	go s.Engine.SyncPeers(r.Context())
	writeJSON(w, 200, map[string]any{"ok": true, "report": rep})
}

func (s *Server) cfReconcile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DryRun bool `json:"dry_run"`
	}
	_ = readJSON(r, &req)
	rep, err := s.Engine.ReconcileCloudflare(r.Context(), req.DryRun)
	if err != nil {
		if errors.Is(err, engine.ErrCloudflareOff) {
			writeErr(w, 400, "add a Cloudflare API token first")
			return
		}
		if errors.Is(err, engine.ErrStandby) {
			writeErr(w, 409, err.Error())
			return
		}
		if len(rep.Records) > 0 || len(rep.Changes) > 0 {
			// Partly done: return the report, which carries the error.
			writeJSON(w, 200, rep)
			return
		}
		writeJSON(w, 502, map[string]any{"error": err.Error(), "report": rep})
		return
	}
	writeJSON(w, 200, rep)
}

func (s *Server) cfAOP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Zone string `json:"zone"`
		On   bool   `json:"on"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.Engine.SetAOP(r.Context(), req.Zone, req.On); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// cfAccessApp creates a Cloudflare Access application for the UI hostname
// and stores its AUD so Access sign-in works immediately.
func (s *Server) cfAccessApp(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Team   string   `json:"team"`
		Emails []string `json:"emails"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx := r.Context()
	cf, err := s.Store.Cloudflare(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if cf.APIToken == "" || cf.AccountID == "" || cf.UIHostname == "" {
		writeErr(w, 400, "set the API token, account and UI hostname first")
		return
	}
	if req.Team == "" {
		writeErr(w, 400, "enter your Zero Trust team name")
		return
	}
	var emails []string
	for _, e := range req.Emails {
		if e = strings.TrimSpace(strings.ToLower(e)); e != "" {
			emails = append(emails, e)
		}
	}
	if len(emails) == 0 {
		writeErr(w, 400, "add at least one email that may sign in")
		return
	}
	app, err := cloudflare.New(cf.APIToken).CreateAccessApp(ctx, cf.AccountID, "Halyard ("+s.Cfg.Node.Name+")", cf.UIHostname, emails)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	cf.AccessTeam, cf.AccessAUD, cf.AccessAppID, cf.AccessEmails = req.Team, app.AUD, app.ID, emails
	if err := s.Store.SetCloudflare(ctx, cf); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.Store.AddEvent(ctx, "info", "cloudflare", "Created Access application for "+cf.UIHostname)
	go s.Engine.SyncPeers(ctx)
	writeJSON(w, 200, app)
}

// Revisions ------------------------------------------------------------------

func (s *Server) listRevisions(w http.ResponseWriter, r *http.Request) {
	revs, err := s.Store.Revisions(r.Context(), 200)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if revs == nil {
		revs = []model.Revision{}
	}
	writeJSON(w, 200, revs)
}

func (s *Server) getRevision(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	rev, err := s.Store.Revision(r.Context(), id)
	if err != nil {
		writeErr(w, 404, "revision not found")
		return
	}
	// Previous applied revision for the diff.
	var prev string
	revs, _ := s.Store.Revisions(r.Context(), 500)
	for _, x := range revs {
		if x.ID < id && x.Status == "applied" {
			if p, err := s.Store.Revision(r.Context(), x.ID); err == nil {
				prev = p.HAProxy
			}
			break
		}
	}
	writeJSON(w, 200, map[string]any{"revision": rev, "previous_haproxy": prev})
}

func (s *Server) rollback(w http.ResponseWriter, r *http.Request) {
	if !s.Cfg.IsPrimary() {
		writeErr(w, 409, "this node is a standby; make changes on the primary")
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	res, err := s.Engine.Rollback(r.Context(), id, currentUser(r).Username)
	if err != nil {
		var ve *engine.ValidationError
		if errors.As(err, &ve) {
			writeJSON(w, 422, map[string]string{"error": "HAProxy rejected the old config", "output": ve.Output})
			return
		}
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	out, _, err := s.Engine.Render(r.Context())
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	cur, _ := os.ReadFile(s.Cfg.HAProxy.ConfigPath)
	writeJSON(w, 200, map[string]any{"haproxy": out.HAProxy, "keepalived": out.Keepalived, "current": string(cur)})
}

func (s *Server) apply(w http.ResponseWriter, r *http.Request) {
	if !s.Cfg.IsPrimary() {
		writeErr(w, 409, "this node is a standby; make changes on the primary")
		return
	}
	var req struct {
		Message string `json:"message"`
	}
	_ = readJSON(r, &req)
	if req.Message == "" {
		req.Message = "Manual apply"
	}
	res, err := s.Engine.Apply(r.Context(), currentUser(r).Username, req.Message)
	if err != nil {
		var ve *engine.ValidationError
		if errors.As(err, &ve) {
			writeJSON(w, 422, map[string]string{"error": "HAProxy rejected the config", "output": ve.Output})
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, res)
}

func nowUTC() time.Time { return time.Now().UTC() }
