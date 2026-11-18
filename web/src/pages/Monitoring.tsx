import { useState } from 'react';
import { Link } from 'react-router-dom';
import { Event, ago, fmtDate, fmtTime, get, post } from '../api';
import { Badge, ErrorBox, Icon, Loading, Seg, healthBadge, poolBadge, useLoad, useToast } from '../ui';
import {
  HealthLegend, HealthStrip, LineChart, Meter, Series, Sparkline, fmtBytes, fmtDur, fmtMs, fmtNum, fmtPct, fmtRate,
} from '../charts';

type Range = '1h' | '6h' | '24h' | '7d';
interface Sample { t: number; v: Record<string, number>; s?: Record<string, Record<string, number>> }
interface SeriesResp { range: string; step: number; points: Sample[] }

interface MonServer {
  name: string; address: string; backup: boolean; status: string; state: number; check_status: string; check_code: string;
  check_ms: number; check_failures: number; downtime_sec: number; last_change_sec: number; sessions: number;
  max_sessions: number; weight: number; avg_ms: number; errors: number; resp_5xx: number; resp_total: number;
}
interface MonPool {
  id: string; name: string; mode: string; status: string; up: number; total: number; sessions: number; queue: number;
  avg_ms: number; resp_5xx: number; resp_total: number; spark: number[]; servers: MonServer[];
}
interface Mon {
  time: string; node: string; role: string; version: string;
  haproxy: { up: boolean; version?: string; uptime_sec?: number; conns?: number; max_conns?: number; conn_rate?: number; ssl_rate?: number; idle_pct?: number };
  keepalived: boolean;
  host: {
    cpu_pct: number; cores: number; mem_total: number; mem_used: number; load: number[]; disk_total: number; disk_used: number;
    uptime_sec: number; net: { name: string; rx_rate: number; tx_rate: number; rx_bytes: number; tx_bytes: number }[] | null;
  };
  tunnel: { configured: boolean; name: string; status: string; connections: number; colos: string[] | null; error: string; checked: string };
  pools: MonPool[]; certs: { id: string; domains: string[]; source: string; status: string; not_after: string; days_left: number }[];
  vips: { id: string; address: string; label: string; interface: string; held: boolean }[];
  peers: PeerStatus[] | null; events: Event[];
}

interface PeerStatus { name: string; url: string; address: string; ok: boolean; time: string; error: string }

const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);
const col = (pts: Sample[], k: string) => pts.map((p) => (p.v[k] ?? null));

function Tile({ label, value, detail, tone, to }: { label: string; value: React.ReactNode; detail?: React.ReactNode; tone?: 'g' | 'w' | 'b' | 'n'; to?: string }) {
  const body = (
    <>
      <div className="k">{tone && <span className={'dot ' + (tone === 'g' ? '' : tone === 'n' ? 'g' : tone)} />}{label}</div>
      <div className="v" style={{ fontSize: 20 }}>{value}</div>
      {detail && <div className="d muted">{detail}</div>}
    </>
  );
  return to ? <Link to={to} className="card stat mon-tile">{body}</Link> : <div className="card stat mon-tile">{body}</div>;
}

function ChartCard({ title, sub, children }: { title: string; sub?: string; children: React.ReactNode }) {
  return (
    <div className="card" style={{ minWidth: 0 }}>
      <div className="ch"><h3>{title}</h3>{sub && <span className="muted" style={{ fontSize: 12 }}>{sub}</span>}</div>
      <div className="cb">{children}</div>
    </div>
  );
}

