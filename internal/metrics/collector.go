// Package metrics samples HAProxy, the host and cluster state every few
// seconds, keeps a history at three resolutions (10 s for an hour, 1 min for
// two days, 10 min for two weeks) and records health changes as events.
package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/joshuadouch/halyard/internal/haproxy"
	"github.com/joshuadouch/halyard/internal/render"
	"github.com/joshuadouch/halyard/internal/store"
)

// Resolutions and how much of each is kept.
const (
	rawKeep  = 360  // 10 s samples: one hour
	minKeep  = 2880 // 1 min samples: two days
	tenKeep  = 2016 // 10 min samples: two weeks
	tierMin  = 60
	tierTen  = 600
	minTTL   = 48 * time.Hour
	tenTTL   = 14 * 24 * time.Hour
	stateKey = "st" // server state: merged with max (worst) instead of mean
)

// Server states as stored in samples; higher is worse.
const (
	StateUnknown = 0
	StateUp      = 1
	StateDrain   = 2
	StateMaint   = 3
	StateDown    = 4
)

// Point is one sample. Missing values are left out of the maps.
type Point struct {
	T     int64                `json:"t"`
	V     map[string]float64   `json:"v"`
	Pools map[string]PoolPoint `json:"p,omitempty"`
}

// PoolPoint is one pool's part of a sample; Srv is keyed by member name.
type PoolPoint struct {
	V   map[string]float64            `json:"v"`
	Srv map[string]map[string]float64 `json:"s,omitempty"`
}

