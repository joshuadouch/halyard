//go:build linux

package metrics

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func readHost(dataDir string) HostRaw {
	h := HostRaw{Net: map[string]NetRaw{}}
	if f, err := os.Open("/proc/stat"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fs := strings.Fields(sc.Text())
			if len(fs) > 4 && fs[0] == "cpu" {
				for i, v := range fs[1:] {
					n, _ := strconv.ParseUint(v, 10, 64)
					if i == 8 || i == 9 { // guest time is already counted in user
						continue
					}
					h.CPUTotal += n
					if i != 3 && i != 4 { // idle, iowait
						h.CPUBusy += n
					}
				}
			} else if len(fs) > 0 && strings.HasPrefix(fs[0], "cpu") {
				h.Cores++
			}
		}
		f.Close()
	}
	if f, err := os.Open("/proc/meminfo"); err == nil {
		var avail, total uint64
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fs := strings.Fields(sc.Text())
			if len(fs) < 2 {
				continue
			}
			n, _ := strconv.ParseUint(fs[1], 10, 64)
			switch fs[0] {
			case "MemTotal:":
				total = n * 1024
			case "MemAvailable:":
				avail = n * 1024
			}
		}
		f.Close()
		h.MemTotal = total
		if total >= avail {
			h.MemUsed = total - avail
		}
	}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		fs := strings.Fields(string(b))
		for i := 0; i < 3 && i < len(fs); i++ {
			h.Load[i], _ = strconv.ParseFloat(fs[i], 64)
		}
	}
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		if fs := strings.Fields(string(b)); len(fs) > 0 {
			h.UptimeSec, _ = strconv.ParseFloat(fs[0], 64)
		}
	}
	var st syscall.Statfs_t
	if dataDir == "" {
		dataDir = "/"
	}
	if err := syscall.Statfs(dataDir, &st); err == nil {
		bs := uint64(st.Bsize)
		h.DiskTotal = st.Blocks * bs
		h.DiskUsed = (st.Blocks - st.Bfree) * bs
	}
	if f, err := os.Open("/proc/net/dev"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			name, rest, ok := strings.Cut(sc.Text(), ":")
			if !ok {
				continue
			}
			name = strings.TrimSpace(name)
			fs := strings.Fields(rest)
			if name == "lo" || len(fs) < 9 {
				continue
			}
			rx, _ := strconv.ParseUint(fs[0], 10, 64)
			tx, _ := strconv.ParseUint(fs[8], 10, 64)
			h.Net[name] = NetRaw{Rx: rx, Tx: tx}
		}
		f.Close()
	}
	return h
}
