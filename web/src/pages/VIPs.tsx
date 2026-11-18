import { useState } from 'react';
import { del, get, post, put, VIP, VIPRow } from '../api';
import { Badge, ErrorBox, Field, Icon, Loading, Seg, Toggle, useLoad, useToast } from '../ui';

interface NodesResp { self: { name: string; role: string }; peers: { name: string }[] | null }

const blank: VIP = { id: '', address: '', label: '', interface: 'eth0', vrid: 51, preferred_node: '', preempt: true, track_haproxy: true };

export default function VIPs() {
  const toast = useToast();
  const { data, error, reload } = useLoad(() => Promise.all([get<VIPRow[]>('/vips'), get<NodesResp>('/nodes')]));
  const [edit, setEdit] = useState<VIP | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  if (error) return <ErrorBox error={error} />;
  if (!data) return <Loading />;
  const [vips, nodes] = data;
  const nodeNames = [nodes.self.name, ...(nodes.peers ?? []).map((p) => p.name)];
  const nextVRID = Math.max(50, ...vips.map((v) => v.vrid)) + 1;

  const save = async () => {
    if (!edit) return;
    setBusy(true); setErr(null);
    try {
      if (edit.id) await put('/vips/' + edit.id, edit); else await post('/vips', edit);
      toast('VIP saved and applied'); setEdit(null); reload();
    } catch (e) { setErr(e); } finally { setBusy(false); }
  };
  const remove = async (v: VIPRow) => {
    if (!confirm(`Delete VIP ${v.address}?`)) return;
    try { await del('/vips/' + v.id); toast('VIP deleted'); reload(); } catch (e) { setErr(e); }
  };
  const ip = edit?.address.split('/')[0];

  return (
    <>
      <div className="head">
        <div><h1>Virtual IPs</h1><div className="sub">Floating addresses managed by keepalived. Each host listens on exactly the VIP you choose.</div></div>
        <button className="btn p" onClick={() => { setErr(null); setEdit({ ...blank, vrid: nextVRID }); }}><Icon name="plus" />Add VIP</button>
      </div>
      <ErrorBox error={!edit && err} />
      <div className="row" style={{ gap: 16, alignItems: 'flex-start' }}>
        <div className="card" style={{ flex: 1, minWidth: 0 }}>
          {vips.length === 0 ? <div className="empty">No VIPs yet. Add the first address hosts should listen on.</div> : (
            <table>
              <thead><tr><th>Address</th><th>Interface</th><th>VRID</th><th>Preferred node</th><th>Hosts</th><th>On this node</th><th></th></tr></thead>
              <tbody>{vips.map((v) => (
                <tr key={v.id}>
                  <td><div className="mono" style={{ fontSize: 13, color: '#fff' }}>{v.address}</div><div className="dim" style={{ fontSize: 11.5 }}>{v.label}</div></td>
                  <td className="mono muted">{v.interface}</td><td className="mono">{v.vrid}</td>
                  <td className="muted">{v.preferred_node || nodes.self.name + ' (primary)'}</td>
                  <td>{v.hosts.length ? <span title={v.hosts.join('\n')}>{v.hosts.length}</span> : <span className="dim">0</span>}</td>
                  <td>{v.local ? <Badge kind="g">● Held here</Badge> : <Badge kind="n">Not held</Badge>}</td>
                  <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                    <button className="btn s" onClick={() => { setErr(null); setEdit(v); }}>Edit</button>{' '}
                    <button className="btn s ghost" onClick={() => remove(v)}><Icon name="trash" size={12} /></button>
                  </td>
                </tr>))}</tbody>
            </table>
          )}
        </div>
        {edit && (
          <div className="card" style={{ width: 340, flex: 'none' }}>
            <div className="ch"><h3>{edit.id ? 'Edit VIP' : 'Add VIP'}</h3><button className="btn s ghost" onClick={() => setEdit(null)}><Icon name="x" size={14} /></button></div>
            <div className="cb">
              <ErrorBox error={err} />
              <Field label="Address (CIDR)"><input className="input mono" value={edit.address} placeholder="10.0.10.10/24" onChange={(e) => setEdit({ ...edit, address: e.target.value })} /></Field>
              <Field label="Label"><input className="input" value={edit.label} placeholder="LAN" onChange={(e) => setEdit({ ...edit, label: e.target.value })} /></Field>
              <div className="grid2">
                <Field label="Interface"><input className="input mono" value={edit.interface} onChange={(e) => setEdit({ ...edit, interface: e.target.value })} /></Field>
                <Field label="VRRP ID"><input className="input mono" type="number" value={edit.vrid} onChange={(e) => setEdit({ ...edit, vrid: +e.target.value })} /></Field>
              </div>
              {nodeNames.length > 1 && <Field label="Preferred node"><Seg value={edit.preferred_node} onChange={(v) => setEdit({ ...edit, preferred_node: v })}
                options={[{ v: '', label: 'Primary' }, ...nodeNames.map((n) => ({ v: n, label: n }))]} /></Field>}
              <Toggle on={edit.track_haproxy} onChange={(v) => setEdit({ ...edit, track_haproxy: v })} label="Track HAProxy health" help="Fail over if HAProxy stops running on this node." />
              <Toggle on={edit.preempt} onChange={(v) => setEdit({ ...edit, preempt: v })} label="Preempt" help="Move back to the preferred node when it recovers." />
              {ip && <div className="warnbox">Proxmox firewall: if IP filter is on for this guest's NIC, add {ip} to its IP set on every node, or traffic to the VIP is dropped.</div>}
              <div className="row" style={{ gap: 8, justifyContent: 'flex-end' }}>
                <button className="btn ghost" onClick={() => setEdit(null)}>Cancel</button>
                <button className="btn p" onClick={save} disabled={busy}>{busy ? 'Applying…' : edit.id ? 'Save VIP' : 'Create VIP'}</button>
              </div>
            </div>
          </div>
        )}
      </div>
    </>
  );
}
