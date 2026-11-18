package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshuadouch/halyard/internal/config"
	"github.com/joshuadouch/halyard/internal/engine"
	"github.com/joshuadouch/halyard/internal/store"
)

type client struct {
	t    *testing.T
	base string
	hc   *http.Client
	srv  *Server
}

func (c *client) do(method, path string, body any, csrf bool) (int, map[string]any) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if csrf {
		req.Header.Set("X-Halyard", "1")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		out = map[string]any{"_raw": string(raw)}
	}
	return resp.StatusCode, out
}

func newTestServer(t *testing.T) *client {
	t.Helper()
	if _, err := exec.LookPath("haproxy"); err != nil {
		t.Skip("haproxy not installed")
	}
	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.DryRun = true
	cfg.Node.Name = "lb-test"
	cfg.Cluster.Secret = "s"
	cfg.HAProxy.ConfigPath = filepath.Join(dir, "haproxy.cfg")
	cfg.HAProxy.Socket = filepath.Join(dir, "admin.sock")
	cfg.HAProxy.StateFile = ""
	cfg.Keepalived.Enabled = false
	cfg.Cloudflared.Enabled = false

	st, err := store.Open(cfg.Path("halyard.db"))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := engine.New(cfg, st, log)
	_ = os.MkdirAll(eng.Paths.MapDir, 0o750)
	_ = os.WriteFile(eng.Paths.CloudflareIPs, []byte("173.245.48.0/20\n"), 0o644)
	if err := eng.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv := &Server{Cfg: cfg, Store: st, Engine: eng, Log: log, Version: "test"}
	ts := httptest.NewTLSServer(srv.Handler())
	t.Cleanup(func() { ts.Close() })
	hc := ts.Client()
	hc.Jar, _ = cookiejar.New(nil)
	return &client{t: t, base: ts.URL, hc: hc, srv: srv}
}

