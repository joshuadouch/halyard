package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/joshuadouch/halyard/internal/config"
	"github.com/joshuadouch/halyard/internal/engine"
	"github.com/joshuadouch/halyard/internal/store"
)

const kvInterfaceLabels = "interface_labels"

// Interface is a network interface on this node, for picking where a VIP lives.
type Interface struct {
	Name     string   `json:"name"`
	MAC      string   `json:"mac"`
	Up       bool     `json:"up"`
	Addrs    []string `json:"addrs"`    // this node's own addresses, CIDR form
	Networks []string `json:"networks"` // networks those addresses are on
	Label    string   `json:"label"`
	VIPs     []string `json:"vips"` // VIPs configured on this interface
}

func (s *Server) interfaceLabels(ctx context.Context) map[string]string {
	m := map[string]string{}
	if err := s.Store.GetKV(ctx, kvInterfaceLabels, &m); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.Log.Warn("interface labels", "err", err)
	}
	return m
}

func (s *Server) localInterfaces(ctx context.Context) ([]Interface, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	labels := s.interfaceLabels(ctx)
	vips, _ := s.Store.VIPs(ctx)
	out := []Interface{}
	for _, i := range ifs {
		if i.Flags&net.FlagLoopback != 0 {
			continue
		}
		it := Interface{Name: i.Name, MAC: i.HardwareAddr.String(), Up: i.Flags&net.FlagUp != 0, Label: labels[i.Name], Addrs: []string{}, Networks: []string{}, VIPs: []string{}}
		vipIPs := map[string]bool{}
		for _, v := range vips {
			if v.Interface == i.Name {
				it.VIPs = append(it.VIPs, v.Address)
				vipIPs[v.IP()] = true
			}
		}
		if as, err := i.Addrs(); err == nil {
			for _, a := range as {
				n, ok := a.(*net.IPNet)
				if !ok || n.IP.IsLinkLocalUnicast() || vipIPs[n.IP.String()] {
					continue
				}
				it.Addrs = append(it.Addrs, n.String())
				netw := &net.IPNet{IP: n.IP.Mask(n.Mask), Mask: n.Mask}
				it.Networks = append(it.Networks, netw.String())
			}
		}
		out = append(out, it)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out, nil
}

// listInterfaces lists this node's interfaces, or a peer's with ?node=.
func (s *Server) listInterfaces(w http.ResponseWriter, r *http.Request) {
	var out []Interface
	var err error
	if p, ok := s.peerNamed(r.URL.Query().Get("node")); ok {
		out, err = s.peerInterfaces(r.Context(), p, ifaceCommand{Op: "list"})
	} else {
		out, err = s.localInterfaces(r.Context())
	}
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, out)
}

// NodeInterfaces is one node's interfaces for the all-nodes view.
type NodeInterfaces struct {
	Node       string      `json:"node"`
	Self       bool        `json:"self"`
	Error      string      `json:"error,omitempty"`
	Interfaces []Interface `json:"interfaces"`
}

// allInterfaces lists interfaces on this node and every peer.
func (s *Server) allInterfaces(w http.ResponseWriter, r *http.Request) {
	out := make([]NodeInterfaces, 1+len(s.Cfg.Cluster.Peers))
	local, err := s.localInterfaces(r.Context())
	out[0] = NodeInterfaces{Node: s.Cfg.Node.Name, Self: true, Interfaces: local}
	if err != nil {
		out[0].Error = err.Error()
	}
	var wg sync.WaitGroup
	for i, p := range s.Cfg.Cluster.Peers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
			defer cancel()
			ifs, err := s.peerInterfaces(ctx, p, ifaceCommand{Op: "list"})
			out[i+1] = NodeInterfaces{Node: p.Name, Interfaces: ifs}
			if err != nil {
				out[i+1].Error = err.Error()
			}
		}()
	}
	wg.Wait()
	for i := range out {
		if out[i].Interfaces == nil {
			out[i].Interfaces = []Interface{}
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) peerNamed(name string) (config.Peer, bool) {
	if name == "" || name == s.Cfg.Node.Name {
		return config.Peer{}, false
	}
	for _, p := range s.Cfg.Cluster.Peers {
		if p.Name == name {
			return p, true
		}
	}
	return config.Peer{}, false
}

// ifaceCommand is sealed with the cluster secret, like a sync payload.
type ifaceCommand struct {
	From  string    `json:"from"`
	Time  time.Time `json:"time"`
	Op    string    `json:"op"` // list or label
	Name  string    `json:"name,omitempty"`
	Label string    `json:"label,omitempty"`
}

var peerClient = &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}

func (s *Server) peerInterfaces(ctx context.Context, p config.Peer, c ifaceCommand) ([]Interface, error) {
	if p.URL == "" {
		return nil, fmt.Errorf("%s has no url in cluster.peers", p.Name)
	}
	c.From, c.Time = s.Cfg.Node.Name, time.Now().UTC()
	body, _ := json.Marshal(c)
	sealed, err := engine.Seal(s.Cfg.Cluster.Secret, body)
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(p.URL, "/")+"/api/cluster/interfaces", bytes.NewReader(sealed))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := peerClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s is not reachable: %v", p.Name, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == 404 || resp.StatusCode == 405:
		return nil, fmt.Errorf("%s is on an older version; upgrade it to see its interfaces", p.Name)
	case resp.StatusCode != 200:
		return nil, fmt.Errorf("%s: HTTP %d: %s", p.Name, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	plain, err := engine.Open(s.Cfg.Cluster.Secret, raw)
	if err != nil {
		return nil, fmt.Errorf("%s: reply could not be authenticated", p.Name)
	}
	var out []Interface
	return out, json.Unmarshal(plain, &out)
}

// clusterInterfaces answers a peer's sealed request for this node's
// interfaces, optionally setting a label first.
func (s *Server) clusterInterfaces(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if s.Cfg.Cluster.Secret == "" {
		writeErr(w, 400, "cluster secret not configured")
		return
	}
	plain, err := engine.Open(s.Cfg.Cluster.Secret, raw)
	if err != nil {
		writeErr(w, 400, "request could not be authenticated")
		return
	}
	var c ifaceCommand
	if err := json.Unmarshal(plain, &c); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if d := time.Since(c.Time); d > 5*time.Minute || d < -5*time.Minute {
		writeErr(w, 400, "request is too old or clocks are out of sync")
		return
	}
	if c.Op == "label" {
		if err := s.saveLabel(r.Context(), c.Name, c.Label); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	out, err := s.localInterfaces(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	b, _ := json.Marshal(out)
	sealed, err := engine.Seal(s.Cfg.Cluster.Secret, b)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(sealed)
}

func (s *Server) setInterfaceLabel(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req struct {
		Label string `json:"label"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if p, ok := s.peerNamed(r.URL.Query().Get("node")); ok {
		if _, err := s.peerInterfaces(r.Context(), p, ifaceCommand{Op: "label", Name: name, Label: req.Label}); err != nil {
			writeErr(w, 502, err.Error())
			return
		}
	} else if err := s.saveLabel(r.Context(), name, req.Label); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) saveLabel(ctx context.Context, name, label string) error {
	label = strings.TrimSpace(label)
	if len(label) > 64 || len(name) > 32 || name == "" {
		return errors.New("label is too long")
	}
	labels := s.interfaceLabels(ctx)
	if label == "" {
		delete(labels, name)
	} else {
		labels[name] = label
	}
	return s.Store.SetKV(ctx, kvInterfaceLabels, labels)
}
