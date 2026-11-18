package render_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshuadouch/halyard/internal/config"
	"github.com/joshuadouch/halyard/internal/engine"
	"github.com/joshuadouch/halyard/internal/model"
	. "github.com/joshuadouch/halyard/internal/render"
	"github.com/joshuadouch/halyard/internal/store"
)

func testInput(t *testing.T, dir string) Input {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.Node.Name = "lb-01"
	cfg.Node.Address = "10.0.30.11"
	cfg.Cluster.Secret = "test-secret"
	cfg.Cluster.Peers = []config.Peer{{Name: "lb-02", Address: "10.0.30.12"}}
	cfg.HAProxy.StateFile = filepath.Join(dir, "server-state")
	cfg.HAProxy.Socket = filepath.Join(dir, "admin.sock")

	vip := model.VIP{ID: "v1", Address: "10.0.10.10/24", Label: "apps", Interface: "eth1", VRID: 51, TrackHAProxy: true}
	web := model.Pool{ID: "p1", Name: "web", Algorithm: "roundrobin", Health: model.HealthCheck{Enabled: true, Type: "http", Method: "GET", Path: "/healthz", ExpectStatus: "2xx,3xx"},
		Members: []model.Member{{Name: "web-1", Address: "10.0.20.21:8080"}, {Name: "web-2", Address: "10.0.20.22:8080", State: "drain"}}}
	api := model.Pool{ID: "p2", Name: "api", Algorithm: "leastconn", Sticky: "cookie",
		Members: []model.Member{{Name: "api-1", Address: "10.0.20.31:9000"}}}
	db := model.Pool{ID: "p3", Name: "postgres", Mode: model.ModeTCP, Health: model.HealthCheck{Enabled: true, Type: "tcp"},
		Members: []model.Member{{Name: "pg-1", Address: "10.0.20.41:5432"}, {Name: "pg-2", Address: "10.0.20.42:5432", Backup: true}}}
	hosts := []model.Host{
		{ID: "h1", Hostnames: []string{"app.example.com"}, VIPID: "v1", HTTPPort: 80, HTTPSPort: 443, ForceHTTPS: true, HSTS: true,
			Routes: []model.Route{{PathPrefix: "/api", PoolID: "p2"}, {PathPrefix: "/", PoolID: "p1"}}, Exposure: model.ExposurePublic, RequireAOP: true, Enabled: true},
		{ID: "h2", Hostnames: []string{"*.lab.example.com"}, VIPID: "v1", HTTPPort: 80, HTTPSPort: 443,
			Routes: []model.Route{{PathPrefix: "/", PoolID: "p1"}}, AllowCIDRs: []string{"10.0.0.0/8"}, Enabled: true},
		{ID: "h3", Hostnames: []string{"db.example.com"}, VIPID: "v1", Mode: model.ModeTCP, TCPPort: 5432,
			Routes: []model.Route{{PoolID: "p3"}}, Enabled: true},
	}
	for i := range hosts {
		if err := hosts[i].Normalize(); err != nil {
			t.Fatalf("normalize host %s: %v", hosts[i].ID, err)
		}
	}
	pools := []model.Pool{web, api, db}
	for i := range pools {
		if err := pools[i].Normalize(); err != nil {
			t.Fatalf("normalize pool %s: %v", pools[i].Name, err)
		}
	}
	cf := model.CloudflareSettings{RealIP: true}
	return Input{Snap: store.Snapshot{VIPs: []model.VIP{vip}, Hosts: hosts, Pools: pools}, Config: cfg, Cloudflare: cf, Paths: DefaultPaths(cfg)}
}

