// Package engine applies Halyard state to the node: it renders config,
// validates it, reloads HAProxy and keepalived, records revisions, syncs
// peers and keeps Cloudflare and certificates up to date.
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/joshuadouch/halyard/internal/acme"
	"github.com/joshuadouch/halyard/internal/cloudflare"
	"github.com/joshuadouch/halyard/internal/config"
	"github.com/joshuadouch/halyard/internal/haproxy"
	"github.com/joshuadouch/halyard/internal/model"
	"github.com/joshuadouch/halyard/internal/render"
	"github.com/joshuadouch/halyard/internal/store"
)

// Engine coordinates applies. Only one apply runs at a time.
type Engine struct {
	Cfg     *config.Config
	Store   *store.Store
	Paths   render.Paths
	Runtime haproxy.Runtime
	Log     *slog.Logger
	Issuer  *acme.Issuer
	// Version is the running build, e.g. v0.1.5.
	Version string

	upgradeMu  sync.Mutex
	upgradeJob UpgradeJob
	latest     latestCache

	mu       sync.Mutex
	cfMu     sync.Mutex
	issuing  sync.Map // cert id -> bool
	lastSync sync.Map // peer name -> PeerStatus
	cfReport CloudflareReport
}

// New builds an engine.
func New(cfg *config.Config, st *store.Store, log *slog.Logger) *Engine {
	return &Engine{
		Cfg: cfg, Store: st, Log: log,
		Paths:   render.DefaultPaths(cfg),
		Runtime: haproxy.Runtime{Socket: cfg.HAProxy.Socket},
		Issuer:  &acme.Issuer{Store: st},
	}
}

// Bootstrap creates directories and files HAProxy needs before the first apply.
func (e *Engine) Bootstrap(ctx context.Context) error {
	for _, d := range []string{e.Cfg.DataDir, e.Paths.CertDir, e.Paths.MapDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return err
		}
	}
	if err := EnsureSelfSigned(filepath.Join(e.Paths.CertDir, "default.pem"), "", "halyard.invalid"); err != nil {
		return fmt.Errorf("default cert: %w", err)
	}
	if _, err := os.Stat(e.Paths.CloudflareIPs); err != nil {
		if err := e.RefreshCloudflareIPs(ctx); err != nil {
			e.Log.Warn("could not fetch Cloudflare IP ranges; real IP restore stays off until it succeeds", "err", err)
			_ = writeAtomic(e.Paths.CloudflareIPs, []byte("# Cloudflare ranges not fetched yet\n"), 0o644)
		}
	}
	return nil
}

// RefreshCloudflareIPs downloads Cloudflare's edge ranges for real IP restore.
func (e *Engine) RefreshCloudflareIPs(ctx context.Context) error {
	r, err := cloudflare.FetchIPRanges(ctx)
	if err != nil {
		return err
	}
	body := "# Cloudflare edge ranges, refreshed " + time.Now().UTC().Format(time.RFC3339) + "\n" +
		strings.Join(append(r.IPv4, r.IPv6...), "\n") + "\n"
	old, _ := os.ReadFile(e.Paths.CloudflareIPs)
	if stripComments(string(old)) == stripComments(body) {
		return nil
	}
	return writeAtomic(e.Paths.CloudflareIPs, []byte(body), 0o644)
}

func stripComments(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// selfIPs are addresses whose CF-Connecting-IP header is trusted (local cloudflared).
func (e *Engine) selfIPs(vips []model.VIP) string {
	set := map[string]bool{"127.0.0.1": true, "::1": true}
	if e.Cfg.Node.Address != "" {
		set[e.Cfg.Node.Address] = true
	}
	for _, v := range vips {
		set[v.IP()] = true
	}
	if ifs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range ifs {
			if ipn, ok := a.(*net.IPNet); ok {
				set[ipn.IP.String()] = true
			}
		}
	}
	var l []string
	for k := range set {
		l = append(l, k)
	}
	sort.Strings(l)
	return strings.Join(l, "\n") + "\n"
}

// ApplyResult is what an apply returns.
type ApplyResult struct {
	Revision int64  `json:"revision"`
	Changed  bool   `json:"changed"`
	Output   string `json:"output"`
}

// ValidationError is returned when HAProxy or keepalived reject the config.
type ValidationError struct {
	Output string
}

func (v *ValidationError) Error() string { return "configuration rejected:\n" + v.Output }

// Render produces config for the current state without applying it.
func (e *Engine) Render(ctx context.Context) (*render.Output, store.Snapshot, error) {
	snap, err := e.Store.TakeSnapshot(ctx)
	if err != nil {
		return nil, snap, err
	}
	cf, err := e.Store.Cloudflare(ctx)
	if err != nil {
		return nil, snap, err
	}
	out, err := render.Render(render.Input{Snap: snap, Config: e.Cfg, Cloudflare: cf, Paths: e.Paths})
	return out, snap, err
}