// Tunnel is the latest known Cloudflare Tunnel state.
type Tunnel struct {
	Configured  bool      `json:"configured"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Connections int       `json:"connections"`
	Colos       []string  `json:"colos"`
	Error       string    `json:"error"`
	Checked     time.Time `json:"checked"`
}

// Host is the node's resource use at the last sample.
type Host struct {
	CPUPct    float64    `json:"cpu_pct"`
	Cores     int        `json:"cores"`
	MemTotal  uint64     `json:"mem_total"`
	MemUsed   uint64     `json:"mem_used"`
	Load      [3]float64 `json:"load"`
	DiskTotal uint64     `json:"disk_total"`
	DiskUsed  uint64     `json:"disk_used"`
	UptimeSec float64    `json:"uptime_sec"`
	Net       []Iface    `json:"net"`
	raw       HostRaw
}

// Iface is one network interface's traffic.
type Iface struct {
	Name    string  `json:"name"`
	RxBytes uint64  `json:"rx_bytes"`
	TxBytes uint64  `json:"tx_bytes"`
	RxRate  float64 `json:"rx_rate"`
	TxRate  float64 `json:"tx_rate"`
}

// Current is the latest raw state, for live views and Prometheus.
type Current struct {
	Time      time.Time         `json:"time"`
	HAProxyUp bool              `json:"haproxy_up"`
	Info      map[string]string `json:"-"`
	Stats     []haproxy.Stat    `json:"-"`
	Host      Host              `json:"host"`
	Tunnel    Tunnel            `json:"tunnel"`
	VIPHeld   map[string]bool   `json:"-"`
}

// Collector samples periodically. Create it with New and run Run.
type Collector struct {
	Store   *store.Store
	Runtime haproxy.Runtime
	DataDir string
	Node    string
	Log     *slog.Logger
	// Interval between samples; 10 s by default.
	Interval time.Duration
	// TunnelFn, if set, is polled every two minutes for the tunnel state.
	TunnelFn func(ctx context.Context) Tunnel
	// LocalAddrs lists this node's addresses; defaults to the interfaces'.
	LocalAddrs func() map[string]bool

	mu        sync.RWMutex
	raw       []Point
	min       []Point
	ten       []Point
	pendMin   []Point
	pendTen   []Point
	cur       Current
	prevT     time.Time
	prevStats map[string]haproxy.Stat
	states    map[string]int
	vipHeld   map[string]bool
	tunnel    Tunnel
}

// Load restores stored history. Call before Run.
func (c *Collector) Load(ctx context.Context) {
	now := time.Now()
	load := func(tier int, since time.Time) []Point {
		rows, err := c.Store.Metrics(ctx, tier, since.Unix())
		if err != nil {
			c.logWarn("load metrics", "err", err)
			return nil
		}
		out := make([]Point, 0, len(rows))
		for _, r := range rows {
			var p Point
			if json.Unmarshal([]byte(r.Data), &p) == nil {
				out = append(out, p)
			}
		}
		return out
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.min = load(tierMin, now.Add(-minTTL))
	c.ten = load(tierTen, now.Add(-tenTTL))
	// Minutes already saved for the current ten-minute bucket.
	bucket := now.Unix() / tierTen
	for _, p := range c.min {
		if p.T/tierTen == bucket {
			c.pendTen = append(c.pendTen, p)
		}
	}
}

// Run samples until ctx ends.
func (c *Collector) Run(ctx context.Context) {
	iv := c.Interval
	if iv <= 0 {
		iv = 10 * time.Second
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	tun := time.NewTicker(2 * time.Minute)
	defer tun.Stop()
	c.Sample(ctx)
	if c.TunnelFn != nil {
		go c.pollTunnel(ctx)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.Sample(ctx)
		case <-tun.C:
			if c.TunnelFn != nil {
				go c.pollTunnel(ctx)
			}
		}
	}
}

func (c *Collector) logWarn(msg string, args ...any) {
	if c.Log != nil {
		c.Log.Warn(msg, args...)
	}
}

func (c *Collector) pollTunnel(ctx context.Context) {
	t := c.TunnelFn(ctx)
	t.Checked = time.Now().UTC()
	c.mu.Lock()
	old := c.tunnel
	c.tunnel = t
	c.mu.Unlock()
	if !t.Configured || !old.Configured || old.Status == t.Status || t.Status == "" {
		return
	}
	level := "info"
	if t.Status != "healthy" {
		level = "warn"
	}
	c.Store.AddEvent(ctx, level, "health", fmt.Sprintf("Cloudflare Tunnel %s is %s (%d connections)", t.Name, t.Status, t.Connections))
}

// NormState maps an HAProxy status string to a state code.
func NormState(status string) int {
	s := strings.ToUpper(status)
	switch {
	case strings.HasPrefix(s, "UP"), s == "NO CHECK", s == "OPEN":
		return StateUp
	case strings.HasPrefix(s, "NOLB"), strings.HasPrefix(s, "DRAIN"):
		return StateDrain
	case strings.HasPrefix(s, "MAINT"):
		return StateMaint
	case strings.HasPrefix(s, "DOWN"):
		return StateDown
	}
	return StateUnknown
}

// StateName is the label used in events and the UI.
func StateName(st int) string {
	return [...]string{"unknown", "up", "draining", "in maintenance", "down"}[st]
}

func round(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	if math.Abs(v) >= 100 {
		return math.Round(v)
	}
	return math.Round(v*100) / 100
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

type event struct{ level, kind, msg string }

// Sample takes one sample now.
func (c *Collector) Sample(ctx context.Context) {
	now := time.Now()
	stats, err := c.Runtime.ShowStat()
	haUp := err == nil
	var info map[string]string
	if haUp {
		info, _ = c.Runtime.ShowInfo()
	}
	pools, _ := c.Store.Pools(ctx)
	vips, _ := c.Store.VIPs(ctx)
	hostRaw := readHost(c.DataDir)
	addrs := localAddrs
	if c.LocalAddrs != nil {
		addrs = c.LocalAddrs
	}
	local := addrs()

	var events []event
	var save []struct {
		tier int
		p    Point
		ttl  time.Duration
	}

	c.mu.Lock()
	iv := c.Interval
	if iv <= 0 {
		iv = 10 * time.Second
	}
	dt := now.Sub(c.prevT).Seconds()
	fresh := !c.prevT.IsZero() && dt > 0 && dt < 5*iv.Seconds()
	prev := c.prevStats
	rate := func(cur, old int64) float64 {
		if cur < old {
			return 0 // counters reset by a reload
		}
		return float64(cur-old) / dt
	}
	curStats := make(map[string]haproxy.Stat, len(stats))
	for _, s := range stats {
		curStats[s.Proxy+"/"+s.Server] = s
	}

	p := Point{T: now.Unix(), V: map[string]float64{}}
	if haUp {
		p.V["ha"] = 1
	} else {
		p.V["ha"] = 0
	}
	// Frontends: client-facing traffic.
	var sess, queue float64
	var req, conn, bin, bout, r2, r3, r4, r5, ereq float64
	var errs, msSum, msW float64
	for _, s := range stats {
		key := s.Proxy + "/" + s.Server
		o, had := prev[key]
		switch {
		case s.Server == "FRONTEND" && strings.HasPrefix(s.Proxy, "fe_"):
			sess += float64(s.CurSessions)
			if !fresh || !had {
				continue
			}
			if s.ReqTotal > 0 {
				req += rate(s.ReqTotal, o.ReqTotal)
			} else {
				req += rate(s.TotSessions, o.TotSessions)
			}
			conn += rate(s.TotSessions, o.TotSessions)
			bin += rate(s.BytesIn, o.BytesIn)
			bout += rate(s.BytesOut, o.BytesOut)
			r2 += rate(s.Resp2xx, o.Resp2xx)
			r3 += rate(s.Resp3xx, o.Resp3xx)
			r4 += rate(s.Resp4xx, o.Resp4xx)
			r5 += rate(s.Resp5xx, o.Resp5xx)
			ereq += rate(s.EReq, o.EReq) + rate(s.DReq, o.DReq)
		case s.Server == "BACKEND" && strings.HasPrefix(s.Proxy, "be_"):
			queue += float64(s.QueueCur)
			if !fresh || !had {
				continue
			}
			errs += rate(s.EConn, o.EConn) + rate(s.EResp, o.EResp)
			w := rate(s.TotSessions, o.TotSessions)
			if s.ReqTotal > 0 {
				w = rate(s.ReqTotal, o.ReqTotal)
			}
			if w > 0 {
				msSum += w * float64(s.AvgTotalMS)
				msW += w
			}
		}
	}
	if haUp {
		p.V["sess"] = sess
		p.V["q"] = queue
		if fresh {
			p.V["req"], p.V["conn"], p.V["in"], p.V["out"] = round(req), round(conn), round(bin), round(bout)
			p.V["r2"], p.V["r3"], p.V["r4"], p.V["r5"] = round(r2), round(r3), round(r4), round(r5)
			p.V["ereq"], p.V["err"] = round(ereq), round(errs)
			if msW > 0 {
				p.V["ms"] = round(msSum / msW)
			}
		}
	}

	// Host.
	host := Host{
		Cores: hostRaw.Cores, MemTotal: hostRaw.MemTotal, MemUsed: hostRaw.MemUsed, Load: hostRaw.Load,
		DiskTotal: hostRaw.DiskTotal, DiskUsed: hostRaw.DiskUsed, UptimeSec: hostRaw.UptimeSec, raw: hostRaw,
	}
	prevHost := c.cur.Host.raw
	if fresh && hostRaw.CPUTotal > prevHost.CPUTotal && hostRaw.CPUBusy >= prevHost.CPUBusy {
		host.CPUPct = round(float64(hostRaw.CPUBusy-prevHost.CPUBusy) / float64(hostRaw.CPUTotal-prevHost.CPUTotal) * 100)
		p.V["cpu"] = host.CPUPct
	}
	if hostRaw.MemTotal > 0 {
		p.V["mem"] = round(float64(hostRaw.MemUsed) / float64(hostRaw.MemTotal) * 100)
	}
	if hostRaw.DiskTotal > 0 {
		p.V["disk"] = round(float64(hostRaw.DiskUsed) / float64(hostRaw.DiskTotal) * 100)
	}
	p.V["load"] = hostRaw.Load[0]
	var rx, tx float64
	names := make([]string, 0, len(hostRaw.Net))
	for n := range hostRaw.Net {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		nr := hostRaw.Net[n]
		ifc := Iface{Name: n, RxBytes: nr.Rx, TxBytes: nr.Tx}
		if o, ok := prevHost.Net[n]; ok && fresh {
			ifc.RxRate = round(rate(int64(nr.Rx), int64(o.Rx)))
			ifc.TxRate = round(rate(int64(nr.Tx), int64(o.Tx)))
			rx += ifc.RxRate
			tx += ifc.TxRate
		}
		host.Net = append(host.Net, ifc)
	}
	if fresh {
		p.V["rx"], p.V["tx"] = round(rx), round(tx)
	}

	// Pools and their members.
	if haUp {
		p.Pools = map[string]PoolPoint{}
	}
	newStates := map[string]int{}
	for _, pl := range pools {
		if !haUp {
			break
		}
		be := render.BackendName(pl.Name)
		pp := PoolPoint{V: map[string]float64{}, Srv: map[string]map[string]float64{}}
		if s, ok := curStats[be+"/BACKEND"]; ok {
			pp.V["sess"] = float64(s.CurSessions)
			pp.V["q"] = float64(s.QueueCur)
			if o, had := prev[be+"/BACKEND"]; had && fresh {
				r := rate(s.TotSessions, o.TotSessions)
				if s.ReqTotal > 0 {
					r = rate(s.ReqTotal, o.ReqTotal)
				}
				pp.V["req"] = round(r)
				pp.V["r5"] = round(rate(s.Resp5xx, o.Resp5xx))
				pp.V["r4"] = round(rate(s.Resp4xx, o.Resp4xx))
				pp.V["err"] = round(rate(s.EConn, o.EConn) + rate(s.EResp, o.EResp))
				pp.V["in"] = round(rate(s.BytesIn, o.BytesIn))
				pp.V["out"] = round(rate(s.BytesOut, o.BytesOut))
				if r > 0 {
					pp.V["ms"] = float64(s.AvgTotalMS)
				}
			}
		}
		var up, total float64
		for _, m := range pl.Members {
			key := be + "/" + render.ServerName(m.Name)
			s, ok := curStats[key]
			if !ok {
				continue
			}
			total++
			st := NormState(s.Status)
			if st == StateUp {
				up++
			}
			sv := map[string]float64{stateKey: float64(st), "sess": float64(s.CurSessions)}
			if s.CheckStatus != "" && st != StateMaint {
				sv["chk"] = float64(s.CheckMS)
			}
			if o, had := prev[key]; had && fresh {
				r := rate(s.TotSessions, o.TotSessions)
				sv["rate"] = round(r)
				sv["err"] = round(rate(s.EConn, o.EConn) + rate(s.EResp, o.EResp))
				if r > 0 {
					sv["ms"] = float64(s.AvgTotalMS)
				}
			}
			pp.Srv[m.Name] = sv
			newStates[key] = st
			if old, seen := c.states[key]; seen && old != st && st != StateUnknown {
				level := "info"
				msg := fmt.Sprintf("%s in pool %s is %s", m.Name, pl.Name, StateName(st))
				switch {
				case st == StateDown:
					level = "warn"
					if s.CheckStatus != "" {
						msg += fmt.Sprintf(" (check %s%s)", s.CheckStatus, codeSuffix(s.CheckCode))
					}
				case st == StateUp && old == StateDown:
					msg += " again"
				}
				events = append(events, event{level, "health", msg})
			}
		}
		pp.V["up"], pp.V["total"] = up, total
		p.Pools[pl.ID] = pp
	}
	if haUp {
		c.states = newStates
	}
	if !haUp && c.cur.HAProxyUp {
		events = append(events, event{"error", "health", "HAProxy stopped answering on its runtime socket on " + c.Node})
	} else if haUp && !c.cur.HAProxyUp && !c.prevT.IsZero() {
		events = append(events, event{"info", "health", "HAProxy is answering again on " + c.Node})
	}

	// VIP ownership.
	held := map[string]bool{}
	for _, v := range vips {
		ip := v.IP()
		held[ip] = local[ip]
		if old, ok := c.vipHeld[ip]; ok && old != held[ip] {
			name := v.Address
			if v.Label != "" {
				name += " (" + v.Label + ")"
			}
			if held[ip] {
				events = append(events, event{"warn", "vip", "VIP " + name + " moved to " + c.Node})
			} else {
				events = append(events, event{"warn", "vip", "VIP " + name + " left " + c.Node})
			}
		}
	}
	c.vipHeld = held
	nHeld := 0.0
	for _, h := range held {
		if h {
			nHeld++
		}
	}
	p.V["vips"] = nHeld
	if c.tunnel.Configured {
		p.V["tun"] = float64(c.tunnel.Connections)
	}

	c.cur = Current{Time: now.UTC(), HAProxyUp: haUp, Info: info, Stats: stats, Host: host, Tunnel: c.tunnel, VIPHeld: held}
	if haUp {
		c.prevStats = curStats
	}
	c.prevT = now

	// History.
	c.raw = appendKeep(c.raw, p, rawKeep)
	if len(c.pendMin) > 0 && c.pendMin[0].T/tierMin != p.T/tierMin {
		m := Merge(c.pendMin)
		m.T = c.pendMin[0].T / tierMin * tierMin
		c.min = appendKeep(c.min, m, minKeep)
		save = append(save, struct {
			tier int
			p    Point
			ttl  time.Duration
		}{tierMin, m, minTTL})
		c.pendMin = nil
		if len(c.pendTen) > 0 && c.pendTen[0].T/tierTen != m.T/tierTen {
			t := Merge(c.pendTen)
			t.T = c.pendTen[0].T / tierTen * tierTen
			c.ten = appendKeep(c.ten, t, tenKeep)
			save = append(save, struct {
				tier int
				p    Point
				ttl  time.Duration
			}{tierTen, t, tenTTL})
			c.pendTen = nil
		}
		c.pendTen = append(c.pendTen, m)
	}
	c.pendMin = append(c.pendMin, p)
	c.mu.Unlock()

	for _, s := range save {
		b, _ := json.Marshal(s.p)
		if err := c.Store.SaveMetric(ctx, s.tier, s.p.T, b, now.Add(-s.ttl).Unix()); err != nil {
			c.logWarn("save metrics", "err", err)
		}
	}
	for _, e := range events {
		c.Store.AddEvent(ctx, e.level, e.kind, e.msg)
	}
}

func codeSuffix(code string) string {
	if code == "" {
		return ""
	}
	return " " + code
}

func appendKeep(s []Point, p Point, keep int) []Point {
	s = append(s, p)
	if len(s) > keep {
		s = append(s[:0:0], s[len(s)-keep:]...)
	}
	return s
}

// mergeMaps averages each key over the maps that have it; stateKey takes the max.
func mergeMaps(ms []map[string]float64) map[string]float64 {
	sum := map[string]float64{}
	n := map[string]int{}
	for _, m := range ms {
		for k, v := range m {
			if k == stateKey {
				if v > sum[k] {
					sum[k] = v
				}
				n[k] = 1
				continue
			}
			sum[k] += v
			n[k]++
		}
	}
	out := make(map[string]float64, len(sum))
	for k, v := range sum {
		out[k] = round(v / float64(n[k]))
	}
	return out
}

// Merge averages points into one, with the first point's time.
func Merge(ps []Point) Point {
	if len(ps) == 0 {
		return Point{}
	}
	out := Point{T: ps[0].T}
	vs := make([]map[string]float64, 0, len(ps))
	poolV := map[string][]map[string]float64{}
	srvV := map[string]map[string][]map[string]float64{}
	for _, p := range ps {
		vs = append(vs, p.V)
		for id, pp := range p.Pools {
			poolV[id] = append(poolV[id], pp.V)
			if srvV[id] == nil {
				srvV[id] = map[string][]map[string]float64{}
			}
			for name, sv := range pp.Srv {
				srvV[id][name] = append(srvV[id][name], sv)
			}
		}
	}
	out.V = mergeMaps(vs)
	if len(poolV) > 0 {
		out.Pools = map[string]PoolPoint{}
		for id, v := range poolV {
			pp := PoolPoint{V: mergeMaps(v)}
			if len(srvV[id]) > 0 {
				pp.Srv = map[string]map[string]float64{}
				for name, sv := range srvV[id] {
					pp.Srv[name] = mergeMaps(sv)
				}
			}
			out.Pools[id] = pp
		}
	}
	return out
}

// Downsample merges every n consecutive points.
func Downsample(ps []Point, n int) []Point {
	if n <= 1 {
		return ps
	}
	out := make([]Point, 0, len(ps)/n+1)
	for i := 0; i < len(ps); i += n {
		j := i + n
		if j > len(ps) {
			j = len(ps)
		}
		out = append(out, Merge(ps[i:j]))
	}
	return out
}

// Ranges the UI offers, with their step in seconds.
var Ranges = map[string]struct {
	Dur  time.Duration
	Step int
}{
	"1h":  {time.Hour, 10},
	"6h":  {6 * time.Hour, 60},
	"24h": {24 * time.Hour, 240},
	"7d":  {7 * 24 * time.Hour, 1800},
	"14d": {14 * 24 * time.Hour, 3600},
}

// Series returns points covering the range at its step.
func (c *Collector) Series(rng string) ([]Point, int) {
	r, ok := Ranges[rng]
	if !ok {
		r = Ranges["1h"]
	}
	since := time.Now().Add(-r.Dur).Unix()
	c.mu.RLock()
	defer c.mu.RUnlock()
	after := func(ps []Point, from, to int64) []Point {
		i := sort.Search(len(ps), func(i int) bool { return ps[i].T >= from })
		j := sort.Search(len(ps), func(i int) bool { return ps[i].T >= to })
		return ps[i:j]
	}
	var out []Point
	switch r.Step {
	case 10:
		// After a restart the raw ring is short: fill the start with minutes.
		first := time.Now().Unix()
		if len(c.raw) > 0 {
			first = c.raw[0].T
		}
		out = append(out, after(c.min, since, first)...)
		out = append(out, after(c.raw, since, math.MaxInt64)...)
	case 60, 240:
		out = Downsample(after(c.min, since, math.MaxInt64), r.Step/60)
	default:
		out = Downsample(after(c.ten, since, math.MaxInt64), r.Step/600)
	}
	return out, r.Step
}

// Spark returns one pool value from the last hour, 1 min per point.
func (c *Collector) Spark(poolID, key string) []float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]float64, 0, 60)
	for i := 0; i < len(c.raw); i += 6 {
		j := i + 6
		if j > len(c.raw) {
			j = len(c.raw)
		}
		var sum float64
		var n int
		for _, p := range c.raw[i:j] {
			if v, ok := p.Pools[poolID].V[key]; ok {
				sum += v
				n++
			}
		}
		if n > 0 {
			out = append(out, round(sum/float64(n)))
		} else {
			out = append(out, 0)
		}
	}
	return out
}

// Current returns the latest raw state.
func (c *Collector) Current() Current {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cur
}
