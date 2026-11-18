package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/joshuadouch/halyard/internal/haproxy"
	"github.com/joshuadouch/halyard/internal/metrics"
	"github.com/joshuadouch/halyard/internal/render"
	"github.com/joshuadouch/halyard/internal/store"
)

const kvMetricsToken = "metrics_token"

// monServer is one pool member on the Monitoring page.
type monServer struct {
	Name        string  `json:"name"`
	Address     string  `json:"address"`
	Backup      bool    `json:"backup"`
	Status      string  `json:"status"`
	State       int     `json:"state"`
	CheckStatus string  `json:"check_status"`
	CheckCode   string  `json:"check_code"`
	CheckMS     int     `json:"check_ms"`
	ChkFail     int64   `json:"check_failures"`
	Downtime    int64   `json:"downtime_sec"`
	LastChange  int     `json:"last_change_sec"`
	Sessions    int     `json:"sessions"`
	MaxSessions int     `json:"max_sessions"`
	Weight      int     `json:"weight"`
	AvgMS       int     `json:"avg_ms"`
	Errors      int64   `json:"errors"`
	Resp5xx     int64   `json:"resp_5xx"`
	RespTotal   int64   `json:"resp_total"`
	Spark       []int64 `json:"-"`
}

type monPool struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Mode     string      `json:"mode"`
	Status   string      `json:"status"`
	Up       int         `json:"up"`
	Total    int         `json:"total"`
	Sessions int         `json:"sessions"`
	Queue    int         `json:"queue"`
	AvgMS    int         `json:"avg_ms"`
	Resp5xx  int64       `json:"resp_5xx"`
	RespAll  int64       `json:"resp_total"`
	Spark    []float64   `json:"spark"`
	Servers  []monServer `json:"servers"`
}

type monCert struct {
	ID       string    `json:"id"`
	Domains  []string  `json:"domains"`
	Source   string    `json:"source"`
	Status   string    `json:"status"`
	NotAfter time.Time `json:"not_after"`
	DaysLeft int       `json:"days_left"`
}

type monVIP struct {
	ID        string `json:"id"`
	Address   string `json:"address"`
	Label     string `json:"label"`
	Interface string `json:"interface"`
	Held      bool   `json:"held"`
}

// monitoring is the live view for the Monitoring page.
func (s *Server) monitoring(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.Metrics == nil {
		writeErr(w, 503, "metrics are not running")
		return
	}
	cur := s.Metrics.Current()
	pools, _ := s.Store.Pools(ctx)
	vips, _ := s.Store.VIPs(ctx)
	certs, _ := s.Store.Certs(ctx)
	events, _ := s.Store.EventsOfKind(ctx, 30, "health", "vip")

	byKey := map[string]haproxy.Stat{}
	for _, st := range cur.Stats {
		byKey[st.Proxy+"/"+st.Server] = st
	}
	statuses, _ := s.poolStatusesFrom(pools, cur.Stats, cur.HAProxyUp)
	mp := make([]monPool, 0, len(pools))
	for i, p := range pools {
		be := render.BackendName(p.Name)
		ps := statuses[i]
		row := monPool{ID: p.ID, Name: p.Name, Mode: string(p.Mode), Status: ps.Status, Up: ps.Up, Total: ps.Total,
			Sessions: ps.Sessions, AvgMS: ps.AvgMS, Resp5xx: ps.Resp5xx, RespAll: ps.RespAll,
			Spark: s.Metrics.Spark(p.ID, "req"), Servers: []monServer{}}
		if b, ok := byKey[be+"/BACKEND"]; ok {
			row.Queue = b.QueueCur
		}
		for _, m := range p.Members {
			sv := monServer{Name: m.Name, Address: m.Address, Backup: m.Backup, Weight: m.Weight, Status: "unknown"}
			if st, ok := byKey[be+"/"+render.ServerName(m.Name)]; ok {
				sv.Status, sv.State = st.Status, metrics.NormState(st.Status)
				sv.CheckStatus, sv.CheckCode, sv.CheckMS = st.CheckStatus, st.CheckCode, st.CheckMS
				sv.ChkFail, sv.Downtime, sv.LastChange = st.ChkFail, st.Downtime, st.LastChange
				sv.Sessions, sv.MaxSessions, sv.Weight, sv.AvgMS = st.CurSessions, st.MaxSess, st.Weight, st.AvgTotalMS
				sv.Errors, sv.Resp5xx, sv.RespTotal = st.EConn+st.EResp, st.Resp5xx, st.RespTotal
			}
			row.Servers = append(row.Servers, sv)
		}
		mp = append(mp, row)
	}

	mc := []monCert{}
	now := time.Now()
	for _, c := range certs {
		if c.CertPEM == "" || c.NotAfter.IsZero() {
			continue
		}
		mc = append(mc, monCert{ID: c.ID, Domains: c.Domains, Source: c.Source, Status: c.Status, NotAfter: c.NotAfter,
			DaysLeft: int(math.Floor(c.NotAfter.Sub(now).Hours() / 24))})
	}
	sort.Slice(mc, func(i, j int) bool { return mc[i].NotAfter.Before(mc[j].NotAfter) })

	mv := []monVIP{}
	for _, v := range vips {
		mv = append(mv, monVIP{ID: v.ID, Address: v.Address, Label: v.Label, Interface: v.Interface, Held: cur.VIPHeld[v.IP()]})
	}

	ha := map[string]any{"up": cur.HAProxyUp}
	if cur.Info != nil {
		num := func(k string) float64 { f, _ := strconv.ParseFloat(cur.Info[k], 64); return f }
		ha["version"] = cur.Info["Version"]
		ha["uptime_sec"] = num("Uptime_sec")
		ha["conns"] = num("CurrConns")
		ha["max_conns"] = num("Maxconn")
		ha["conn_rate"] = num("ConnRate")
		ha["sess_rate"] = num("SessRate")
		ha["ssl_rate"] = num("SslRate")
		ha["idle_pct"] = num("Idle_pct")
		ha["run_queue"] = num("Run_queue")
	}

	writeJSON(w, 200, map[string]any{
		"time": cur.Time, "node": s.Cfg.Node.Name, "role": s.Cfg.Node.Role, "version": s.Version,
		"haproxy": ha, "keepalived": s.Cfg.Keepalived.Enabled, "host": cur.Host, "tunnel": cur.Tunnel,
		"pools": mp, "certs": mc, "vips": mv, "peers": s.Engine.PeerStatuses(), "events": events,
	})
}