export default function Monitoring() {
  const [range, setRange] = useState<Range>('1h');
  const [poolID, setPoolID] = useState<string | null>(null);
  const mon = useLoad(() => get<Mon>('/monitoring'), [], 10000);
  const ser = useLoad(() => get<SeriesResp>('/monitoring/series?range=' + range), [range], range === '1h' ? 10000 : 60000);
  if (mon.error && !mon.data) return <ErrorBox error={mon.error} />;
  if (!mon.data) return <Loading />;
  const m = mon.data;
  const pts = ser.data?.points ?? [];
  const step = ser.data?.step ?? 10;
  const t = pts.map((p) => p.t);
  const healthyPools = m.pools.filter((p) => p.status === 'healthy').length;
  const badPools = m.pools.filter((p) => p.status === 'down' || p.status === 'degraded').length;
  const held = m.vips.filter((v) => v.held).length;
  const peers = m.peers ?? [];
  const peersOK = peers.filter((p) => p.ok).length;
  const soonest = m.certs[0];
  const memPct = m.host.mem_total ? (m.host.mem_used / m.host.mem_total) * 100 : 0;
  const diskPct = m.host.disk_total ? (m.host.disk_used / m.host.disk_total) * 100 : 0;
  const pool = m.pools.find((p) => p.id === poolID) ?? null;

  return (
    <>
      <div className="head mon-head">
        <div>
          <h1>Monitoring</h1>
          <div className="sub">{m.node} · {m.role} · updated {fmtTime(m.time)}</div>
        </div>
        <Seg value={range} onChange={setRange} options={(['1h', '6h', '24h', '7d'] as Range[]).map((r) => ({ v: r, label: r }))} />
      </div>
      {!m.haproxy.up && <div className="warnbox">HAProxy is not answering on its runtime socket, so traffic numbers are missing. Check the service on this node.</div>}

      <div className="mon-tiles">
        <Tile label="HAProxy" tone={m.haproxy.up ? 'g' : 'b'} value={m.haproxy.up ? 'Running' : 'Not answering'}
          detail={m.haproxy.up ? `${m.haproxy.version ?? ''} · up ${fmtDur(m.haproxy.uptime_sec ?? 0)}` : 'runtime socket closed'} />
        <Tile label="Pools" to="/pools" tone={m.pools.length === 0 ? 'n' : badPools ? 'w' : 'g'} value={`${healthyPools} / ${m.pools.length}`}
          detail={badPools ? `${badPools} need attention` : 'all healthy'} />
        <Tile label="VIPs held here" to="/vips" tone="n" value={`${held} / ${m.vips.length}`}
          detail={m.keepalived ? 'keepalived on' : 'keepalived off'} />
        <Tile label="Cloudflare Tunnel" to="/cloudflare" tone={!m.tunnel.configured ? 'n' : m.tunnel.status === 'healthy' ? 'g' : 'w'}
          value={m.tunnel.configured ? cap(m.tunnel.status || 'checking') : 'Not set up'}
          detail={m.tunnel.configured ? `${m.tunnel.connections} connections${m.tunnel.colos?.length ? ' · ' + m.tunnel.colos.join(', ') : ''}` : undefined} />
        <Tile label="Peers in sync" to="/nodes" tone={peers.length === 0 ? 'n' : peersOK === peers.length ? 'g' : 'w'}
          value={peers.length ? `${peersOK} / ${peers.length}` : 'Single node'}
          detail={peers.length ? peers.map((p) => p.name).join(', ') : undefined} />
        <Tile label="Next certificate expiry" to="/certificates" tone={!soonest ? 'n' : soonest.days_left < 7 ? 'b' : soonest.days_left < 14 ? 'w' : 'g'}
          value={soonest ? `${soonest.days_left} days` : 'None'} detail={soonest ? soonest.domains[0] : undefined} />
      </div>

      {!!ser.error && <ErrorBox error={ser.error} />}
      <h2 className="mon-h2">Traffic</h2>
      <div className="mon-grid">
        <ChartCard title="Requests" sub="per second"><LineChart t={t} step={step} area series={[{ name: 'Requests/s', values: col(pts, 'req') }]} /></ChartCard>
        <ChartCard title="Responses by status" sub="per second">
          <LineChart t={t} step={step} series={[
            { name: '2xx', values: col(pts, 'r2') }, { name: '3xx', values: col(pts, 'r3') },
            { name: '4xx', values: col(pts, 'r4') }, { name: '5xx', values: col(pts, 'r5') },
          ]} />
        </ChartCard>
        <ChartCard title="Response time" sub="average, all pools"><LineChart t={t} step={step} fmt={fmtMs} area series={[{ name: 'Avg time', values: col(pts, 'ms') }]} /></ChartCard>
        <ChartCard title="Errors" sub="per second">
          <LineChart t={t} step={step} series={[{ name: 'Backend errors', values: col(pts, 'err') }, { name: 'Bad or denied requests', values: col(pts, 'ereq') }]} />
        </ChartCard>
        <ChartCard title="Sessions" sub="open now">
          <LineChart t={t} step={step} series={[{ name: 'Open', values: col(pts, 'sess') }, { name: 'Queued', values: col(pts, 'q') }]} />
        </ChartCard>
        <ChartCard title="Bandwidth" sub="client traffic">
          <LineChart t={t} step={step} fmt={fmtRate} series={[{ name: 'In', values: col(pts, 'in') }, { name: 'Out', values: col(pts, 'out') }]} />
        </ChartCard>
      </div>

      <h2 className="mon-h2">Pools</h2>
      <div className="card" style={{ marginBottom: 16 }}>
        {m.pools.length === 0 ? <div className="empty">No pools yet.</div> : (
          <div className="mon-scroll">
            <table>
              <thead><tr><th>Pool</th><th>Status</th><th>Requests/s</th><th>Avg time</th><th>5xx</th><th>Open</th><th>Queue</th></tr></thead>
              <tbody>{m.pools.map((p) => (
                <tr key={p.id} className={'click' + (p.id === poolID ? ' mon-sel' : '')} onClick={() => setPoolID(p.id === poolID ? null : p.id)}>
                  <td><b>{p.name}</b> <span className="dim">{p.mode}</span></td>
                  <td>{poolBadge(p.status, p.up, p.total)}</td>
                  <td><div className="row" style={{ gap: 8, alignItems: 'center' }}><Sparkline values={p.spark} /><span>{fmtNum(p.spark[p.spark.length - 1] ?? 0)}</span></div></td>
                  <td>{fmtMs(p.avg_ms)}</td>
                  <td>{p.resp_total ? fmtPct((p.resp_5xx / p.resp_total) * 100) : '–'}</td>
                  <td>{p.sessions}</td>
                  <td>{p.queue}</td>
                </tr>
              ))}</tbody>
            </table>
          </div>
        )}
      </div>
      {pool && <PoolPanel pool={pool} range={range} onClose={() => setPoolID(null)} />}

      <h2 className="mon-h2">This node</h2>
      <div className="mon-grid">
        <ChartCard title="Resources" sub={`${m.host.cores} cores · up ${fmtDur(m.host.uptime_sec)}`}>
          <div className="stack" style={{ gap: 14 }}>
            <Meter label="CPU" pct={m.host.cpu_pct} detail={`load ${m.host.load.map((l) => l.toFixed(2)).join(' · ')}`} />
            <Meter label="Memory" pct={memPct} detail={`${fmtBytes(m.host.mem_used)} of ${fmtBytes(m.host.mem_total)}`} />
            <Meter label="Disk (data directory)" pct={diskPct} detail={`${fmtBytes(m.host.disk_used)} of ${fmtBytes(m.host.disk_total)}`} />
          </div>
        </ChartCard>
        <ChartCard title="CPU and memory" sub="percent used">
          <LineChart t={t} step={step} fmt={fmtPct} max={100} series={[{ name: 'CPU', values: col(pts, 'cpu') }, { name: 'Memory', values: col(pts, 'mem') }, { name: 'Disk', values: col(pts, 'disk') }]} />
        </ChartCard>
        <ChartCard title="Load average" sub="1 minute"><LineChart t={t} step={step} area series={[{ name: 'Load', values: col(pts, 'load') }]} /></ChartCard>
        <ChartCard title="Network" sub="all interfaces">
          <LineChart t={t} step={step} fmt={fmtRate} series={[{ name: 'Received', values: col(pts, 'rx') }, { name: 'Sent', values: col(pts, 'tx') }]} />
          {(m.host.net ?? []).length > 0 && (
            <div className="mon-ifaces">
              {(m.host.net ?? []).map((n) => (
                <div key={n.name}><span className="mono">{n.name}</span><span className="muted">↓ {fmtRate(n.rx_rate)} · ↑ {fmtRate(n.tx_rate)}</span></div>
              ))}
            </div>
          )}
        </ChartCard>
      </div>

      <div className="mon-grid" style={{ marginTop: 16 }}>
        <ChartCard title="Certificates" sub="soonest expiry first">
          {m.certs.length === 0 ? <div className="muted">No certificates issued yet.</div> : (
            <div className="stack" style={{ gap: 12 }}>
              {m.certs.slice(0, 8).map((c) => {
                const tone = c.days_left < 7 ? '#ef4444' : c.days_left < 14 ? '#f59e0b' : '#22c55e';
                return (
                  <div key={c.id}>
                    <div className="meter-k"><span className="mono" style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{c.domains[0]}{c.domains.length > 1 ? ` +${c.domains.length - 1}` : ''}</span>
                      <b>{c.days_left} days</b></div>
                    <div className="bar"><i style={{ width: Math.max(2, Math.min(100, (c.days_left / 90) * 100)) + '%', background: tone }} /></div>
                    <div className="dim" style={{ fontSize: 11.5, marginTop: 3 }}>{c.source === 'acme' ? 'Auto-renews' : 'Uploaded'} · expires {fmtDate(c.not_after)}</div>
                  </div>
                );
              })}
            </div>
          )}
        </ChartCard>
        <ChartCard title="VIPs" sub={`held by ${m.node}: ${held}`}>
          {m.vips.length === 0 ? <div className="muted">No VIPs yet.</div> : (
            <div className="stack" style={{ gap: 10 }}>
              {m.vips.map((v) => (
                <div key={v.id} className="row" style={{ gap: 10, alignItems: 'center', justifyContent: 'space-between' }}>
                  <div style={{ minWidth: 0 }}><div className="mono">{v.address}</div><div className="dim" style={{ fontSize: 11.5 }}>{v.label || v.interface}</div></div>
                  {v.held ? <Badge kind="g">● Held here</Badge> : <Badge kind="n">On peer</Badge>}
                </div>
              ))}
            </div>
          )}
        </ChartCard>
        <ChartCard title="Health events" sub="members, VIPs, tunnel">
          {m.events.length === 0 ? <div className="muted">No health changes recorded yet.</div> : (
            <div className="stack" style={{ gap: 11, fontSize: 12.5 }}>
              {m.events.slice(0, 12).map((e) => (
                <div key={e.id} className="row" style={{ gap: 10 }}>
                  <span className={'dot ' + (e.level === 'error' ? 'b' : e.level === 'warn' ? 'w' : '')} style={{ marginTop: 5 }} />
                  <div style={{ minWidth: 0 }}>{e.message}<div className="dim">{ago(e.time)} · {fmtTime(e.time)}</div></div>
                </div>
              ))}
            </div>
          )}
        </ChartCard>
      </div>

      <PrometheusCard />
    </>
  );
}

