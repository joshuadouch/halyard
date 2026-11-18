import { useState } from 'react';
import { Link } from 'react-router-dom';
import { Cert, CFView, del, fmtDate, get, post } from '../api';
import { Badge, Chips, ErrorBox, Field, Icon, Loading, Seg, useLoad, useToast } from '../ui';

export default function Certificates() {
  const toast = useToast();
  const { data, error, reload } = useLoad(() => Promise.all([get<Cert[]>('/certs'), get<CFView>('/cloudflare?light=1').catch(() => null)]), [], 5000);
  const [mode, setMode] = useState<'acme' | 'upload' | null>(null);
  const [domains, setDomains] = useState<string[]>([]);
  const [keyType, setKeyType] = useState<'ec256' | 'rsa2048'>('ec256');
  const [certPEM, setCertPEM] = useState('');
  const [keyPEM, setKeyPEM] = useState('');
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  if (error && !data) return <ErrorBox error={error} />;
  if (!data) return <Loading />;
  const [certs, cf] = data;
  const ready = cf?.token_set && cf.acme_email;

  const submit = async () => {
    setBusy(true); setErr(null);
    try {
      if (mode === 'acme') await post('/certs', { domains, key_type: keyType });
      else await post('/certs', { cert_pem: certPEM, key_pem: keyPEM });
      toast(mode === 'acme' ? 'Requested. Issuance usually takes under a minute.' : 'Certificate uploaded');
      setMode(null); setDomains([]); setCertPEM(''); setKeyPEM(''); reload();
    } catch (e) { setErr(e); } finally { setBusy(false); }
  };
  const days = (c: Cert) => Math.round((new Date(c.not_after).getTime() - Date.now()) / 86400000);

  return (
    <>
      <div className="head">
        <div><h1>Certificates</h1><div className="sub">Issued with DNS-01 through your Cloudflare token, so internal-only hosts get trusted certs with no open ports. Renewed 30 days before expiry.</div></div>
        <div className="row" style={{ gap: 8 }}>
          <button className="btn" onClick={() => { setErr(null); setMode('upload'); }}>Upload</button>
          <button className="btn p" onClick={() => { setErr(null); setMode('acme'); }}><Icon name="plus" />Request certificate</button>
        </div>
      </div>
      <div className="row" style={{ gap: 16, alignItems: 'flex-start' }}>
        <div className="card" style={{ flex: 1, minWidth: 0 }}>
          {certs.length === 0 ? <div className="empty">No certificates yet. Until you add one, HTTPS hosts use a self-signed fallback.</div> : (
            <table>
              <thead><tr><th>Certificate</th><th>Issuer</th><th style={{ width: 210 }}>Expires</th><th>Hosts</th><th>Status</th><th></th></tr></thead>
              <tbody>{certs.map((c) => {
                const d = days(c); const has = c.status !== 'pending' && c.status !== 'failed';
                const warn = has && d < 14;
                return (
                  <tr key={c.id}>
                    <td><b>{c.domains[0]}</b>{c.domains.length > 1 && <div className="dim" style={{ fontSize: 11.5 }}>+ {c.domains.slice(1).join(', ')}</div>}
                      {c.last_error && <div style={{ color: '#fca5a5', fontSize: 11.5, whiteSpace: 'normal', maxWidth: 420 }}>{c.last_error}</div>}</td>
                    <td className="muted" style={{ fontSize: 12 }}>{c.source === 'acme' ? `Let's Encrypt · DNS-01` : `Uploaded${c.issuer ? ' · ' + c.issuer : ''}`}</td>
                    <td>{has ? <>
                      <div className="row" style={{ justifyContent: 'space-between', fontSize: 12, marginBottom: 5 }}><span>{fmtDate(c.not_after)}</span><span style={{ color: warn ? '#fbbf24' : undefined }} className={warn ? '' : 'dim'}>{d > 365 ? Math.round(d / 365) + ' yrs' : d + ' days'}</span></div>
                      <div className="bar"><i style={{ width: Math.max(4, Math.min(100, (d / 90) * 100)) + '%', background: warn ? '#f59e0b' : '#22c55e' }} /></div>
                    </> : <span className="dim">—</span>}</td>
                    <td>{c.hosts}</td>
                    <td>{c.issuing ? <Badge kind="w"><Icon name="sync" size={11} /> Issuing</Badge> : c.status === 'valid' ? <Badge kind="g">Valid</Badge> : c.status === 'failed' ? <Badge kind="b">Failed</Badge> : <Badge kind="w">{c.status}</Badge>}</td>
                    <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                      {c.source === 'acme' && <button className="btn s" disabled={c.issuing} onClick={() => post('/certs/' + c.id + '/renew').then(() => { toast('Renewal started'); reload(); }).catch(setErr)}>{has ? 'Renew' : 'Retry'}</button>}{' '}
                      <button className="btn s ghost" onClick={() => confirm('Delete this certificate?') && del('/certs/' + c.id).then(reload).catch(setErr)}><Icon name="trash" size={12} /></button>
                    </td>
                  </tr>
                );
              })}</tbody>
            </table>
          )}
          {!mode && <div className="cb"><ErrorBox error={err} /></div>}
        </div>
        {mode && (
          <div className="card panel" style={{ width: 380, flex: 'none' }}>
            <div className="ch"><h3>{mode === 'acme' ? 'Request certificate' : 'Upload certificate'}</h3><button className="btn s ghost" onClick={() => setMode(null)}><Icon name="x" size={14} /></button></div>
            <div className="cb">
              <ErrorBox error={err} />
              {mode === 'acme' ? <>
                {!ready && <div className="warnbox">Add your Cloudflare API token and an ACME email on the <Link to="/cloudflare">Cloudflare page</Link> first.</div>}
                <Field label="Domains" help="A wildcard like *.lab.example.net covers every host one level below it."><Chips values={domains} onChange={setDomains} placeholder="*.lab.example.net" /></Field>
                <Field label="Key type"><Seg value={keyType} onChange={setKeyType} options={[{ v: 'ec256', label: 'ECDSA P-256' }, { v: 'rsa2048', label: 'RSA 2048' }]} /></Field>
                {cf?.acme_staging && <div className="note">Using the Let's Encrypt staging CA (browsers will not trust it). Turn staging off on the Cloudflare page for real certificates.</div>}
              </> : <>
                <Field label="Certificate (PEM, full chain)"><textarea className="input" value={certPEM} onChange={(e) => setCertPEM(e.target.value)} placeholder="-----BEGIN CERTIFICATE-----" /></Field>
                <Field label="Private key (PEM)"><textarea className="input" value={keyPEM} onChange={(e) => setKeyPEM(e.target.value)} placeholder="-----BEGIN PRIVATE KEY-----" /></Field>
                <small className="help" style={{ marginBottom: 14 }}>Use this for Cloudflare Origin CA certificates or your internal CA.</small>
              </>}
              <div className="row" style={{ gap: 8, justifyContent: 'flex-end' }}>
                <button className="btn ghost" onClick={() => setMode(null)}>Cancel</button>
                <button className="btn p" onClick={submit} disabled={busy || (mode === 'acme' && (!domains.length || !ready))}>{mode === 'acme' ? 'Request' : 'Upload'}</button>
              </div>
            </div>
          </div>
        )}
      </div>
    </>
  );
}
