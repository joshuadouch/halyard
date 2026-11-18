package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/joshuadouch/halyard/internal/config"
)

func TestUIOrigin(t *testing.T) {
	cases := []struct {
		name, listen, addr string
		peers              bool
		want               string
	}{
		{"single node", ":8443", "10.0.30.11", false, "https://127.0.0.1:8443"},
		{"cluster uses node address", ":8443", "10.0.30.11", true, "https://10.0.30.11:8443"},
		{"cluster wildcard listen", "0.0.0.0:9000", "10.0.30.11", true, "https://10.0.30.11:9000"},
		{"cluster specific listen", "10.0.40.5:8443", "10.0.30.11", true, "https://10.0.40.5:8443"},
		{"cluster loopback listen", "127.0.0.1:8443", "10.0.30.11", true, "https://127.0.0.1:8443"},
		{"cluster without address", ":8443", "", true, "https://127.0.0.1:8443"},
		{"ipv6 address", "[::]:8443", "fd00::11", true, "https://[fd00::11]:8443"},
	}
	for _, c := range cases {
		cfg := config.Default()
		cfg.Listen, cfg.Node.Address = c.listen, c.addr
		if c.peers {
			cfg.Cluster.Peers = []config.Peer{{Name: "b", Address: "10.0.30.12", URL: "https://10.0.30.12:8443"}}
		}
		e := &Engine{Cfg: cfg}
		if got := e.UIOrigin(); got != c.want {
			t.Errorf("%s: UIOrigin() = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestStandbyDoesNotReconcileCloudflare(t *testing.T) {
	cfg := config.Default()
	cfg.Node.Role = "standby"
	e := &Engine{Cfg: cfg}
	if _, err := e.ReconcileCloudflare(context.Background(), false); !errors.Is(err, ErrStandby) {
		t.Fatalf("err = %v, want ErrStandby", err)
	}
}
