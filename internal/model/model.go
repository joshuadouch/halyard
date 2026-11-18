// Package model holds the objects Halyard stores and renders into HAProxy,
// keepalived and Cloudflare configuration.
package model

import (
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
)

// Exposure decides how a host is reached and which Cloudflare objects are
// created for it.
type Exposure string

const (
	ExposureLAN    Exposure = "lan"    // optional A record to the VIP, never proxied
	ExposurePublic Exposure = "public" // proxied DNS record pointing at the public IP
	ExposureTunnel Exposure = "tunnel" // CNAME to the managed Cloudflare Tunnel
)

// Mode is the proxy mode of a host or pool.
type Mode string

const (
	ModeHTTP Mode = "http"
	ModeTCP  Mode = "tcp"
)

// VIP is an address managed by keepalived and bound by HAProxy frontends.
type VIP struct {
	ID            string `json:"id"`
	Address       string `json:"address"` // CIDR form, e.g. 10.0.10.10/24
	Label         string `json:"label"`
	Interface     string `json:"interface"`
	VRID          int    `json:"vrid"`
	PreferredNode string `json:"preferred_node"` // node name, empty = first node
	Preempt       bool   `json:"preempt"`
	TrackHAProxy  bool   `json:"track_haproxy"`
}

// IP returns the bare address without prefix length.
func (v VIP) IP() string {
	ip, _, err := net.ParseCIDR(v.Address)
	if err != nil {
		return strings.Split(v.Address, "/")[0]
	}
	return ip.String()
}

// Route sends requests matching a path to a pool. Routes are evaluated in
// order and the first match wins; an empty PathPrefix is the default route.
type Route struct {
	PathPrefix string `json:"path_prefix"`
	PoolID     string `json:"pool_id"`
	// RewritePrefix, when set on a path route, replaces the matched
	// PathPrefix before the request goes upstream: prefix /a/b with rewrite
	// /c sends /a/b/x?q to the pool as /c/x?q.
	RewritePrefix string `json:"rewrite_prefix,omitempty"`
	// HostHeader, when set, replaces the Host header sent upstream.
	HostHeader string `json:"host_header,omitempty"`
}

// Host is a set of hostnames served on one VIP.
type Host struct {
	ID            string   `json:"id"`
	Hostnames     []string `json:"hostnames"`
	VIPID         string   `json:"vip_id"`
	Mode          Mode     `json:"mode"`
	HTTPPort      int      `json:"http_port"`  // 0 disables plain HTTP (http mode)
	HTTPSPort     int      `json:"https_port"` // 0 disables TLS (http mode)
	TCPPort       int      `json:"tcp_port"`   // tcp mode only
	ForceHTTPS    bool     `json:"force_https"`
	HSTS          bool     `json:"hsts"`
	CertificateID string   `json:"certificate_id"` // empty = pick by SNI from all certs
	Routes        []Route  `json:"routes"`
	Exposure      Exposure `json:"exposure"`
	PublishDNS    bool     `json:"publish_dns"`    // create/maintain DNS records in Cloudflare
	RequireAOP    bool     `json:"require_aop"`    // require Cloudflare Authenticated Origin Pulls
	AllowCIDRs    []string `json:"allow_cidrs"`    // empty = allow all (checked against real client IP)
	AccessListID  string   `json:"access_list_id"` // optional reusable access list
	Enabled       bool     `json:"enabled"`
	Notes         string   `json:"notes"`
}

// Member is one upstream server in a pool.
type Member struct {
	Name    string `json:"name"`
	Address string `json:"address"` // host:port
	Weight  int    `json:"weight"`
	Backup  bool   `json:"backup"`
	State   string `json:"state"` // ready, drain, maint
}

// HealthCheck configures active checks for a pool.
type HealthCheck struct {
	Enabled      bool   `json:"enabled"`
	Type         string `json:"type"` // http or tcp
	Method       string `json:"method"`
	Path         string `json:"path"`
	HostHeader   string `json:"host_header"`
	ExpectStatus string `json:"expect_status"` // e.g. "200", "200,204" or "2xx,3xx"
	IntervalSec  int    `json:"interval_sec"`
	Fall         int    `json:"fall"`
	Rise         int    `json:"rise"`
}

