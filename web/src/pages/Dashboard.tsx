import { useEffect, useRef, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { Event, get, PoolStatus, Revision, VIP, ago, fmtTime } from '../api';
import { Badge, ErrorBox, Icon, Loading, poolBadge, useLoad } from '../ui';
import type { Me } from '../App';

interface Dash {
  live: boolean; req_rate: number; sessions: number; members_up: number; members_total: number;
  resp_5xx: number; resp_total: number; hosts: number; pools: PoolStatus[] | null;
  vips: (VIP & { hosts: number; local: boolean })[]; certs: number; certs_expiring: number;
  events: Event[] | null; last_revision: Revision[] | null;
}

function Spark({ points }: { points: number[] }) {
  const w = 800, h = 170;
  if (points.length < 2) return <div className="empty" style={{ height: h }}>Collecting samples…</div>;
  const max = Math.max(1, ...points) * 1.15;
  const step = w / (points.length - 1);
  const d = points.map((p, i) => `${i ? 'L' : 'M'}${(i * step).toFixed(1)} ${(h - (p / max) * h).toFixed(1)}`).join('');
  return (
    <svg width="100%" height={h} viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none">
      <defs><linearGradient id="g" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stopColor="#4f8cff" stopOpacity=".35" /><stop offset="1" stopColor="#4f8cff" stopOpacity="0" /></linearGradient></defs>
      {[0, 1, 2, 3].map((i) => <line key={i} x1="0" x2={w} y1={i * 56 + 2} y2={i * 56 + 2} stroke="#1f2a3a" />)}
      <path d={d + `L${w} ${h}L0 ${h}Z`} fill="url(#g)" />
      <path d={d} fill="none" stroke="#4f8cff" strokeWidth="2" vectorEffect="non-scaling-stroke" />
    </svg>
  );
}

export default function Dashboard({ me }: { me: Me }) {
  const { data, error } = useLoad(() => get<Dash>('/dashboard'), [], 5000);
  const [series, setSeries] = useState<number[]>([]);
  const last = useRef<Dash | null>(null);
  const nav = useNavigate();
  useEffect(() => {
    if (data && data !== last.current) {
      last.current = data;
      setSeries((s) => [...s.slice(-59), data.req_rate]);
    }
  }, [data]);
  if (error && !data) return <ErrorBox error={error} />;
  if (!data) return <Loading />;
  const rate5xx = data.resp_total ? (data.resp_5xx / data.resp_total) * 100 : 0;
  const pools = data.pools ?? [];
  const hour = new Date().getHours();
  const greet = hour < 12 ? 'Good morning' : hour < 18 ? 'Good afternoon' : 'Good evening';
  const rev = data.last_revision?.[0];
  return (
    <>
      <div className="head">
        <div>
          <h1>{greet}, {me.user.username}</h1>
          <div className="sub">{data.hosts} hosts on {data.vips.length} VIPs{rev ? ` · last change applied ${ago(rev.time)}` : ''}</div>
        </div>
        <div className="row" style={{ gap: 8 }}>
          <Link className="btn p" to="/hosts/new"><Icon name="plus" />New host</Link>
        </div>
      </div>
      {!data.live && <div className="warnbox">Live stats are unavailable: HAProxy's runtime socket is not answering. Traffic may still be flowing; check the Nodes page.</div>}
      <div className="row" style={{ marginBottom: 16 }}>
        <div className="card stat"><div className="k">Requests / sec</div><div className="v">{data.req_rate.toLocaleString()}</div><div className="d muted">{data.sessions} open sessions</div></div>
        <div className="card stat"><div className="k">5xx rate</div><div className="v">{rate5xx.toFixed(2)}%</div><div className="d muted">since last reload</div></div>
        <div className="card stat"><div className="k">Pool members healthy</div><div className="v">{data.members_up} <span className="muted" style={{ fontSize: 16, fontWeight: 500 }}>/ {data.members_total}</span></div>
          <div className="d" style={{ color: data.members_up < data.members_total ? '#fbbf24' : undefined }}>{data.members_total - data.members_up} not up</div></div>
        <div className="card stat"><div className="k">Certificates</div><div className="v">{data.certs}</div>
          <div className="d" style={{ color: data.certs_expiring ? '#fbbf24' : undefined }}>{data.certs_expiring ? `${data.certs_expiring} expire within 14 days` : 'none expiring soon'}</div></div>
      </div>
      <div className="row" style={{ marginBottom: 16 }}>
        <div className="card" style={{ flex: 1.9, minWidth: 0 }}>
          <div className="ch"><h3>Traffic</h3><span className="muted" style={{ fontSize: 12 }}>requests/sec · live, 5 s samples</span></div>
          <div className="cb"><Spark points={series} /></div>
        </div>
        <div className="card" style={{ flex: 1, minWidth: 0 }}>
          <div className="ch"><h3>VIPs</h3><Link to="/vips" className="muted" style={{ fontSize: 12, textDecoration: 'none' }}>Manage</Link></div>
          {data.vips.length === 0 ? <div className="empty">No VIPs yet. <Link to="/vips">Add one</Link> to start.</div> :
            <table><tbody>{data.vips.map((v) => (
              <tr key={v.id}><td><div className="mono">{v.address}</div><div className="dim" style={{ fontSize: 11.5 }}>{v.label || v.interface}</div></td>
                <td className="muted">{v.hosts} hosts</td>
                <td>{v.local ? <Badge kind="g">● Held here</Badge> : <Badge kind="n">On peer</Badge>}</td></tr>
            ))}</tbody></table>}
        </div>
      </div>
      <div className="row">
        <div className="card" style={{ flex: 1.9, minWidth: 0 }}>
          <div className="ch"><h3>Pools</h3><Link to="/pools" className="muted" style={{ fontSize: 12, textDecoration: 'none' }}>View all</Link></div>
          {pools.length === 0 ? <div className="empty">No pools yet.</div> :
            <table>
              <thead><tr><th>Pool</th><th>Status</th><th>Sessions/s</th><th>Open</th><th>Avg time</th></tr></thead>
              <tbody>{[...pools].sort((a, b) => b.rate - a.rate).slice(0, 8).map((p) => (
                <tr key={p.id} className="click" onClick={() => nav('/pools/' + p.id)}>
                  <td><b>{p.name}</b></td><td>{poolBadge(p.status, p.up, p.total)}</td><td>{p.rate}</td><td>{p.sessions}</td><td>{p.avg_ms} ms</td>
                </tr>))}</tbody>
            </table>}
        </div>
        <div className="card" style={{ flex: 1, minWidth: 0 }}>
          <div className="ch"><h3>Recent events</h3><Link to="/history" className="muted" style={{ fontSize: 12, textDecoration: 'none' }}>History</Link></div>
          <div className="cb" style={{ display: 'flex', flexDirection: 'column', gap: 13, fontSize: 12.5 }}>
            {(data.events ?? []).slice(0, 8).map((e) => (
              <div key={e.id} className="row" style={{ gap: 10 }}>
                <span className={'dot ' + (e.level === 'error' ? 'b' : e.level === 'warn' ? 'w' : e.kind === 'cloudflare' ? 'o' : '')} style={{ marginTop: 5 }} />
                <div>{e.message}<div className="dim">{fmtTime(e.time)} · {e.kind}</div></div>
              </div>
            ))}
            {!data.events?.length && <div className="muted">Nothing yet.</div>}
          </div>
        </div>
      </div>
    </>
  );
}
