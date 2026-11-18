// Package cloudflare is a small client for the parts of the Cloudflare API
// Halyard uses: DNS records, Tunnels, Access apps, zone settings and IP ranges.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// BaseURL is the API root; overridden in tests.
var BaseURL = "https://api.cloudflare.com/client/v4"

// OriginPullCAURL is Cloudflare's shared Authenticated Origin Pulls CA.
var OriginPullCAURL = "https://developers.cloudflare.com/ssl/static/authenticated_origin_pull_ca.pem"

// ManagedComment marks DNS records Halyard owns.
const ManagedComment = "managed by halyard"

// Client calls the Cloudflare API with a token.
type Client struct {
	Token string
	HTTP  *http.Client
}

// New returns a client.
func New(token string) *Client {
	return &Client{Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

type envelope struct {
	Success    bool            `json:"success"`
	Errors     []apiError      `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo *struct {
		Page       int `json:"page"`
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) (*envelope, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, BaseURL+path, rd)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("cloudflare %s %s: HTTP %d", method, path, resp.StatusCode)
	}
	if !env.Success {
		var msgs []string
		for _, e := range env.Errors {
			msgs = append(msgs, fmt.Sprintf("%d %s", e.Code, e.Message))
		}
		return nil, fmt.Errorf("cloudflare %s %s: %s", method, path, strings.Join(msgs, "; "))
	}
	if out != nil && len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return nil, err
		}
	}
	return &env, nil
}

// VerifyToken checks the token is active. Account-owned tokens are verified
// against the account endpoint when accountID is given.
func (c *Client) VerifyToken(ctx context.Context, accountID string) error {
	var r struct {
		Status string `json:"status"`
	}
	_, err := c.do(ctx, "GET", "/user/tokens/verify", nil, &r)
	if err != nil && accountID != "" {
		_, err = c.do(ctx, "GET", "/accounts/"+accountID+"/tokens/verify", nil, &r)
	}
	if err != nil {
		return err
	}
	if r.Status != "active" {
		return fmt.Errorf("token status is %q", r.Status)
	}
	return nil
}

// Zone is a Cloudflare zone.
type Zone struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Account struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"account"`
}

// Zones lists the zones the token can see.
func (c *Client) Zones(ctx context.Context) ([]Zone, error) {
	var all []Zone
	for page := 1; ; page++ {
		var zs []Zone
		env, err := c.do(ctx, "GET", fmt.Sprintf("/zones?per_page=50&page=%d", page), nil, &zs)
		if err != nil {
			return nil, err
		}
		all = append(all, zs...)
		if env.ResultInfo == nil || page >= env.ResultInfo.TotalPages {
			break
		}
	}
	return all, nil
}

// ZoneFor returns the zone with the longest suffix match for host.
func ZoneFor(zones []Zone, host string) (Zone, bool) {
	host = strings.TrimPrefix(strings.ToLower(host), "*.")
	sort.Slice(zones, func(i, j int) bool { return len(zones[i].Name) > len(zones[j].Name) })
	for _, z := range zones {
		if host == z.Name || strings.HasSuffix(host, "."+z.Name) {
			return z, true
		}
	}
	return Zone{}, false
}

// DNSRecord is a Cloudflare DNS record.
type DNSRecord struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	TTL     int    `json:"ttl"`
	Comment string `json:"comment"`
}

// DNSRecords lists all records in a zone.
func (c *Client) DNSRecords(ctx context.Context, zoneID string) ([]DNSRecord, error) {
	var all []DNSRecord
	for page := 1; ; page++ {
		var rs []DNSRecord
		env, err := c.do(ctx, "GET", fmt.Sprintf("/zones/%s/dns_records?per_page=500&page=%d", url.PathEscape(zoneID), page), nil, &rs)
		if err != nil {
			return nil, err
		}
		all = append(all, rs...)
		if env.ResultInfo == nil || page >= env.ResultInfo.TotalPages {
			break
		}
	}
	return all, nil
}

// CreateDNS creates a record.
func (c *Client) CreateDNS(ctx context.Context, zoneID string, r DNSRecord) error {
	_, err := c.do(ctx, "POST", "/zones/"+url.PathEscape(zoneID)+"/dns_records", r, nil)
	return err
}

// UpdateDNS overwrites a record.
func (c *Client) UpdateDNS(ctx context.Context, zoneID string, r DNSRecord) error {
	_, err := c.do(ctx, "PUT", "/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(r.ID), r, nil)
	return err
}

// DeleteDNS deletes a record.
func (c *Client) DeleteDNS(ctx context.Context, zoneID, id string) error {
	_, err := c.do(ctx, "DELETE", "/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(id), nil, nil)
	return err
}

// Tunnel is a Cloudflare Tunnel.
type Tunnel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	ConfigSrc   string `json:"config_src,omitempty"`
	Connections []struct {
		ColoName           string `json:"colo_name"`
		OriginIP           string `json:"origin_ip"`
		ClientVersion      string `json:"client_version"`
		IsPendingReconnect bool   `json:"is_pending_reconnect"`
	} `json:"connections"`
}

// Tunnels lists non-deleted tunnels in an account.
func (c *Client) Tunnels(ctx context.Context, accountID string) ([]Tunnel, error) {
	var ts []Tunnel
	_, err := c.do(ctx, "GET", "/accounts/"+url.PathEscape(accountID)+"/cfd_tunnel?is_deleted=false&per_page=100", nil, &ts)
	return ts, err
}

// Tunnel fetches one tunnel.
func (c *Client) Tunnel(ctx context.Context, accountID, id string) (Tunnel, error) {
	var t Tunnel
	_, err := c.do(ctx, "GET", "/accounts/"+url.PathEscape(accountID)+"/cfd_tunnel/"+url.PathEscape(id), nil, &t)
	return t, err
}

// CreateTunnel creates a remotely-managed tunnel.
func (c *Client) CreateTunnel(ctx context.Context, accountID, name string) (Tunnel, error) {
	var t Tunnel
	_, err := c.do(ctx, "POST", "/accounts/"+url.PathEscape(accountID)+"/cfd_tunnel",
		map[string]any{"name": name, "config_src": "cloudflare"}, &t)
	return t, err
}

// TunnelToken returns the connector token for a tunnel.
func (c *Client) TunnelToken(ctx context.Context, accountID, id string) (string, error) {
	var tok string
	_, err := c.do(ctx, "GET", "/accounts/"+url.PathEscape(accountID)+"/cfd_tunnel/"+url.PathEscape(id)+"/token", nil, &tok)
	return tok, err
}

// OriginRequest holds per-rule origin settings.
type OriginRequest struct {
	OriginServerName string `json:"originServerName,omitempty"`
	NoTLSVerify      bool   `json:"noTLSVerify,omitempty"`
	HTTPHostHeader   string `json:"httpHostHeader,omitempty"`
}

// IngressRule is one tunnel ingress rule.
type IngressRule struct {
	Hostname      string         `json:"hostname,omitempty"`
	Service       string         `json:"service"`
	OriginRequest *OriginRequest `json:"originRequest,omitempty"`
}

// TunnelConfig fetches the current ingress rules.
func (c *Client) TunnelConfig(ctx context.Context, accountID, id string) ([]IngressRule, error) {
	var r struct {
		Config struct {
			Ingress []IngressRule `json:"ingress"`
		} `json:"config"`
	}
	_, err := c.do(ctx, "GET", "/accounts/"+url.PathEscape(accountID)+"/cfd_tunnel/"+url.PathEscape(id)+"/configurations", nil, &r)
	return r.Config.Ingress, err
}

// PutTunnelConfig replaces the ingress rules.
func (c *Client) PutTunnelConfig(ctx context.Context, accountID, id string, rules []IngressRule) error {
	_, err := c.do(ctx, "PUT", "/accounts/"+url.PathEscape(accountID)+"/cfd_tunnel/"+url.PathEscape(id)+"/configurations",
		map[string]any{"config": map[string]any{"ingress": rules}}, nil)
	return err
}

// IPRanges are Cloudflare's edge ranges.
type IPRanges struct {
	IPv4 []string `json:"ipv4_cidrs"`
	IPv6 []string `json:"ipv6_cidrs"`
}

// FetchIPRanges gets Cloudflare's published ranges (no token needed).
func FetchIPRanges(ctx context.Context) (IPRanges, error) {
	var r IPRanges
	_, err := (&Client{HTTP: &http.Client{Timeout: 20 * time.Second}}).do(ctx, "GET", "/ips", nil, &r)
	if err == nil && len(r.IPv4) == 0 {
		err = fmt.Errorf("cloudflare returned no IP ranges")
	}
	return r, err
}

// FetchOriginPullCA downloads the shared origin-pull CA certificate.
func FetchOriginPullCA(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", OriginPullCAURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("origin pull CA: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if !bytes.Contains(b, []byte("BEGIN CERTIFICATE")) {
		return nil, fmt.Errorf("origin pull CA: response is not a PEM certificate")
	}
	return b, nil
}

// SetAOP turns zone-level Authenticated Origin Pulls on or off.
func (c *Client) SetAOP(ctx context.Context, zoneID string, on bool) error {
	v := "off"
	if on {
		v = "on"
	}
	_, err := c.do(ctx, "PATCH", "/zones/"+url.PathEscape(zoneID)+"/settings/tls_client_auth", map[string]string{"value": v}, nil)
	return err
}

// AOPEnabled reports the zone-level setting.
func (c *Client) AOPEnabled(ctx context.Context, zoneID string) (bool, error) {
	var r struct {
		Value string `json:"value"`
	}
	_, err := c.do(ctx, "GET", "/zones/"+url.PathEscape(zoneID)+"/settings/tls_client_auth", nil, &r)
	return r.Value == "on", err
}

// AccessApp is a Cloudflare Access application.
type AccessApp struct {
	ID     string `json:"id"`
	AUD    string `json:"aud"`
	Name   string `json:"name"`
	Domain string `json:"domain"`
}

// CreateAccessApp creates a self-hosted Access app and an allow policy for emails.
func (c *Client) CreateAccessApp(ctx context.Context, accountID, name, domain string, emails []string) (AccessApp, error) {
	var app AccessApp
	_, err := c.do(ctx, "POST", "/accounts/"+url.PathEscape(accountID)+"/access/apps", map[string]any{
		"name": name, "domain": domain, "type": "self_hosted", "session_duration": "24h", "app_launcher_visible": false,
	}, &app)
	if err != nil {
		return app, err
	}
	if len(emails) > 0 {
		var include []map[string]any
		for _, e := range emails {
			include = append(include, map[string]any{"email": map[string]string{"email": e}})
		}
		_, err = c.do(ctx, "POST", "/accounts/"+url.PathEscape(accountID)+"/access/apps/"+url.PathEscape(app.ID)+"/policies", map[string]any{
			"name": "Halyard admins", "decision": "allow", "include": include, "precedence": 1,
		}, nil)
	}
	return app, err
}
