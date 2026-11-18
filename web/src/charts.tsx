import { useEffect, useLayoutEffect, useRef, useState } from 'react';

// Categorical series colours for the dark surface, in fixed order (validated
// for colour-vision deficiency against #111827). Status colours are separate.
export const SERIES = ['#3987e5', '#d95926', '#199e70', '#c98500', '#d55181', '#9085e9'];
export const STATUS = { good: '#22c55e', warn: '#f59e0b', bad: '#ef4444', maint: '#4f8cff', none: '#334155' };

export interface Series { name: string; values: (number | null)[]; color?: string }

// Formatting -----------------------------------------------------------------
export function fmtNum(v: number): string {
  const a = Math.abs(v);
  if (a >= 1e9) return (v / 1e9).toFixed(1) + 'G';
  if (a >= 1e6) return (v / 1e6).toFixed(1) + 'M';
  if (a >= 1e4) return (v / 1e3).toFixed(0) + 'k';
  if (a >= 1e3) return (v / 1e3).toFixed(1) + 'k';
  if (a >= 100 || v === 0) return v.toFixed(0);
  if (a >= 10) return v.toFixed(1).replace(/\.0$/, '');
  return v.toFixed(2).replace(/\.?0+$/, '') || '0';
}
export function fmtBytes(v: number): string {
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (Math.abs(v) >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return (v >= 100 || i === 0 ? v.toFixed(0) : v.toFixed(1)) + ' ' + u[i];
}
export const fmtRate = (v: number) => fmtBytes(v) + '/s';
export const fmtMs = (v: number) => (v >= 1000 ? (v / 1000).toFixed(v >= 10000 ? 0 : 1) + ' s' : Math.round(v) + ' ms');
export const fmtPct = (v: number) => (v >= 10 ? v.toFixed(0) : v.toFixed(1)) + '%';
export function fmtDur(sec: number): string {
  if (!sec || sec < 0) return '0 s';
  const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60);
  if (d) return `${d} d ${h} h`;
  if (h) return `${h} h ${m} min`;
  if (m) return `${m} min`;
  return `${Math.floor(sec)} s`;
}

function niceMax(v: number): number {
  if (v <= 0) return 1;
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  for (const m of [1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10]) if (m * p >= v) return m * p;
  return 10 * p;
}

function timeLabel(t: number, span: number): string {
  const d = new Date(t * 1000);
  if (span > 2 * 86400) return d.toLocaleDateString(undefined, { weekday: 'short', day: 'numeric' });
  return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
}

function useWidth<T extends HTMLElement>() {
  const ref = useRef<T>(null);
  const [w, setW] = useState(0);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    setW(el.clientWidth);
    const ro = new ResizeObserver(() => setW(el.clientWidth));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return [ref, w] as const;
}