function PoolPanel({ pool, range, onClose }: { pool: MonPool; range: Range; onClose: () => void }) {
  const ser = useLoad(() => get<SeriesResp>(`/monitoring/series?range=${range}&pool=${encodeURIComponent(pool.id)}`), [pool.id, range], range === '1h' ? 10000 : 60000);
  const pts = ser.data?.points ?? [];
  const step = ser.data?.step ?? 10;
  const t = pts.map((p) => p.t);
  const names = pool.servers.map((s) => s.name);
  const perServer = (k: string): Series[] => names.slice(0, 6).map((n) => ({ name: n, values: pts.map((p) => p.s?.[n]?.[k] ?? null) }));
  return (
    <div className="card mon-pool" style={{ marginBottom: 16 }}>
      <div className="ch">
        <div className="row" style={{ gap: 10, alignItems: 'center', minWidth: 0 }}><h3>{pool.name}</h3>{poolBadge(pool.status, pool.up, pool.total)}</div>
        <div className="row" style={{ gap: 6 }}>
          <Link className="btn s" to={'/pools/' + pool.id}>Edit pool</Link>
          <button className="btn s ghost" onClick={onClose} aria-label="Close"><Icon name="x" size={14} /></button>
        </div>
      </div>
      <div className="cb">
        {!!ser.error && <ErrorBox error={ser.error} />}
        <div className="mon-grid">
          <div><div className="mon-sub">Requests per second</div>
            <LineChart t={t} step={step} height={150} series={[{ name: 'Requests/s', values: col(pts, 'req') }, { name: '4xx', values: col(pts, 'r4') }, { name: '5xx', values: col(pts, 'r5') }]} /></div>
          <div><div className="mon-sub">Response time by member</div>
            <LineChart t={t} step={step} height={150} fmt={fmtMs} series={perServer('ms')} /></div>
          <div><div className="mon-sub">Open sessions by member</div>
            <LineChart t={t} step={step} height={150} series={perServer('sess')} /></div>
          <div><div className="mon-sub">Health check time by member</div>
            <LineChart t={t} step={step} height={150} fmt={fmtMs} series={perServer('chk')} /></div>
        </div>
        {names.length > 6 && <div className="dim" style={{ fontSize: 11.5, marginTop: 6 }}>Charts show the first 6 members; the table lists all.</div>}

        <div className="mon-sub" style={{ marginTop: 18, display: 'flex', justifyContent: 'space-between', flexWrap: 'wrap', gap: 8 }}>
          <span>Health over the last {range}</span><HealthLegend />
        </div>
        <div className="stack" style={{ gap: 8 }}>
          {pool.servers.map((s) => (
            <div key={s.name} className="mon-hrow">
              <span className="mono" title={s.address}>{s.name}</span>
              <HealthStrip t={t} step={step} states={pts.map((p) => p.s?.[s.name]?.st ?? null)} />
            </div>
          ))}
        </div>

        <div className="mon-scroll" style={{ marginTop: 18 }}>
          <table>
            <thead><tr><th>Member</th><th>State</th><th>Last check</th><th>Open</th><th>Peak</th><th>Avg time</th><th>Errors</th><th>Failed checks</th><th>Down for</th><th>Changed</th></tr></thead>
            <tbody>{pool.servers.map((s) => (
              <tr key={s.name}>
                <td><b>{s.name}</b>{s.backup && <span className="dim"> backup</span>}<div className="dim mono">{s.address}</div></td>
                <td>{healthBadge(s.status)}</td>
                <td className="mono">{s.check_status ? `${s.check_status}${s.check_code ? ' ' + s.check_code : ''} · ${s.check_ms} ms` : '–'}</td>
                <td>{s.sessions}</td>
                <td>{s.max_sessions}</td>
                <td>{fmtMs(s.avg_ms)}</td>
                <td>{fmtNum(s.errors)}</td>
                <td>{fmtNum(s.check_failures)}</td>
                <td>{fmtDur(s.downtime_sec)}</td>
                <td>{s.last_change_sec ? fmtDur(s.last_change_sec) + ' ago' : '–'}</td>
              </tr>
            ))}</tbody>
          </table>
        </div>
      </div>
    </div>
  );
}

