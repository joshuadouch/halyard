package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sort"
	"strings"

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

func (s *Server) listInterfaces(w http.ResponseWriter, r *http.Request) {
	ifs, err := net.Interfaces()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	labels := s.interfaceLabels(r.Context())
	vips, _ := s.Store.VIPs(r.Context())
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
	writeJSON(w, 200, out)
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
	label := strings.TrimSpace(req.Label)
	if len(label) > 64 || len(name) > 32 || name == "" {
		writeErr(w, 400, "label is too long")
		return
	}
	labels := s.interfaceLabels(r.Context())
	if label == "" {
		delete(labels, name)
	} else {
		labels[name] = label
	}
	if err := s.Store.SetKV(r.Context(), kvInterfaceLabels, labels); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
