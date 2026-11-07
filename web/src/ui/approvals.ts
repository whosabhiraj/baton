import { api, errText, type Approval } from '../api';
import { refresh, state } from '../store';
import { ago, h, renderIf, setKids } from '../util';
import type { Panel } from './overlay';

interface LineupAgent {
  account?: string;
  role: string;
  model?: string;
  effort?: string;
  task: string;
  plan?: { id: string; title: string; acceptance: string }[];
  [k: string]: unknown;
}

interface Result {
  spawned?: { callsign?: string; account?: string; role?: string }[];
  failed?: { account?: string; role?: string; code?: string; error?: string }[];
}

const EFFORTS = ['', 'low', 'medium', 'high'];

/** One editable agent of a lineup; get() returns the edited agent, or null when left out. */
function agentEditor(a: LineupAgent, i: number): { el: HTMLElement; get: () => LineupAgent | null } {
  const include = h('input', { type: 'checkbox', checked: true, 'aria-label': `Include agent ${i + 1}` });
  const acct = h(
    'select',
    { 'aria-label': 'Account' },
    ...state.accounts.map((x) => h('option', { value: x.id, selected: x.id === a.account }, x.label || x.id)),
  );
  if (a.account && !state.accounts.some((x) => x.id === a.account)) acct.append(h('option', { value: a.account, selected: true }, a.account));
  const model = h('input', { type: 'text', value: a.model ?? '', 'aria-label': 'Model', placeholder: 'account default' });
  const effort = h('select', { 'aria-label': 'Effort' }, ...EFFORTS.map((e) => h('option', { value: e, selected: e === (a.effort ?? '') }, e || 'default')));
  const task = h('textarea', { rows: 2, 'aria-label': 'Task' });
  task.value = a.task;
  const plan = a.plan ?? [];
  const el = h(
    'div',
    { class: 'lineup-agent' },
    h('label', { class: 'row' }, include, h('strong', {}, `${i + 1}. ${a.role}`), h('span', { class: 'muted num' }, `${plan.length} step${plan.length === 1 ? '' : 's'}`)),
    h('div', { class: 'lineup-fields' }, acct, model, effort),
    task,
    plan.length > 0 &&
      h('ol', { class: 'lineup-plan' }, ...plan.map((s) => h('li', {}, s.title, h('span', { class: 'muted' }, ' · done when: ' + s.acceptance)))),
  );
  const sync = () => el.classList.toggle('off', !include.checked);
  include.addEventListener('change', sync);
  return {
    el,
    get: () =>
      include.checked
        ? { ...a, account: acct.value, model: model.value.trim(), effort: effort.value, task: task.value.trim() || a.task }
        : null,
  };
}

