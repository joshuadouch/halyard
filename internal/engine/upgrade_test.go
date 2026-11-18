package engine

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joshuadouch/halyard/internal/config"
	"github.com/joshuadouch/halyard/internal/store"
)

func TestRollingUpgrade(t *testing.T) {
	var mu sync.Mutex
	peerVersion := "v0.1.4"
	var got upgradeCommand
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/health":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "version": peerVersion})
		case "/api/cluster/upgrade":
			b, _ := io.ReadAll(r.Body)
			plain, err := Open("s", b)
			if err != nil {
				http.Error(w, "bad seal", 400)
				return
			}
			_ = json.Unmarshal(plain, &got)
			peerVersion = got.Target
		case "/api/cluster/sync":
		default:
			http.NotFound(w, r)
		}
	}))
	defer peer.Close()

	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.DryRun = true
	cfg.Node.Name = "lb-01"
	cfg.Cluster.Secret = "s"
	cfg.Cluster.Peers = []config.Peer{{Name: "lb-02", URL: peer.URL}}
	st, err := store.Open(filepath.Join(dir, "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.Version = "v0.1.4"

	if err := e.StartUpgrade("v0.1.9; rm -rf /", "josh"); err == nil {
		t.Fatal("accepted an invalid version")
	}
	if err := e.StartUpgrade("v0.1.5", "josh"); err != nil {
		t.Fatal(err)
	}
	if err := e.StartUpgrade("v0.1.5", "josh"); err == nil {
		t.Fatal("started a second upgrade while one is running")
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		e.upgradeMu.Lock()
		running := e.upgradeJob.Running
		e.upgradeMu.Unlock()
		if !running {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	e.upgradeMu.Lock()
	job := e.upgradeJob
	e.upgradeMu.Unlock()
	if job.Running || job.Error != "" {
		t.Fatalf("job did not finish cleanly: %+v", job)
	}
	if got.Target != "v0.1.5" || got.From != "lb-01" {
		t.Fatalf("peer got %+v", got)
	}
	log := strings.Join(job.Log, "\n")
	for _, want := range []string{"Upgrading lb-02 from v0.1.4 to v0.1.5", "lb-02 is healthy on v0.1.5", "Upgrading lb-01"} {
		if !strings.Contains(log, want) {
			t.Errorf("log is missing %q:\n%s", want, log)
		}
	}
	if strings.Index(log, "lb-02 is healthy") > strings.Index(log, "Upgrading lb-01") {
		t.Error("this node was upgraded before the peer was healthy")
	}
}

func TestReceiveUpgradeRejectsForgery(t *testing.T) {
	cfg := config.Default()
	cfg.DryRun = true
	cfg.Cluster.Secret = "s"
	e := &Engine{Cfg: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Version: "v0.1.4"}
	b, _ := json.Marshal(upgradeCommand{From: "x", Time: time.Now(), Target: "v0.1.5"})
	sealed, _ := Seal("other-secret", b)
	if err := e.ReceiveUpgrade(sealed); err == nil {
		t.Fatal("accepted a command sealed with the wrong secret")
	}
	old, _ := json.Marshal(upgradeCommand{From: "x", Time: time.Now().Add(-time.Hour), Target: "v0.1.5"})
	sealed, _ = Seal("s", old)
	if err := e.ReceiveUpgrade(sealed); err == nil {
		t.Fatal("accepted a replayed command")
	}
}