// Pool is a group of members with a balancing algorithm.
type Pool struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Mode          Mode        `json:"mode"`
	Algorithm     string      `json:"algorithm"` // roundrobin, leastconn, source, first, random
	Sticky        string      `json:"sticky"`    // "", cookie, source
	UpstreamTLS   bool        `json:"upstream_tls"`
	VerifyTLS     bool        `json:"verify_tls"`
	SlowStartSec  int         `json:"slow_start_sec"`
	Retries       int         `json:"retries"`
	Health        HealthCheck `json:"health"`
	Members       []Member    `json:"members"`
	TimeoutServer int         `json:"timeout_server_sec"`
}

// Certificate is a PEM bundle used by HAProxy.
type Certificate struct {
	ID        string    `json:"id"`
	Domains   []string  `json:"domains"`
	Source    string    `json:"source"` // acme or upload
	KeyType   string    `json:"key_type"`
	CertPEM   string    `json:"cert_pem,omitempty"`
	KeyPEM    string    `json:"key_pem,omitempty"`
	NotAfter  time.Time `json:"not_after"`
	Issuer    string    `json:"issuer"`
	Status    string    `json:"status"` // valid, pending, failed, renewing
	LastError string    `json:"last_error"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Public returns a copy without key material for API responses.
func (c Certificate) Public() Certificate {
	c.KeyPEM = ""
	return c
}

// CloudflareSettings holds the Cloudflare integration state.
type CloudflareSettings struct {
	APIToken      string   `json:"api_token,omitempty"`
	AccountID     string   `json:"account_id"`
	TunnelID      string   `json:"tunnel_id"`
	TunnelName    string   `json:"tunnel_name"`
	PublicIP      string   `json:"public_ip"` // target for "public" exposure A records
	ACMEEmail     string   `json:"acme_email"`
	ACMEStaging   bool     `json:"acme_staging"`
	RealIP        bool     `json:"real_ip"`
	AOPZones      []string `json:"aop_zones"` // zones with Authenticated Origin Pulls enabled
	UIHostname    string   `json:"ui_hostname"`
	AccessTeam    string   `json:"access_team"` // e.g. example (for example.cloudflareaccess.com)
	AccessAUD     string   `json:"access_aud"`
	AccessAppID   string   `json:"access_app_id"`
	AccessEmails  []string `json:"access_emails"`
	LastReconcile string   `json:"last_reconcile"`
}

// Event is an entry in the activity feed.
type Event struct {
	ID      int64     `json:"id"`
	Time    time.Time `json:"time"`
	Level   string    `json:"level"` // info, warn, error
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
}

// Revision is an applied configuration.
type Revision struct {
	ID        int64     `json:"id"`
	Time      time.Time `json:"time"`
	Author    string    `json:"author"`
	Message   string    `json:"message"`
	HAProxy   string    `json:"haproxy,omitempty"`
	Keepalive string    `json:"keepalived,omitempty"`
	Snapshot  string    `json:"snapshot,omitempty"` // JSON of all objects
	Status    string    `json:"status"`             // applied, failed, rolled_back
	Error     string    `json:"error"`
}

// User is a local account. Users can also sign in via Cloudflare Access when
// their email matches.
type User struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	Email        string `json:"email"`
	PasswordHash string `json:"-"`
	TOTPSecret   string `json:"-"`
	TOTPEnabled  bool   `json:"totp_enabled"`
}

var (
	hostnameRe = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$|^[a-z0-9-]+$`)
	nameRe     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)
)

// ValidHostname reports whether h is a plausible DNS name (wildcards allowed).
func ValidHostname(h string) bool { return hostnameRe.MatchString(strings.ToLower(h)) }

// Validate checks a VIP.
func (v *VIP) Validate() error {
	ip, _, err := net.ParseCIDR(v.Address)
	if err != nil {
		return fmt.Errorf("address must be CIDR, e.g. 10.0.10.10/24")
	}
	_ = ip
	if v.Interface == "" || !nameRe.MatchString(v.Interface) {
		return fmt.Errorf("interface is required")
	}
	if v.VRID < 1 || v.VRID > 255 {
		return fmt.Errorf("VRRP ID must be 1-255")
	}
	return nil
}

