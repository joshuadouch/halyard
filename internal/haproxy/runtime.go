// Package haproxy talks to the HAProxy Runtime API over its stats socket.
package haproxy

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Runtime is a client for the HAProxy admin socket.
type Runtime struct {
	Socket string
}

var safeArg = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)

// Exec runs one command and returns its output.
func (r Runtime) Exec(cmd string) (string, error) {
	conn, err := net.DialTimeout("unix", r.Socket, 2*time.Second)
	if err != nil {
		return "", fmt.Errorf("haproxy runtime api: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, cmd+"\n"); err != nil {
		return "", err
	}
	b, err := io.ReadAll(bufio.NewReader(conn))
	return string(b), err
}

// SetServerState sets ready, drain or maint on a server.
func (r Runtime) SetServerState(backend, server, state string) error {
	if !safeArg.MatchString(backend) || !safeArg.MatchString(server) {
		return fmt.Errorf("invalid backend or server name")
	}
	switch state {
	case "ready", "drain", "maint":
	default:
		return fmt.Errorf("invalid state %q", state)
	}
	out, err := r.Exec(fmt.Sprintf("set server %s/%s state %s", backend, server, state))
	if err != nil {
		return err
	}
	if s := strings.TrimSpace(out); s != "" {
		return fmt.Errorf("haproxy: %s", s)
	}
	return nil
}

// SetWeight changes a server weight without reload.
func (r Runtime) SetWeight(backend, server string, weight int) error {
	if !safeArg.MatchString(backend) || !safeArg.MatchString(server) {
		return fmt.Errorf("invalid backend or server name")
	}
	out, err := r.Exec(fmt.Sprintf("set weight %s/%s %d", backend, server, weight))
	if err != nil {
		return err
	}
	if s := strings.TrimSpace(out); s != "" {
		return fmt.Errorf("haproxy: %s", s)
	}
	return nil
}

// ServersState returns "show servers state" output for the state file.
func (r Runtime) ServersState() (string, error) { return r.Exec("show servers state") }

// Stat is one row of "show stat".
type Stat struct {
	Proxy       string  `json:"proxy"`
	Server      string  `json:"server"` // FRONTEND, BACKEND or a server name
	Status      string  `json:"status"`
	Weight      int     `json:"weight"`
	CurSessions int     `json:"cur_sessions"`
	TotSessions int64   `json:"tot_sessions"`
	Rate        int     `json:"rate"`     // sessions/s
	ReqRate     int     `json:"req_rate"` // HTTP requests/s (frontends)
	CheckStatus string  `json:"check_status"`
	CheckCode   string  `json:"check_code"`
	CheckMS     int     `json:"check_ms"`
	LastChange  int     `json:"last_change_sec"`
	Resp5xx     int64   `json:"resp_5xx"`
	RespTotal   int64   `json:"resp_total"`
	AvgRespMS   int     `json:"avg_resp_ms"`
	AvgTotalMS  int     `json:"avg_total_ms"`
	BytesIn     int64   `json:"bytes_in"`
	BytesOut    int64   `json:"bytes_out"`
	ErrorRate   float64 `json:"-"`

	// Counters used by the metrics collector.
	Type      int   `json:"-"` // 0 frontend, 1 backend, 2 server
	Resp2xx   int64 `json:"-"`
	Resp3xx   int64 `json:"-"`
	Resp4xx   int64 `json:"-"`
	ReqTotal  int64 `json:"-"` // HTTP requests (frontends and backends)
	EConn     int64 `json:"-"` // connection errors to servers
	EResp     int64 `json:"-"` // response errors from servers
	EReq      int64 `json:"-"` // request errors (frontends)
	DReq      int64 `json:"-"` // denied requests (frontends)
	QueueCur  int   `json:"-"`
	ChkFail   int64 `json:"-"`
	Downtime  int64 `json:"-"` // seconds spent down
	MaxSess   int   `json:"-"`
	SessLimit int   `json:"-"`
}

// ShowStat parses "show stat".
func (r Runtime) ShowStat() ([]Stat, error) {
	out, err := r.Exec("show stat")
	if err != nil {
		return nil, err
	}
	return ParseStat(out)
}

// ParseStat parses the CSV output of "show stat".
func ParseStat(out string) ([]Stat, error) {
	out = strings.TrimPrefix(strings.TrimSpace(out), "# ")
	rd := csv.NewReader(strings.NewReader(out))
	rd.FieldsPerRecord = -1
	rows, err := rd.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	idx := map[string]int{}
	for i, h := range rows[0] {
		idx[h] = i
	}
	get := func(row []string, k string) string {
		if i, ok := idx[k]; ok && i < len(row) {
			return row[i]
		}
		return ""
	}
	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	atoi64 := func(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }
	var stats []Stat
	for _, row := range rows[1:] {
		if len(row) < 2 {
			continue
		}
		s := Stat{
			Proxy: get(row, "pxname"), Server: get(row, "svname"), Status: get(row, "status"),
			Weight: atoi(get(row, "weight")), CurSessions: atoi(get(row, "scur")), TotSessions: atoi64(get(row, "stot")),
			Rate: atoi(get(row, "rate")), ReqRate: atoi(get(row, "req_rate")), CheckStatus: get(row, "check_status"),
			CheckCode: get(row, "check_code"), CheckMS: atoi(get(row, "check_duration")), LastChange: atoi(get(row, "lastchg")),
			Resp5xx: atoi64(get(row, "hrsp_5xx")), AvgRespMS: atoi(get(row, "rtime")), AvgTotalMS: atoi(get(row, "ttime")),
			BytesIn: atoi64(get(row, "bin")), BytesOut: atoi64(get(row, "bout")),
			Type: atoi(get(row, "type")), Resp2xx: atoi64(get(row, "hrsp_2xx")), Resp3xx: atoi64(get(row, "hrsp_3xx")),
			Resp4xx: atoi64(get(row, "hrsp_4xx")), ReqTotal: atoi64(get(row, "req_tot")), EConn: atoi64(get(row, "econ")),
			EResp: atoi64(get(row, "eresp")), EReq: atoi64(get(row, "ereq")), DReq: atoi64(get(row, "dreq")),
			QueueCur: atoi(get(row, "qcur")), ChkFail: atoi64(get(row, "chkfail")), Downtime: atoi64(get(row, "downtime")),
			MaxSess: atoi(get(row, "smax")), SessLimit: atoi(get(row, "slim")),
		}
		for _, k := range []string{"hrsp_1xx", "hrsp_2xx", "hrsp_3xx", "hrsp_4xx", "hrsp_5xx", "hrsp_other"} {
			s.RespTotal += atoi64(get(row, k))
		}
		stats = append(stats, s)
	}
	return stats, nil
}

// ShowInfo returns "show info" as key/value pairs (Uptime_sec, CurrConns, ...).
func (r Runtime) ShowInfo() (map[string]string, error) {
	out, err := r.Exec("show info")
	if err != nil {
		return nil, err
	}
	return ParseInfo(out), nil
}

// ParseInfo parses "show info" output.
func ParseInfo(out string) map[string]string {
	m := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(l, ":")
		if ok && k != "" {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}
