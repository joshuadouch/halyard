import { Link, useNavigate } from 'react-router-dom';
import { get, Pool, PoolStatus } from '../api';
import { ErrorBox, Icon, Loading, poolBadge, useLoad } from '../ui';

export default function Pools() {
  const nav = useNavigate();
  const { data, error } = useLoad(() => Promise.all([get<Pool[]>('/pools'), get<{ live: boolean; pools: PoolStatus[] | null }>('/stats')]), [], 5000);
  if (error && !data) return <ErrorBox error={error} />;
  if (!data) return <Loading />;
  const [pools, stats] = data;
  const st = (id: string) => stats.pools?.find((p) => p.id === id);
  return (
    <>
      <div className="head">
        <div><h1>Pools</h1><div className="sub">Groups of backend servers with health checks and a balancing algorithm.</div></div>
        <Link className="btn p" to="/pools/new"><Icon name="plus" />New pool</Link>
      </div>
      <div className="card">
        {pools.length === 0 ? <div className="empty">No pools yet.</div> : (
          <table>
            <thead><tr><th>Pool</th><th>Mode</th><th>Algorithm</th><th>Members</th><th>Health check</th><th>Status</th><th>Sessions/s</th></tr></thead>
            <tbody>{pools.map((p) => {
              const s = st(p.id);
              return (
                <tr key={p.id} className="click" onClick={() => nav('/pools/' + p.id)}>
                  <td><b>{p.name}</b></td><td className="muted">{p.mode}</td><td className="muted">{p.algorithm}{p.sticky ? ` · sticky ${p.sticky}` : ''}</td>
                  <td>{p.members.length}</td>
                  <td className="mono muted" style={{ fontSize: 12 }}>{p.health.enabled ? (p.health.type === 'http' ? `${p.health.method} ${p.health.path}` : 'tcp connect') : 'off'}</td>
                  <td>{s ? poolBadge(s.status, s.up, s.total) : poolBadge('unknown', 0, 0)}</td>
                  <td>{s?.rate ?? 0}</td>
                </tr>
              );
            })}</tbody>
          </table>
        )}
      </div>
    </>
  );
}
