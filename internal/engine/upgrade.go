package engine

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/joshuadouch/halyard/internal/config"
)

var (
	tagRe  = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
	repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

	// GitHubAPI is overridden in tests.
	GitHubAPI = "https://api.github.com"
)

// UpgradeJob is the state of a rolling upgrade started from the UI.
type UpgradeJob struct {
	Running  bool      `json:"running"`
	Target   string    `json:"target"`
	Author   string    `json:"author"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	// Step is "peers", "self" (this node is restarting) or "".
	Step  string   `json:"step"`
	Log   []string `json:"log"`
	Error string   `json:"error"`
}

// UpgradeNode is one node's running version.
type UpgradeNode struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	Version   string `json:"version"`
	Reachable bool   `json:"reachable"`
	Self      bool   `json:"self"`
	Error     string `json:"error,omitempty"`
}

// UpgradeInfo is what the Nodes page shows about versions.
type UpgradeInfo struct {
	Current    string        `json:"current"`
	Latest     string        `json:"latest"`
	LatestURL  string        `json:"latest_url"`
	CheckError string        `json:"check_error,omitempty"`
	Nodes      []UpgradeNode `json:"nodes"`
	Job        UpgradeJob    `json:"job"`
}

type latestCache struct {
	tag, url string
	err      error
	at       time.Time
}

// upgradeCommand is sealed with the cluster secret like a sync payload.
type upgradeCommand struct {
	From   string    `json:"from"`
	Time   time.Time `json:"time"`
	Target string    `json:"target"`
}

var insecureClient = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}

// LatestRelease returns the newest published release tag (cached 10 minutes).
func (e *Engine) LatestRelease(ctx context.Context) (string, string, error) {
	e.upgradeMu.Lock()
	c := e.latest
	e.upgradeMu.Unlock()
	if time.Since(c.at) < 10*time.Minute {
		return c.tag, c.url, c.err
	}
	tag, url, err := func() (string, string, error) {
		if !repoRe.MatchString(e.Cfg.UpdateRepo) {
			return "", "", errors.New("update_repo is not set to owner/name")
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", GitHubAPI+"/repos/"+e.Cfg.UpdateRepo+"/releases/latest", nil)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "halyard")
		hc := &http.Client{Timeout: 10 * time.Second}
		resp, err := hc.Do(req)
		if err != nil {
			return "", "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return "", "", fmt.Errorf("GitHub returned HTTP %d (is the repository public?)", resp.StatusCode)
		}
		var r struct {
			TagName string `json:"tag_name"`
			HTMLURL string `json:"html_url"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return "", "", err
		}
		return r.TagName, r.HTMLURL, nil
	}()
	e.upgradeMu.Lock()
	e.latest = latestCache{tag: tag, url: url, err: err, at: time.Now()}
	e.upgradeMu.Unlock()
	return tag, url, err
}

func peerHealth(ctx context.Context, p config.Peer) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(p.URL, "/")+"/api/health", nil)
	if err != nil {
		return "", err
	}
	resp, err := insecureClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var h struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&h); err != nil {
		return "", err
	}
	if h.Version == "" {
		h.Version = "older than v0.1.5"
	}
	return h.Version, nil
}

// UpgradeInfo reports versions of this node and its peers.
func (e *Engine) UpgradeInfo(ctx context.Context) UpgradeInfo {
	info := UpgradeInfo{Current: e.Version}
	if tag, url, err := e.LatestRelease(ctx); err != nil {
		info.CheckError = err.Error()
	} else {
		info.Latest, info.LatestURL = tag, url
	}
	info.Nodes = append(info.Nodes, UpgradeNode{Name: e.Cfg.Node.Name, Version: e.Version, Reachable: true, Self: true})
	for _, p := range e.Cfg.Cluster.Peers {
		n := UpgradeNode{Name: p.Name, URL: p.URL}
		ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		v, err := peerHealth(ctx, p)
		cancel()
		if err != nil {
			n.Error = err.Error()
		} else {
			n.Version, n.Reachable = v, true
		}
		info.Nodes = append(info.Nodes, n)
	}
	e.upgradeMu.Lock()
	info.Job = e.upgradeJob
	info.Job.Log = append([]string(nil), e.upgradeJob.Log...)
	e.upgradeMu.Unlock()
	return info
}

func (e *Engine) jobLog(format string, args ...any) {
	msg := time.Now().Format("15:04:05") + "  " + fmt.Sprintf(format, args...)
	e.Log.Info("upgrade: " + msg)
	e.upgradeMu.Lock()
	e.upgradeJob.Log = append(e.upgradeJob.Log, msg)
	e.upgradeMu.Unlock()
}

func (e *Engine) jobEnd(err error) {
	e.upgradeMu.Lock()
	e.upgradeJob.Running = false
	e.upgradeJob.Step = ""
	e.upgradeJob.Finished = time.Now().UTC()
	if err != nil {
		e.upgradeJob.Error = err.Error()
	}
	e.upgradeMu.Unlock()
	if err != nil {
		e.jobLog("Stopped: %v", err)
		e.Store.AddEvent(context.Background(), "error", "upgrade", "Upgrade stopped: "+err.Error())
	}
}

