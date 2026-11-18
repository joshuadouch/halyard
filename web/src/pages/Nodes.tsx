import { ago, get } from '../api';
import { Badge, ErrorBox, Loading, useLoad } from '../ui';

interface Peer { name: string; url: string; address: string; ok: boolean; time: string; error: string }
interface NodesResp {
  self: { name: string; role: string; address: string; vips_held: string[] | null; haproxy_socket: boolean; keepalived: boolean };
  peers: Peer[] | null;
}

export default function Nodes() {
  const { data, error } = useLoad(() => get<NodesResp>('/nodes'), [], 10000);
  if (error && !data) return <ErrorBox error={error} />;
  if (!data) return <Loading />;
  const s = data.self;
  return (
    <>
      <div className="head"><div><h1>Nodes</h1><div className="sub">The primary takes edits and pushes config to standby nodes. keepalived decides which node holds each VIP.</div></div></div>
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
          <div className="ch"><h3>Peers</h3></div>
          {!data.peers?.length ? <div className="cb muted">No peers configured. To add a standby, install Halyard on a second VM with <span className="mono">role: standby</span>, then list it under <span className="mono">cluster.peers</span> in <span className="mono">/etc/halyard/halyard.yaml</span> on this node (same <span className="mono">cluster.secret</span> on both).</div> :
            <table><thead><tr><th>Peer</th><th>Address</th><th>Last sync</th></tr></thead><tbody>
              {data.peers.map((p) => <tr key={p.name}><td><b>{p.name}</b><div className="dim mono" style={{ fontSize: 11 }}>{p.url}</div></td><td className="mono">{p.address}</td>
                <td>{!p.time || p.time.startsWith('0001') ? <span className="dim">not yet</span> : p.ok ? <Badge kind="g">OK · {ago(p.time)}</Badge> : <><Badge kind="b">Failed · {ago(p.time)}</Badge><div style={{ color: '#fca5a5', fontSize: 11.5 }}>{p.error}</div></>}</td></tr>)}
            </tbody></table>}
        </div>
      </div>
    </>
  );
}
