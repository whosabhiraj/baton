import { accountById, emit, select, state, type Run } from '../store';
import { gsap } from 'gsap';
import { I, icon } from '../icons';
import { ago, bullet, ENDED, h, renderIf, setKids } from '../util';

const calm = matchMedia('(prefers-reduced-motion: reduce)');
const reached = new Map<string, number>(); // run id -> steps done last render

export function stepProgress(r: Run): { done: number; total: number } {
  const total = r.plan?.length ?? 0;
  const done = Object.values(r.step_states ?? {}).filter((s) => s === 'done').length;
  return { done, total };
}

export function currentStep(r: Run): string {
  const plan = r.plan ?? [];
  for (const want of ['active', 'blocked', 'pending'] as const) {
    const s = plan.find((p) => r.step_states?.[p.id] === want);
    if (s) return s.title;
  }
  return plan.length ? 'all steps done' : '-';
}

export function visibleRuns(): Run[] {
  return state.runs.filter((r) => !state.accountFilter || r.account_id === state.accountFilter);
}

export function statusBadge(status: string): HTMLElement {
  return h('span', { class: 'badge st-' + status }, status.replace('_', ' '));
}

/** The route strip: the plan as stations on the agent's line. Done stops filled, the current
 *  stop lit ("you are here"), blocked stops orange. When a stop is newly reached it pulses once. */
export function route(r: Run, color: string | undefined, cls = ''): HTMLElement {
  const plan = r.plan ?? [];
  const { done, total } = stepProgress(r);
  const here = plan.find((p) => r.step_states?.[p.id] === 'active' || r.step_states?.[p.id] === 'blocked');
  const el = h(
    'span',
    { class: 'route ' + cls, role: 'img', 'aria-label': total ? `${done} of ${total} steps done` : 'no plan yet' },
    ...plan.map((s) => {
      const st = r.step_states?.[s.id] ?? 'pending';
      return h('i', { class: `stn stn-${st}${s === here ? ' here' : ''}`, title: `${s.title} (${st})` });
    }),
  );
  el.style.setProperty('--c', color || '#a9adb9');
  const before = reached.get(r.run_id + cls);
  reached.set(r.run_id + cls, done);
  if (before !== undefined && done > before && !calm.matches) {
    const stn = el.querySelectorAll('.stn-done')[done - 1];
    if (stn) requestAnimationFrame(() => gsap.fromTo(stn, { scale: 2.4 }, { scale: 1, duration: 0.55, ease: 'power3.out' }));
  }
  return el;
}

export function runsList(host: HTMLElement): () => void {
  const head = h('div', { class: 'list-head' });
  const body = h('tbody');
  const table = h(
    'table',
    { class: 'runs' },
    h('caption', { class: 'sr' }, 'Agents. Enter selects a row; arrow keys move between agents.'),
    h(
      'thead',
      {},
      h('tr', {}, ...['Agent', 'Status', 'Account', 'Route', '', 'Last event'].map((t) => h('th', { scope: 'col' }, t))),
    ),
    body,
  );
  setKids(host, head, h('div', { class: 'table-wrap' }, table));

  return () => {
    const now = Date.now();
    const rows = visibleRuns();
    const f = accountById(state.accountFilter ?? undefined);
    renderIf(head, state.accountFilter ?? '', () =>
      setKids(
        head,
        h('h2', {}, 'Agents'),
        f &&
          h(
            'button',
            { class: 'chip', title: 'Show all agents', onclick: () => { state.accountFilter = null; emit(); } },
            bullet(f),
            f.label || f.id,
            icon(I.close, 14),
          ),
      ),
    );
    const sig = JSON.stringify([state.selected, rows.map((r) => [r.run_id, r.status, r.callsign, r.step_states, r.open_questions.length, ago(r.last_event_ts, now), r.account_id, r.plan?.length])]);
    renderIf(body, sig, () => {
      const focused = document.activeElement instanceof HTMLElement ? document.activeElement.dataset.id : undefined;
      if (!rows.length) {
        setKids(body, h('tr', {}, h('td', { colspan: 6, class: 'muted empty' }, state.loaded ? 'No agents yet. Press S to spawn one.' : 'Loading...')));
        return;
      }
      setKids(body, ...rows.map((r) => row(r, now)));
      if (focused) body.querySelector<HTMLElement>(`tr[data-id="${CSS.escape(focused)}"]`)?.focus();
    });
  };
}

function row(r: Run, now: number): HTMLElement {
  const a = accountById(r.account_id);
  const { done, total } = stepProgress(r);
  const pick = () => select(r.run_id);
  const tr = h(
    'tr',
    {
      class: 'run' + (state.selected === r.run_id ? ' sel' : '') + (ENDED.has(r.status) ? ' ended' : ''),
      tabindex: 0,
      'data-id': r.run_id,
      'aria-selected': state.selected === r.run_id,
      onclick: pick,
      onkeydown: (e: KeyboardEvent) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          pick();
        }
      },
    },
    h('td', {}, h('span', { class: 'callsign' }, r.callsign || r.agent_id), h('div', { class: 'sub muted' }, [r.role, r.model || r.kind].filter(Boolean).join(' · '))),
    h('td', {}, statusBadge(r.status)),
    h('td', {}, h('span', { class: 'row acct-cell' }, bullet(a, r.account_id), a?.label || r.account_id || '-')),
    h('td', { class: 'plan-cell' }, h('div', { class: 'row' }, route(r, a?.color), h('span', { class: 'num muted' }, total ? `${done}/${total}` : '')), h('div', { class: 'sub step' }, currentStep(r))),
    h('td', {}, r.open_questions.length > 0 ? h('span', { class: 'qmark', title: `${r.open_questions.length} open question(s)` }, '?' + (r.open_questions.length > 1 ? r.open_questions.length : '')) : ''),
    h('td', { class: 'num muted' }, ago(r.last_event_ts, now)),
  );
  return tr;
}