func TestAPIFlow(t *testing.T) {
	c := newTestServer(t)

	if code, _ := c.do("GET", "/api/dashboard", nil, false); code != 401 {
		t.Fatalf("dashboard without a session: %d", code)
	}
	if code, out := c.do("POST", "/api/setup", map[string]string{"username": "admin", "password": "short"}, false); code != 400 {
		t.Fatalf("weak password accepted: %d %v", code, out)
	}
	if code, out := c.do("POST", "/api/setup", map[string]string{"username": "admin", "password": "a-long-password"}, false); code != 200 {
		t.Fatalf("setup: %d %v", code, out)
	}
	if code, _ := c.do("POST", "/api/setup", map[string]string{"username": "evil", "password": "a-long-password"}, false); code != 403 {
		t.Fatal("setup ran twice")
	}

	vip := map[string]any{"address": "127.0.0.10/8", "label": "test", "interface": "lo", "vrid": 51}
	if code, _ := c.do("POST", "/api/vips", vip, false); code != 403 {
		t.Fatal("mutation without X-Halyard header was allowed")
	}
	code, out := c.do("POST", "/api/vips", vip, true)
	if code != 200 {
		t.Fatalf("create vip: %d %v", code, out)
	}
	vipID := out["id"].(string)

	pool := map[string]any{"name": "web", "members": []map[string]any{{"name": "web-1", "address": "127.0.0.1:18081"}}}
	code, out = c.do("POST", "/api/pools", pool, true)
	if code != 200 {
		t.Fatalf("create pool: %d %v", code, out)
	}
	poolID := out["id"].(string)

	host := map[string]any{"hostnames": []string{"app.example.test"}, "vip_id": vipID, "http_port": 18080,
		"routes": []map[string]any{{"path_prefix": "/", "pool_id": poolID}}, "enabled": true}
	if code, out = c.do("POST", "/api/hosts", host, true); code != 200 {
		t.Fatalf("create host: %d %v", code, out)
	}

	// A config HAProxy rejects is rolled back and nothing changes.
	bad := map[string]any{"name": "bad", "members": []map[string]any{{"name": "x", "address": "not-an-address"}}}
	if code, out = c.do("POST", "/api/pools", bad, true); code == 200 {
		t.Fatalf("invalid pool accepted: %v", out)
	}
	code, list := c.do("GET", "/api/pools", nil, false)
	if code != 200 || strings.Contains(list["_raw"].(string), `"bad"`) {
		t.Fatalf("invalid pool was saved: %v", list)
	}

	code, out = c.do("GET", "/api/preview", nil, false)
	if code != 200 || !strings.Contains(out["haproxy"].(string), "be_web") {
		t.Fatalf("preview: %d %v", code, out)
	}

	// Access lists: passwords are hashed, never returned, and kept when blank.
	acl := map[string]any{"name": "office", "allow_cidrs": []string{"192.168.1.0/24"}, "satisfy_any": true,
		"users": []map[string]any{{"username": "josh", "password": "a-long-password"}}}
	code, out = c.do("POST", "/api/access-lists", acl, true)
	if code != 200 {
		t.Fatalf("create access list: %d %v", code, out)
	}
	aclID := out["id"].(string)
	if raw, _ := json.Marshal(out); strings.Contains(string(raw), "$6$") || strings.Contains(string(raw), "a-long-password") {
		t.Fatalf("access list response leaks a password: %s", raw)
	}
	acl["users"] = []map[string]any{{"username": "josh", "password": ""}}
	if code, out = c.do("PUT", "/api/access-lists/"+aclID, acl, true); code != 200 {
		t.Fatalf("resave access list with blank password: %d %v", code, out)
	}
	acl["users"] = []map[string]any{{"username": "new", "password": ""}}
	if code, _ = c.do("PUT", "/api/access-lists/"+aclID, acl, true); code != 400 {
		t.Fatalf("new user without a password accepted: %d", code)
	}
	host["access_list_id"] = aclID
	host["hostnames"] = []string{"office.example.test"}
	if code, out = c.do("POST", "/api/hosts", host, true); code != 200 {
		t.Fatalf("host with access list: %d %v", code, out)
	}
	code, out = c.do("GET", "/api/preview", nil, false)
	if cfg := out["haproxy"].(string); code != 200 || !strings.Contains(cfg, "user josh password $6$") || !strings.Contains(cfg, "http_auth(ul_") {
		t.Fatalf("preview lacks the access list: %d %v", code, out)
	}
	if code, _ = c.do("DELETE", "/api/access-lists/"+aclID, nil, true); code != 400 {
		t.Fatalf("deleted an access list a host still uses: %d", code)
	}
}

func TestPeerInterfaces(t *testing.T) {
	c := newTestServer(t)
	if code, out := c.do("POST", "/api/setup", map[string]string{"username": "admin", "password": "a-long-password"}, false); code != 200 {
		t.Fatalf("setup: %d %v", code, out)
	}
	// The node is its own peer here, reached over the sealed cluster endpoint.
	c.srv.Cfg.Cluster.Peers = []config.Peer{{Name: "lb-peer", URL: c.base}}
	if code, out := c.do("PUT", "/api/interfaces/lo/label?node=lb-peer", map[string]string{"label": "Loopback"}, true); code != 200 {
		t.Fatalf("label via peer: %d %v", code, out)
	}
	if got := c.srv.interfaceLabels(context.Background())["lo"]; got != "Loopback" {
		t.Fatalf("label not saved on the peer: %q", got)
	}
	code, out := c.do("GET", "/api/nodes/interfaces", nil, false)
	raw, _ := json.Marshal(out)
	if code != 200 {
		t.Fatalf("all interfaces: %d %s", code, raw)
	}
	req, _ := http.NewRequest("GET", c.base+"/api/nodes/interfaces", nil)
	resp, err := c.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var nodes []NodeInterfaces
	if err := json.NewDecoder(resp.Body).Decode(&nodes); err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 || !nodes[0].Self || nodes[1].Node != "lb-peer" || nodes[1].Error != "" {
		t.Fatalf("unexpected nodes: %+v", nodes)
	}
	// Forged requests are refused.
	resp2, err := c.hc.Post(c.base+"/api/cluster/interfaces", "application/octet-stream", strings.NewReader("not sealed"))
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 400 {
		t.Fatalf("unsealed request: %d", resp2.StatusCode)
	}
}
