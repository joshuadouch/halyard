import { useEffect } from 'react';

// On narrow screens, tables with a header row are shown as stacked cards (see
// .rt in style.css). Each cell needs its column name for that, so copy the
// header text onto every cell as data-label. Done centrally so every page,
// including ones added later, gets it without per-table markup.
function label(root: ParentNode) {
  root.querySelectorAll('table').forEach((t) => {
    const head = t.tHead?.rows[0];
    if (!head) return;
    t.classList.add('rt');
    const names: string[] = [];
    for (const th of Array.from(head.cells)) for (let i = 0; i < th.colSpan; i++) names.push(th.textContent?.trim() ?? '');
    for (const body of Array.from(t.tBodies)) for (const tr of Array.from(body.rows)) {
      let col = 0;
      for (const td of Array.from(tr.cells)) {
        const name = td.colSpan > 1 ? '' : names[col] ?? '';
        if (td.getAttribute('data-label') !== name) td.setAttribute('data-label', name);
        col += td.colSpan;
      }
    }
  });
}

export function useResponsiveTables() {
  useEffect(() => {
    const root = document.getElementById('root');
    if (!root) return;
    let queued = false;
    const run = () => { queued = false; label(root); };
    run();
    const mo = new MutationObserver(() => { if (!queued) { queued = true; requestAnimationFrame(run); } });
    mo.observe(root, { childList: true, subtree: true });
    return () => mo.disconnect();
  }, []);
}
