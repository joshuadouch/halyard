import { useEffect, useState } from 'react';
import { BrowserRouter, Navigate, NavLink, Route, Routes, useLocation } from 'react-router-dom';
import { get, post, setUnauthorizedHandler, User } from './api';
import { Badge, Icon, Logo, ToastProvider } from './ui';
import Login from './pages/Login';
import Dashboard from './pages/Dashboard';
import Hosts from './pages/Hosts';
import HostEditor from './pages/HostEditor';
import Pools from './pages/Pools';
import PoolDetail from './pages/PoolDetail';
import VIPs from './pages/VIPs';
import Certificates from './pages/Certificates';
import Cloudflare from './pages/Cloudflare';
import Nodes from './pages/Nodes';
import History from './pages/History';
import Settings from './pages/Settings';
import AccessLists from './pages/AccessLists';

export interface Me { user: User; node: string; role: string; version: string; has_password: boolean }

const nav = [
  ['Overview'], ['/', 'dash', 'Dashboard'],
  ['Traffic'], ['/hosts', 'host', 'Hosts'], ['/pools', 'pool', 'Pools'], ['/vips', 'vip', 'VIPs'],
  ['Security'], ['/certificates', 'cert', 'Certificates'], ['/access', 'lock', 'Access lists'],
  ['Platform'], ['/cloudflare', 'cloud', 'Cloudflare'], ['/nodes', 'node', 'Nodes'], ['/history', 'hist', 'Config history'], ['/settings', 'set', 'Settings'],
];

const titles: Record<string, string> = {
  '': 'Dashboard', hosts: 'Hosts', pools: 'Pools', vips: 'VIPs', certificates: 'Certificates', access: 'Access lists',
  cloudflare: 'Cloudflare', nodes: 'Nodes', history: 'Config history', settings: 'Settings',
};

function Shell({ me, onLogout }: { me: Me; onLogout: () => void }) {
  const loc = useLocation();
  const section = loc.pathname.split('/')[1] || '';
  return (
    <>
      <aside className="side">
        <div className="brand"><Logo />Halyard<span className="badge n" style={{ marginLeft: 'auto', fontSize: 10 }}>{me.version}</span></div>
        <nav className="nav">
          {nav.map((n) => n.length === 1
            ? <div key={n[0]} className="navlabel">{n[0]}</div>
            : <NavLink key={n[0]} to={n[0]} end={n[0] === '/'} className={({ isActive }) => (isActive ? 'on' : '')}>
                <Icon name={n[1]} />{n[2]}
              </NavLink>)}
        </nav>
        <div className="foot">
          <div className="node"><span className="dot" /><b style={{ color: '#e6edf6', fontWeight: 600 }}>{me.node}</b>
            <Badge kind={me.role === 'primary' ? 'a' : 'n'}>{me.role.toUpperCase()}</Badge></div>
          <div className="node" style={{ marginTop: 10 }}>
            <span className="muted">{me.user.username}</span>
            <button className="btn s ghost" style={{ marginLeft: 'auto' }} onClick={onLogout} title="Sign out"><Icon name="out" size={13} /></button>
          </div>
        </div>
      </aside>
      <div className="main">
        <div className="top">
          <div className="crumb"><b>{titles[section] ?? ''}</b></div>
          {me.role !== 'primary' && <Badge kind="w">Standby node: read only</Badge>}
          <div style={{ marginLeft: 'auto' }} className="applybar"><span className="dot" />Live</div>
          <div className="avatar" title={me.user.username}>{me.user.username.slice(0, 2).toUpperCase()}</div>
        </div>
        <div className="content">
          <Routes>
            <Route path="/" element={<Dashboard me={me} />} />
            <Route path="/hosts" element={<Hosts />} />
            <Route path="/hosts/:id" element={<HostEditor />} />
            <Route path="/pools" element={<Pools />} />
            <Route path="/pools/:id" element={<PoolDetail />} />
            <Route path="/vips" element={<VIPs />} />
            <Route path="/certificates" element={<Certificates />} />
            <Route path="/access" element={<AccessLists />} />
            <Route path="/cloudflare" element={<Cloudflare />} />
            <Route path="/nodes" element={<Nodes />} />
            <Route path="/history" element={<History />} />
            <Route path="/settings" element={<Settings me={me} />} />
            <Route path="*" element={<Navigate to="/" />} />
          </Routes>
        </div>
      </div>
    </>
  );
}

export default function App() {
  const [me, setMe] = useState<Me | null | undefined>(undefined);
  const load = () => get<Me>('/me').then(setMe).catch(() => setMe(null));
  useEffect(() => {
    setUnauthorizedHandler(() => setMe(null));
    load();
  }, []);
  if (me === undefined) return null;
  return (
    <ToastProvider>
      <BrowserRouter>
        {me ? <Shell me={me} onLogout={() => post('/logout').finally(() => setMe(null))} /> : <Login onDone={load} />}
      </BrowserRouter>
    </ToastProvider>
  );
}
