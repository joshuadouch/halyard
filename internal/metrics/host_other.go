//go:build !linux

package metrics

func readHost(string) HostRaw { return HostRaw{Net: map[string]NetRaw{}} }
