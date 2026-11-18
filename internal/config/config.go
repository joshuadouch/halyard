// Package config loads the node-local Halyard configuration file.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Peer is another Halyard node that receives config from this one.
type Peer struct {
	Name    string `yaml:"name"`
	Address string `yaml:"address"` // IP used for unicast VRRP
	URL     string `yaml:"url"`     // management URL, e.g. https://10.0.30.12:8443
}

// Config is /etc/halyard/halyard.yaml.
type Config struct {
	// Listen is the management UI/API address. Keep it off the VIPs.
	Listen string `yaml:"listen"`
	// TLSCert/TLSKey for the management UI. Generated self-signed if empty.
	TLSCert string `yaml:"tls_cert"`
	TLSKey  string `yaml:"tls_key"`
	DataDir string `yaml:"data_dir"`

	Node struct {
		Name    string `yaml:"name"`
		Address string `yaml:"address"` // this node's own IP for unicast VRRP
		Role    string `yaml:"role"`    // primary or standby
	} `yaml:"node"`

	Cluster struct {
		Secret string `yaml:"secret"` // shared secret for config sync and VRRP auth
		Peers  []Peer `yaml:"peers"`
	} `yaml:"cluster"`

	HAProxy struct {
		Binary      string `yaml:"binary"`
		ConfigPath  string `yaml:"config_path"`
		Socket      string `yaml:"socket"`
		ReloadCmd   string `yaml:"reload_cmd"`
		StateFile   string `yaml:"state_file"`
		StatsListen string `yaml:"stats_listen"`
	} `yaml:"haproxy"`

	Keepalived struct {
		Enabled    bool   `yaml:"enabled"`
		ConfigPath string `yaml:"config_path"`
		ReloadCmd  string `yaml:"reload_cmd"`
	} `yaml:"keepalived"`

	Cloudflared struct {
		Enabled   bool   `yaml:"enabled"`
		EnvFile   string `yaml:"env_file"`
		ReloadCmd string `yaml:"reload_cmd"`
	} `yaml:"cloudflared"`

	// DryRun renders and validates but never reloads services. Useful for
	// development and tests.
	DryRun bool `yaml:"dry_run"`
}

// Default returns a config with sensible defaults for a Debian install.
func Default() *Config {
	c := &Config{Listen: ":8443", DataDir: "/var/lib/halyard"}
	host, _ := os.Hostname()
	c.Node.Name = host
	c.Node.Role = "primary"
	c.HAProxy.Binary = "/usr/sbin/haproxy"
	c.HAProxy.ConfigPath = "/etc/haproxy/haproxy.cfg"
	c.HAProxy.Socket = "/run/haproxy/admin.sock"
	c.HAProxy.ReloadCmd = "systemctl reload haproxy"
	c.HAProxy.StateFile = "/var/lib/haproxy/server-state"
	c.HAProxy.StatsListen = "127.0.0.1:8404"
	c.Keepalived.Enabled = true
	c.Keepalived.ConfigPath = "/etc/keepalived/keepalived.conf"
	c.Keepalived.ReloadCmd = "systemctl reload-or-restart keepalived"
	c.Cloudflared.Enabled = true
	c.Cloudflared.EnvFile = "/etc/halyard/cloudflared.env"
	c.Cloudflared.ReloadCmd = "systemctl restart cloudflared-halyard"
	return c
}

// Load reads path over the defaults. A missing file yields defaults.
func Load(path string) (*Config, error) {
	c := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, c.check()
		}
		return nil, err
	}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, c.check()
}

func (c *Config) check() error {
	if c.Node.Name == "" {
		return fmt.Errorf("node.name is required")
	}
	if c.Node.Role != "primary" && c.Node.Role != "standby" {
		return fmt.Errorf("node.role must be primary or standby")
	}
	if len(c.Cluster.Peers) > 0 && c.Cluster.Secret == "" {
		return fmt.Errorf("cluster.secret is required when peers are configured")
	}
	return nil
}

// Path joins a path under the data directory.
func (c *Config) Path(parts ...string) string {
	return filepath.Join(append([]string{c.DataDir}, parts...)...)
}

// IsPrimary reports whether this node accepts edits.
func (c *Config) IsPrimary() bool { return c.Node.Role == "primary" }
