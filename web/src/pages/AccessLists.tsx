import { useState } from 'react';
import { AccessList, AccessListRow, AuthUser, del, get, post, put } from '../api';
import { Badge, Chips, ErrorBox, Field, Icon, Loading, Seg, useLoad, useToast } from '../ui';

const blank: AccessList = { id: '', name: '', allow_cidrs: [], users: [], satisfy_any: true, notes: '' };

function rule(a: AccessList): string {
  const ips = a.allow_cidrs.length, users = a.users.length;
  if (ips && users) return a.satisfy_any ? 'Allowed IP or password' : 'Allowed IP and password';
  if (users) return 'Password';
  return 'Allowed IP';
}

export default function AccessLists() {
  const toast = useToast();
  const { data, error, reload } = useLoad(() => get<AccessListRow[]>('/access-lists'));
  const [edit, setEdit] = useState<(AccessList & { saved: string[] }) | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  if (error) return <ErrorBox error={error} />;
  if (!data) return <Loading />;

  const open = (a: AccessList) => { setErr(null); setEdit({ ...a, users: a.users.map((u) => ({ username: u.username, password: '' })), saved: a.users.map((u) => u.username) }); };
  const setUser = (i: number, patch: Partial<AuthUser>) => edit && setEdit({ ...edit, users: edit.users.map((u, j) => (j === i ? { ...u, ...patch } : u)) });
  const save = async () => {
    if (!edit) return;
    setBusy(true); setErr(null);
    const { saved: _saved, ...body } = edit;
    try {
      if (edit.id) await put('/access-lists/' + edit.id, body); else await post('/access-lists', body);
      toast('Access list saved and applied'); setEdit(null); reload();
    } catch (e) { setErr(e); } finally { setBusy(false); }
  };
  const remove = async (a: AccessListRow) => {
    if (!confirm(`Delete access list ${a.name}?`)) return;
    try { await del('/access-lists/' + a.id); toast('Access list deleted'); reload(); } catch (e) { setErr(e); }
  };
  const both = !!edit && edit.allow_cidrs.length > 0 && edit.users.length > 0;

  return (
    <>
      <div className="head">
        <div><h1>Access lists</h1><div className="sub">Restrict hosts to allowed client IPs, a basic auth login, or both. Pick a list on each host.</div></div>
        <button className="btn p" onClick={() => open(blank)}><Icon name="plus" />New access list</button>
      </div>
      <ErrorBox error={!edit && err} />
      <div className="row" style={{ gap: 16, alignItems: 'flex-start' }}>
        <div className="card" style={{ flex: 1, minWidth: 0, overflowX: 'auto' }}>
          {data.length === 0 ? <div className="empty">No access lists yet.</div> : (
            <table>
              <thead><tr><th>Name</th><th>Visitors need</th><th>Allowed IPs</th><th>Users</th><th>Hosts</th><th></th></tr></thead>
              <tbody>{data.map((a) => (
                <tr key={a.id}>
                  <td><b>{a.name}</b>{a.notes && <div className="dim" style={{ fontSize: 11.5 }}>{a.notes}</div>}</td>
                  <td><Badge kind="w">{rule(a)}</Badge></td>
                  <td className="mono muted" style={{ fontSize: 12 }}>{a.allow_cidrs.length ? a.allow_cidrs.join(', ') : <span className="dim">—</span>}</td>
                  <td className="muted">{a.users.length ? a.users.map((u) => u.username).join(', ') : <span className="dim">—</span>}</td>
                  <td>{a.hosts.length ? <span title={a.hosts.join('\n')}>{a.hosts.length === 1 ? a.hosts[0] : `${a.hosts.length} hosts`}</span> : <span className="dim">none</span>}</td>
                  <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                    <button className="btn s" onClick={() => open(a)}>Edit</button>{' '}
                    <button className="btn s ghost" onClick={() => remove(a)} title={a.hosts.length ? 'In use by hosts' : 'Delete'}><Icon name="trash" size={12} /></button>
                  </td>
                </tr>))}</tbody>
            </table>
          )}
        </div>
        {edit && (
          <div className="card" style={{ width: 380, flex: 'none' }}>
            <div className="ch"><h3>{edit.id ? 'Edit access list' : 'New access list'}</h3><button className="btn s ghost" onClick={() => setEdit(null)}><Icon name="x" size={14} /></button></div>
            <div className="cb">
              <ErrorBox error={err} />
              <Field label="Name"><input className="input" value={edit.name} placeholder="e.g. Home and office" onChange={(e) => setEdit({ ...edit, name: e.target.value })} /></Field>
              <Field label="Allowed client IPs" help="Real visitor IPs, after Cloudflare is unwrapped. Single addresses or CIDR ranges.">
                <Chips mono values={edit.allow_cidrs} onChange={(v) => setEdit({ ...edit, allow_cidrs: v })} placeholder="e.g. 192.168.1.0/24" />
              </Field>
              <Field label="Basic auth users" help={edit.id ? 'Leave a password blank to keep it.' : 'Only a hash is stored.'}>
                <div style={{ display: 'grid', gap: 6 }}>
                  {edit.users.map((u, i) => (
                    <div key={i} className="row" style={{ gap: 6 }}>
                      <input className="input mono" style={{ flex: 1, minWidth: 0 }} value={u.username} placeholder="username" onChange={(e) => setUser(i, { username: e.target.value })} />
                      <input className="input" style={{ flex: 1, minWidth: 0 }} type="password" autoComplete="new-password" value={u.password ?? ''}
                        placeholder={edit.saved.includes(u.username) ? '(unchanged)' : 'password'} onChange={(e) => setUser(i, { password: e.target.value })} />
                      <button className="btn s ghost" onClick={() => setEdit({ ...edit, users: edit.users.filter((_, j) => j !== i) })}><Icon name="x" size={12} /></button>
                    </div>))}
                  <div><button className="btn s" onClick={() => setEdit({ ...edit, users: [...edit.users, { username: '', password: '' }] })}><Icon name="plus" size={12} />Add user</button></div>
                </div>
              </Field>
              {both && <Field label="Visitors need" help={edit.satisfy_any ? 'Allowed IPs get straight in; everyone else is asked for a password.' : 'Other IPs are refused, and allowed IPs must still log in.'}>
                <Seg value={edit.satisfy_any ? 'any' : 'all'} onChange={(v) => setEdit({ ...edit, satisfy_any: v === 'any' })}
                  options={[{ v: 'any', label: 'Either one' }, { v: 'all', label: 'Both' }]} />
              </Field>}
              <Field label="Notes"><input className="input" value={edit.notes} onChange={(e) => setEdit({ ...edit, notes: e.target.value })} /></Field>
              <div className="note">Passwords only work on HTTP hosts. The login is checked by HAProxy and removed before the request reaches your backend.</div>
              <div className="row" style={{ gap: 8, justifyContent: 'flex-end' }}>
                <button className="btn ghost" onClick={() => setEdit(null)}>Cancel</button>
                <button className="btn p" onClick={save} disabled={busy}>{busy ? 'Applying…' : edit.id ? 'Save' : 'Create'}</button>
              </div>
            </div>
          </div>
        )}
      </div>
    </>
  );
}
