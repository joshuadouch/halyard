package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshuadouch/halyard/internal/config"
	"github.com/joshuadouch/halyard/internal/engine"
	"github.com/joshuadouch/halyard/internal/metrics"
	"github.com/joshuadouch/halyard/internal/model"
	"github.com/joshuadouch/halyard/internal/store"
)

func TestPrometheusEndpoint(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.Node.Name = "lb-test"
	cfg.HAProxy.Socket = filepath.Join(dir, "missing.sock")
	st, err := store.Open(filepath.Join(dir, "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	_ = st.Put(ctx, store.KindVIP, "v1", model.VIP{ID: "v1", Address: "10.9.9.9/24", Label: "lan \"main\"", Interface: "eth1"})
	_ = st.Put(ctx, store.KindPool, "p1", model.Pool{ID: "p1", Name: "web", Members: []model.Member{{Name: "a", Address: "10.0.0.1:80"}}})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := engine.New(cfg, st, log)
	col := &metrics.Collector{Store: st, Runtime: eng.Runtime, DataDir: dir, Node: "lb-test"}
	col.Sample(ctx)
	srv := &Server{Cfg: cfg, Store: st, Engine: eng, Log: log, Version: "test", Metrics: col}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	get := func(path, tok string) (int, string) {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, _ := get("/metrics", ""); code != 401 {
		t.Fatalf("/metrics without a token: %d", code)
	}
	if code, _ := get("/api/monitoring", ""); code != 401 {
		t.Fatalf("/api/monitoring without a session: %d", code)
	}
	tok, err := srv.metricsTokenValue(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := get("/metrics", "wrong"); code != 401 {
		t.Fatalf("/metrics with a wrong token: %d", code)
	}
	code, body := get("/metrics", tok)
	if code != 200 {
		t.Fatalf("/metrics: %d %s", code, body)
	}
	for _, want := range []string{
		`halyard_build_info{version="test",node="lb-test",role="primary"} 1`,
		"halyard_haproxy_up 0",
		`halyard_vip_held{vip="10.9.9.9",label="lan \"main\"",interface="eth1"} 0`,
		`halyard_pool_members{pool="web"} 1`,
		"# TYPE halyard_host_memory_bytes gauge",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
	// Every family is declared once and its samples are contiguous.
	seen := map[string]bool{}
	last := ""
	for _, l := range strings.Split(strings.TrimSpace(body), "\n") {
		if strings.HasPrefix(l, "#") {
			continue
		}
		name := strings.FieldsFunc(l, func(r rune) bool { return r == '{' || r == ' ' })[0]
		if name != last {
			if seen[name] {
				t.Errorf("samples of %s are not grouped", name)
			}
			seen[name] = true
			last = name
		}
	}
	// Rotating changes the token.
	tok2, _ := srv.metricsTokenValue(ctx, true)
	if tok2 == tok {
		t.Fatal("token did not rotate")
	}
	if code, _ := get("/metrics", tok); code != 401 {
		t.Fatal("old token still works")
	}
}
