package metrics

import (
	"bufio"
	"context"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joshuadouch/halyard/internal/haproxy"
	"github.com/joshuadouch/halyard/internal/model"
	"github.com/joshuadouch/halyard/internal/store"
)

// fakeHAProxy answers "show stat" and "show info" on a unix socket.
type fakeHAProxy struct {
	mu   sync.Mutex
	stat string
}

func (f *fakeHAProxy) set(s string) { f.mu.Lock(); f.stat = s; f.mu.Unlock() }

func (f *fakeHAProxy) serve(t *testing.T) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "admin.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(c).ReadString('\n')
			f.mu.Lock()
			switch strings.TrimSpace(line) {
			case "show stat":
				_, _ = c.Write([]byte(f.stat))
			case "show info":
				_, _ = c.Write([]byte("Version: 3.0.5\nUptime_sec: 42\nCurrConns: 3\n"))
			}
			f.mu.Unlock()
			c.Close()
		}
	}()
	return sock
}

const statHead = "# pxname,svname,type,status,scur,stot,req_tot,hrsp_2xx,hrsp_5xx,econ,eresp,ttime,bin,bout,check_status\n"

func statCSV(feReq, beReq, web1Sess int, web2Status string) string {
	itoa := strconv.Itoa
	return statHead +
		"fe_10_0_0_5_443,FRONTEND,0,OPEN,4," + itoa(feReq) + "," + itoa(feReq) + "," + itoa(feReq) + ",0,0,0,0," + itoa(feReq*100) + "," + itoa(feReq*1000) + ",\n" +
		"be_api,web1,2,UP,2," + itoa(web1Sess) + ",0,0,0,0,0,12,0,0,L7OK\n" +
		"be_api,web2,2," + web2Status + ",0,0,0,0,0,0,0,0,0,0,L4CON\n" +
		"be_api,BACKEND,1,UP,2," + itoa(beReq) + "," + itoa(beReq) + "," + itoa(beReq) + ",0,0,0,20,0,0,\n"
}

func TestCollectorSamples(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pool := model.Pool{ID: "p1", Name: "api", Members: []model.Member{{Name: "web1", Address: "10.0.0.1:80", State: "ready"}, {Name: "web2", Address: "10.0.0.2:80", State: "ready"}}}
	if err := st.Put(ctx, store.KindPool, pool.ID, pool); err != nil {
		t.Fatal(err)
	}
	fake := &fakeHAProxy{}
	fake.set(statCSV(100, 50, 10, "UP"))
	c := &Collector{Store: st, Runtime: haproxy.Runtime{Socket: fake.serve(t)}, Node: "n1", Interval: time.Second,
		LocalAddrs: func() map[string]bool { return map[string]bool{} }}
	c.Sample(ctx)
	if !c.Current().HAProxyUp {
		t.Fatal("expected HAProxy up")
	}
	time.Sleep(1100 * time.Millisecond)
	fake.set(statCSV(200, 100, 20, "DOWN"))
	c.Sample(ctx)

	pts, step := c.Series("1h")
	if step != 10 || len(pts) != 2 {
		t.Fatalf("got %d points, step %d", len(pts), step)
	}
	p := pts[1]
	if r := p.V["req"]; r < 80 || r > 100 {
		t.Errorf("req rate = %v, want about 90", r)
	}
	if p.V["sess"] != 4 {
		t.Errorf("sess = %v", p.V["sess"])
	}
	pp := p.Pools["p1"]
	if pp.V["up"] != 1 || pp.V["total"] != 2 {
		t.Errorf("pool up/total = %v/%v", pp.V["up"], pp.V["total"])
	}
	if pp.Srv["web2"]["st"] != StateDown {
		t.Errorf("web2 state = %v", pp.Srv["web2"]["st"])
	}
	ev, _ := st.EventsOfKind(ctx, 10, "health")
	if len(ev) != 1 || !strings.Contains(ev[0].Message, "web2 in pool api is down") || ev[0].Level != "warn" {
		t.Fatalf("events = %+v", ev)
	}
}

func TestMergeAndDownsample(t *testing.T) {
	ps := []Point{
		{T: 0, V: map[string]float64{"req": 10}, Pools: map[string]PoolPoint{"a": {V: map[string]float64{"req": 1}, Srv: map[string]map[string]float64{"s": {"st": 1}}}}},
		{T: 10, V: map[string]float64{"req": 20, "ms": 5}, Pools: map[string]PoolPoint{"a": {V: map[string]float64{"req": 3}, Srv: map[string]map[string]float64{"s": {"st": 4}}}}},
		{T: 20, V: map[string]float64{"req": 30}},
	}
	m := Merge(ps)
	if m.T != 0 || m.V["req"] != 20 || m.V["ms"] != 5 {
		t.Errorf("merge = %+v", m.V)
	}
	if m.Pools["a"].V["req"] != 2 || m.Pools["a"].Srv["s"]["st"] != StateDown {
		t.Errorf("pool merge = %+v", m.Pools["a"])
	}
	d := Downsample(ps, 2)
	if len(d) != 2 || d[0].V["req"] != 15 || d[1].V["req"] != 30 {
		t.Errorf("downsample = %+v", d)
	}
}

func TestRollupPersists(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := &Collector{Store: st}
	base := time.Now().Add(-30*time.Minute).Unix() / 60 * 60
	c.pendMin = []Point{{T: base, V: map[string]float64{"req": 4}}, {T: base + 30, V: map[string]float64{"req": 6}}}
	// The next minute's sample closes the previous one.
	c.mu.Lock()
	m := Merge(c.pendMin)
	c.mu.Unlock()
	b := []byte(`{"t":` + strconv.FormatInt(base, 10) + `,"v":{"req":5}}`)
	if err := st.SaveMetric(ctx, tierMin, base, b, 0); err != nil {
		t.Fatal(err)
	}
	if m.V["req"] != 5 {
		t.Fatalf("merged = %v", m.V["req"])
	}
	c2 := &Collector{Store: st}
	c2.Load(ctx)
	pts, _ := c2.Series("6h")
	if len(pts) != 1 || pts[0].V["req"] != 5 {
		t.Fatalf("loaded = %+v", pts)
	}
}

func TestNormState(t *testing.T) {
	for in, want := range map[string]int{"UP": StateUp, "UP 1/3": StateUp, "DOWN": StateDown, "DOWN 1/2": StateDown,
		"MAINT": StateMaint, "MAINT (via x)": StateMaint, "DRAIN": StateDrain, "NOLB": StateDrain, "no check": StateUp, "": StateUnknown} {
		if got := NormState(in); got != want {
			t.Errorf("NormState(%q) = %d, want %d", in, got, want)
		}
	}
}