export function approvalsPanel(): Panel {
  const list = h('div', { class: 'approvals' });
  const err = h('div', { class: 'err', role: 'alert', hidden: true });
  const done = h('div', { class: 'lineup-result', role: 'status', hidden: true });
  const el = h('div', {}, done, list, err);

  async function decide(a: Approval, verb: 'approve' | 'reject', note: string, agents?: LineupAgent[]): Promise<void> {
    err.hidden = true;
    done.hidden = true;
    try {
      const res = await api.decide(a.id, verb, note.trim(), agents);
      const r = (res.result ?? {}) as Result;
      if (a.kind === 'lineup' && verb === 'approve') {
        setKids(
          done,
          h('strong', {}, res.title),
          ...(r.spawned ?? []).map((s) => h('div', {}, `spawned ${s.callsign} on ${s.account} (${s.role})`)),
          ...(r.failed ?? []).map((f) => h('div', { class: 'bad' }, `not spawned: ${f.account} ${f.role}: ${f.code} ${f.error ?? ''}`)),
        );
        done.hidden = false;
      }
    } catch (e) {
      err.textContent = errText(e);
      err.hidden = false;
    }
    await refresh();
  }

  function card(a: Approval): HTMLElement {
    const note = h('input', { type: 'text', 'aria-label': 'Optional note', placeholder: a.kind === 'lineup' ? 'Note (a rejection tells the conductor why)' : 'Optional note' });
    const d = (a.detail ?? {}) as Record<string, unknown>;
    const head = [
      h('div', { class: 'row between' }, h('strong', {}, a.title), h('span', { class: 'badge' }, a.kind)),
      h('div', { class: 'sub muted' }, `requested by ${a.requested_by}, ${ago(a.created_at)} ago` + (a.run_id ? `, run ${a.run_id}` : '')),
    ];
    if (a.kind === 'lineup') {
      const editors = ((d.agents as LineupAgent[] | undefined) ?? []).map(agentEditor);
      const approve = () => {
        const agents = editors.map((e) => e.get()).filter((x): x is LineupAgent => x !== null);
        if (!agents.length) {
          err.textContent = 'Keep at least one agent, or reject the lineup.';
          err.hidden = false;
          return;
        }
        void decide(a, 'approve', note.value, agents);
      };
      return h(
        'section',
        { class: 'approval' },
        ...head,
        typeof d.note === 'string' && d.note && h('p', { class: 'lineup-note' }, d.note),
        ...editors.map((e) => e.el),
        h(
          'div',
          { class: 'row' },
          note,
          h('button', { class: 'primary', onclick: approve, title: 'Spawn the checked agents as edited' }, 'Approve and spawn'),
          h('button', { class: 'danger', onclick: () => void decide(a, 'reject', note.value) }, 'Reject'),
        ),
      );
    }
    if (a.kind === 'access') {
      // detail { kind, summary, agent_id, command, cwd, reason, paths, host ... }: show every string (and string list), skip objects
      const rows: HTMLElement[] = [];
      for (const [k, v] of Object.entries(d)) {
        if (k === 'summary' || k === 'agent_id') continue;
        const val = typeof v === 'string' ? v : Array.isArray(v) && v.every((x) => typeof x === 'string') ? v.join('\n') : null;
        if (!val) continue;
        rows.push(h('dt', {}, k.replace(/_/g, ' ')), h('dd', {}, val));
      }
      return h(
        'section',
        { class: 'approval access' },
        h('div', { class: 'row between' }, h('strong', { class: 'access-label' }, 'Agent asks for access'), h('span', { class: 'badge' }, a.kind)),
        h('div', {}, typeof d.summary === 'string' && d.summary ? d.summary : a.title),
        h('div', { class: 'sub muted' }, `${a.title}, ${ago(a.created_at)} ago` + (a.run_id ? `, run ${a.run_id}` : '')),
        rows.length > 0 && h('dl', { class: 'access-detail' }, ...rows),
        h(
          'div',
          { class: 'row' },
          note,
          h('button', { class: 'primary', onclick: () => void decide(a, 'approve', note.value) }, 'Approve'),
          h('button', { class: 'danger', onclick: () => void decide(a, 'reject', note.value) }, 'Reject'),
        ),
      );
    }
    const detail = Object.keys(d).length ? JSON.stringify(d, null, 1) : '';
    return h(
      'section',
      { class: 'approval' },
      ...head,
      detail && h('pre', { class: 'detail' }, detail),
      h(
        'div',
        { class: 'row' },
        note,
        h('button', { class: 'primary', onclick: () => void decide(a, 'approve', note.value) }, 'Approve'),
        h('button', { class: 'danger', onclick: () => void decide(a, 'reject', note.value) }, 'Reject'),
      ),
    );
  }

  return {
    title: 'Pending approvals',
    el,
    update() {
      renderIf(list, JSON.stringify(state.approvals), () => {
        if (!state.approvals.length) return setKids(list, h('p', { class: 'muted' }, 'Nothing is waiting for approval.'));
        setKids(list, ...state.approvals.map(card));
      });
    },
  };
}
