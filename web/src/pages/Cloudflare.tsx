import { useEffect, useState } from 'react';
import { ago, CFReport, CFView, get, post, put } from '../api';
import { Badge, Chips, ErrorBox, ExposureBadge, Field, Icon, Loading, Toggle, useLoad, useToast } from '../ui';

type Tab = 'settings' | 'tunnel' | 'dns' | 'security';

export default function Cloudflare() {
  const toast = useToast();
  const { data, error, reload } = useLoad(() => get<CFView>('/cloudflare'));
  const [tab, setTab] = useState<Tab>('settings');
  const [form, setForm] = useState<CFView | null>(null);
  const [token, setToken] = useState('');
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState('');
  const [report, setReport] = useState<CFReport | null>(null);
  const [tunnelName, setTunnelName] = useState('halyard');
  const [team, setTeam] = useState('');
  const [emails, setEmails] = useState<string[]>([]);

  useEffect(() => {
    if (data) {
      setForm(data); setReport(data.report);
      setTeam(data.access_team); setEmails(data.access_emails ?? []);
      if (!data.token_set) setTab('settings');
    }
  }, [data]);
  if (error && !data) return <ErrorBox error={error} />;
  if (!data || !form) return <Loading />;

  const run = async (key: string, fn: () => Promise<unknown>, ok: string) => {
    setBusy(key); setErr(null);
    try { await fn(); toast(ok); reload(); } catch (e) { setErr(e); } finally { setBusy(''); }
  };
  const settingsBody = (extra: Partial<CFView> = {}) => {
    const f = { ...form, ...extra };
    return {
      api_token: token, account_id: f.account_id, public_ip: f.public_ip, acme_email: f.acme_email,
      acme_staging: f.acme_staging, real_ip: f.real_ip, ui_hostname: f.ui_hostname, access_team: f.access_team,
      access_aud: f.access_aud, access_emails: f.access_emails ?? [], tunnel_id: '', tunnel_name: '', aop_zones: [],
      access_app_id: '', last_reconcile: '',
    };
  };
  const saveSettings = () => run('save', () => put('/cloudflare', settingsBody()).then(() => setToken('')), 'Cloudflare settings saved');
  const reconcile = (dry: boolean) => run('rec' + dry, async () => setReport(await post<CFReport>('/cloudflare/reconcile', { dry_run: dry })), dry ? 'Preview ready' : 'Cloudflare reconciled');
  const records = report?.records ?? [];
  const drift = records.filter((r) => r.status !== 'in_sync').length;
  const t = data.tunnel;

  return (
    <>
      <div className="head">
        <div>
          <div className="row" style={{ alignItems: 'center', gap: 10 }}><h1>Cloudflare</h1>{data.token_set ? (data.error ? <Badge kind="b">● Token error</Badge> : <Badge kind="g">● Connected</Badge>) : <Badge kind="n">Not connected</Badge>}</div>
          <div className="sub">{data.token_set ? <>Account <span className="mono">{data.account_id || '?'}</span> · {data.zones?.length ?? 0} zones · last reconcile {data.last_reconcile ? ago(data.last_reconcile) : 'never'}</> : 'Add a scoped API token to issue certificates and manage DNS, tunnels and Access.'}</div>
        </div>
        {data.token_set && <div className="row" style={{ gap: 8 }}>
          <button className="btn" onClick={() => reconcile(true)} disabled={!!busy}>Preview changes</button>
          <button className="btn p" onClick={() => reconcile(false)} disabled={!!busy}><Icon name="sync" />Reconcile now</button>
        </div>}
      </div>
      <div className="tabs">
        {(['settings', 'tunnel', 'dns', 'security'] as Tab[]).map((x) => (
          <span key={x} className={tab === x ? 'on' : ''} style={{ cursor: 'pointer' }} onClick={() => setTab(x)}>
            {{ settings: 'Settings', tunnel: 'Tunnel', dns: `DNS records${drift ? ` (${drift})` : ''}`, security: 'Real IP, AOP & Access' }[x]}
          </span>))}
      </div>
      <ErrorBox error={err || (data.error && new Error(data.error))} />
      {(report?.notes?.length ?? 0) > 0 && <div className="warnbox">{report!.notes!.map((n, i) => <div key={i}>{n}</div>)}</div>}

      {tab === 'settings' && (
        <div className="row" style={{ gap: 16, alignItems: 'flex-start' }}>
          <div className="card" style={{ flex: 1 }}><div className="ch"><h3>Connection</h3></div><div className="cb">
            <Field label="API token" help={<>Create a custom token with <b>Zone · DNS · Edit</b>, <b>Zone · Zone Settings · Edit</b>, <b>Zone · Zone · Read</b>, <b>Account · Cloudflare Tunnel · Edit</b> and <b>Account · Access: Apps and Policies · Edit</b>.</>}>
              <input className="input mono" type="password" value={token} placeholder={data.token_set ? '•••••••• (saved; paste to replace)' : 'paste token'} onChange={(e) => setToken(e.target.value)} />
            </Field>
            <Field label="Account ID" help="Filled in from your zones if left blank."><input className="input mono" value={form.account_id} onChange={(e) => setForm({ ...form, account_id: e.target.value })} /></Field>
            <Field label="Public IP for “Public” hosts" help="Your WAN address. Proxied A records point here."><input className="input mono" value={form.public_ip} onChange={(e) => setForm({ ...form, public_ip: e.target.value })} placeholder="203.0.113.24" /></Field>
          </div></div>
          <div className="card" style={{ flex: 1 }}><div className="ch"><h3>Certificates & UI</h3></div><div className="cb">
            <Field label="ACME account email"><input className="input" value={form.acme_email} onChange={(e) => setForm({ ...form, acme_email: e.target.value })} /></Field>
            <Toggle on={form.acme_staging} onChange={(v) => setForm({ ...form, acme_staging: v })} label="Use Let's Encrypt staging" help="For testing only: certificates are not trusted by browsers." />
            <Field label="Management UI hostname" help="Published through the tunnel and protected by Cloudflare Access. The UI also stays reachable on its internal address."><input className="input mono" value={form.ui_hostname} onChange={(e) => setForm({ ...form, ui_hostname: e.target.value })} placeholder="halyard.example.net" /></Field>
            <div className="row" style={{ justifyContent: 'flex-end' }}><button className="btn p" onClick={saveSettings} disabled={!!busy}>Save settings</button></div>
          </div></div>
        </div>
      )}

      {tab === 'tunnel' && (
        <div className="row" style={{ gap: 16, alignItems: 'flex-start' }}>
          <div className="card" style={{ flex: 1 }}>
            <div className="ch"><h3>Managed tunnel</h3>{t && <Badge kind={t.status === 'healthy' ? 'g' : t.status === 'down' || t.status === 'inactive' ? 'b' : 'w'}>● {t.status}</Badge>}</div>
            <div className="cb">
              {data.tunnel_id ? <div className="kv">
                <div>Name</div><div>{data.tunnel_name}</div>
                <div>Tunnel ID</div><div className="mono">{data.tunnel_id}</div>
                <div>CNAME target</div><div className="mono">{data.tunnel_id}.cfargotunnel.com</div>
                <div>Connectors</div><div>{t?.connections?.length ? t.connections.map((c, i) => <div key={i}><span className="dot" /> {c.origin_ip} · {c.colo_name} · {c.client_version}</div>) : <span className="dim">none connected</span>}</div>
              </div> : <>
                <p className="muted" style={{ marginBottom: 14 }}>Halyard creates a remotely managed tunnel, writes its token for the local <span className="mono">cloudflared</span> service and keeps the ingress rules in sync with your Tunnel hosts. Every node runs a connector, so the tunnel survives a node failure.</p>
                <Field label="Tunnel name"><input className="input" value={tunnelName} onChange={(e) => setTunnelName(e.target.value)} /></Field>
                <button className="btn p" disabled={!data.token_set || !!busy} onClick={() => run('tun', () => post('/cloudflare/tunnel', { name: tunnelName }), 'Tunnel ready')}><Icon name="tunnel" />Create tunnel</button>
              </>}
            </div>
          </div>
          <div className="card" style={{ flex: 1.3 }}>
            <div className="ch"><h3>Ingress rules</h3><span className="muted" style={{ fontSize: 12 }}>generated from Tunnel hosts</span></div>
            {(report?.ingress?.length ?? 0) === 0 ? <div className="empty">No tunnel hosts yet. Set a host's exposure to “Cloudflare Tunnel”.</div> :
              <table><thead><tr><th>Hostname</th><th>Service</th><th>Origin</th></tr></thead><tbody>
                {report!.ingress!.map((r, i) => <tr key={i}><td className="mono">{r.hostname ?? <span className="dim">catch-all</span>}</td><td className="mono muted">{r.service}</td>
                  <td className="muted" style={{ fontSize: 12 }}>{r.originRequest?.noTLSVerify ? 'TLS not verified' : r.originRequest?.originServerName ? 'SNI ' + r.originRequest.originServerName : ''}</td></tr>)}
              </tbody></table>}
          </div>
        </div>
      )}

      {tab === 'dns' && (
        <div className="card">
          <div className="ch"><h3>Managed DNS records</h3><span className="muted" style={{ fontSize: 12 }}>{report?.time ? `${report.dry_run ? 'preview' : 'checked'} ${ago(report.time)}` : 'run a reconcile to check'}</span></div>
          {records.length === 0 ? <div className="empty">No managed records. Hosts with “Manage DNS records” on appear here.</div> :
            <table>
              <thead><tr><th>Name</th><th>Exposure</th><th>Type</th><th>Wanted</th><th>In Cloudflare</th><th>Proxied</th><th>Sync</th></tr></thead>
              <tbody>{records.map((r, i) => (
                <tr key={i}>
                  <td className="mono">{r.desired.name}</td>
                  <td><ExposureBadge e={r.desired.type === 'CNAME' ? 'tunnel' : r.desired.proxied ? 'public' : 'lan'} /></td>
                  <td className="mono">{r.desired.type}</td><td className="mono muted">{r.desired.content}</td>
                  <td className="mono muted">{r.actual ? (r.actual.content === r.desired.content ? '=' : r.actual.content) : '—'}</td>
                  <td><span className={r.desired.proxied ? 'cf' : 'dim'}><Icon name="cloud" /></span></td>
                  <td>{r.status === 'in_sync' ? <Badge kind="g"><Icon name="check" size={11} /> In sync</Badge> : r.status === 'conflict' ? <Badge kind="b">Conflict: record not owned by Halyard</Badge> : r.status === 'drift' ? <Badge kind="w">Drift</Badge> : <Badge kind="w">Missing</Badge>}</td>
                </tr>))}</tbody>
            </table>}
          {(report?.changes?.length ?? 0) > 0 && <div className="cb"><b>{report!.dry_run ? 'Would change' : 'Changes made'}</b>
            {report!.changes!.map((c, i) => <div key={i} className="mono" style={{ fontSize: 12, marginTop: 4 }}>{c.action.padEnd(8)} {c.record} {c.detail}</div>)}</div>}
        </div>
      )}

      {tab === 'security' && (
        <div className="row" style={{ gap: 16, alignItems: 'flex-start' }}>
          <div className="card" style={{ flex: 1 }}>
            <div className="ch"><h3>Real client IP</h3></div>
            <div className="cb">
              <Toggle on={form.real_ip} onChange={(v) => setForm({ ...form, real_ip: v })} label={<>Restore visitor IP from <span className="mono">CF-Connecting-IP</span></>}
                help="Trusted only from Cloudflare's published ranges (refreshed daily) and the local cloudflared. Logs, IP allow lists and X-Forwarded-For then show the real visitor." />
              <div className="row" style={{ justifyContent: 'flex-end' }}><button className="btn" onClick={saveSettings} disabled={!!busy}>Save</button></div>
            </div>
            <div className="ch" style={{ borderTop: '1px solid var(--line)' }}><h3>Authenticated Origin Pulls</h3></div>
            <div className="cb">
              <p className="muted" style={{ marginBottom: 12 }}>With AOP on, Cloudflare presents a client certificate to your origin. Hosts with “Require AOP” then reject any connection that skips Cloudflare. Tunnel hosts don't need it.</p>
              {(data.zones ?? []).map((z) => (
                <div key={z.id} className="row" style={{ alignItems: 'center', gap: 10, marginBottom: 10 }}>
                  <Toggle on={!!data.aop[z.name]} onChange={(on) => run('aop' + z.name, () => post('/cloudflare/aop', { zone: z.name, on }), `AOP ${on ? 'enabled' : 'disabled'} for ${z.name}`)} />
                  <span className="mono">{z.name}</span>
                </div>))}
              {!data.zones?.length && <div className="dim">Connect a token to list zones.</div>}
            </div>
          </div>
          <div className="card" style={{ flex: 1 }}>
            <div className="ch"><h3>Cloudflare Access for this UI</h3>{data.access_aud ? <Badge kind="g">● Protected</Badge> : <Badge kind="n">Not set up</Badge>}</div>
            <div className="cb">
              <p className="muted" style={{ marginBottom: 12 }}>The UI stays reachable internally with your password. Through <span className="mono">{data.ui_hostname || 'your UI hostname'}</span> it sits behind Access, and Halyard verifies the Access JWT so you're signed in automatically.</p>
              <Field label="Zero Trust team name" help="The part before .cloudflareaccess.com"><input className="input mono" value={team} onChange={(e) => setTeam(e.target.value)} placeholder="example" /></Field>
              <Field label="Emails allowed to sign in" help="Each gets a Halyard account on first Access sign-in."><Chips values={emails} onChange={setEmails} placeholder="you@example.com" /></Field>
              {data.access_aud ? <>
                <div className="kv" style={{ marginBottom: 14 }}><div>Application AUD</div><div className="mono" style={{ wordBreak: 'break-all' }}>{data.access_aud}</div></div>
                <button className="btn" disabled={!!busy} onClick={() => run('acc', () => put('/cloudflare', settingsBody({ access_team: team, access_emails: emails })), 'Access settings saved')}>Save team and emails</button>
              </> : <button className="btn p" disabled={!data.ui_hostname || !data.tunnel_id || !!busy}
                onClick={() => run('acc', () => post('/cloudflare/access-app', { team, emails }), 'Access application created')}>Create Access application</button>}
              {(!data.ui_hostname || !data.tunnel_id) && !data.access_aud && <small className="help">Set the UI hostname and create the tunnel first.</small>}
            </div>
          </div>
        </div>
      )}
    </>
  );
}
