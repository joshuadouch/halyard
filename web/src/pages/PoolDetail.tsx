import { useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { del, get, Member, Pool, PoolStatus, post, put, Stat } from '../api';
import { ErrorBox, Field, healthBadge, Icon, Loading, poolBadge, Seg, Toggle, useLoad, useToast } from '../ui';

const blank: Pool = {
  id: '', name: '', mode: 'http', algorithm: 'roundrobin', sticky: '', upstream_tls: false, verify_tls: false,
  slow_start_sec: 0, retries: 2, timeout_server_sec: 0,
  health: { enabled: true, type: 'http', method: 'GET', path: '/', host_header: '', expect_status: '2xx,3xx', interval_sec: 5, fall: 3, rise: 2 },
  members: [{ name: 'm1', address: '', weight: 100, backup: false, state: 'ready' }],
};

function HealthStrip({ history }: { history: string[] }) {
  const cells = [...Array(Math.max(0, 40 - history.length)).fill('g0'), ...history.slice(-40)];
  return <div className="hb">{cells.map((c, i) => <i key={i} className={c === 'up' ? '' : c === 'down' ? 'b' : c === 'other' ? 'w' : 'g'} />)}</div>;
}

export default function PoolDetail() {
  const { id = 'new' } = useParams();
  const isNew = id === 'new';
  const nav = useNavigate();
  const toast = useToast();
  const [p, setP] = useState<Pool | null>(isNew ? structuredClone(blank) : null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [history, setHistory] = useState<Record<string, string[]>>({});
  const stats = useLoad(() => (isNew ? Promise.resolve(null) : get<{ live: boolean; pools: PoolStatus[] | null }>('/stats')), [id], 5000);

  useEffect(() => { if (!isNew) get<Pool>('/pools/' + id).then(setP).catch(setErr); }, [id, isNew]);
  const ps = stats.data?.pools?.find((x) => x.id === id);
  useEffect(() => {
    if (!ps) return;
    setHistory((h) => {
      const n = { ...h };
      for (const m of ps.members) {
        const s = m.status.toUpperCase();
        const v = s.startsWith('UP') || s === 'NO CHECK' ? 'up' : s.startsWith('DOWN') ? 'down' : s.startsWith('MAINT') ? 'g0' : 'other';
        n[m.server] = [...(n[m.server] ?? []), v].slice(-40);
      }
      return n;
    });
  }, [stats.data]); // eslint-disable-line react-hooks/exhaustive-deps

  if (!p) return err ? <ErrorBox error={err} /> : <Loading />;
  const set = (patch: Partial<Pool>) => setP({ ...p, ...patch });
  const setHC = (patch: Partial<Pool['health']>) => set({ health: { ...p.health, ...patch } });
  const setM = (i: number, patch: Partial<Member>) => set({ members: p.members.map((m, j) => (j === i ? { ...m, ...patch } : m)) });
  const statFor = (name: string): Stat | undefined => ps?.members.find((m) => m.server === name.replace(/[^a-zA-Z0-9_]/g, '_'));

  const save = async () => {
    setBusy(true); setErr(null);
    try {
      const saved = isNew ? await post<Pool>('/pools', p) : await put<Pool>('/pools/' + id, p);
      toast('Pool saved and applied');
      if (isNew) nav('/pools/' + saved.id, { replace: true }); else setP(saved);
    } catch (e) { setErr(e); } finally { setBusy(false); }
  };
  const remove = async () => {
    if (!confirm(`Delete pool ${p.name}?`)) return;
    try { await del('/pools/' + id); nav('/pools'); } catch (e) { setErr(e); }
  };
  const memberState = async (name: string, state: string) => {
    try {
      const r = await post('/pools/' + id + '/members/' + encodeURIComponent(name) + '/state', { state });
      if (r.runtime_warning) toast('Saved; live change failed: ' + r.runtime_warning, true); else toast(`${name} → ${state}`);
      setP({ ...p, members: p.members.map((m) => (m.name === name ? { ...m, state: state as Member['state'] } : m)) });
      stats.reload();
    } catch (e) { setErr(e); }
  };

  return (
    <>
      <div className="head">
        <div>
          <div className="row" style={{ alignItems: 'center', gap: 10 }}><h1>{isNew ? 'New pool' : p.name}</h1>{ps && poolBadge(ps.status, ps.up, ps.total)}</div>
          <div className="sub">{isNew ? 'Members are checked continuously and taken out of rotation when they fail.' : `${p.mode.toUpperCase()} · ${p.algorithm} · ${p.members.length} members`}</div>
        </div>
        <div className="row" style={{ gap: 8 }}>
          {!isNew && <button className="btn danger" onClick={remove}><Icon name="trash" size={14} />Delete</button>}
          <button className="btn p" onClick={save} disabled={busy}><Icon name="check" />{busy ? 'Applying…' : 'Save & apply'}</button>
        </div>
      </div>
      <ErrorBox error={err} />
      {ps && (
        <div className="row" style={{ marginBottom: 16 }}>
          <div className="card stat"><div className="k">Sessions / sec</div><div className="v">{ps.rate}</div></div>
          <div className="card stat"><div className="k">Open sessions</div><div className="v">{ps.sessions}</div></div>
          <div className="card stat"><div className="k">Avg total time</div><div className="v">{ps.avg_ms} ms</div></div>
          <div className="card stat"><div className="k">5xx responses</div><div className="v">{ps.resp_5xx}</div><div className="d muted">of {ps.resp_total} since reload</div></div>
        </div>
      )}
      <div className="card" style={{ marginBottom: 16 }}>
        <div className="ch"><h3>Members</h3><button className="btn s" onClick={() => set({ members: [...p.members, { name: 'm' + (p.members.length + 1), address: '', weight: 100, backup: false, state: 'ready' }] })}><Icon name="plus" size={12} />Add member</button></div>
        <table>
          <thead><tr><th>Name</th><th>Address (host:port)</th><th>Weight</th><th>Backup</th>{!isNew && <><th>Live state</th><th>Last check</th><th>Health</th><th>Sessions</th></>}<th style={{ textAlign: 'right' }}>Actions</th></tr></thead>
          <tbody>
            {p.members.map((m, i) => {
              const s = statFor(m.name);
              return (
                <tr key={i} className="memrow" style={s?.status.startsWith('DOWN') ? { background: '#1a1013' } : undefined}>
                  <td style={{ width: 120 }}><input className="input" value={m.name} onChange={(e) => setM(i, { name: e.target.value })} /></td>
                  <td><input className="input mono" value={m.address} placeholder="10.0.50.21:8080" onChange={(e) => setM(i, { address: e.target.value })} /></td>
                  <td style={{ width: 80 }}><input className="input mono" type="number" value={m.weight} onChange={(e) => setM(i, { weight: +e.target.value })} /></td>
                  <td style={{ width: 60 }}><Toggle on={m.backup} onChange={(v) => setM(i, { backup: v })} /></td>
                  {!isNew && <>
                    <td>{s ? healthBadge(s.status) : <span className="dim">{m.state === 'ready' ? 'not applied' : m.state}</span>}</td>
                    <td className="muted" style={{ fontSize: 12 }}>{s?.check_status ? `${s.check_status} ${s.check_code} · ${s.check_ms} ms` : '—'}</td>
                    <td><HealthStrip history={history[m.name.replace(/[^a-zA-Z0-9_]/g, '_')] ?? []} /></td>
                    <td>{s?.cur_sessions ?? 0}</td>
                  </>}
                  <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                    {!isNew && m.state === 'ready' && <><button className="btn s" onClick={() => memberState(m.name, 'drain')}>Drain</button> <button className="btn s ghost" onClick={() => memberState(m.name, 'maint')}>Maint</button></>}
                    {!isNew && m.state !== 'ready' && <button className="btn s" onClick={() => memberState(m.name, 'ready')}>Enable</button>}
                    {' '}<button className="btn s ghost" title="Remove (save to apply)" onClick={() => set({ members: p.members.filter((_, j) => j !== i) })}><Icon name="x" size={12} /></button>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
        <div className="cb"><small className="help" style={{ marginTop: 0 }}>Drain and Maint take effect instantly through the HAProxy Runtime API. Other member edits apply when you save.</small></div>
      </div>
      <div className="grid3">
        <div className="card"><div className="ch"><h3>Load balancing</h3></div><div className="cb">
          <Field label="Name"><input className="input" value={p.name} onChange={(e) => set({ name: e.target.value })} placeholder="immich" /></Field>
          <Field label="Mode"><Seg value={p.mode} onChange={(m) => set({ mode: m, sticky: m === 'tcp' && p.sticky === 'cookie' ? '' : p.sticky, health: { ...p.health, type: m === 'tcp' && p.health.type === 'http' ? 'tcp' : p.health.type } })} options={[{ v: 'http', label: 'HTTP' }, { v: 'tcp', label: 'TCP' }]} /></Field>
          <Field label="Algorithm"><select className="input" value={p.algorithm} onChange={(e) => set({ algorithm: e.target.value })}>
            <option value="roundrobin">Round robin</option><option value="leastconn">Least connections</option><option value="source">Source IP hash</option><option value="first">First available</option><option value="random">Random (power of two)</option>
          </select></Field>
          <Field label="Sticky sessions"><select className="input" value={p.sticky} onChange={(e) => set({ sticky: e.target.value })}>
            <option value="">Off</option>{p.mode === 'http' && <option value="cookie">Cookie (HY_SRV)</option>}<option value="source">Source IP (30 min)</option>
          </select></Field>
          <div className="grid2">
            <Field label="Slow start (s)"><input className="input mono" type="number" value={p.slow_start_sec} onChange={(e) => set({ slow_start_sec: +e.target.value })} /></Field>
            <Field label="Retries"><input className="input mono" type="number" value={p.retries} onChange={(e) => set({ retries: +e.target.value })} /></Field>
          </div>
        </div></div>
        <div className="card"><div className="ch"><h3>Health check</h3><Toggle on={p.health.enabled} onChange={(v) => setHC({ enabled: v })} /></div><div className="cb">
          {p.health.enabled ? <>
            <Field label="Type"><Seg value={p.health.type as 'http' | 'tcp'} onChange={(t) => setHC({ type: t })} options={[{ v: 'http', label: 'HTTP' }, { v: 'tcp', label: 'TCP connect' }]} /></Field>
            {p.health.type === 'http' && <>
              <div className="grid2">
                <Field label="Method"><select className="input" value={p.health.method} onChange={(e) => setHC({ method: e.target.value })}><option>GET</option><option>HEAD</option><option>OPTIONS</option></select></Field>
                <Field label="Expect status"><input className="input mono" value={p.health.expect_status} onChange={(e) => setHC({ expect_status: e.target.value })} /></Field>
              </div>
              <Field label="Path"><input className="input mono" value={p.health.path} onChange={(e) => setHC({ path: e.target.value })} /></Field>
              <Field label="Host header (optional)"><input className="input mono" value={p.health.host_header} onChange={(e) => setHC({ host_header: e.target.value })} /></Field>
            </>}
            <div className="grid3">
              <Field label="Every (s)"><input className="input mono" type="number" value={p.health.interval_sec} onChange={(e) => setHC({ interval_sec: +e.target.value })} /></Field>
              <Field label="Down after"><input className="input mono" type="number" value={p.health.fall} onChange={(e) => setHC({ fall: +e.target.value })} /></Field>
              <Field label="Up after"><input className="input mono" type="number" value={p.health.rise} onChange={(e) => setHC({ rise: +e.target.value })} /></Field>
            </div>
          </> : <div className="muted">Members are always considered up. Turn checks on so failed servers leave rotation automatically.</div>}
        </div></div>
        <div className="card"><div className="ch"><h3>Upstream</h3></div><div className="cb">
          <Toggle on={p.upstream_tls} onChange={(v) => set({ upstream_tls: v })} label="Connect to members over TLS" help="For backends that only speak HTTPS (Proxmox UI, UniFi, etc.)." />
          {p.upstream_tls && <Toggle on={p.verify_tls} onChange={(v) => set({ verify_tls: v })} label="Verify member certificates" help="Off accepts self-signed certificates." />}
          <Field label="Server timeout (s)" help="0 uses the default of 60 s. Raise for long uploads or streaming."><input className="input mono" type="number" value={p.timeout_server_sec} onChange={(e) => set({ timeout_server_sec: +e.target.value })} /></Field>
        </div></div>
      </div>
    </>
  );
}
