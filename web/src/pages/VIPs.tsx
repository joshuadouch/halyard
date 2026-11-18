import { useState } from 'react';
import { del, get, post, put, VIP, VIPRow } from '../api';
import { Badge, ErrorBox, Field, Icon, Loading, Seg, Toggle, useLoad, useToast } from '../ui';

interface NodesResp { self: { name: string; role: string }; peers: { name: string }[] | null }
interface Iface { name: string; mac: string; up: boolean; addrs: string[]; networks: string[]; label: string; vips: string[] }

function ip4(s: string): number | null {
  const p = s.split('.');
  if (p.length !== 4 || p.some((x) => !/^\d{1,3}$/.test(x) || +x > 255)) return null;
  return p.reduce((a, x) => a * 256 + +x, 0);
}
function inNet(ip: string, cidr: string): boolean {
  const [n, bits] = cidr.split('/');
  const a = ip4(ip), b = ip4(n), m = +bits;
  if (a === null || b === null || !(m >= 0 && m <= 32)) return false;
  const size = 2 ** (32 - m);
  return Math.floor(a / size) === Math.floor(b / size);
}
// The interface whose network contains ip, if exactly one does.
function ifaceFor(ifs: Iface[], ip: string): Iface | undefined {
  const hits = ifs.filter((i) => i.networks.some((n) => inNet(ip, n)));
  return hits.length === 1 ? hits[0] : undefined;
}
const ifaceText = (i: Iface) => `${i.name}${i.label ? ' · ' + i.label : ''} (${i.addrs.length ? i.addrs.join(', ') : 'no address'})`;

function LabelInput({ iface, onSaved }: { iface: Iface; onSaved: () => void }) {
  const [v, setV] = useState(iface.label);
  const toast = useToast();
  const save = async () => {
    if (v.trim() === iface.label) return;
    try { await put(`/interfaces/${encodeURIComponent(iface.name)}/label`, { label: v }); toast(`Labelled ${iface.name}`); onSaved(); } catch { toast('Could not save label'); }
  };
  return <input className="input" style={{ height: 30 }} value={v} placeholder="e.g. Servers VLAN 20" onChange={(e) => setV(e.target.value)} onBlur={save} onKeyDown={(e) => { if (e.key === 'Enter') (e.target as HTMLInputElement).blur(); }} />;
}

const blank: VIP = { id: '', address: '', label: '', interface: 'eth0', vrid: 51, preferred_node: '', preempt: true, track_haproxy: true };

