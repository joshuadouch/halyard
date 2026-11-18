// Command halyard is a self-hosted load balancer manager for HAProxy and
// keepalived with Cloudflare integration.
package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joshuadouch/halyard/internal/api"
	"github.com/joshuadouch/halyard/internal/auth"
	"github.com/joshuadouch/halyard/internal/config"
	"github.com/joshuadouch/halyard/internal/engine"
	"github.com/joshuadouch/halyard/internal/metrics"
	"github.com/joshuadouch/halyard/internal/model"
	"github.com/joshuadouch/halyard/internal/store"
	"github.com/joshuadouch/halyard/internal/web"
	"golang.org/x/term"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func usage() {
	fmt.Fprintf(os.Stderr, `Halyard %s

Usage:
  halyard serve        [-config /etc/halyard/halyard.yaml]   run the API, UI and background jobs
  halyard render       [-config ...]                         print the HAProxy and keepalived config
  halyard user add     [-config ...] <username> [email]      create a user (prompts for password)
  halyard user passwd  [-config ...] <username>              reset a password and turn off 2FA
  halyard version
`, version)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	if cmd == "user" && len(args) > 0 {
		cmd = "user " + args[0]
		args = args[1:]
	}
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	cfgPath := fs.String("config", "/etc/halyard/halyard.yaml", "config file")
	_ = fs.Parse(args)
	var err error
	switch cmd {
	case "serve":
		err = serve(*cfgPath)
	case "render":
		err = renderCmd(*cfgPath)
	case "user add":
		err = userAdd(*cfgPath, fs.Args())
	case "user passwd":
		err = userPasswd(*cfgPath, fs.Args())
	case "version", "-v", "--version":
		fmt.Println(version)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func open(cfgPath string) (*config.Config, *store.Store, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return nil, nil, err
	}
	st, err := store.Open(cfg.Path("halyard.db"))
	return cfg, st, err
}

func serve(cfgPath string) error {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, st, err := open(cfgPath)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	eng := engine.New(cfg, st, log)
	eng.Version = version
	if err := eng.Bootstrap(ctx); err != nil {
		return err
	}
	// Re-apply on every node when this version renders differently from the
	// files on disk, so fixes in a new release take effect after an upgrade.
	if out, _, err := eng.Render(ctx); err != nil {
		log.Error("current state does not render; leaving HAProxy and keepalived config alone", "err", err)
	} else {
		cur, _ := os.ReadFile(cfg.HAProxy.ConfigPath)
		curKA, _ := os.ReadFile(cfg.Keepalived.ConfigPath)
		if string(cur) != out.HAProxy || (cfg.Keepalived.Enabled && string(curKA) != out.Keepalived) {
			if _, err := eng.Apply(ctx, "system", "Startup ("+version+")"); err != nil {
				log.Error("startup apply failed", "err", err)
			}
		}
	}
	if cfg.IsPrimary() {
		go eng.SyncPeers(ctx)
		go func() {
			if err := eng.WriteTunnelToken(ctx); err != nil {
				log.Warn("tunnel token", "err", err)
			}
		}()
	}
	go eng.Loop(ctx)

	col := &metrics.Collector{Store: st, Runtime: eng.Runtime, DataDir: cfg.DataDir, Node: cfg.Node.Name, Log: log}
	col.TunnelFn = func(ctx context.Context) metrics.Tunnel {
		cf, err := st.Cloudflare(ctx)
		if err != nil || cf.APIToken == "" || cf.TunnelID == "" {
			return metrics.Tunnel{}
		}
		t := metrics.Tunnel{Configured: true, Name: cf.TunnelName}
		cs := eng.CloudflareStatus(ctx, false)
		if cs.Tunnel == nil {
			t.Status, t.Error = "unknown", cs.Error
			return t
		}
		t.Name, t.Status, t.Connections = cs.Tunnel.Name, cs.Tunnel.Status, len(cs.Tunnel.Connections)
		seen := map[string]bool{}
		for _, c := range cs.Tunnel.Connections {
			if !seen[c.ColoName] {
				seen[c.ColoName] = true
				t.Colos = append(t.Colos, c.ColoName)
			}
		}
		return t
	}
	col.Load(ctx)
	go col.Run(ctx)

	certFile, keyFile := cfg.TLSCert, cfg.TLSKey
	if certFile == "" {
		certFile, keyFile = cfg.Path("ui-cert.pem"), cfg.Path("ui-key.pem")
		if err := engine.EnsureSelfSigned(certFile, keyFile, cfg.Node.Name); err != nil {
			return err
		}
	}
	srv := &api.Server{Cfg: cfg, Store: st, Engine: eng, Log: log, UI: web.FS(), Version: version, Metrics: col}
	hs := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12},
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	log.Info("halyard listening", "addr", cfg.Listen, "node", cfg.Node.Name, "role", cfg.Node.Role, "version", version)
	if err := hs.ListenAndServeTLS(certFile, keyFile); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func renderCmd(cfgPath string) error {
	cfg, st, err := open(cfgPath)
	if err != nil {
		return err
	}
	defer st.Close()
	eng := engine.New(cfg, st, slog.Default())
	out, _, err := eng.Render(context.Background())
	if err != nil {
		return err
	}
	fmt.Println("### haproxy.cfg")
	fmt.Print(out.HAProxy)
	fmt.Println("### keepalived.conf")
	fmt.Print(out.Keepalived)
	return nil
}

func readPassword(prompt string) (string, error) {
	if env := os.Getenv("HALYARD_PASSWORD"); env != "" {
		return env, nil
	}
	fmt.Fprint(os.Stderr, prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(line), err
}

func userAdd(cfgPath string, args []string) error {
	if len(args) < 1 {
		return errors.New("usage: halyard user add <username> [email]")
	}
	_, st, err := open(cfgPath)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	if _, err := st.UserByName(ctx, args[0]); err == nil {
		return fmt.Errorf("user %s already exists", args[0])
	}
	pw, err := readPassword("Password: ")
	if err != nil {
		return err
	}
	h, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	u := model.User{ID: store.NewID(), Username: args[0], PasswordHash: h}
	if len(args) > 1 {
		u.Email = strings.ToLower(args[1])
	}
	if err := st.SaveUser(ctx, u); err != nil {
		return err
	}
	fmt.Println("created", u.Username)
	return nil
}

func userPasswd(cfgPath string, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: halyard user passwd <username>")
	}
	_, st, err := open(cfgPath)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	u, err := st.UserByName(ctx, args[0])
	if err != nil {
		return fmt.Errorf("no user %s", args[0])
	}
	pw, err := readPassword("New password: ")
	if err != nil {
		return err
	}
	h, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	u.PasswordHash, u.TOTPEnabled, u.TOTPSecret = h, false, ""
	if err := st.SaveUser(ctx, u); err != nil {
		return err
	}
	fmt.Println("password reset for", u.Username, "(two-factor turned off)")
	return nil
}