// monitoringSeries returns history for charts: the whole node, or one pool.
func (s *Server) monitoringSeries(w http.ResponseWriter, r *http.Request) {
	if s.Metrics == nil {
		writeErr(w, 503, "metrics are not running")
		return
	}
	rng := r.URL.Query().Get("range")
	if _, ok := metrics.Ranges[rng]; !ok {
		rng = "1h"
	}
	pool := r.URL.Query().Get("pool")
	pts, step := s.Metrics.Series(rng)
	type sample struct {
		T int64                         `json:"t"`
		V map[string]float64            `json:"v"`
		S map[string]map[string]float64 `json:"s,omitempty"`
	}
	out := make([]sample, 0, len(pts))
	for _, p := range pts {
		if pool == "" {
			out = append(out, sample{T: p.T, V: p.V})
			continue
		}
		pp, ok := p.Pools[pool]
		if !ok {
			out = append(out, sample{T: p.T, V: map[string]float64{}})
			continue
		}
		out = append(out, sample{T: p.T, V: pp.V, S: pp.Srv})
	}
	writeJSON(w, 200, map[string]any{"range": rng, "step": step, "points": out})
}

func (s *Server) metricsTokenValue(ctx context.Context, rotate bool) (string, error) {
	var tok string
	if !rotate {
		if err := s.Store.GetKV(ctx, kvMetricsToken, &tok); err == nil && tok != "" {
			return tok, nil
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok = "hlm_" + hex.EncodeToString(b)
	return tok, s.Store.SetKV(ctx, kvMetricsToken, tok)
}

func (s *Server) getMetricsToken(w http.ResponseWriter, r *http.Request) {
	tok, err := s.metricsTokenValue(r.Context(), false)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"token": tok, "haproxy_exporter": s.Cfg.HAProxy.StatsListen != ""})
}

func (s *Server) rotateMetricsToken(w http.ResponseWriter, r *http.Request) {
	tok, err := s.metricsTokenValue(r.Context(), true)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.Store.AddEvent(r.Context(), "info", "auth", "Prometheus token rotated by "+currentUser(r).Username)
	writeJSON(w, 200, map[string]any{"token": tok})
}