func TestRender(t *testing.T) {
	dir := t.TempDir()
	in := testInput(t, dir)
	out, err := Render(in)
	if err != nil {
		t.Fatal(err)
	}
	cfg := out.HAProxy
	for _, want := range []string{
		"backend be_web",
		"backend be_api",
		"backend be_postgres",
		"balance leastconn",
		"cookie ",
		"http-check expect rstatus ^(2[0-9][0-9]|3[0-9][0-9])$",
		"server web_2 10.0.20.22:8080",
		"backup",
		"10.0.10.10:443",
		"10.0.10.10:5432",
		"path_beg /api",
		"-m end .lab.example.com",
		"ssl_c_used",
		"Strict-Transport-Security",
		"default_backend halyard_notfound",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("haproxy.cfg is missing %q", want)
		}
	}
	ka := out.Keepalived
	for _, want := range []string{"vrrp_instance", "virtual_router_id 51", "10.0.10.10/24", "unicast_peer", "10.0.30.12", "priority 150", "chk_haproxy"} {
		if !strings.Contains(ka, want) {
			t.Errorf("keepalived.conf is missing %q", want)
		}
	}
	if strings.Contains(ka, "test-secret") {
		t.Error("keepalived.conf leaks the cluster secret")
	}

	// Validate with the real HAProxy binary when it is installed.
	bin, err := exec.LookPath("haproxy")
	if err != nil {
		t.Skip("haproxy not installed; skipping haproxy -c")
	}
	for p, c := range out.Files {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.MkdirAll(in.Paths.CertDir, 0o755)
	_ = os.MkdirAll(in.Paths.MapDir, 0o755)
	crt, key, err := engine.SelfSigned("halyard", []string{"halyard.local"})
	if err != nil {
		t.Fatal(err)
	}
	must := func(p string, b []byte) {
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	must(filepath.Join(in.Paths.CertDir, "default.pem"), append(crt, key...))
	must(in.Paths.AOPCA, crt)
	must(in.Paths.CloudflareIPs, []byte("173.245.48.0/20\n"))
	must(in.Paths.SelfIPs, []byte("127.0.0.1\n"))
	cfgPath := filepath.Join(dir, "haproxy.cfg")
	must(cfgPath, []byte(cfg))
	if b, err := exec.Command(bin, "-c", "-q", "-f", cfgPath).CombinedOutput(); err != nil {
		t.Fatalf("haproxy -c failed: %v\n%s\n---\n%s", err, b, cfg)
	}
}

func TestRenderRejectsMissingPool(t *testing.T) {
	in := testInput(t, t.TempDir())
	in.Snap.Hosts[0].Routes = []model.Route{{PathPrefix: "/", PoolID: "nope"}}
	if _, err := Render(in); err == nil {
		t.Fatal("expected an error for a route to a missing pool")
	}
}

func TestExpectToHAProxy(t *testing.T) {
	cases := map[string]string{
		"200":     "status 200",
		"200,204": "rstatus ^(200|204)$",
		"2xx":     "rstatus ^(2[0-9][0-9])$",
		"2xx,3xx": "rstatus ^(2[0-9][0-9]|3[0-9][0-9])$",
	}
	for in, want := range cases {
		got, err := ExpectToHAProxy(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"abc", "2xx;reload", "999x", "20"} {
		if _, err := ExpectToHAProxy(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestMatchWildcard(t *testing.T) {
	cases := []struct {
		pattern, name string
		ok            bool
	}{
		{"*.example.com", "a.example.com", true},
		{"*.example.com", "example.com", false},
		{"*.example.com", "a.b.example.com", false},
		{"app.example.com", "app.example.com", true},
		{"app.example.com", "APP.example.com", true},
	}
	for _, c := range cases {
		if got := MatchWildcard(c.pattern, c.name); got != c.ok {
			t.Errorf("MatchWildcard(%q, %q) = %v", c.pattern, c.name, got)
		}
	}
}

func TestVRRPPasswordIsStableAndShort(t *testing.T) {
	a, b := VRRPPassword("s", 51), VRRPPassword("s", 51)
	if a != b || len(a) > 8 || a == VRRPPassword("s", 52) {
		t.Fatalf("unexpected VRRP passwords %q %q", a, b)
	}
}
