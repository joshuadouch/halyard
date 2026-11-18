import { useEffect, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { Cert, CFView, del, get, Host, Pool, post, put, VIP } from '../api';
import { Chips, ErrorBox, Field, Icon, Loading, Seg, Toggle, useToast } from '../ui';

const blank = (vip: string, pool: string): Host => ({
  id: '', hostnames: [], vip_id: vip, mode: 'http', http_port: 80, https_port: 443, tcp_port: 0, force_https: true,
  hsts: false, certificate_id: '', routes: [{ path_prefix: '', pool_id: pool }], exposure: 'lan', publish_dns: true,
  require_aop: false, allow_cidrs: [], enabled: true, notes: '',
});

export default function HostEditor() {
  const { id = 'new' } = useParams();
  const isNew = id === 'new';
  const nav = useNavigate();
  const toast = useToast();
  const [h, setH] = useState<Host | null>(null);
  const [vips, setVips] = useState<VIP[]>([]);
  const [pools, setPools] = useState<Pool[]>([]);
  const [certs, setCerts] = useState<Cert[]>([]);
  const [cf, setCf] = useState<CFView | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    Promise.all([get<VIP[]>('/vips'), get<Pool[]>('/pools'), get<Cert[]>('/certs'), isNew ? Promise.resolve(null) : get<Host>('/hosts/' + id)])
      .then(([v, p, c, host]) => {
        setVips(v); setPools(p); setCerts(c);
        setH(host ?? blank(v[0]?.id ?? '', p[0]?.id ?? ''));
      }).catch(setErr);
    get<CFView>('/cloudflare').then(setCf).catch(() => {});
  }, [id, isNew]);

  if (!h) return err ? <ErrorBox error={err} /> : <Loading />;
  const set = (patch: Partial<Host>) => setH({ ...h, ...patch });
  const vip = vips.find((v) => v.id === h.vip_id);
  const tunnelName = cf?.tunnel_name || 'your tunnel';

  const save = async () => {
    setBusy(true); setErr(null);
    try {
      const body = { ...h, allow_cidrs: h.allow_cidrs ?? [] };
      const saved = isNew ? await post<Host>('/hosts', body) : await put<Host>('/hosts/' + id, body);
      toast('Saved and applied');
      if (isNew) nav('/hosts/' + saved.id, { replace: true }); else setH(saved);
    } catch (e) { setErr(e); } finally { setBusy(false); }
  };
  const remove = async () => {
    if (!confirm(`Delete ${h.hostnames[0] ?? 'this host'}? Traffic for it stops immediately.`)) return;
    setBusy(true);
    try { await del('/hosts/' + id); toast('Host deleted'); nav('/hosts'); } catch (e) { setErr(e); setBusy(false); }
  };
  const setRoute = (i: number, patch: Partial<Host['routes'][0]>) => set({ routes: h.routes.map((r, j) => (j === i ? { ...r, ...patch } : r)) });
  const modePools = pools.filter((p) => p.mode === h.mode);

  return (
    <>
      <div className="head">
        <div>
          <h1>{isNew ? 'New host' : h.hostnames[0] || 'Host'}</h1>
          <div className="sub">{isNew ? 'Saving validates the whole config with haproxy -c and applies it without dropping connections.' : 'Changes apply immediately when you save. Bad configs are rejected and nothing changes.'}</div>
        </div>
        <div className="row" style={{ gap: 8 }}>
          {!isNew && <button className="btn danger" onClick={remove} disabled={busy}><Icon name="trash" size={14} />Delete</button>}
          <button className="btn p" onClick={save} disabled={busy}><Icon name="check" />{busy ? 'Applying…' : 'Save & apply'}</button>
        </div>
      </div>
      <ErrorBox error={err} />
      <div className="row" style={{ gap: 16, alignItems: 'flex-start' }}>
        <div className="stack" style={{ flex: 1.1, minWidth: 0 }}>
          <div className="card"><div className="cb">
            <Field label="Mode"><Seg value={h.mode} onChange={(m) => set({ mode: m, routes: [{ path_prefix: '', pool_id: pools.find((p) => p.mode === m)?.id ?? '' }], exposure: m === 'tcp' ? 'lan' : h.exposure })}
              options={[{ v: 'http', label: 'HTTP / HTTPS' }, { v: 'tcp', label: 'TCP stream' }]} /></Field>
            {h.mode === 'http' && <Field label="Hostnames" help="Press Enter after each. Wildcards like *.lab.example.net work."><Chips values={h.hostnames} onChange={(v) => set({ hostnames: v.map((x) => x.toLowerCase()) })} placeholder="add hostname…" /></Field>}
            <div className="grid2">
              <Field label="Listen on VIP">
                <select className="input" value={h.vip_id} onChange={(e) => set({ vip_id: e.target.value })}>
                  <option value="">Choose a VIP…</option>
                  {vips.map((v) => <option key={v.id} value={v.id}>{v.address.split('/')[0]}{v.label ? ` · ${v.label}` : ''}</option>)}
                </select>
              </Field>
              {h.mode === 'http' ? (
                <div className="grid2">
                  <Field label="HTTPS port"><input className="input mono" type="number" value={h.https_port || ''} placeholder="off" onChange={(e) => set({ https_port: +e.target.value || 0 })} /></Field>
                  <Field label="HTTP port"><input className="input mono" type="number" value={h.http_port || ''} placeholder="off" onChange={(e) => set({ http_port: +e.target.value || 0 })} /></Field>
                </div>
              ) : <Field label="TCP port"><input className="input mono" type="number" value={h.tcp_port || ''} onChange={(e) => set({ tcp_port: +e.target.value || 0 })} /></Field>}
            </div>
            {h.mode === 'http' && <>
              <Field label="Exposure">
                <Seg value={h.exposure} onChange={(x) => set({ exposure: x, require_aop: x === 'public' ? h.require_aop : false })} options={[
                  { v: 'lan', label: <><Icon name="lan" size={13} />LAN only</> },
                  { v: 'public', label: <><Icon name="globe" size={13} />Public (proxied DNS)</> },
                  { v: 'tunnel', label: <><Icon name="tunnel" size={13} />Cloudflare Tunnel</> }]} />
                <small className="help">
                  {h.exposure === 'lan' && (h.publish_dns ? <>Publishes an unproxied A record pointing at <span className="mono">{vip?.address.split('/')[0] ?? 'the VIP'}</span> (split-horizon friendly).</> : 'No DNS records are created.')}
                  {h.exposure === 'public' && <>Publishes a proxied record to your public IP{cf?.public_ip ? <> <span className="mono">{cf.public_ip}</span></> : ' (set it on the Cloudflare page)'}. Forward ports on your router to this VIP.</>}
                  {h.exposure === 'tunnel' && <>Creates a CNAME to <span className="mono">{tunnelName}</span> and an ingress rule to <span className="mono">https://{vip?.address.split('/')[0] ?? 'VIP'}:{h.https_port || h.http_port}</span>. Visitor IPs come from <span className="mono">CF-Connecting-IP</span>.</>}
                </small>
              </Field>
              <Toggle on={h.publish_dns} onChange={(v) => set({ publish_dns: v })} label="Manage DNS records in Cloudflare" help="Records Halyard creates are tagged; records you made by hand are never touched." />
              <div className="grid2">
                <Field label="Certificate" help={h.https_port ? 'Automatic picks the best match by name from your certificates.' : 'Enable an HTTPS port to use a certificate.'}>
                  <select className="input" value={h.certificate_id} onChange={(e) => set({ certificate_id: e.target.value })} disabled={!h.https_port}>
                    <option value="">Automatic (match by name)</option>
                    {certs.filter((c) => c.status === 'valid' || c.status === 'renewing').map((c) => <option key={c.id} value={c.id}>{c.domains.join(', ')}</option>)}
                  </select>
                </Field>
                <Field label="Allow only these client IPs" help="Real visitor IPs, after Cloudflare is unwrapped. Empty allows everyone.">
                  <Chips mono values={h.allow_cidrs ?? []} onChange={(v) => set({ allow_cidrs: v })} placeholder="e.g. 10.0.0.0/8" />
                </Field>
              </div>
              <Toggle on={h.force_https} onChange={(v) => set({ force_https: v })} label="Redirect HTTP to HTTPS" />
              <Toggle on={h.hsts} onChange={(v) => set({ hsts: v })} label="Send HSTS header" help="Browsers will refuse plain HTTP for a year. Only turn on once HTTPS works." />
              {h.exposure === 'public' && <Toggle on={h.require_aop} onChange={(v) => set({ require_aop: v })} label="Require Cloudflare Authenticated Origin Pulls"
                help="Rejects any TLS connection that does not present Cloudflare's client certificate, so nobody can bypass Cloudflare by hitting your IP directly. Turn AOP on for the zone on the Cloudflare page first." />}
            </>}
            {h.mode === 'tcp' && <Field label="Allow only these client IPs"><Chips mono values={h.allow_cidrs ?? []} onChange={(v) => set({ allow_cidrs: v })} placeholder="e.g. 10.0.0.0/8" /></Field>}
            <Toggle on={h.enabled} onChange={(v) => set({ enabled: v })} label="Enabled" />
            <Field label="Notes"><input className="input" value={h.notes} onChange={(e) => set({ notes: e.target.value })} /></Field>
          </div></div>
        </div>

        <div className="card" style={{ flex: 1, minWidth: 0 }}>
          <div className="ch"><h3>Routes</h3>{h.mode === 'http' && <button className="btn s" onClick={() => set({ routes: [{ path_prefix: '/', pool_id: modePools[0]?.id ?? '' }, ...h.routes] })}><Icon name="plus" size={12} />Add path route</button>}</div>
          {modePools.length === 0 && <div className="cb"><div className="note">No {h.mode.toUpperCase()} pools yet. <Link to="/pools">Create one</Link> first.</div></div>}
          <table>
            <thead><tr><th>Path prefix</th><th>Pool</th><th></th></tr></thead>
            <tbody>
              {h.routes.map((r, i) => (
                <tr key={i} className="memrow">
                  <td>{r.path_prefix === '' ? <span className="mono dim">everything else</span> :
                    <input className="input mono" value={r.path_prefix} onChange={(e) => setRoute(i, { path_prefix: e.target.value })} />}</td>
                  <td><select className="input" value={r.pool_id} onChange={(e) => setRoute(i, { pool_id: e.target.value })}>
                    <option value="">Choose…</option>
                    {modePools.map((p) => <option key={p.id} value={p.id}>{p.name} ({p.members.length})</option>)}
                  </select></td>
                  <td style={{ width: 40 }}>{r.path_prefix !== '' && <button className="btn s ghost" onClick={() => set({ routes: h.routes.filter((_, j) => j !== i) })}><Icon name="x" size={12} /></button>}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <div className="cb">
            {h.mode === 'http' && !h.routes.some((r) => r.path_prefix === '') && <button className="btn s" onClick={() => set({ routes: [...h.routes, { path_prefix: '', pool_id: modePools[0]?.id ?? '' }] })}>Add default route</button>}
            <small className="help">Path routes are checked top to bottom; the first prefix that matches wins.</small>
            {h.routes.map((r) => pools.find((p) => p.id === r.pool_id)).filter(Boolean).filter((p, i, a) => a.indexOf(p) === i).map((p) => (
              <div key={p!.id} style={{ marginTop: 14 }}>
                <div className="row" style={{ justifyContent: 'space-between', alignItems: 'center', marginBottom: 6 }}>
                  <b>Pool {p!.name}</b><Link className="btn s" to={'/pools/' + p!.id}>Open pool</Link>
                </div>
                <div className="muted" style={{ fontSize: 12 }}>{p!.algorithm} · health {p!.health.enabled ? `${p!.health.type} ${p!.health.path || ''}` : 'off'}</div>
                <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, marginTop: 6 }}>
                  {p!.members.map((m) => <span key={m.name} className="chip mono">{m.address}{m.backup ? ' (backup)' : ''}{m.state !== 'ready' ? ` · ${m.state}` : ''}</span>)}
                </div>
              </div>
            ))}
          </div>
        </div>
      </div>
    </>
  );
}
