import { useState } from 'react';
import { del, get, post, User } from '../api';
import { Badge, ErrorBox, Field, Icon, Loading, useLoad, useToast } from '../ui';
import type { Me } from '../App';

export default function Settings({ me }: { me: Me }) {
  const toast = useToast();
  const users = useLoad(() => get<User[]>('/users'));
  const [err, setErr] = useState<unknown>(null);
  const [pw, setPw] = useState({ current: '', new: '' });
  const [totp, setTotp] = useState<{ secret: string; uri: string } | null>(null);
  const [code, setCode] = useState('');
  const [nu, setNu] = useState({ username: '', email: '', password: '' });
  const [totpOn, setTotpOn] = useState(me.user.totp_enabled);

  const go = (fn: () => Promise<unknown>, ok: string) => async () => {
    setErr(null);
    try { await fn(); toast(ok); } catch (e) { setErr(e); }
  };

  return (
    <>
      <div className="head"><div><h1>Settings</h1><div className="sub">Your account and who can manage this load balancer.</div></div></div>
      <ErrorBox error={err} />
      <div className="grid2" style={{ alignItems: 'start' }}>
        <div className="stack">
          <div className="card"><div className="ch"><h3>Password</h3></div><div className="cb">
            {me.has_password && <Field label="Current password"><input className="input" type="password" value={pw.current} onChange={(e) => setPw({ ...pw, current: e.target.value })} /></Field>}
            <Field label="New password" help="At least 10 characters."><input className="input" type="password" value={pw.new} onChange={(e) => setPw({ ...pw, new: e.target.value })} /></Field>
            <button className="btn p" onClick={go(() => post('/me/password', pw).then(() => setPw({ current: '', new: '' })), 'Password changed')}>Change password</button>
          </div></div>
          <div className="card"><div className="ch"><h3>Two-factor sign-in</h3>{totpOn ? <Badge kind="g">On</Badge> : <Badge kind="n">Off</Badge>}</div><div className="cb">
            {!totpOn && !totp && <><p className="muted" style={{ marginBottom: 12 }}>Ask for a code from an authenticator app when signing in with a password. Cloudflare Access sign-in uses your Access policy instead.</p>
              <button className="btn" onClick={go(async () => setTotp(await post('/me/totp')), 'Scan the secret into your app')}>Set up</button></>}
            {!totpOn && totp && <>
              <Field label="Secret (enter in your authenticator app)"><div className="input mono" style={{ userSelect: 'all' }}>{totp.secret}</div></Field>
              <Field label="Or open this link on your phone"><div className="input mono" style={{ fontSize: 11, overflow: 'hidden', textOverflow: 'ellipsis', userSelect: 'all' }}>{totp.uri}</div></Field>
              <Field label="Code from the app"><input className="input mono" value={code} onChange={(e) => setCode(e.target.value)} /></Field>
              <button className="btn p" onClick={go(() => post('/me/totp/enable', { code }).then(() => { setTotpOn(true); setTotp(null); setCode(''); }), 'Two-factor is on')}>Turn on</button>
            </>}
            {totpOn && <>
              <Field label="Current code to turn off"><input className="input mono" value={code} onChange={(e) => setCode(e.target.value)} /></Field>
              <button className="btn danger" onClick={go(() => post('/me/totp/disable', { code }).then(() => { setTotpOn(false); setCode(''); }), 'Two-factor is off')}>Turn off</button>
            </>}
          </div></div>
        </div>
        <div className="card">
          <div className="ch"><h3>Users</h3></div>
          {!users.data ? <Loading /> : <table>
            <thead><tr><th>User</th><th>Email (for Access)</th><th>2FA</th><th></th></tr></thead>
            <tbody>{users.data.map((u) => (
              <tr key={u.id}><td><b>{u.username}</b>{u.id === me.user.id && <span className="dim"> (you)</span>}</td><td className="muted">{u.email || '—'}</td>
                <td>{u.totp_enabled ? <Badge kind="g">On</Badge> : <span className="dim">off</span>}</td>
                <td style={{ textAlign: 'right' }}>{u.id !== me.user.id && <button className="btn s ghost" onClick={go(() => confirm(`Remove ${u.username}?`) ? del('/users/' + u.id).then(users.reload) : Promise.resolve(), 'User removed')}><Icon name="trash" size={12} /></button>}</td></tr>
            ))}</tbody>
          </table>}
          <div className="cb" style={{ borderTop: '1px solid var(--line)' }}>
            <b style={{ display: 'block', marginBottom: 10 }}>Add user</b>
            <div className="grid2">
              <Field label="Username"><input className="input" value={nu.username} onChange={(e) => setNu({ ...nu, username: e.target.value })} /></Field>
              <Field label="Email"><input className="input" value={nu.email} onChange={(e) => setNu({ ...nu, email: e.target.value })} /></Field>
            </div>
            <Field label="Password" help="Leave empty for an Access-only user (signs in through Cloudflare Access with the email above)."><input className="input" type="password" value={nu.password} onChange={(e) => setNu({ ...nu, password: e.target.value })} /></Field>
            <button className="btn p" onClick={go(() => post('/users', nu).then(() => { setNu({ username: '', email: '', password: '' }); users.reload(); }), 'User added')}><Icon name="plus" />Add user</button>
          </div>
        </div>
      </div>
    </>
  );
}
