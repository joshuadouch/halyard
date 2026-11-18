import { createContext, ReactNode, useCallback, useContext, useEffect, useRef, useState } from 'react';
import { ApiError } from './api';

const P: Record<string, ReactNode> = {
  dash: <><rect x="3" y="3" width="7" height="9" rx="1" /><rect x="14" y="3" width="7" height="5" rx="1" /><rect x="14" y="12" width="7" height="9" rx="1" /><rect x="3" y="16" width="7" height="5" rx="1" /></>,
  host: <><circle cx="12" cy="12" r="9" /><path d="M3 12h18M12 3a14 14 0 0 1 0 18M12 3a14 14 0 0 0 0 18" /></>,
  pool: <><rect x="3" y="4" width="18" height="6" rx="1.5" /><rect x="3" y="14" width="18" height="6" rx="1.5" /><path d="M7 7h.01M7 17h.01" /></>,
  vip: <><path d="M12 2v6M12 16v6M2 12h6M16 12h6" /><circle cx="12" cy="12" r="4" /></>,
  cert: <><rect x="4" y="10" width="16" height="11" rx="2" /><path d="M8 10V7a4 4 0 0 1 8 0v3" /></>,
  cloud: <path d="M7 18a5 5 0 1 1 1.1-9.9A6 6 0 0 1 19 11a3.5 3.5 0 0 1-1 7z" />,
  node: <><rect x="3" y="4" width="18" height="12" rx="2" /><path d="M8 20h8M12 16v4" /></>,
  hist: <><path d="M3 12a9 9 0 1 0 3-6.7L3 8" /><path d="M3 3v5h5M12 7v5l3 2" /></>,
  set: <><circle cx="12" cy="12" r="3" /><path d="M12 2v3M12 19v3M4.2 4.2l2.1 2.1M17.7 17.7l2.1 2.1M2 12h3M19 12h3M4.2 19.8l2.1-2.1M17.7 6.3l2.1-2.1" /></>,
  pulse: <path d="M3 12h4l3-8 4 16 3-8h4" />,
  plus: <path d="M12 5v14M5 12h14" />,
  check: <path d="M20 6L9 17l-5-5" />,
  x: <path d="M18 6L6 18M6 6l12 12" />,
  sync: <><path d="M21 12a9 9 0 0 1-15.5 6.2L3 16M3 12a9 9 0 0 1 15.5-6.2L21 8" /><path d="M21 3v5h-5M3 21v-5h5" /></>,
  tunnel: <><path d="M3 20V12a9 9 0 0 1 18 0v8" /><path d="M8 20v-7a4 4 0 0 1 8 0v7" /></>,
  globe: <><circle cx="12" cy="12" r="9" /><path d="M3 12h18" /></>,
  lan: <><rect x="9" y="2" width="6" height="5" rx="1" /><rect x="2" y="17" width="6" height="5" rx="1" /><rect x="16" y="17" width="6" height="5" rx="1" /><path d="M12 7v5M5 17v-3h14v3" /></>,
  trash: <><path d="M3 6h18M8 6V4h8v2M6 6l1 14h10l1-14" /></>,
  lock: <><path d="M12 3l8 3v6c0 5-3.5 8-8 9-4.5-1-8-4-8-9V6z" /><path d="M9 12l2 2 4-4" /></>,
  menu: <path d="M4 6h16M4 12h16M4 18h16" />,
  out: <><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9" /></>,
};

export function Icon({ name, size = 16 }: { name: string; size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
      strokeLinecap="round" strokeLinejoin="round" style={{ flex: 'none' }}>{P[name]}</svg>
  );
}

export function Logo() {
  return (
    <div className="logo">
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="#fff" strokeWidth="2" strokeLinecap="round"><path d="M12 3v18M5 8l7-5 7 5M5 16l7 5 7-5" /></svg>
    </div>
  );
}

export function Badge({ kind = 'n', children }: { kind?: string; children: ReactNode }) {
  return <span className={'badge ' + kind}>{children}</span>;
}

export function ExposureBadge({ e }: { e: string }) {
  if (e === 'tunnel') return <Badge kind="o"><Icon name="tunnel" size={11} /> Tunnel</Badge>;
  if (e === 'public') return <Badge kind="v"><Icon name="globe" size={11} /> Public</Badge>;
  return <Badge kind="n"><Icon name="lan" size={11} /> LAN only</Badge>;
}

export function Toggle({ on, onChange, label, help }: { on: boolean; onChange: (v: boolean) => void; label?: ReactNode; help?: ReactNode }) {
  const t = <button type="button" className={'toggle' + (on ? ' on' : '')} onClick={() => onChange(!on)} aria-pressed={on} />;
  if (!label) return t;
  return (
    <div className="row" style={{ gap: 10, alignItems: 'flex-start', marginBottom: 14 }}>
      {t}
      <div style={{ cursor: 'pointer' }} onClick={() => onChange(!on)}>
        <div>{label}</div>
        {help && <div className="dim" style={{ fontSize: 11.5 }}>{help}</div>}
      </div>
    </div>
  );
}