export default function VIPs() {
  const toast = useToast();
  const { data, error, reload } = useLoad(() => Promise.all([get<VIPRow[]>('/vips'), get<NodesResp>('/nodes'), get<Iface[]>('/interfaces')]));
  const [edit, setEdit] = useState<VIP | null>(null);
  const [ifTouched, setIfTouched] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  if (error) return <ErrorBox error={error} />;
  if (!data) return <Loading />;
  const [vips, nodes, ifs] = data;
  const byName = new Map(ifs.map((i) => [i.name, i]));
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
  const match = ip ? ifaceFor(ifs, ip) : undefined;
  const chosen = edit ? byName.get(edit.interface) : undefined;
  const open = (v: VIP) => { setErr(null); setIfTouched(!!v.id); setEdit(v); };
  const setAddress = (address: string) => {
    if (!edit) return;
    const m = ifaceFor(ifs, address.split('/')[0]);
    setEdit({ ...edit, address, interface: !ifTouched && m ? m.name : edit.interface });
  };

  return (
    <>
      <div className="head">
        <div><h1>Virtual IPs</h1><div className="sub">Floating addresses managed by keepalived. Each host listens on exactly the VIP you choose.</div></div>
        <button className="btn p" onClick={() => open({ ...blank, interface: ifs.find((i) => i.up)?.name ?? ifs[0]?.name ?? 'eth0', vrid: nextVRID })}><Icon name="plus" />Add VIP</button>
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
                  <td><span className="mono muted">{v.interface}</span>{byName.get(v.interface)?.label && <div className="dim" style={{ fontSize: 11.5 }}>{byName.get(v.interface)!.label}</div>}</td><td className="mono">{v.vrid}</td>
                  <td className="muted">{v.preferred_node || nodes.self.name + ' (primary)'}</td>
                  <td>{v.hosts.length ? <span title={v.hosts.join('\n')}>{v.hosts.length}</span> : <span className="dim">0</span>}</td>
                  <td>{v.local ? <Badge kind="g">● Held here</Badge> : <Badge kind="n">Not held</Badge>}</td>
                  <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                    <button className="btn s" onClick={() => open(v)}>Edit</button>{' '}
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
              <Field label="Address (CIDR)"><input className="input mono" value={edit.address} placeholder="10.0.10.10/24" onChange={(e) => setAddress(e.target.value)} /></Field>
              <Field label="Label"><input className="input" value={edit.label} placeholder="LAN" onChange={(e) => setEdit({ ...edit, label: e.target.value })} /></Field>
              <Field label="Interface" help={
                !ip || ip4(ip) === null ? 'Pick the NIC on the VIP\'s network.'
                  : match && match.name === edit.interface ? <>On {match.networks.find((n) => inNet(ip, n))}, same as this node's {match.addrs[0]}.</>
                  : match ? <span style={{ color: '#fbbf24' }}>{ip} is on {match.name}'s network ({match.networks.find((n) => inNet(ip, n))}). Use {match.name} unless you mean to put it elsewhere.</span>
                  : chosen && chosen.addrs.length === 0 ? 'This NIC has no address of its own, which is normal for a VIP-only network.'
                  : <span style={{ color: '#fbbf24' }}>This node has no address on {ip}'s network. Check the interface.</span>}>
                <select className="input mono" value={byName.has(edit.interface) ? edit.interface : '__other'} onChange={(e) => { setIfTouched(true); setEdit({ ...edit, interface: e.target.value === '__other' ? '' : e.target.value }); }}>
                  {ifs.map((i) => <option key={i.name} value={i.name}>{ifaceText(i)}{i.up ? '' : ' (down)'}</option>)}
                  <option value="__other">Other (type a name)…</option>
                </select>
              </Field>
              {!byName.has(edit.interface) && <Field label="Interface name" help="Not present on this node. Interface names must be the same on every node.">
                <input className="input mono" value={edit.interface} placeholder="eth2" onChange={(e) => setEdit({ ...edit, interface: e.target.value })} /></Field>}
              <Field label="VRRP ID"><input className="input mono" type="number" value={edit.vrid} onChange={(e) => setEdit({ ...edit, vrid: +e.target.value })} /></Field>
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
      <div className="card" style={{ marginTop: 16 }}>
        <div className="ch"><h3>Interfaces on {nodes.self.name}</h3></div>
        <div className="cb muted" style={{ paddingBottom: 0 }}>Label each NIC so it is easy to pick. The MAC matches the one shown in Proxmox under the container's Network tab, which tells you which bridge it is on. Interface names must be the same on every node.</div>
        <table>
          <thead><tr><th>Interface</th><th style={{ width: 240 }}>Label</th><th>Addresses</th><th>MAC</th><th>VIPs</th></tr></thead>
          <tbody>{ifs.map((i) => (
            <tr key={i.name}>
              <td><span className="mono" style={{ color: '#fff' }}>{i.name}</span> {i.up ? <Badge kind="g">up</Badge> : <Badge kind="b">down</Badge>}</td>
              <td><LabelInput iface={i} onSaved={reload} /></td>
              <td className="mono muted">{i.addrs.length ? i.addrs.join(', ') : <span className="dim">none</span>}</td>
              <td className="mono dim">{i.mac || '—'}</td>
              <td className="mono muted">{i.vips.length ? i.vips.join(', ') : <span className="dim">—</span>}</td>
            </tr>))}</tbody>
        </table>
      </div>
    </>
  );
}