// Apply renders, validates and activates the current state.
func (e *Engine) Apply(ctx context.Context, author, message string) (*ApplyResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.applyLocked(ctx, author, message, true)
}

func (e *Engine) applyLocked(ctx context.Context, author, message string, propagate bool) (*ApplyResult, error) {
	out, snap, err := e.Render(ctx)
	if err != nil {
		return nil, err
	}
	snapJSON, _ := json.Marshal(redactSnapshot(snap))
	rev := model.Revision{Author: author, Message: message, HAProxy: out.HAProxy, Keepalive: out.Keepalived, Snapshot: string(snapJSON)}

	filesChanged, err := e.writeSupportFiles(ctx, snap, out)
	if err != nil {
		rev.Status, rev.Error = "failed", err.Error()
		_, _ = e.Store.AddRevision(ctx, rev)
		return nil, err
	}

	cur, _ := os.ReadFile(e.Cfg.HAProxy.ConfigPath)
	curKA, _ := os.ReadFile(e.Cfg.Keepalived.ConfigPath)
	changed := filesChanged || string(cur) != out.HAProxy || (e.Cfg.Keepalived.Enabled && string(curKA) != out.Keepalived)

	candidate := e.Cfg.HAProxy.ConfigPath + ".halyard-new"
	if err := writeAtomic(candidate, []byte(out.HAProxy), 0o640); err != nil {
		return nil, err
	}
	defer os.Remove(candidate)
	check, err := runCmd(ctx, e.Cfg.HAProxy.Binary, "-c", "-q", "-f", candidate)
	if err != nil {
		rev.Status, rev.Error = "failed", check
		id, _ := e.Store.AddRevision(ctx, rev)
		e.Store.AddEvent(ctx, "error", "config", fmt.Sprintf("Config rev %d rejected by haproxy -c", id))
		return nil, &ValidationError{Output: cleanCheckOutput(check, candidate)}
	}

	var kaCandidate string
	if e.Cfg.Keepalived.Enabled {
		kaCandidate = e.Cfg.Keepalived.ConfigPath + ".halyard-new"
		if err := writeAtomic(kaCandidate, []byte(out.Keepalived), 0o640); err != nil {
			return nil, err
		}
		defer os.Remove(kaCandidate)
		if _, err := exec.LookPath("keepalived"); err == nil {
			if o, err := runCmd(ctx, "keepalived", "--config-test="+kaCandidate); err != nil {
				rev.Status, rev.Error = "failed", o
				_, _ = e.Store.AddRevision(ctx, rev)
				return nil, &ValidationError{Output: o}
			}
		}
	}

	res := &ApplyResult{Changed: changed}
	if !e.Cfg.DryRun {
		if changed || len(cur) == 0 {
			if e.Cfg.HAProxy.StateFile != "" {
				if st, err := e.Runtime.ServersState(); err == nil {
					_ = writeAtomic(e.Cfg.HAProxy.StateFile, []byte(st), 0o644)
				}
			}
			if err := os.Rename(candidate, e.Cfg.HAProxy.ConfigPath); err != nil {
				return nil, err
			}
			if o, err := runShell(ctx, e.Cfg.HAProxy.ReloadCmd); err != nil {
				rev.Status, rev.Error = "failed", "reload: "+o
				_, _ = e.Store.AddRevision(ctx, rev)
				return nil, fmt.Errorf("haproxy reload failed: %s", o)
			}
		}
		if e.Cfg.Keepalived.Enabled && string(curKA) != out.Keepalived {
			if err := os.Rename(kaCandidate, e.Cfg.Keepalived.ConfigPath); err != nil {
				return nil, err
			}
			if o, err := runShell(ctx, e.Cfg.Keepalived.ReloadCmd); err != nil {
				e.Store.AddEvent(ctx, "warn", "keepalived", "keepalived reload failed: "+o)
			}
		}
	} else {
		if err := writeAtomic(e.Cfg.HAProxy.ConfigPath, []byte(out.HAProxy), 0o640); err != nil {
			return nil, err
		}
		if e.Cfg.Keepalived.Enabled {
			_ = writeAtomic(e.Cfg.Keepalived.ConfigPath, []byte(out.Keepalived), 0o640)
		}
	}
	rev.Status = "applied"
	id, err := e.Store.AddRevision(ctx, rev)
	if err != nil {
		return nil, err
	}
	res.Revision = id
	verb := "applied"
	if !changed {
		verb = "applied (no config change)"
	}
	e.Store.AddEvent(ctx, "info", "config", fmt.Sprintf("Config rev %d %s by %s: %s", id, verb, author, message))

	if propagate && e.Cfg.IsPrimary() {
		go e.SyncPeers(context.Background())
		go func() {
			if _, err := e.ReconcileCloudflare(context.Background(), false); err != nil && !errors.Is(err, ErrCloudflareOff) {
				e.Log.Warn("cloudflare reconcile", "err", err)
			}
		}()
	}
	return res, nil
}