// prometheus serves /metrics to a signed-in user or a scraper with the bearer token.
func (s *Server) prometheus(w http.ResponseWriter, r *http.Request) {
	ok := false
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		var tok string
		if err := s.Store.GetKV(r.Context(), kvMetricsToken, &tok); err == nil && tok != "" &&
			subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(h, "Bearer ")), []byte(tok)) == 1 {
			ok = true
		}
	}
	if !ok {
		if _, err := s.authenticate(r); err == nil {
			ok = true
		}
	}
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer realm="halyard"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.Metrics == nil {
		http.Error(w, "metrics are not running", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	s.writePrometheus(r.Context(), w)
	if r.URL.Query().Get("haproxy") == "1" && s.Cfg.HAProxy.StatsListen != "" {
		s.appendHAProxyExporter(r.Context(), w)
	}
}

func (s *Server) appendHAProxyExporter(ctx context.Context, w io.Writer) {
	addr := s.Cfg.HAProxy.StatsListen
	if strings.HasPrefix(addr, ":") || strings.HasPrefix(addr, "*:") || strings.HasPrefix(addr, "0.0.0.0:") {
		addr = "127.0.0.1:" + addr[strings.LastIndex(addr, ":")+1:]
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "http://"+addr+"/metrics", nil)
	if err != nil {
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(w, "# haproxy exporter unavailable: %s\n", strings.ReplaceAll(err.Error(), "\n", " "))
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 64<<20))
}

// promWriter collects samples by metric family and writes the Prometheus
// text format, with each family's samples grouped together.
type promWriter struct {
	order []string
	fams  map[string]*promFamily
	cur   *promFamily
}

type promFamily struct {
	help, typ string
	lines     []string
}

func newPromWriter() *promWriter { return &promWriter{fams: map[string]*promFamily{}} }

func (p *promWriter) head(name, typ, help string) {
	f, ok := p.fams[name]
	if !ok {
		f = &promFamily{help: help, typ: typ}
		p.fams[name] = f
		p.order = append(p.order, name)
	}
	p.cur = f
}

func (p *promWriter) flush(w io.Writer) {
	for _, name := range p.order {
		f := p.fams[name]
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, f.help, name, f.typ)
		for _, l := range f.lines {
			_, _ = io.WriteString(w, l)
		}
	}
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// v writes one value; labels are name, value pairs.
func (p *promWriter) v(name string, val float64, labels ...string) {
	var b strings.Builder
	b.WriteString(name)
	if len(labels) > 0 {
		b.WriteByte('{')
		for i := 0; i+1 < len(labels); i += 2 {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(labels[i])
			b.WriteString(`="`)
			b.WriteString(labelEscaper.Replace(labels[i+1]))
			b.WriteByte('"')
		}
		b.WriteByte('}')
	}
	b.WriteByte(' ')
	if val == math.Trunc(val) && math.Abs(val) < 1e15 {
		b.WriteString(strconv.FormatInt(int64(val), 10))
	} else {
		b.WriteString(strconv.FormatFloat(val, 'g', 6, 64))
	}
	b.WriteByte('\n')
	p.cur.lines = append(p.cur.lines, b.String())
}

func (p *promWriter) gauge(name, help string, val float64, labels ...string) {
	p.head(name, "gauge", help)
	p.v(name, val, labels...)
}