// StartUpgrade upgrades every peer one at a time, then this node.
func (e *Engine) StartUpgrade(target, author string) error {
	if !e.Cfg.IsPrimary() {
		return errors.New("start upgrades from the primary")
	}
	if !tagRe.MatchString(target) {
		return errors.New("invalid version")
	}
	e.upgradeMu.Lock()
	if e.upgradeJob.Running {
		e.upgradeMu.Unlock()
		return errors.New("an upgrade is already running")
	}
	e.upgradeJob = UpgradeJob{Running: true, Target: target, Author: author, Started: time.Now().UTC(), Step: "peers"}
	e.upgradeMu.Unlock()
	e.Store.AddEvent(context.Background(), "info", "upgrade", fmt.Sprintf("%s started an upgrade to %s", author, target))
	go e.runUpgrade(target)
	return nil
}

func (e *Engine) runUpgrade(target string) {
	ctx := context.Background()
	for _, p := range e.Cfg.Cluster.Peers {
		if err := e.upgradePeer(ctx, p, target); err != nil {
			e.jobEnd(fmt.Errorf("%s: %w. %s was not upgraded", p.Name, err, e.Cfg.Node.Name))
			return
		}
	}
	if e.Version == target {
		e.jobLog("%s is already on %s", e.Cfg.Node.Name, target)
		e.jobLog("All nodes are on %s", target)
		e.Store.AddEvent(ctx, "info", "upgrade", "All nodes upgraded to "+target)
		e.jobEnd(nil)
		return
	}
	e.upgradeMu.Lock()
	e.upgradeJob.Step = "self"
	e.upgradeMu.Unlock()
	e.jobLog("Upgrading %s (this node). The page reconnects when it is back.", e.Cfg.Node.Name)
	e.Store.AddEvent(ctx, "info", "upgrade", fmt.Sprintf("Upgrading %s to %s", e.Cfg.Node.Name, target))
	if err := e.runUpgrader(target); err != nil {
		e.jobEnd(err)
		return
	}
	if e.Cfg.DryRun {
		e.jobLog("Dry run: this node was not changed")
		e.jobEnd(nil)
	}
	// On success the installer restarts this process; nothing more to do.
}

func (e *Engine) upgradePeer(ctx context.Context, p config.Peer, target string) error {
	v, err := peerHealth(ctx, p)
	if err != nil {
		return fmt.Errorf("not reachable: %v", err)
	}
	if v == target {
		e.jobLog("%s is already on %s", p.Name, target)
		return nil
	}
	e.jobLog("Upgrading %s from %s to %s", p.Name, v, target)
	body, _ := json.Marshal(upgradeCommand{From: e.Cfg.Node.Name, Time: time.Now().UTC(), Target: target})
	sealed, err := Seal(e.Cfg.Cluster.Secret, body)
	if err != nil {
		return err
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(p.URL, "/")+"/api/cluster/upgrade", bytes.NewReader(sealed))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := insecureClient.Do(req)
	if err != nil {
		return err
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	switch {
	case resp.StatusCode == 404 || resp.StatusCode == 405:
		return errors.New("its version cannot upgrade from the UI yet; run halyard-update on it once")
	case resp.StatusCode != 200:
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	e.jobLog("%s is installing; waiting for it to come back on %s", p.Name, target)
	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Second)
		hctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		v, err := peerHealth(hctx, p)
		cancel()
		if err == nil && v == target {
			e.jobLog("%s is healthy on %s", p.Name, target)
			// Make sure it has the current config before moving on.
			e.SyncPeers(ctx)
			return nil
		}
	}
	return errors.New("did not come back on the new version within 15 minutes; check `journalctl -u 'halyard-upgrade-*'` on it")
}

// ReceiveUpgrade handles an upgrade command from the primary.
func (e *Engine) ReceiveUpgrade(sealed []byte) error {
	if e.Cfg.Cluster.Secret == "" {
		return errors.New("cluster secret not configured")
	}
	plain, err := Open(e.Cfg.Cluster.Secret, sealed)
	if err != nil {
		return errors.New("command could not be authenticated")
	}
	var c upgradeCommand
	if err := json.Unmarshal(plain, &c); err != nil {
		return err
	}
	if d := time.Since(c.Time); d > 5*time.Minute || d < -5*time.Minute {
		return errors.New("command is too old or clocks are out of sync")
	}
	if !tagRe.MatchString(c.Target) {
		return errors.New("invalid version")
	}
	if c.Target == e.Version {
		return nil
	}
	e.Store.AddEvent(context.Background(), "info", "upgrade", fmt.Sprintf("Upgrading to %s (requested by %s)", c.Target, c.From))
	return e.runUpgrader(c.Target)
}

// runUpgrader starts the installer in its own systemd unit, so it survives
// the installer restarting this service.
func (e *Engine) runUpgrader(target string) error {
	if !tagRe.MatchString(target) || !repoRe.MatchString(e.Cfg.UpdateRepo) {
		return errors.New("invalid version or update_repo")
	}
	if e.Cfg.DryRun {
		e.Log.Info("dry run: not upgrading", "target", target)
		return nil
	}
	unit := fmt.Sprintf("halyard-upgrade-%d", time.Now().Unix())
	script := `bash -c "$(curl -fsSL --retry 3 --connect-timeout 20 "https://raw.githubusercontent.com/${HALYARD_REPO}/main/install/halyard-install.sh")"`
	cmd := exec.Command("systemd-run", "--unit="+unit, "--collect",
		"--setenv=HALYARD_VERSION="+target, "--setenv=HALYARD_REPO="+e.Cfg.UpdateRepo,
		"--setenv=LANG=C.UTF-8", "--setenv=LC_ALL=C.UTF-8",
		"/bin/bash", "-c", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("could not start the installer: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
