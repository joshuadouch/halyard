package engine

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/joshuadouch/halyard/internal/config"
	"github.com/joshuadouch/halyard/internal/model"
	"github.com/joshuadouch/halyard/internal/store"
)

func TestReceiveSyncReplacesUsers(t *testing.T) {
	if _, err := exec.LookPath("haproxy"); err != nil {
		t.Skip("haproxy not installed")
	}
	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.DryRun = true
	cfg.Node.Name = "lb-02"
	cfg.Node.Role = "standby"
	cfg.Cluster.Secret = "s3cret"
	cfg.HAProxy.ConfigPath = filepath.Join(dir, "haproxy.cfg")
	cfg.HAProxy.StateFile = ""
	cfg.Keepalived.Enabled = false
	st, err := store.Open(cfg.Path("halyard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_ = os.MkdirAll(e.Paths.MapDir, 0o750)
	_ = os.WriteFile(e.Paths.CloudflareIPs, []byte("173.245.48.0/20\n"), 0o644)
	if err := e.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// A local admin made before the first sync, with the same username.
	if err := st.SaveUser(ctx, model.User{ID: "local", Username: "admin", PasswordHash: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, "tok", "local", time.Hour); err != nil {
		t.Fatal(err)
	}

	send := func(users []SyncUser) error {
		body, _ := json.Marshal(SyncPayload{From: "lb-01", Time: time.Now().UTC(), Users: users})
		sealed, _ := Seal("s3cret", body)
		return e.ReceiveSync(ctx, sealed)
	}
	if err := send([]SyncUser{{ID: "p1", Username: "admin", PasswordHash: "hash", TOTPSecret: "S", TOTPEnabled: true}}); err != nil {
		t.Fatal(err)
	}
	u, err := st.UserByName(ctx, "admin")
	if err != nil || u.ID != "p1" || u.PasswordHash != "hash" || !u.TOTPEnabled {
		t.Fatalf("user not replaced: %+v %v", u, err)
	}
	if _, err := st.SessionUser(ctx, "tok"); err == nil {
		t.Fatal("session of a removed user survived")
	}
	// A second, unchanged sync is accepted without re-applying.
	if err := send([]SyncUser{{ID: "p1", Username: "admin", PasswordHash: "hash2"}}); err != nil {
		t.Fatal(err)
	}
	if u, _ := st.UserByName(ctx, "admin"); u.PasswordHash != "hash2" {
		t.Fatal("password change not synced")
	}
}