// LineChart: time series on one shared y axis, with a crosshair tooltip.
export function LineChart({ t, series, fmt = fmtNum, height = 170, step = 10, max, area }: {
  t: number[]; series: Series[]; fmt?: (v: number) => string; height?: number; step?: number; max?: number; area?: boolean;
}) {
  const [ref, width] = useWidth<HTMLDivElement>();
  const [hover, setHover] = useState<number | null>(null);
  const colored = series.map((s, i) => ({ ...s, color: s.color ?? SERIES[i % SERIES.length] }));
  const has = t.length > 1 && colored.some((s) => s.values.some((v) => v != null));
  let top = max ?? 0;
  if (max == null) for (const s of colored) for (const v of s.values) if (v != null && v > top) top = v;
  top = max ?? niceMax(top * 1.1);
  const ticks = [0, 0.25, 0.5, 0.75, 1];
  const labelW = Math.max(...ticks.map((f) => fmt(top * f).length)) * 6.2;
  const padL = Math.ceil(labelW) + 10, padR = 10, padT = 8, padB = 22;
  const W = Math.max(width, 120), H = height;
  const iw = W - padL - padR, ih = H - padT - padB;
  const t0 = t[0] ?? 0, t1 = t[t.length - 1] ?? 1, span = Math.max(1, t1 - t0);
  const x = (tt: number) => padL + ((tt - t0) / span) * iw;
  const y = (v: number) => padT + ih - (Math.min(v, top) / top) * ih;

  const paths = colored.map((s) => {
    let d = '', run: [number, number][] = [], areas: string[] = [];
    const flush = () => {
      if (run.length > 1 && area) areas.push(`M${run[0][0]} ${padT + ih}` + run.map(([a, b]) => `L${a} ${b}`).join('') + `L${run[run.length - 1][0]} ${padT + ih}Z`);
      run = [];
    };
    s.values.forEach((v, i) => {
      // A gap is a jump much longer than both the step and the previous spacing
      // (the 1 h view starts with minute samples after a restart).
      const prevGap = i > 1 ? t[i - 1] - t[i - 2] : step;
      const gap = i > 0 && t[i] - t[i - 1] > Math.max(step, prevGap) * 2.5;
      if (v == null || gap) flush();
      if (v == null) return;
      const px = x(t[i]).toFixed(1), py = y(v).toFixed(1);
      d += (run.length === 0 ? 'M' : 'L') + px + ' ' + py;
      run.push([+px, +py]);
    });
    flush();
    return { d, areas, color: s.color };
  });

  const onMove = (e: React.PointerEvent) => {
    if (!has) return;
    const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
    const tt = t0 + ((e.clientX - r.left - padL) / iw) * span;
    let best = 0;
    for (let i = 1; i < t.length; i++) if (Math.abs(t[i] - tt) < Math.abs(t[best] - tt)) best = i;
    setHover(best);
  };

  const xt = width < 360 ? [0, 0.5, 1] : [0, 0.25, 0.5, 0.75, 1];
  const last = (s: Series) => { for (let i = s.values.length - 1; i >= 0; i--) if (s.values[i] != null) return s.values[i]; return null; };

  return (
    <div className="lc">
      {colored.length > 1 && (
        <div className="lc-legend">
          {colored.map((s) => {
            const v = hover != null ? s.values[hover] : last(s);
            return <span key={s.name}><i style={{ background: s.color }} />{s.name}<b>{v == null ? '–' : fmt(v)}</b></span>;
          })}
        </div>
      )}
      <div ref={ref} className="lc-plot" style={{ height: H }} onPointerMove={onMove} onPointerDown={onMove} onPointerLeave={() => setHover(null)}>
        {width > 0 && (
          <svg width={W} height={H} role="img" aria-label={colored.map((s) => s.name).join(', ')}>
            {ticks.map((f) => (
              <g key={f}>
                <line x1={padL} x2={W - padR} y1={padT + ih - f * ih} y2={padT + ih - f * ih} stroke="#1f2a3a" />
                <text x={padL - 6} y={padT + ih - f * ih + 3.5} textAnchor="end" className="lc-ax">{fmt(top * f)}</text>
              </g>
            ))}
            {has && xt.map((f) => (
              <text key={f} x={padL + f * iw} y={H - 6} textAnchor={f === 0 ? 'start' : f === 1 ? 'end' : 'middle'} className="lc-ax">{timeLabel(t0 + f * span, span)}</text>
            ))}
            {paths.map((p, i) => p.areas.map((a, j) => <path key={i + '-' + j} d={a} fill={p.color} opacity={0.12} />))}
            {paths.map((p, i) => <path key={i} d={p.d} fill="none" stroke={p.color} strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" />)}
            {hover != null && (
              <g>
                <line x1={x(t[hover])} x2={x(t[hover])} y1={padT} y2={padT + ih} stroke="#5e6d84" strokeDasharray="3 3" />
                {colored.map((s) => s.values[hover] != null && (
                  <circle key={s.name} cx={x(t[hover])} cy={y(s.values[hover]!)} r={4} fill={s.color} stroke="#111827" strokeWidth={2} />
                ))}
              </g>
            )}
          </svg>
        )}
        {!has && <div className="lc-empty">Collecting samples…</div>}
        {hover != null && has && (
          <div className="lc-tip" style={x(t[hover]) > W / 2 ? { right: W - x(t[hover]) + 10 } : { left: x(t[hover]) + 10 }}>
            <div className="dim">{new Date(t[hover] * 1000).toLocaleString(undefined, { weekday: 'short', hour: '2-digit', minute: '2-digit', second: step < 60 ? '2-digit' : undefined })}</div>
            {colored.map((s) => (
              <div key={s.name} className="lc-tiprow"><i style={{ background: s.color }} />{s.name}<b>{s.values[hover] == null ? '–' : fmt(s.values[hover]!)}</b></div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

export function Sparkline({ values, width = 90, height = 24, color = SERIES[0] }: { values: number[]; width?: number; height?: number; color?: string }) {
  if (values.length < 2) return <svg width={width} height={height} />;
  const m = Math.max(1e-9, ...values);
  const pts = values.map((v, i) => `${((i / (values.length - 1)) * (width - 2) + 1).toFixed(1)},${(height - 2 - (v / m) * (height - 4)).toFixed(1)}`).join(' ');
  return (
    <svg width={width} height={height} aria-hidden="true">
      <polyline points={pts} fill="none" stroke={color} strokeWidth={1.5} strokeLinejoin="round" />
    </svg>
  );
}

// State codes from the API: 0 unknown, 1 up, 2 draining, 3 maintenance, 4 down.
export const STATE_NAMES = ['no data', 'up', 'draining', 'maintenance', 'down'];
const STATE_COLORS = [STATUS.none, STATUS.good, STATUS.warn, STATUS.maint, STATUS.bad];

// HealthStrip shows one member's state over time as coloured segments.
export function HealthStrip({ t, states, step }: { t: number[]; states: (number | null)[]; step: number }) {
  const [ref, width] = useWidth<HTMLDivElement>();
  const [tip, setTip] = useState<string | null>(null);
  const n = t.length;
  const t0 = t[0] ?? 0, t1 = (t[n - 1] ?? 1) + step, span = Math.max(1, t1 - t0);
  const segs: { x: number; w: number; s: number; from: number; to: number }[] = [];
  for (let i = 0; i < n; i++) {
    const s = states[i] ?? 0;
    // Each sample lasts until the next, unless there is a gap in the data.
    const spacing = Math.max(step, i > 0 ? t[i] - t[i - 1] : i + 1 < n ? t[i + 1] - t[i] : step);
    const end = i + 1 < n && t[i + 1] - t[i] <= spacing * 2.5 ? t[i + 1] : t[i] + spacing;
    const prev = segs[segs.length - 1];
    if (prev && prev.s === s && Math.abs(prev.to - t[i]) < 1) { prev.to = end; continue; }
    segs.push({ x: 0, w: 0, s, from: t[i], to: end });
  }
  for (const g of segs) { g.x = ((g.from - t0) / span) * width; g.w = Math.max(1, ((g.to - g.from) / span) * width); }
  const fmtT = (x: number) => new Date(x * 1000).toLocaleString(undefined, { weekday: 'short', hour: '2-digit', minute: '2-digit' });
  return (
    <div ref={ref} className="hs" onPointerLeave={() => setTip(null)}>
      {width > 0 && (
        <svg width={width} height={14}>
          <rect x={0} y={0} width={width} height={14} rx={3} fill="#172030" />
          {segs.filter((g) => g.s !== 0).map((g, i) => (
            <rect key={i} x={g.x} y={0} width={g.w} height={14} fill={STATE_COLORS[g.s]}
              onPointerEnter={() => setTip(`${STATE_NAMES[g.s]} · ${fmtT(g.from)} – ${fmtT(g.to)}`)}
              onPointerDown={() => setTip(`${STATE_NAMES[g.s]} · ${fmtT(g.from)} – ${fmtT(g.to)}`)} />
          ))}
        </svg>
      )}
      {tip && <div className="hs-tip">{tip}</div>}
    </div>
  );
}

export function HealthLegend() {
  return (
    <div className="lc-legend">
      {[1, 2, 3, 4].map((s) => <span key={s}><i style={{ background: STATE_COLORS[s] }} />{STATE_NAMES[s]}</span>)}
    </div>
  );
}

// Meter is a labelled usage bar (CPU, memory, disk).
export function Meter({ label, pct, detail }: { label: string; pct: number; detail?: string }) {
  const color = pct >= 90 ? STATUS.bad : pct >= 75 ? STATUS.warn : SERIES[0];
  return (
    <div className="meter">
      <div className="meter-k"><span>{label}</span><b>{fmtPct(pct)}</b></div>
      <div className="bar"><i style={{ width: Math.min(100, Math.max(0, pct)) + '%', background: color }} /></div>
      {detail && <div className="dim" style={{ fontSize: 11.5, marginTop: 4 }}>{detail}</div>}
    </div>
  );
}

export function useInterval(fn: () => void, ms: number) {
  const saved = useRef(fn);
  saved.current = fn;
  useEffect(() => {
    const id = window.setInterval(() => saved.current(), ms);
    return () => window.clearInterval(id);
  }, [ms]);
}
