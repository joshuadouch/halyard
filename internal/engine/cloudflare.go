package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/joshuadouch/halyard/internal/cloudflare"
	"github.com/joshuadouch/halyard/internal/render"
)

// ErrCloudflareOff means no Cloudflare token is configured.
var ErrCloudflareOff = errors.New("cloudflare is not configured")

// ErrStandby means a standby was asked to change Cloudflare.
var ErrStandby = errors.New("this node is a standby; Cloudflare is synced from the primary")

// CloudflareReport is the outcome of the latest reconcile.
type CloudflareReport struct {
	Time    time.Time                `json:"time"`
	DryRun  bool                     `json:"dry_run"`
	Changes []cloudflare.Change      `json:"changes"`
	Records []cloudflare.RecordState `json:"records"`
	Ingress []cloudflare.IngressRule `json:"ingress"`
	Notes   []string                 `json:"notes"`
	Error   string                   `json:"error"`
}

// LastCloudflareReport returns the latest reconcile report.
// It does not wait for a reconcile in progress.
func (e *Engine) LastCloudflareReport() CloudflareReport {
	e.cfStateMu.Lock()
	defer e.cfStateMu.Unlock()
	return e.cfReport
}

func (e *Engine) setReport(r CloudflareReport) {
	e.cfStateMu.Lock()
	e.cfReport = r
	e.cfStateMu.Unlock()
}

// CloudflareStatus is the live account view for the Cloudflare page.
type CloudflareStatus struct {
	Zones  []cloudflare.Zone
	AOP    map[string]bool
	Tunnel *cloudflare.Tunnel
	Error  string
	at     time.Time
	token  string
}

// CloudflareStatus returns zones, per-zone AOP state and tunnel health,
// cached for a minute so pages load quickly. force skips the cache.
func (e *Engine) CloudflareStatus(ctx context.Context, force bool) CloudflareStatus {
	s, err := e.Store.Cloudflare(ctx)
	if err != nil || s.APIToken == "" {
		return CloudflareStatus{AOP: map[string]bool{}}
	}
	e.cfStateMu.Lock()
	c := e.cfStatus
	e.cfStateMu.Unlock()
	if !force && c.token == s.APIToken && time.Since(c.at) < time.Minute {
		return c
	}
	st := CloudflareStatus{AOP: map[string]bool{}, at: time.Now(), token: s.APIToken}
	cl := cloudflare.New(s.APIToken)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	zones, err := cl.Zones(ctx)
	if err != nil {
		st.Error = err.Error()
	}
	st.Zones = zones
	// Ask for every zone's AOP setting and the tunnel at the same time.
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, z := range zones {
		wg.Add(1)
		go func(z cloudflare.Zone) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if on, err := cl.AOPEnabled(ctx, z.ID); err == nil {
				mu.Lock()
				st.AOP[z.Name] = on
				mu.Unlock()
			}
		}(z)
	}
	if s.TunnelID != "" && s.AccountID != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if t, err := cl.Tunnel(ctx, s.AccountID, s.TunnelID); err == nil {
				st.Tunnel = &t
			}
		}()
	}
	wg.Wait()
	e.cfStateMu.Lock()
	e.cfStatus = st
	e.cfStateMu.Unlock()
	return st
}

// InvalidateCloudflareStatus drops the cached account view.
func (e *Engine) InvalidateCloudflareStatus() {
	e.cfStateMu.Lock()
	e.cfStatus = CloudflareStatus{}
	e.cfStateMu.Unlock()
}

// UIOrigin is where the tunnel sends traffic for the management hostname.
//
// Every node runs a cloudflared replica of the same tunnel, and Cloudflare
// hands each request to whichever connector it likes. An origin of 127.0.0.1
// would therefore land on a read-only standby as often as not, so in a
// cluster the origin is the primary's own address: any connector forwards
// the UI to the node that accepts edits and owns the user list.
func (e *Engine) UIOrigin() string {
	host, port, err := net.SplitHostPort(e.Cfg.Listen)
	if err != nil || port == "" {
		host, port = "", "8443"
	}
	return "https://" + net.JoinHostPort(uiOriginHost(host, e.Cfg.Node.Address, len(e.Cfg.Cluster.Peers) > 0), port)
}

// uiOriginHost picks the address cloudflared should dial for the UI.
func uiOriginHost(listenHost, nodeAddr string, cluster bool) string {
	if ip := net.ParseIP(listenHost); ip != nil && ip.IsLoopback() {
		// Only reachable locally; peers could not reach it anyway.
		return listenHost
	}
	if listenHost == "localhost" {
		return "127.0.0.1"
	}
	if !cluster {
		return "127.0.0.1"
	}
	if listenHost != "" {
		if ip := net.ParseIP(listenHost); ip == nil || !ip.IsUnspecified() {
			return listenHost
		}
	}
	if nodeAddr != "" {
		return nodeAddr
	}
	return "127.0.0.1"
}