function PrometheusCard() {
  const tok = useLoad(() => get<{ token: string; haproxy_exporter?: boolean }>('/monitoring/token'), []);
  const [show, setShow] = useState(false);
  const toast = useToast();
  const url = window.location.origin + '/metrics';
  const host = window.location.host;
  const rotate = async () => {
    if (!confirm('Make a new token? Scrapers using the old one stop working.')) return;
    try { const r = await post<{ token: string }>('/monitoring/token'); tok.setData({ ...tok.data, token: r.token }); setShow(true); toast('New token made'); } catch (e) { toast(String((e as Error).message), true); }
  };
  const copy = (s: string) => navigator.clipboard?.writeText(s).then(() => toast('Copied'), () => toast('Copy failed', true));
  const token = tok.data?.token ?? '';
  const scrape = `scrape_configs:
  - job_name: halyard
    scheme: https
    tls_config:
      insecure_skip_verify: true   # self-signed UI certificate
    authorization:
      credentials: ${show ? token : '<token>'}
    static_configs:
      - targets: ['${host}']`;
  return (
    <div className="card" style={{ marginTop: 16 }}>
      <div className="ch"><h3>Prometheus</h3><span className="muted" style={{ fontSize: 12 }}>this node</span></div>
      <div className="cb">
        <div className="muted" style={{ marginBottom: 12 }}>
          Scrape <span className="mono">{url}</span> with the bearer token below for pools, members, VIPs, certificates, peers, the tunnel and this node's resources.
          Add <span className="mono">?haproxy=1</span> to include HAProxy's own exporter. Each node has its own token.
        </div>
        {tok.error != null ? <ErrorBox error={tok.error} /> : (
          <div className="row mon-token" style={{ gap: 8, alignItems: 'center', flexWrap: 'wrap', marginBottom: 12 }}>
            <code className="mono input" style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis' }}>{show ? token : '•'.repeat(24)}</code>
            <button className="btn s" onClick={() => setShow(!show)}>{show ? 'Hide' : 'Show'}</button>
            <button className="btn s" onClick={() => copy(token)} disabled={!token}>Copy</button>
            <button className="btn s danger" onClick={rotate}>New token</button>
          </div>
        )}
        <div className="code mono mon-code">{scrape}</div>
      </div>
    </div>
  );
}
