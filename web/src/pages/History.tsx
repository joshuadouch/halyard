import { useState } from 'react';
import { fmtDate, fmtTime, get, post, Revision } from '../api';
import { Badge, ErrorBox, Icon, Loading, useLoad, useToast } from '../ui';

// Minimal line diff (LCS) for config previews.
function diff(a: string, b: string): { t: ' ' | '-' | '+'; l: string }[] {
  const x = a.split('\n'), y = b.split('\n');
  const n = x.length, m = y.length;
  if (n * m > 4_000_000) return y.map((l) => ({ t: ' ', l }));
  const dp: number[][] = Array.from({ length: n + 1 }, () => new Array(m + 1).fill(0));
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) dp[i][j] = x[i] === y[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
  const out: { t: ' ' | '-' | '+'; l: string }[] = [];
  let i = 0, j = 0;
  while (i < n && j < m) {
    if (x[i] === y[j]) { out.push({ t: ' ', l: x[i] }); i++; j++; }
    else if (dp[i + 1][j] >= dp[i][j + 1]) out.push({ t: '-', l: x[i++] });
    else out.push({ t: '+', l: y[j++] });
  }
  while (i < n) out.push({ t: '-', l: x[i++] });
  while (j < m) out.push({ t: '+', l: y[j++] });
  return out;
}

function Diff({ a, b }: { a: string; b: string }) {
  const d = diff(a, b);
  // Show changed lines with 3 lines of context.
  const keep = new Set<number>();
  d.forEach((x, i) => { if (x.t !== ' ') for (let k = i - 3; k <= i + 3; k++) keep.add(k); });
  if (!keep.size) return <div className="muted">No changes to haproxy.cfg in this revision.</div>;
  const rows: JSX.Element[] = [];
  let gap = false;
  d.forEach((x, i) => {
    if (!keep.has(i)) { if (!gap) rows.push(<span key={i} className="c">  …</span>); gap = true; return; }
    gap = false;
    rows.push(<span key={i} className={x.t === '+' ? 'ins' : x.t === '-' ? 'del' : ''}>{x.t} {x.l}{'\n'}</span>);
  });
  return <div className="code mono diff">{rows}</div>;
}

export default function History() {
  const toast = useToast();
  const { data, error, reload } = useLoad(() => get<Revision[]>('/revisions'));
  const [sel, setSel] = useState<{ revision: Revision; previous_haproxy: string } | null>(null);
  const [err, setErr] = useState<unknown>(null);
  if (error) return <ErrorBox error={error} />;
  if (!data) return <Loading />;
  const open = (id: number) => get('/revisions/' + id).then(setSel).catch(setErr);
  const rollback = async (id: number) => {
    if (!confirm(`Restore hosts, pools and VIPs to revision ${id}? This applies immediately.`)) return;
    try { const r = await post('/revisions/' + id + '/rollback'); toast(`Rolled back (new rev ${r.revision})`); reload(); setSel(null); } catch (e) { setErr(e); }
  };
  return (
    <>
      <div className="head"><div><h1>Config history</h1><div className="sub">Every apply is recorded with who made it and the exact config. Roll back to any applied revision.</div></div></div>
      <ErrorBox error={err} />
      <div className="row" style={{ gap: 16, alignItems: 'flex-start' }}>
        <div className="card" style={{ flex: 1, minWidth: 0 }}>
          <table>
            <thead><tr><th>Rev</th><th>When</th><th>By</th><th>Change</th><th>Status</th></tr></thead>
            <tbody>{data.map((r) => (
              <tr key={r.id} className="click" onClick={() => open(r.id)} style={sel?.revision.id === r.id ? { background: '#131c2b' } : undefined}>
                <td className="mono">{r.id}</td><td className="muted">{fmtDate(r.time)} {fmtTime(r.time)}</td><td>{r.author}</td>
                <td style={{ whiteSpace: 'normal' }}>{r.message}</td>
                <td>{r.status === 'applied' ? <Badge kind="g">Applied</Badge> : <Badge kind="b">Rejected</Badge>}</td>
              </tr>))}</tbody>
          </table>
        </div>
        {sel && (
          <div className="card panel" style={{ flex: 1.3, minWidth: 0 }}>
            <div className="ch"><h3>Revision {sel.revision.id}</h3>
              <div className="row" style={{ gap: 8 }}>
                {sel.revision.status === 'applied' && <button className="btn s" onClick={() => rollback(sel.revision.id)}>Roll back to this</button>}
                <button className="btn s ghost mobile-only" onClick={() => setSel(null)} aria-label="Close"><Icon name="x" size={14} /></button>
              </div></div>
            <div className="cb">
              {sel.revision.error && <div className="err mono">{sel.revision.error}</div>}
              <Diff a={sel.previous_haproxy} b={sel.revision.haproxy ?? ''} />
            </div>
          </div>
        )}
      </div>
    </>
  );
}