// ReconcileCloudflare syncs DNS records and tunnel ingress with the hosts.
func (e *Engine) ReconcileCloudflare(ctx context.Context, dryRun bool) (CloudflareReport, error) {
	e.cfMu.Lock()
	defer e.cfMu.Unlock()
	rep := CloudflareReport{Time: time.Now().UTC(), DryRun: dryRun}
	if !dryRun && !e.Cfg.IsPrimary() {
		// The primary owns DNS and tunnel ingress; a standby writing them
		// would point the UI at itself until the primary's next reconcile.
		return rep, ErrStandby
	}
	settings, err := e.Store.Cloudflare(ctx)
	if err != nil {
		return rep, err
	}
	if settings.APIToken == "" {
		return rep, ErrCloudflareOff
	}
	snap, err := e.Store.TakeSnapshot(ctx)
	if err != nil {
		return rep, err
	}
	hasCert := func(name string) bool {
		for _, c := range snap.Certs {
			if c.CertPEM == "" || c.Source == "upload" && strings.Contains(c.Issuer, "self-signed") {
				continue
			}
			for _, d := range c.Domains {
				if render.MatchWildcard(d, name) {
					return true
				}
			}
		}
		return false
	}
	plan := cloudflare.Plan(cloudflare.PlanInput{Hosts: snap.Hosts, VIPs: snap.VIPs, Settings: settings, UIOrigin: e.UIOrigin(), HasCert: hasCert})
	rep.Notes = plan.Notes
	rep.Ingress = plan.Ingress
	cl := cloudflare.New(settings.APIToken)
	fail := func(err error) (CloudflareReport, error) {
		rep.Error = err.Error()
		e.setReport(rep)
		return rep, err
	}
	changes, states, dnsErr := cloudflare.ReconcileDNS(ctx, cl, plan.Records, dryRun)
	rep.Changes, rep.Records = changes, states
	if dnsErr != nil && len(changes) == 0 && len(states) == 0 {
		// Nothing could be checked at all (e.g. the zone list failed).
		e.Store.AddEvent(ctx, "error", "cloudflare", "DNS sync failed: "+dnsErr.Error())
		return fail(dnsErr)
	}
	if settings.TunnelID != "" && settings.AccountID != "" && !dryRun {
		cur, err := cl.TunnelConfig(ctx, settings.AccountID, settings.TunnelID)
		if err != nil {
			return fail(fmt.Errorf("read tunnel config: %w", err))
		}
		if !sameIngress(cur, plan.Ingress) {
			ingress := plan.Ingress
			if len(ingress) == 0 {
				ingress = []cloudflare.IngressRule{{Service: "http_status:404"}}
			}
			if err := cl.PutTunnelConfig(ctx, settings.AccountID, settings.TunnelID, ingress); err != nil {
				return fail(fmt.Errorf("update tunnel config: %w", err))
			}
			rep.Changes = append(rep.Changes, cloudflare.Change{Action: "update", Record: settings.TunnelName,
				Detail: fmt.Sprintf("tunnel ingress set to %d rules", len(ingress))})
			e.Store.AddEvent(ctx, "info", "cloudflare", fmt.Sprintf("Tunnel %s ingress synced (%d rules)", settings.TunnelName, len(ingress)))
		}
	}
	n := 0
	for _, c := range rep.Changes {
		if c.Action == "create" || c.Action == "update" || c.Action == "delete" {
			n++
		}
	}
	if n > 0 && !dryRun {
		e.Store.AddEvent(ctx, "info", "cloudflare", fmt.Sprintf("Cloudflare reconciled: %d change(s)", n))
	}
	if dnsErr != nil {
		// Other records were still synced; report what failed.
		rep.Error = "Some DNS records could not be synced: " + dnsErr.Error()
		if !dryRun {
			e.Store.AddEvent(ctx, "error", "cloudflare", rep.Error)
		}
	}
	settings.LastReconcile = rep.Time.Format(time.RFC3339)
	_ = e.Store.SetCloudflare(ctx, settings)
	e.setReport(rep)
	if dnsErr != nil {
		return rep, errors.New(rep.Error)
	}
	return rep, nil
}

func sameIngress(a, b []cloudflare.IngressRule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if x.Hostname != y.Hostname || x.Service != y.Service {
			return false
		}
		var xo, yo cloudflare.OriginRequest
		if x.OriginRequest != nil {
			xo = *x.OriginRequest
		}
		if y.OriginRequest != nil {
			yo = *y.OriginRequest
		}
		if xo != yo {
			return false
		}
	}
	return true
}

