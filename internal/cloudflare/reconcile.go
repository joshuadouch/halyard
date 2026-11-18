package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/joshuadouch/halyard/internal/model"
)

// Desired describes the Cloudflare objects Halyard wants to exist.
type Desired struct {
	Records []DNSRecord
	Ingress []IngressRule
	Notes   []string // things that could not be planned, shown to the user
}

// PlanInput is what Plan needs.
type PlanInput struct {
	Hosts    []model.Host
	VIPs     []model.VIP
	Settings model.CloudflareSettings
	// UIOrigin is where tunnel traffic for the UI hostname goes, e.g. https://127.0.0.1:8443.
	UIOrigin string
	// HasCert reports whether a trusted cert covers the hostname.
	HasCert func(string) bool
}

// Plan computes desired DNS records and tunnel ingress rules.
func Plan(in PlanInput) Desired {
	var d Desired
	vips := map[string]model.VIP{}
	for _, v := range in.VIPs {
		vips[v.ID] = v
	}
	tunnelTarget := ""
	if in.Settings.TunnelID != "" {
		tunnelTarget = in.Settings.TunnelID + ".cfargotunnel.com"
	}
	seen := map[string]bool{}
	addRec := func(r DNSRecord) {
		if seen[r.Name] {
			d.Notes = append(d.Notes, fmt.Sprintf("%s is claimed by more than one host; kept the first", r.Name))
			return
		}
		seen[r.Name] = true
		r.TTL = 1
		r.Comment = ManagedComment
		d.Records = append(d.Records, r)
	}
	for _, h := range in.Hosts {
		if !h.Enabled {
			continue
		}
		v, ok := vips[h.VIPID]
		if !ok {
			continue
		}
		for _, n := range h.Hostnames {
			switch h.Exposure {
			case model.ExposureLAN:
				if !h.PublishDNS {
					continue
				}
				typ := "A"
				if strings.Contains(v.IP(), ":") {
					typ = "AAAA"
				}
				addRec(DNSRecord{Type: typ, Name: n, Content: v.IP(), Proxied: false})
			case model.ExposurePublic:
				if !h.PublishDNS {
					continue
				}
				// A public VIP is reachable as is. A private VIP sits behind NAT,
				// so the record points at the WAN address from settings instead.
				target := v.IP()
				if ip := net.ParseIP(target); ip == nil || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
					if in.Settings.PublicIP == "" {
						d.Notes = append(d.Notes, fmt.Sprintf("%s: VIP %s is a private address, so set the WAN address (NAT) in Cloudflare settings to publish it", n, target))
						continue
					}
					target = in.Settings.PublicIP
				}
				typ := "A"
				if ip := net.ParseIP(target); ip != nil && ip.To4() == nil {
					typ = "AAAA"
				}
				addRec(DNSRecord{Type: typ, Name: n, Content: target, Proxied: true})
			case model.ExposureTunnel:
				if tunnelTarget == "" {
					d.Notes = append(d.Notes, fmt.Sprintf("%s: create a tunnel first", n))
					continue
				}
				if h.PublishDNS {
					addRec(DNSRecord{Type: "CNAME", Name: n, Content: tunnelTarget, Proxied: true})
				}
				rule := IngressRule{Hostname: n}
				if h.HTTPSPort != 0 {
					rule.Service = fmt.Sprintf("https://%s", hostPort(v.IP(), h.HTTPSPort))
					or := &OriginRequest{}
					if strings.HasPrefix(n, "*.") || in.HasCert == nil || !in.HasCert(n) {
						or.NoTLSVerify = true
					} else {
						or.OriginServerName = n
					}
					rule.OriginRequest = or
				} else {
					rule.Service = fmt.Sprintf("http://%s", hostPort(v.IP(), h.HTTPPort))
				}
				d.Ingress = append(d.Ingress, rule)
			}
		}
	}
	if ui := in.Settings.UIHostname; ui != "" && tunnelTarget != "" && in.UIOrigin != "" {
		addRec(DNSRecord{Type: "CNAME", Name: ui, Content: tunnelTarget, Proxied: true})
		d.Ingress = append(d.Ingress, IngressRule{Hostname: ui, Service: in.UIOrigin, OriginRequest: &OriginRequest{NoTLSVerify: true}})
	}
	// cloudflared matches rules in order: exact names before wildcards.
	sort.SliceStable(d.Ingress, func(i, j int) bool {
		return !strings.HasPrefix(d.Ingress[i].Hostname, "*.") && strings.HasPrefix(d.Ingress[j].Hostname, "*.")
	})
	if len(d.Ingress) > 0 {
		d.Ingress = append(d.Ingress, IngressRule{Service: "http_status:404"})
	}
	return d
}

func hostPort(ip string, port int) string {
	return net.JoinHostPort(ip, fmt.Sprint(port))
}

// Change is one action taken (or that would be taken) during reconcile.
type Change struct {
	Action string `json:"action"` // create, update, delete, conflict, ok
	Zone   string `json:"zone"`
	Record string `json:"record"`
	Detail string `json:"detail"`
}

// RecordState is a managed record and whether it matches what Halyard wants.
type RecordState struct {
	Zone    string     `json:"zone"`
	Desired DNSRecord  `json:"desired"`
	Actual  *DNSRecord `json:"actual"`
	Status  string     `json:"status"` // in_sync, drift, missing, conflict
}

