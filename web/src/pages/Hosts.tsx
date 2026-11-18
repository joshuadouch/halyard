import { Link, useNavigate } from 'react-router-dom';
import { get, Host, Pool, VIP } from '../api';
import { Badge, ErrorBox, ExposureBadge, Icon, Loading, useLoad } from '../ui';

export default function Hosts() {
  const nav = useNavigate();
  const { data, error } = useLoad(() => Promise.all([get<Host[]>('/hosts'), get<VIP[]>('/vips'), get<Pool[]>('/pools')]));
  if (error) return <ErrorBox error={error} />;
  if (!data) return <Loading />;
  const [hosts, vips, pools] = data;
  const vip = (id: string) => vips.find((v) => v.id === id);
  const pool = (id: string) => pools.find((p) => p.id === id)?.name ?? '?';
  return (
    <>
      <div className="head">
        <div><h1>Hosts</h1><div className="sub">Each host binds its hostnames to one VIP and routes paths to pools.</div></div>
        <Link className="btn p" to="/hosts/new"><Icon name="plus" />New host</Link>
      </div>
      {vips.length === 0 && <div className="note">Add a VIP first: every host listens on a VIP. <Link to="/vips">Go to VIPs</Link></div>}
      {pools.length === 0 && <div className="note">Add a pool of backend servers before creating hosts. <Link to="/pools">Go to Pools</Link></div>}
      <div className="card">
        {hosts.length === 0 ? <div className="empty">No hosts yet.</div> : (
          <table>
            <thead><tr><th>Hostnames</th><th>VIP</th><th>Ports</th><th>Exposure</th><th>Routes</th><th>Status</th></tr></thead>
            <tbody>
              {hosts.map((h) => {
                const v = vip(h.vip_id);
                return (
                  <tr key={h.id} className="click" onClick={() => nav('/hosts/' + h.id)}>
                    <td><b>{h.hostnames[0] ?? '(tcp)'}</b>{h.hostnames.length > 1 && <span className="dim"> +{h.hostnames.length - 1}</span>}</td>
                    <td className="mono">{v ? v.address.split('/')[0] : <span style={{ color: '#fca5a5' }}>missing</span>}</td>
                    <td className="mono muted">{h.mode === 'tcp' ? `tcp ${h.tcp_port}` : [h.https_port && `${h.https_port} TLS`, h.http_port && (h.force_https ? `${h.http_port} → TLS` : `${h.http_port}`)].filter(Boolean).join(' · ')}</td>
                    <td><ExposureBadge e={h.exposure} />{h.require_aop && <> <Badge kind="a">AOP</Badge></>}</td>
                    <td className="muted">{h.routes.map((r) => (r.path_prefix || '/*') + ' → ' + pool(r.pool_id)).join(', ')}</td>
                    <td>{h.enabled ? <Badge kind="g">Enabled</Badge> : <Badge kind="n">Disabled</Badge>}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>
    </>
  );
}