// Normalize fills defaults and validates a pool.
func (p *Pool) Normalize() error {
	if !nameRe.MatchString(p.Name) {
		return fmt.Errorf("pool name must be letters, digits, dot, dash or underscore")
	}
	if p.Mode == "" {
		p.Mode = ModeHTTP
	}
	if p.Mode != ModeHTTP && p.Mode != ModeTCP {
		return fmt.Errorf("mode must be http or tcp")
	}
	switch p.Algorithm {
	case "":
		p.Algorithm = "roundrobin"
	case "roundrobin", "leastconn", "source", "first", "random":
	default:
		return fmt.Errorf("unknown algorithm %q", p.Algorithm)
	}
	switch p.Sticky {
	case "", "cookie", "source":
	default:
		return fmt.Errorf("sticky must be empty, cookie or source")
	}
	if p.Sticky == "cookie" && p.Mode == ModeTCP {
		return fmt.Errorf("cookie stickiness needs http mode")
	}
	if p.Health.IntervalSec <= 0 {
		p.Health.IntervalSec = 5
	}
	if p.Health.Fall <= 0 {
		p.Health.Fall = 3
	}
	if p.Health.Rise <= 0 {
		p.Health.Rise = 2
	}
	if p.Health.Type == "" {
		if p.Mode == ModeHTTP {
			p.Health.Type = "http"
		} else {
			p.Health.Type = "tcp"
		}
	}
	if p.Health.Type == "http" {
		if p.Health.Method == "" {
			p.Health.Method = "GET"
		}
		if p.Health.Path == "" {
			p.Health.Path = "/"
		}
		if p.Health.ExpectStatus == "" {
			p.Health.ExpectStatus = "2xx,3xx"
		}
	}
	if p.Retries == 0 {
		p.Retries = 2
	}
	seen := map[string]bool{}
	for i := range p.Members {
		m := &p.Members[i]
		if m.Name == "" {
			m.Name = fmt.Sprintf("m%d", i+1)
		}
		if !nameRe.MatchString(m.Name) {
			return fmt.Errorf("member name %q is invalid", m.Name)
		}
		if seen[m.Name] {
			return fmt.Errorf("duplicate member name %q", m.Name)
		}
		seen[m.Name] = true
		if _, _, err := net.SplitHostPort(m.Address); err != nil {
			return fmt.Errorf("member %s: address must be host:port", m.Name)
		}
		if m.Weight == 0 {
			m.Weight = 100
		}
		if m.Weight < 0 || m.Weight > 256 {
			return fmt.Errorf("member %s: weight must be 0-256", m.Name)
		}
		if m.State == "" {
			m.State = "ready"
		}
		if m.State != "ready" && m.State != "drain" && m.State != "maint" {
			return fmt.Errorf("member %s: state must be ready, drain or maint", m.Name)
		}
	}
	return nil
}