// DNSClient is the subset of Client used by ReconcileDNS (for tests).
type DNSClient interface {
	Zones(ctx context.Context) ([]Zone, error)
	DNSRecords(ctx context.Context, zoneID string) ([]DNSRecord, error)
	CreateDNS(ctx context.Context, zoneID string, r DNSRecord) error
	UpdateDNS(ctx context.Context, zoneID string, r DNSRecord) error
	DeleteDNS(ctx context.Context, zoneID, id string) error
}

// ReconcileDNS makes managed records match desired. With dryRun it only
// reports. Records Halyard did not create are never modified.
func ReconcileDNS(ctx context.Context, c DNSClient, desired []DNSRecord, dryRun bool) ([]Change, []RecordState, error) {
	zones, err := c.Zones(ctx)
	if err != nil {
		return nil, nil, err
	}
	byZone := map[string][]DNSRecord{}
	zoneByID := map[string]Zone{}
	var changes []Change
	for _, r := range desired {
		z, ok := ZoneFor(zones, r.Name)
		if !ok {
			changes = append(changes, Change{Action: "conflict", Record: r.Name, Detail: "no Cloudflare zone matches this name"})
			continue
		}
		byZone[z.ID] = append(byZone[z.ID], r)
		zoneByID[z.ID] = z
	}
	// Zones that only have stale managed records still need a pass.
	for _, z := range zones {
		zoneByID[z.ID] = z
	}
	var states []RecordState
	// One bad zone or record must not stop the others; collect failures.
	var errs []string
	fail := func(ch Change) {
		changes = append(changes, ch)
		errs = append(errs, ch.Record+": "+ch.Detail)
	}
	ids := make([]string, 0, len(zoneByID))
	for id := range zoneByID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, zid := range ids {
		z := zoneByID[zid]
		want := byZone[zid]
		existing, err := c.DNSRecords(ctx, zid)
		if err != nil {
			if len(want) == 0 {
				// Likely a zone the token can see but not edit; nothing to do there.
				continue
			}
			fail(Change{Action: "error", Zone: z.Name, Record: z.Name, Detail: "could not list DNS records: " + err.Error()})
			for _, r := range want {
				states = append(states, RecordState{Zone: z.Name, Desired: r, Status: "error"})
			}
			continue
		}
		byName := map[string][]DNSRecord{}
		for _, e := range existing {
			if e.Type == "A" || e.Type == "AAAA" || e.Type == "CNAME" {
				byName[strings.ToLower(e.Name)] = append(byName[strings.ToLower(e.Name)], e)
			}
		}
		wanted := map[string]bool{}
		for _, r := range want {
			name := strings.ToLower(r.Name)
			wanted[name] = true
			cur := byName[name]
			st := RecordState{Zone: z.Name, Desired: r}
			var managed *DNSRecord
			unmanaged := false
			for i := range cur {
				if cur[i].Comment == ManagedComment {
					managed = &cur[i]
				} else {
					unmanaged = true
				}
			}
			switch {
			case unmanaged:
				st.Status = "conflict"
				if len(cur) > 0 {
					st.Actual = &cur[0]
				}
				changes = append(changes, Change{Action: "conflict", Zone: z.Name, Record: r.Name, Detail: "a record not created by Halyard already exists; left untouched"})
			case managed == nil:
				st.Status = "missing"
				changes = append(changes, Change{Action: "create", Zone: z.Name, Record: r.Name, Detail: r.Type + " " + r.Content})
				if !dryRun {
					if err := c.CreateDNS(ctx, zid, r); err != nil {
						fail(Change{Action: "error", Zone: z.Name, Record: r.Name, Detail: "create failed: " + err.Error()})
						st.Status = "error"
						states = append(states, st)
						continue
					}
					st.Status = "in_sync"
					cp := r
					st.Actual = &cp
				}
			case managed.Type != r.Type || managed.Content != r.Content || managed.Proxied != r.Proxied:
				st.Status = "drift"
				st.Actual = managed
				changes = append(changes, Change{Action: "update", Zone: z.Name, Record: r.Name,
					Detail: fmt.Sprintf("%s %s -> %s %s", managed.Type, managed.Content, r.Type, r.Content)})
				if !dryRun {
					r.ID = managed.ID
					if err := c.UpdateDNS(ctx, zid, r); err != nil {
						fail(Change{Action: "error", Zone: z.Name, Record: r.Name, Detail: "update failed: " + err.Error()})
						st.Status = "error"
						states = append(states, st)
						continue
					}
					st.Status = "in_sync"
					cp := r
					st.Actual = &cp
				}
			default:
				st.Status = "in_sync"
				st.Actual = managed
			}
			states = append(states, st)
		}
		for name, recs := range byName {
			if wanted[name] {
				continue
			}
			for _, e := range recs {
				if e.Comment != ManagedComment {
					continue
				}
				changes = append(changes, Change{Action: "delete", Zone: z.Name, Record: e.Name, Detail: e.Type + " " + e.Content})
				if !dryRun {
					if err := c.DeleteDNS(ctx, zid, e.ID); err != nil {
						fail(Change{Action: "error", Zone: z.Name, Record: e.Name, Detail: "delete failed: " + err.Error()})
						continue
					}
				}
			}
		}
	}
	if len(errs) > 0 {
		return changes, states, errors.New(strings.Join(errs, "; "))
	}
	return changes, states, nil
}