func (p *promWriter) counter(name, help string, val float64, labels ...string) {
	p.head(name, "counter", help)
	p.v(name, val, labels...)
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func (s *Server) writePrometheus(ctx context.Context, w io.Writer) {
	p := newPromWriter()
	defer p.flush(w)
	cur := s.Metrics.Current()
	p.gauge("halyard_build_info", "Halyard version and node.", 1, "version", s.Version, "node", s.Cfg.Node.Name, "role", s.Cfg.Node.Role)
	p.gauge("halyard_haproxy_up", "1 if HAProxy answers on its runtime socket.", b2f(cur.HAProxyUp))
	if cur.Info != nil {
		num := func(k string) float64 { f, _ := strconv.ParseFloat(cur.Info[k], 64); return f }
		p.gauge("halyard_haproxy_uptime_seconds", "Seconds since the HAProxy process started.", num("Uptime_sec"))
		p.gauge("halyard_haproxy_current_connections", "Current client connections.", num("CurrConns"))
		p.gauge("halyard_haproxy_max_connections", "Configured maximum connections.", num("Maxconn"))
		p.gauge("halyard_haproxy_connection_rate", "New connections per second.", num("ConnRate"))
		p.gauge("halyard_haproxy_ssl_rate", "New TLS sessions per second.", num("SslRate"))
		p.gauge("halyard_haproxy_idle_ratio", "Share of time HAProxy was idle (0-1).", num("Idle_pct")/100)
	}

	// Frontends.
	for _, st := range cur.Stats {
		if st.Server != "FRONTEND" || !strings.HasPrefix(st.Proxy, "fe_") {
			continue
		}
		fe := st.Proxy
		p.gauge("halyard_frontend_sessions_current", "Open sessions on a listener.", float64(st.CurSessions), "frontend", fe)
	}
	for _, st := range cur.Stats {
		if st.Server != "FRONTEND" || !strings.HasPrefix(st.Proxy, "fe_") {
			continue
		}
		fe := st.Proxy
		p.counter("halyard_frontend_sessions_total", "Sessions accepted by a listener.", float64(st.TotSessions), "frontend", fe)
		p.counter("halyard_frontend_requests_total", "HTTP requests received by a listener.", float64(st.ReqTotal), "frontend", fe)
		p.counter("halyard_frontend_request_errors_total", "Invalid or denied requests on a listener.", float64(st.EReq+st.DReq), "frontend", fe)
		p.counter("halyard_frontend_bytes_in_total", "Bytes received from clients.", float64(st.BytesIn), "frontend", fe)
		p.counter("halyard_frontend_bytes_out_total", "Bytes sent to clients.", float64(st.BytesOut), "frontend", fe)
		for code, n := range map[string]int64{"2xx": st.Resp2xx, "3xx": st.Resp3xx, "4xx": st.Resp4xx, "5xx": st.Resp5xx} {
			p.counter("halyard_frontend_responses_total", "HTTP responses sent to clients by status class.", float64(n), "frontend", fe, "code", code)
		}
	}

	// Pools and members.
	byKey := map[string]haproxy.Stat{}
	for _, st := range cur.Stats {
		byKey[st.Proxy+"/"+st.Server] = st
	}
	pools, _ := s.Store.Pools(ctx)
	statuses, _ := s.poolStatusesFrom(pools, cur.Stats, cur.HAProxyUp)
	for i, pl := range pools {
		ps := statuses[i]
		be := render.BackendName(pl.Name)
		p.gauge("halyard_pool_members", "Members in a pool.", float64(len(pl.Members)), "pool", pl.Name)
		p.gauge("halyard_pool_members_up", "Members passing health checks.", float64(ps.Up), "pool", pl.Name)
		p.gauge("halyard_pool_healthy", "1 if every active member is up.", b2f(ps.Status == "healthy"), "pool", pl.Name)
		b, ok := byKey[be+"/BACKEND"]
		if !ok {
			continue
		}
		p.gauge("halyard_pool_sessions_current", "Open sessions to a pool.", float64(b.CurSessions), "pool", pl.Name)
		p.gauge("halyard_pool_queue_current", "Requests waiting for a free member.", float64(b.QueueCur), "pool", pl.Name)
		p.gauge("halyard_pool_response_time_seconds", "Average total time of the last 1024 requests.", float64(b.AvgTotalMS)/1000, "pool", pl.Name)
		p.counter("halyard_pool_sessions_total", "Sessions sent to a pool.", float64(b.TotSessions), "pool", pl.Name)
		p.counter("halyard_pool_requests_total", "HTTP requests sent to a pool.", float64(b.ReqTotal), "pool", pl.Name)
		p.counter("halyard_pool_errors_total", "Connection and response errors talking to members.", float64(b.EConn), "pool", pl.Name, "type", "connect")
		p.counter("halyard_pool_errors_total", "Connection and response errors talking to members.", float64(b.EResp), "pool", pl.Name, "type", "response")
		p.counter("halyard_pool_bytes_in_total", "Bytes sent to members.", float64(b.BytesIn), "pool", pl.Name)
		p.counter("halyard_pool_bytes_out_total", "Bytes received from members.", float64(b.BytesOut), "pool", pl.Name)
		for code, n := range map[string]int64{"2xx": b.Resp2xx, "3xx": b.Resp3xx, "4xx": b.Resp4xx, "5xx": b.Resp5xx} {
			p.counter("halyard_pool_responses_total", "HTTP responses from a pool by status class.", float64(n), "pool", pl.Name, "code", code)
		}
		for _, m := range pl.Members {
			st, ok := byKey[be+"/"+render.ServerName(m.Name)]
			if !ok {
				continue
			}
			l := []string{"pool", pl.Name, "server", m.Name, "address", m.Address}
			state := metrics.NormState(st.Status)
			p.gauge("halyard_server_up", "1 if a member is up.", b2f(state == metrics.StateUp), l...)
			p.gauge("halyard_server_state", "Member state: 1 up, 2 draining, 3 maintenance, 4 down, 0 unknown.", float64(state), l...)
			p.gauge("halyard_server_weight", "Member weight.", float64(st.Weight), l...)
			p.gauge("halyard_server_sessions_current", "Open sessions to a member.", float64(st.CurSessions), l...)
			p.gauge("halyard_server_response_time_seconds", "Average total time of the member's last 1024 requests.", float64(st.AvgTotalMS)/1000, l...)
			p.gauge("halyard_server_check_duration_seconds", "Duration of the last health check.", float64(st.CheckMS)/1000, l...)
			p.counter("halyard_server_sessions_total", "Sessions sent to a member.", float64(st.TotSessions), l...)
			p.counter("halyard_server_check_failures_total", "Failed health checks.", float64(st.ChkFail), l...)
			p.counter("halyard_server_downtime_seconds_total", "Seconds the member has been down.", float64(st.Downtime), l...)
			p.counter("halyard_server_errors_total", "Connection and response errors.", float64(st.EConn+st.EResp), l...)
			p.counter("halyard_server_responses_5xx_total", "5xx responses from a member.", float64(st.Resp5xx), l...)
		}
	}

	// VIPs, certificates, peers, tunnel.
	vips, _ := s.Store.VIPs(ctx)
	for _, v := range vips {
		p.gauge("halyard_vip_held", "1 if this node holds the VIP.", b2f(cur.VIPHeld[v.IP()]), "vip", v.IP(), "label", v.Label, "interface", v.Interface)
	}
	certs, _ := s.Store.Certs(ctx)
	for _, c := range certs {
		if c.CertPEM == "" || c.NotAfter.IsZero() {
			continue
		}
		p.gauge("halyard_certificate_expiry_timestamp_seconds", "When a certificate expires (Unix time).",
			float64(c.NotAfter.Unix()), "cert", c.ID, "domain", first(c.Domains), "source", c.Source)
	}
	for _, ps := range s.Engine.PeerStatuses() {
		p.gauge("halyard_peer_sync_ok", "1 if the last config push to the peer worked.", b2f(ps.OK), "peer", ps.Name)
		if !ps.Time.IsZero() {
			p.gauge("halyard_peer_last_sync_timestamp_seconds", "Time of the last config push attempt.", float64(ps.Time.Unix()), "peer", ps.Name)
		}
	}
	if t := cur.Tunnel; t.Configured {
		p.gauge("halyard_tunnel_healthy", "1 if Cloudflare reports the tunnel healthy.", b2f(t.Status == "healthy"), "tunnel", t.Name)
		p.gauge("halyard_tunnel_connections", "Active connections from cloudflared to Cloudflare.", float64(t.Connections), "tunnel", t.Name)
	}

	// Host.
	h := cur.Host
	p.gauge("halyard_host_cpu_usage_ratio", "CPU busy share over the last sample (0-1).", h.CPUPct/100)
	p.gauge("halyard_host_cpus", "CPU cores visible to the node.", float64(h.Cores))
	p.gauge("halyard_host_memory_bytes", "Memory in bytes.", float64(h.MemTotal), "type", "total")
	p.gauge("halyard_host_memory_bytes", "Memory in bytes.", float64(h.MemUsed), "type", "used")
	p.gauge("halyard_host_load", "Load average.", h.Load[0], "period", "1m")
	p.gauge("halyard_host_load", "Load average.", h.Load[1], "period", "5m")
	p.gauge("halyard_host_load", "Load average.", h.Load[2], "period", "15m")
	p.gauge("halyard_host_disk_bytes", "Space on the data directory's filesystem.", float64(h.DiskTotal), "type", "total")
	p.gauge("halyard_host_disk_bytes", "Space on the data directory's filesystem.", float64(h.DiskUsed), "type", "used")
	p.gauge("halyard_host_uptime_seconds", "Seconds since the node booted.", h.UptimeSec)
	for _, n := range h.Net {
		p.counter("halyard_host_network_bytes_total", "Bytes on a network interface.", float64(n.RxBytes), "interface", n.Name, "direction", "rx")
		p.counter("halyard_host_network_bytes_total", "Bytes on a network interface.", float64(n.TxBytes), "interface", n.Name, "direction", "tx")
	}
}