func cleanCheckOutput(s, candidate string) string {
	s = strings.ReplaceAll(s, candidate, "haproxy.cfg")
	var keep []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}

func redactSnapshot(s store.Snapshot) store.Snapshot {
	certs := make([]model.Certificate, len(s.Certs))
	for i, c := range s.Certs {
		c.KeyPEM = ""
		c.CertPEM = ""
		certs[i] = c
	}
	s.Certs = certs
	return s
}

func (e *Engine) writeSupportFiles(ctx context.Context, snap store.Snapshot, out *render.Output) (bool, error) {
	changed := false
	write := func(path string, body []byte, mode os.FileMode) error {
		if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, body) {
			return nil
		}
		changed = true
		return writeAtomic(path, body, mode)
	}
	for _, c := range snap.Certs {
		if c.CertPEM == "" || c.KeyPEM == "" {
			continue
		}
		body := strings.TrimSpace(c.CertPEM) + "\n" + strings.TrimSpace(c.KeyPEM) + "\n"
		if err := write(filepath.Join(e.Paths.CertDir, c.ID+".pem"), []byte(body), 0o600); err != nil {
			return changed, err
		}
	}
	for path, body := range out.Files {
		if err := write(path, []byte(body), 0o644); err != nil {
			return changed, err
		}
	}
	if err := write(e.Paths.SelfIPs, []byte(e.selfIPs(snap.VIPs)), 0o644); err != nil {
		return changed, err
	}
	needAOP := false
	for _, h := range snap.Hosts {
		if h.Enabled && h.RequireAOP {
			needAOP = true
		}
	}
	if needAOP {
		if _, err := os.Stat(e.Paths.AOPCA); err != nil {
			ca, err := cloudflare.FetchOriginPullCA(ctx)
			if err != nil {
				return changed, fmt.Errorf("download Cloudflare origin pull CA: %w", err)
			}
			if err := write(e.Paths.AOPCA, ca, 0o644); err != nil {
				return changed, err
			}
		}
	}
	return changed, nil
}

// Rollback restores the objects from a revision and applies them.
func (e *Engine) Rollback(ctx context.Context, id int64, author string) (*ApplyResult, error) {
	rev, err := e.Store.Revision(ctx, id)
	if err != nil {
		return nil, err
	}
	var snap store.Snapshot
	if err := json.Unmarshal([]byte(rev.Snapshot), &snap); err != nil {
		return nil, fmt.Errorf("revision %d has no usable snapshot", id)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	before, err := e.Store.TakeSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	if err := e.Store.RestoreSnapshot(ctx, snap, false); err != nil {
		return nil, err
	}
	res, err := e.applyLocked(ctx, author, fmt.Sprintf("Rollback to rev %d", id), true)
	if err != nil {
		_ = e.Store.RestoreSnapshot(ctx, before, false)
		return nil, err
	}
	return res, nil
}

func runCmd(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	return strings.TrimSpace(buf.String()), err
}

func runShell(ctx context.Context, cmdline string) (string, error) {
	if strings.TrimSpace(cmdline) == "" {
		return "", nil
	}
	return runCmd(ctx, "/bin/sh", "-c", cmdline)
}

// Loop runs periodic jobs until ctx ends.
func (e *Engine) Loop(ctx context.Context) {
	renew := time.NewTicker(12 * time.Hour)
	ips := time.NewTicker(24 * time.Hour)
	cf := time.NewTicker(15 * time.Minute)
	sync := time.NewTicker(5 * time.Minute)
	defer sync.Stop()
	defer renew.Stop()
	defer ips.Stop()
	defer cf.Stop()
	time.AfterFunc(30*time.Second, func() { e.RenewDue(ctx) })
	// Push to peers soon after start, so an update or restart re-syncs standbys.
	time.AfterFunc(10*time.Second, func() { e.SyncPeers(ctx) })
	for {
		select {
		case <-ctx.Done():
			return
		case <-renew.C:
			e.RenewDue(ctx)
		case <-ips.C:
			if err := e.RefreshCloudflareIPs(ctx); err != nil {
				e.Log.Warn("refresh Cloudflare IPs", "err", err)
			} else if e.Cfg.IsPrimary() {
				_, _ = e.Apply(ctx, "system", "Cloudflare IP ranges refreshed")
			}
		case <-sync.C:
			e.SyncPeers(ctx)
		case <-cf.C:
			if e.Cfg.IsPrimary() {
				if _, err := e.ReconcileCloudflare(ctx, false); err != nil && !errors.Is(err, ErrCloudflareOff) {
					e.Log.Warn("cloudflare reconcile", "err", err)
				}
			}
		}
	}
}
