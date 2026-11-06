import { accountById, emit, select, state, type Run } from '../store';
import { ago, dot, h, renderIf, setKids } from '../util';

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
      h('tr', {}, ...['Agent', 'Status', 'Account', 'Role / model', 'Current step', 'Progress', 'Q', 'Last event'].map((t) => h('th', { scope: 'col' }, t))),
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
            { class: 'chip', onclick: () => { state.accountFilter = null; emit(); } },
            dot(f.color),
            `${f.label || f.id}  x`,
          ),
      ),
    );
    const sig = JSON.stringify([state.selected, rows.map((r) => [r.run_id, r.status, r.callsign, r.step_states, r.open_questions.length, ago(r.last_event_ts, now), r.account_id, r.plan?.length])]);
    renderIf(body, sig, () => {
      const focused = document.activeElement instanceof HTMLElement ? document.activeElement.dataset.id : undefined;
      if (!rows.length) {
        setKids(body, h('tr', {}, h('td', { colspan: 8, class: 'muted empty' }, state.loaded ? 'No agents yet. Press S to spawn one.' : 'Loading...')));
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
      class: 'run' + (state.selected === r.run_id ? ' sel' : ''),
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
    h('td', {}, h('strong', {}, r.callsign || r.agent_id), h('div', { class: 'sub muted' }, r.agent_id)),
    h('td', {}, statusBadge(r.status)),
    h('td', {}, a ? h('span', { class: 'row' }, dot(a.color), a.label || a.id) : (r.account_id ?? '-')),
    h('td', {}, [r.role, r.kind].filter(Boolean).join(' / ') || '-', h('div', { class: 'sub muted' }, r.model ?? '')),
    h('td', { class: 'step' }, currentStep(r)),
    h('td', {}, total ? h('span', { class: 'mini' }, h('progress', { max: total, value: done, 'aria-label': 'Steps done' }), ` ${done}/${total}`) : '-'),
    h('td', {}, r.open_questions.length > 0 ? h('span', { class: 'qmark', title: `${r.open_questions.length} open question(s)` }, '?' + (r.open_questions.length > 1 ? r.open_questions.length : '')) : ''),
    h('td', { class: 'muted' }, ago(r.last_event_ts, now)),
  );
  return tr;
}
