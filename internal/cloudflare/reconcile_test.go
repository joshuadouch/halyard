package cloudflare

import (
	"context"
	"testing"

	"github.com/joshuadouch/halyard/internal/model"
)

func TestPlan(t *testing.T) {
	vips := []model.VIP{{ID: "v1", Address: "10.0.10.10/24"}}
	hosts := []model.Host{
		{Hostnames: []string{"nas.example.com"}, VIPID: "v1", HTTPPort: 80, Exposure: model.ExposureLAN, PublishDNS: true, Enabled: true},
		{Hostnames: []string{"www.example.com"}, VIPID: "v1", HTTPSPort: 443, Exposure: model.ExposurePublic, PublishDNS: true, Enabled: true},
		{Hostnames: []string{"*.apps.example.com"}, VIPID: "v1", HTTPSPort: 443, Exposure: model.ExposureTunnel, PublishDNS: true, Enabled: true},
		{Hostnames: []string{"grafana.example.com"}, VIPID: "v1", HTTPSPort: 443, Exposure: model.ExposureTunnel, PublishDNS: true, Enabled: true},
		{Hostnames: []string{"off.example.com"}, VIPID: "v1", HTTPPort: 80, Exposure: model.ExposureLAN, PublishDNS: true},
	}
	s := model.CloudflareSettings{TunnelID: "abc", PublicIP: "203.0.113.5", UIHostname: "lb.example.com"}
	d := Plan(PlanInput{Hosts: hosts, VIPs: vips, Settings: s, UIOrigin: "https://127.0.0.1:8443",
		HasCert: func(n string) bool { return n == "grafana.example.com" }})

	want := map[string]DNSRecord{
		"nas.example.com":     {Type: "A", Content: "10.0.10.10", Proxied: false},
		"www.example.com":     {Type: "A", Content: "203.0.113.5", Proxied: true},
		"*.apps.example.com":  {Type: "CNAME", Content: "abc.cfargotunnel.com", Proxied: true},
		"grafana.example.com": {Type: "CNAME", Content: "abc.cfargotunnel.com", Proxied: true},
		"lb.example.com":      {Type: "CNAME", Content: "abc.cfargotunnel.com", Proxied: true},
	}
	if len(d.Records) != len(want) {
		t.Fatalf("got %d records, want %d: %+v", len(d.Records), len(want), d.Records)
	}
	for _, r := range d.Records {
		w, ok := want[r.Name]
		if !ok || r.Type != w.Type || r.Content != w.Content || r.Proxied != w.Proxied || r.Comment != ManagedComment {
			t.Errorf("unexpected record %+v", r)
		}
	}
	// Exact names first, wildcard after, catch-all last.
	if n := len(d.Ingress); n != 4 {
		t.Fatalf("got %d ingress rules: %+v", n, d.Ingress)
	}
	if d.Ingress[0].Hostname != "grafana.example.com" || d.Ingress[0].OriginRequest.OriginServerName != "grafana.example.com" {
		t.Errorf("rule 0 = %+v", d.Ingress[0])
	}
	if d.Ingress[1].Hostname != "lb.example.com" || d.Ingress[1].Service != "https://127.0.0.1:8443" {
		t.Errorf("rule 1 = %+v", d.Ingress[1])
	}
	if d.Ingress[2].Hostname != "*.apps.example.com" || !d.Ingress[2].OriginRequest.NoTLSVerify {
		t.Errorf("rule 2 = %+v", d.Ingress[2])
	}
	if d.Ingress[3].Service != "http_status:404" || d.Ingress[3].Hostname != "" {
		t.Errorf("last rule = %+v", d.Ingress[3])
	}
}

type fakeDNS struct {
	zones   []Zone
	records map[string][]DNSRecord
	created []DNSRecord
	updated []DNSRecord
	deleted []string
}

func (f *fakeDNS) Zones(context.Context) ([]Zone, error) { return f.zones, nil }
func (f *fakeDNS) DNSRecords(_ context.Context, z string) ([]DNSRecord, error) {
	return f.records[z], nil
}
func (f *fakeDNS) CreateDNS(_ context.Context, _ string, r DNSRecord) error {
	f.created = append(f.created, r)
	return nil
}
func (f *fakeDNS) UpdateDNS(_ context.Context, _ string, r DNSRecord) error {
	f.updated = append(f.updated, r)
	return nil
}
func (f *fakeDNS) DeleteDNS(_ context.Context, _ string, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func TestReconcileDNS(t *testing.T) {
	m := ManagedComment
	f := &fakeDNS{
		zones: []Zone{{ID: "z1", Name: "example.com"}},
		records: map[string][]DNSRecord{"z1": {
			{ID: "r1", Type: "A", Name: "keep.example.com", Content: "10.0.10.10", Comment: m},
			{ID: "r2", Type: "A", Name: "drift.example.com", Content: "10.0.10.99", Comment: m},
			{ID: "r3", Type: "A", Name: "stale.example.com", Content: "10.0.10.10", Comment: m},
			{ID: "r4", Type: "A", Name: "mine.example.com", Content: "192.168.1.1"},
			{ID: "r5", Type: "A", Name: "other.example.com", Content: "192.168.1.2"},
		}},
	}
	desired := []DNSRecord{
		{Type: "A", Name: "keep.example.com", Content: "10.0.10.10", Comment: m},
		{Type: "A", Name: "drift.example.com", Content: "10.0.10.10", Comment: m},
		{Type: "A", Name: "new.example.com", Content: "10.0.10.10", Comment: m},
		{Type: "A", Name: "mine.example.com", Content: "10.0.10.10", Comment: m},
		{Type: "A", Name: "nozone.test", Content: "10.0.10.10", Comment: m},
	}

	// Dry run changes nothing.
	changes, _, err := ReconcileDNS(context.Background(), f, desired, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.created)+len(f.updated)+len(f.deleted) != 0 {
		t.Fatal("dry run made changes")
	}
	counts := map[string]int{}
	for _, c := range changes {
		counts[c.Action]++
	}
	if counts["create"] != 1 || counts["update"] != 1 || counts["delete"] != 1 || counts["conflict"] != 2 {
		t.Fatalf("unexpected plan %v: %+v", counts, changes)
	}

	_, states, err := ReconcileDNS(context.Background(), f, desired, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 1 || f.created[0].Name != "new.example.com" {
		t.Errorf("created %+v", f.created)
	}
	if len(f.updated) != 1 || f.updated[0].ID != "r2" {
		t.Errorf("updated %+v", f.updated)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "r3" {
		t.Errorf("deleted %v (unmanaged records must never be deleted)", f.deleted)
	}
	for _, s := range states {
		if s.Desired.Name == "mine.example.com" && s.Status != "conflict" {
			t.Errorf("unmanaged record should be a conflict, got %s", s.Status)
		}
	}
}

func TestZoneForPicksLongestMatch(t *testing.T) {
	zones := []Zone{{ID: "a", Name: "example.com"}, {ID: "b", Name: "lab.example.com"}}
	if z, _ := ZoneFor(zones, "*.x.lab.example.com"); z.ID != "b" {
		t.Errorf("got zone %s", z.ID)
	}
	if _, ok := ZoneFor(zones, "example.org"); ok {
		t.Error("matched an unrelated zone")
	}
}