export function Seg<T extends string>({ value, options, onChange }: { value: T; options: { v: T; label: ReactNode }[]; onChange: (v: T) => void }) {
  return (
    <div className="seg">
      {options.map((o) => (
        <span key={o.v} className={o.v === value ? 'on' : ''} onClick={() => onChange(o.v)}>{o.label}</span>
      ))}
    </div>
  );
}

export function Field({ label, children, help, style }: { label: string; children: ReactNode; help?: ReactNode; style?: React.CSSProperties }) {
  return (
    <div className="field" style={style}>
      <label>{label}</label>
      {children}
      {help && <small className="help">{help}</small>}
    </div>
  );
}

export function Chips({ values, onChange, placeholder, mono }: { values: string[]; onChange: (v: string[]) => void; placeholder?: string; mono?: boolean }) {
  const [draft, setDraft] = useState('');
  const commit = () => {
    const parts = draft.split(/[\s,]+/).map((s) => s.trim()).filter(Boolean);
    if (parts.length) onChange([...values, ...parts.filter((p) => !values.includes(p))]);
    setDraft('');
  };
  return (
    <div className="input chips">
      {values.map((v) => (
        <span key={v} className={'chip' + (mono ? ' mono' : '')}>{v}
          <button type="button" onClick={() => onChange(values.filter((x) => x !== v))}><Icon name="x" size={11} /></button>
        </span>
      ))}
      <input value={draft} placeholder={placeholder} onChange={(e) => setDraft(e.target.value)} onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ',' || e.key === ' ') { e.preventDefault(); commit(); }
          if (e.key === 'Backspace' && !draft && values.length) onChange(values.slice(0, -1));
        }} />
    </div>
  );
}

export function ErrorBox({ error }: { error: unknown }) {
  if (!error) return null;
  const e = error as ApiError;
  return (
    <div className="err">
      {e.message || String(error)}
      {e.output && <div className="mono" style={{ marginTop: 8, color: '#fecaca' }}>{e.output}</div>}
    </div>
  );
}

export function Modal({ title, onClose, children }: { title: string; onClose: () => void; children: ReactNode }) {
  useEffect(() => {
    const h = (e: KeyboardEvent) => e.key === 'Escape' && onClose();
    window.addEventListener('keydown', h);
    return () => window.removeEventListener('keydown', h);
  }, [onClose]);
  return (
    <div className="modal-bg" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="card modal">
        <div className="ch"><h3>{title}</h3><button className="btn s ghost" onClick={onClose}><Icon name="x" size={14} /></button></div>
        <div className="cb">{children}</div>
      </div>
    </div>
  );
}

// Toasts -------------------------------------------------------------------
const ToastCtx = createContext<(msg: string, bad?: boolean) => void>(() => {});
export function ToastProvider({ children }: { children: ReactNode }) {
  const [t, setT] = useState<{ msg: string; bad: boolean } | null>(null);
  const timer = useRef<number>();
  const show = useCallback((msg: string, bad = false) => {
    setT({ msg, bad });
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setT(null), 4000);
  }, []);
  return (
    <ToastCtx.Provider value={show}>
      {children}
      {t && <div className={'toast' + (t.bad ? ' bad' : '')}>{t.msg}</div>}
    </ToastCtx.Provider>
  );
}
export const useToast = () => useContext(ToastCtx);

// Data loading -------------------------------------------------------------
export function useLoad<T>(fn: () => Promise<T>, deps: unknown[] = [], pollMs = 0) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [n, setN] = useState(0);
  useEffect(() => {
    let alive = true;
    const run = () => fn().then((d) => { if (alive) { setData(d); setError(null); } }).catch((e) => alive && setError(e));
    run();
    const id = pollMs ? window.setInterval(run, pollMs) : 0;
    return () => { alive = false; if (id) window.clearInterval(id); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, n]);
  return { data, error, reload: () => setN((x) => x + 1), setData };
}

export function Loading() {
  return <div className="empty">Loading…</div>;
}

export function healthBadge(status: string) {
  const s = status.toUpperCase();
  if (s.startsWith('UP')) return <Badge kind="g">● {status}</Badge>;
  if (s.startsWith('DOWN')) return <Badge kind="b">● {status}</Badge>;
  if (s.startsWith('DRAIN')) return <Badge kind="w">{status}</Badge>;
  if (s.startsWith('MAINT')) return <Badge kind="a">{status}</Badge>;
  if (s === 'NO CHECK') return <Badge kind="n">no check</Badge>;
  return <Badge kind="n">{status || 'unknown'}</Badge>;
}

export function poolBadge(status: string, up: number, total: number) {
  if (status === 'healthy') return <Badge kind="g">● Healthy</Badge>;
  if (status === 'degraded') return <Badge kind="w">● Degraded · {up}/{total} up</Badge>;
  if (status === 'down') return <Badge kind="b">● Down</Badge>;
  if (status === 'empty') return <Badge kind="n">No members</Badge>;
  return <Badge kind="n">Unknown</Badge>;
}
