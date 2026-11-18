import { useEffect, useState } from 'react';
import { ago, get, post } from '../api';
import { Badge, ErrorBox, Loading, useLoad, useToast } from '../ui';

interface Peer { name: string; url: string; address: string; ok: boolean; time: string; error: string }
interface NodesResp {
  self: { name: string; role: string; address: string; vips_held: string[] | null; haproxy_socket: boolean; keepalived: boolean };
  peers: Peer[] | null;
}

interface UpNode { name: string; url: string; version: string; reachable: boolean; self: boolean; error?: string }
interface UpJob { running: boolean; target: string; author: string; started: string; finished: string; step: string; log: string[] | null; error: string }
interface UpInfo { current: string; latest: string; latest_url: string; check_error?: string; checked_at: string; nodes: UpNode[]; job: UpJob }

function Software({ primary }: { primary: boolean }) {
  const [info, setInfo] = useState<UpInfo | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [restarting, setRestarting] = useState<string>('');
  const [checking, setChecking] = useState(false);
  const toast = useToast();
  const load = (refresh = false) => get<UpInfo>('/update' + (refresh ? '?refresh=1' : '')).then((i) => { setInfo(i); setErr(null); if (i.job.step === 'self') setRestarting(i.job.target); return i; }).catch((e) => { setErr(e); return null; });
  useEffect(() => { load(); }, []);
  // Poll while a job runs; while this node restarts, wait for /api/health to report the new version.
  useEffect(() => {
    if (!restarting && !info?.job.running) return;
    const t = setInterval(async () => {
      if (restarting) {
        try {
          const h = await fetch('/api/health').then((r) => r.json());
          if (h.version === restarting) window.location.reload();
        } catch { /* still restarting */ }
        return;
      }
      load();
    }, 3000);
    return () => clearInterval(t);
  }, [restarting, info?.job.running]);

  const check = async () => {
    setChecking(true);
    try {
      const i = await load(true);
      if (i) toast(i.check_error ? 'Could not reach GitHub' : i.nodes.some((n) => n.version !== i.latest) ? `${i.latest} is available` : `Up to date (${i.latest})`);
    } finally { setChecking(false); }
  };
  const start = async () => {
    if (!info) return;
    if (!confirm(`Upgrade every node to ${info.latest}? The standby is upgraded first; this node goes last. Traffic keeps flowing, but the UI restarts at the end.`)) return;
    try { setInfo(await post<UpInfo>('/update', { target: info.latest })); } catch (e) { setErr(e); }
  };
  if (!info) return <div className="card" style={{ marginTop: 16 }}><div className="ch"><h3>Software</h3></div>{err ? <div className="cb"><ErrorBox error={err} /></div> : <Loading />}</div>;
  const behind = info.latest && info.nodes.some((n) => n.version !== info.latest);
  const job = info.job;
  return (
    <div className="card" style={{ marginTop: 16 }}>
      <div className="ch"><h3>Software</h3>
        <div className="row" style={{ gap: 8 }}>
          <button className="btn s" onClick={check} disabled={checking || job.running || !!restarting}>{checking ? 'Checking…' : 'Check for updates'}</button>
          {primary && behind && !job.running && !restarting && <button className="btn p s" onClick={start}>Upgrade all nodes to {info.latest}</button>}
        </div>
      </div>
      <div className="cb">
        <ErrorBox error={err} />
        <div className="muted" style={{ marginBottom: 12 }}>
          {info.check_error ? <>Could not check for releases: {info.check_error}</>
            : info.latest ? <>Latest release: <a href={info.latest_url} target="_blank" rel="noreferrer" className="mono">{info.latest}</a>{!behind && ' · every node is up to date'}</> : 'Checking…'}
          {info.checked_at && !info.checked_at.startsWith('0001') && <> · checked {ago(info.checked_at)}</>}
          {!primary && <> · Start upgrades from the primary.</>}
        </div>
        <table>
          <thead><tr><th>Node</th><th>Version</th><th></th></tr></thead>
          <tbody>{info.nodes.map((n) => (
            <tr key={n.name}><td><b>{n.name}</b>{n.self && <span className="dim"> (this node)</span>}</td>
              <td className="mono">{n.reachable ? n.version : <span className="dim">unreachable</span>}</td>
              <td>{!n.reachable ? <Badge kind="b">{n.error ? 'offline' : '?'}</Badge> : info.latest && n.version === info.latest ? <Badge kind="g">up to date</Badge> : info.latest ? <Badge kind="a">update available</Badge> : null}</td></tr>))}</tbody>
        </table>
        {(job.running || job.log?.length || restarting) && (
          <div style={{ marginTop: 14 }}>
            <b>{restarting ? `Restarting this node on ${restarting}…` : job.running ? `Upgrading to ${job.target}…` : job.error ? 'Upgrade stopped' : `Upgrade to ${job.target} finished`}</b>
            {job.error && <div className="err mono" style={{ marginTop: 6 }}>{job.error}</div>}
            <div className="code mono" style={{ marginTop: 8, whiteSpace: 'pre-wrap' }}>{(job.log ?? []).join('\n')}{restarting && '\n…waiting for this node to come back'}</div>
          </div>
        )}
      </div>
    </div>
  );
}