// Normalize fills defaults and validates a host.
func (h *Host) Normalize() error {
	if len(h.Hostnames) == 0 && h.Mode != ModeTCP {
		return fmt.Errorf("at least one hostname is required")
	}
	for i, n := range h.Hostnames {
		n = strings.ToLower(strings.TrimSpace(n))
		if !ValidHostname(n) {
			return fmt.Errorf("invalid hostname %q", n)
		}
		h.Hostnames[i] = n
	}
	if h.VIPID == "" {
		return fmt.Errorf("choose a VIP")
	}
	if h.Mode == "" {
		h.Mode = ModeHTTP
	}
	switch h.Exposure {
	case "":
		h.Exposure = ExposureLAN
	case ExposureLAN, ExposurePublic, ExposureTunnel:
	default:
		return fmt.Errorf("unknown exposure %q", h.Exposure)
	}
	if h.Mode == ModeTCP {
		if h.TCPPort <= 0 || h.TCPPort > 65535 {
			return fmt.Errorf("tcp hosts need a port")
		}
		if h.Exposure == ExposureTunnel || h.Exposure == ExposurePublic {
			return fmt.Errorf("tcp hosts can only be LAN exposed")
		}
		if len(h.Routes) != 1 || h.Routes[0].PathPrefix != "" {
			return fmt.Errorf("tcp hosts need exactly one default route")
		}
	} else {
		if h.HTTPPort == 0 && h.HTTPSPort == 0 {
			return fmt.Errorf("enable HTTP, HTTPS or both")
		}
		if h.ForceHTTPS && h.HTTPSPort == 0 {
			return fmt.Errorf("force HTTPS needs an HTTPS port")
		}
		if h.RequireAOP && h.HTTPSPort == 0 {
			return fmt.Errorf("authenticated origin pulls need HTTPS")
		}
	}
	if len(h.Routes) == 0 {
		return fmt.Errorf("add at least one route")
	}
	defaults := 0
	for i := range h.Routes {
		r := &h.Routes[i]
		if r.PoolID == "" {
			return fmt.Errorf("route %d has no pool", i+1)
		}
		if r.PathPrefix == "" {
			defaults++
		} else if !strings.HasPrefix(r.PathPrefix, "/") || strings.ContainsAny(r.PathPrefix, " \t\"'") {
			return fmt.Errorf("route path %q must start with / and contain no spaces or quotes", r.PathPrefix)
		}
		if r.RewritePrefix != "" {
			if r.PathPrefix == "" {
				return fmt.Errorf("the default route can't rewrite the path; add a path route instead")
			}
			if !strings.HasPrefix(r.RewritePrefix, "/") || strings.ContainsAny(r.RewritePrefix, " \t\"'\\") {
				return fmt.Errorf("rewrite path %q must start with / and contain no spaces, quotes or backslashes", r.RewritePrefix)
			}
		}
		r.HostHeader = strings.TrimSpace(r.HostHeader)
		if r.HostHeader != "" {
			if h.Mode == ModeTCP {
				return fmt.Errorf("tcp hosts can't set an upstream Host header")
			}
			name := r.HostHeader
			if hp, port, err := net.SplitHostPort(name); err == nil && port != "" {
				name = hp
			}
			if !ValidHostname(name) || strings.HasPrefix(name, "*") {
				return fmt.Errorf("upstream Host header %q is not a valid hostname", r.HostHeader)
			}
		}
	}
	if defaults > 1 {
		return fmt.Errorf("only one default route is allowed")
	}
	for _, c := range h.AllowCIDRs {
		if _, _, err := net.ParseCIDR(c); err != nil && net.ParseIP(c) == nil {
			return fmt.Errorf("invalid allow entry %q", c)
		}
	}
	return nil
}

// AccessList is a reusable set of rules a host can require: allowed client
// IPs, HTTP basic auth users, or both.
type AccessList struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	AllowCIDRs []string   `json:"allow_cidrs"`
	Users      []AuthUser `json:"users"`
	// SatisfyAny lets a client in when it passes either rule (an allowed IP
	// skips the password). Off means it must pass both.
	SatisfyAny bool   `json:"satisfy_any"`
	Notes      string `json:"notes"`
}

// AuthUser is a basic auth login. Password is only accepted on input and is
// replaced by PasswordHash (SHA-512 crypt) before storing.
type AuthUser struct {
	Username     string `json:"username"`
	Password     string `json:"password,omitempty"`
	PasswordHash string `json:"password_hash,omitempty"`
}

var authUserRe = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)

// Normalize trims and validates the list. Users must already be hashed.
func (a *AccessList) Normalize() error {
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" {
		return fmt.Errorf("name is required")
	}
	var cidrs []string
	for _, c := range a.AllowCIDRs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(c); err != nil && net.ParseIP(c) == nil {
			return fmt.Errorf("invalid allow entry %q", c)
		}
		cidrs = append(cidrs, c)
	}
	a.AllowCIDRs = cidrs
	seen := map[string]bool{}
	for i := range a.Users {
		u := &a.Users[i]
		u.Username = strings.TrimSpace(u.Username)
		if !authUserRe.MatchString(u.Username) {
			return fmt.Errorf("username %q may only use letters, digits, . _ @ and -", u.Username)
		}
		if seen[u.Username] {
			return fmt.Errorf("username %s is listed twice", u.Username)
		}
		seen[u.Username] = true
		if u.PasswordHash == "" {
			return fmt.Errorf("set a password for %s", u.Username)
		}
	}
	if len(a.AllowCIDRs) == 0 && len(a.Users) == 0 {
		return fmt.Errorf("add at least one allowed IP or user")
	}
	return nil
}