// EnsureTunnel creates the managed tunnel if needed and writes the connector
// token for cloudflared on this node.
func (e *Engine) EnsureTunnel(ctx context.Context, name string) error {
	s, err := e.Store.Cloudflare(ctx)
	if err != nil {
		return err
	}
	if s.APIToken == "" || s.AccountID == "" {
		return errors.New("set the API token and account first")
	}
	cl := cloudflare.New(s.APIToken)
	if s.TunnelID == "" {
		if name == "" {
			name = "halyard"
		}
		ts, err := cl.Tunnels(ctx, s.AccountID)
		if err != nil {
			return err
		}
		for _, t := range ts {
			if t.Name == name {
				if t.ConfigSrc != "" && t.ConfigSrc != "cloudflare" {
					return fmt.Errorf("tunnel %q exists but is locally managed; pick another name", name)
				}
				s.TunnelID, s.TunnelName = t.ID, t.Name
			}
		}
		if s.TunnelID == "" {
			t, err := cl.CreateTunnel(ctx, s.AccountID, name)
			if err != nil {
				return err
			}
			s.TunnelID, s.TunnelName = t.ID, t.Name
			e.Store.AddEvent(ctx, "info", "cloudflare", "Created tunnel "+t.Name)
		}
		if err := e.Store.SetCloudflare(ctx, s); err != nil {
			return err
		}
	}
	return e.WriteTunnelToken(ctx)
}

// WriteTunnelToken fetches the connector token and restarts cloudflared.
func (e *Engine) WriteTunnelToken(ctx context.Context) error {
	if !e.Cfg.Cloudflared.Enabled {
		return nil
	}
	s, err := e.Store.Cloudflare(ctx)
	if err != nil {
		return err
	}
	if s.TunnelID == "" {
		return nil
	}
	tok, err := cloudflare.New(s.APIToken).TunnelToken(ctx, s.AccountID, s.TunnelID)
	if err != nil {
		return err
	}
	body := "TUNNEL_TOKEN=" + tok + "\n"
	if old, _ := os.ReadFile(e.Cfg.Cloudflared.EnvFile); string(old) == body {
		return nil
	}
	if err := writeAtomic(e.Cfg.Cloudflared.EnvFile, []byte(body), 0o600); err != nil {
		return err
	}
	if !e.Cfg.DryRun {
		if o, err := runShell(ctx, e.Cfg.Cloudflared.ReloadCmd); err != nil {
			return fmt.Errorf("restart cloudflared: %s", o)
		}
	}
	return nil
}

// SetAOP enables or disables Authenticated Origin Pulls for a zone name.
func (e *Engine) SetAOP(ctx context.Context, zoneName string, on bool) error {
	s, err := e.Store.Cloudflare(ctx)
	if err != nil {
		return err
	}
	if s.APIToken == "" {
		return ErrCloudflareOff
	}
	cl := cloudflare.New(s.APIToken)
	zones, err := cl.Zones(ctx)
	if err != nil {
		return err
	}
	var zid string
	for _, z := range zones {
		if z.Name == zoneName {
			zid = z.ID
		}
	}
	if zid == "" {
		return fmt.Errorf("zone %s not found for this token", zoneName)
	}
	if on {
		// Make sure the CA is present before Cloudflare starts presenting certs.
		if _, err := os.Stat(e.Paths.AOPCA); err != nil {
			ca, err := cloudflare.FetchOriginPullCA(ctx)
			if err != nil {
				return err
			}
			if err := writeAtomic(e.Paths.AOPCA, ca, 0o644); err != nil {
				return err
			}
		}
	}
	if err := cl.SetAOP(ctx, zid, on); err != nil {
		return err
	}
	var zonesOn []string
	for _, z := range s.AOPZones {
		if z != zoneName {
			zonesOn = append(zonesOn, z)
		}
	}
	if on {
		zonesOn = append(zonesOn, zoneName)
	}
	s.AOPZones = zonesOn
	state := "disabled"
	if on {
		state = "enabled"
	}
	e.Store.AddEvent(ctx, "info", "cloudflare", fmt.Sprintf("Authenticated Origin Pulls %s for %s", state, zoneName))
	// Cloudflare can take a moment to report the new value; trust what we set.
	e.cfStateMu.Lock()
	if e.cfStatus.AOP != nil {
		e.cfStatus.AOP[zoneName] = on
	}
	e.cfStateMu.Unlock()
	return e.Store.SetCloudflare(ctx, s)
}