export default function Nodes() {
  const { data, error, reload } = useLoad(() => get<NodesResp>('/nodes'), [], 10000);
  const toast = useToast();
  const [syncing, setSyncing] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const syncNow = async () => {
    setSyncing(true); setErr(null);
    try {
      const st = await post<Peer[]>('/cluster/sync-now');
      const bad = st.filter((p) => !p.ok).length;
      toast(bad ? `${bad} peer(s) failed to sync` : 'All peers in sync');
      reload();
    } catch (e) { setErr(e); } finally { setSyncing(false); }
  };
  if (error && !data) return <ErrorBox error={error} />;
  if (!data) return <Loading />;
  const s = data.self;
  return (
    <>
      <div className="head"><div><h1>Nodes</h1><div className="sub">The primary takes edits and pushes config to standby nodes. keepalived decides which node holds each VIP.</div></div></div>
      <ErrorBox error={err} />
      <div className="row" style={{ gap: 16, alignItems: 'flex-start' }}>
        <div className="card" style={{ flex: 1 }}>
          <div className="ch"><div className="row" style={{ alignItems: 'center', gap: 8 }}><span className="dot" /><h3>{s.name}</h3><Badge kind={s.role === 'primary' ? 'a' : 'n'}>{s.role.toUpperCase()}</Badge><span className="dim">this node</span></div></div>
          <div className="cb kv">
            <div>Address</div><div className="mono">{s.address || '—'}</div>
            <div>HAProxy runtime</div><div>{s.haproxy_socket ? <Badge kind="g">reachable</Badge> : <Badge kind="b">socket missing</Badge>}</div>
            <div>keepalived</div><div>{s.keepalived ? 'managed' : 'disabled'}</div>
            <div>VIPs held now</div><div>{s.vips_held?.length ? s.vips_held.map((v) => <span key={v} className="chip mono" style={{ marginRight: 6 }}>{v}</span>) : <span className="dim">none</span>}</div>
          </div>
        </div>
        <div className="card" style={{ flex: 1 }}>
          <div className="ch"><h3>Peers</h3>{s.role === 'primary' && !!data.peers?.length && <button className="btn s" disabled={syncing} onClick={syncNow}>{syncing ? 'Syncing…' : 'Sync now'}</button>}</div>
          {!data.peers?.length ? <div className="cb muted">No peers configured. To add a standby, install Halyard on a second VM with <span className="mono">role: standby</span>, then list it under <span className="mono">cluster.peers</span> in <span className="mono">/etc/halyard/halyard.yaml</span> on this node (same <span className="mono">cluster.secret</span> on both).</div> :
            <table><thead><tr><th>Peer</th><th>Address</th><th>Last sync</th></tr></thead><tbody>
              {data.peers.map((p) => <tr key={p.name}><td><b>{p.name}</b><div className="dim mono" style={{ fontSize: 11 }}>{p.url}</div></td><td className="mono">{p.address}</td>
                <td>{!p.time || p.time.startsWith('0001') ? <span className="dim">not yet</span> : p.ok ? <Badge kind="g">OK · {ago(p.time)}</Badge> : <><Badge kind="b">Failed · {ago(p.time)}</Badge><div style={{ color: '#fca5a5', fontSize: 11.5 }}>{p.error}</div></>}</td></tr>)}
            </tbody></table>}
        </div>
      </div>
      <Software primary={s.role === 'primary'} />
    </>
  );
}
