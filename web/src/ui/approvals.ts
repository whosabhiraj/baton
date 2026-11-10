import { api, ApiError, errText, type Approval } from '../api';
import { emit, refresh, state } from '../store';
import { ago, busy, h, setKids, toast } from '../util';
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

/** What the approve button says while the hub works on it. */
function workingLabel(a: Approval, agents = 0): string {
  if (a.kind === 'lineup') return `Spawning ${agents} agent${agents === 1 ? '' : 's'}...`;
  if (a.kind === 'merge') return 'Merging...';
  return 'Approving...';
}

export function approvalsPanel(): Panel {
  const list = h('div', { class: 'approvals' });
  const done = h('div', { class: 'lineup-result', role: 'status', hidden: true });
  const empty = h('p', { class: 'muted' }, 'Nothing is waiting for approval.');
  const el = h('div', {}, done, list);
  // One card per approval, kept while it is pending: live updates must not wipe a lineup being
  // edited, a typed note, or a request in flight.
  const cards = new Map<string, HTMLElement>();
  const decided = new Set<string>(); // decided here: gone at once, before the next refresh

  function forget(id: string): void {
    decided.add(id);
    state.approvals = state.approvals.filter((x) => x.id !== id);
    cards.get(id)?.remove();
    cards.delete(id);
    emit(); // top bar count
  }

  async function decide(a: Approval, card: HTMLElement, btn: HTMLButtonElement, verb: 'approve' | 'reject', note: string, agents?: LineupAgent[]): Promise<void> {
    const err = card.querySelector<HTMLElement>('.card-err')!;
    err.hidden = true;
    const label = verb === 'reject' ? 'Rejecting...' : workingLabel(a, agents?.length);
    const ran = await busy(
      btn,
      label,
      async () => {
        card.classList.add('busy');
        try {
          const res = await api.decide(a.id, verb, note.trim(), agents);
          const r = (res.result ?? {}) as Result;
          forget(a.id);
          if (a.kind === 'lineup' && verb === 'approve') {
            const ok = r.spawned ?? [];
            const bad = r.failed ?? [];
            setKids(
              done,
              h('strong', {}, res.title),
              ...ok.map((s) => h('div', {}, `spawned ${s.callsign} on ${s.account} (${s.role})`)),
              ...bad.map((f) => h('div', { class: 'bad' }, `not spawned: ${f.account} ${f.role}: ${f.code} ${f.error ?? ''}`)),
            );
            done.hidden = false;
            toast(`${ok.length} agent${ok.length === 1 ? '' : 's'} spawned` + (bad.length ? `, ${bad.length} failed` : ''), bad.length ? 'bad' : 'ok');
          } else toast(verb === 'reject' ? 'Rejected' : a.kind === 'merge' ? 'Merged' : 'Approved');
        } catch (e) {
          if (e instanceof ApiError && (e.code === 'ALREADY_DECIDED' || e.code === 'NOT_FOUND')) {
            forget(a.id); // decided elsewhere: the terminal, another tab
            toast(e.code === 'NOT_FOUND' ? 'That approval is gone' : 'Already decided (' + e.message.replace(/^approval already /, '') + ')', 'info');
          } else {
            err.textContent = errText(e);
            err.hidden = false;
          }
        } finally {
          card.classList.remove('busy');
        }
      },
      [card],
    );
    if (ran) void refresh();
  }

  function card(a: Approval): HTMLElement {
    const note = h('input', { type: 'text', 'aria-label': 'Optional note', placeholder: a.kind === 'lineup' ? 'Note (a rejection tells the conductor why)' : 'Optional note' });
    const err = h('div', { class: 'err card-err', role: 'alert', hidden: true });
    const d = (a.detail ?? {}) as Record<string, unknown>;
    const self = h('section', { class: 'approval' + (a.kind === 'access' ? ' access' : '') });
    let approve = (btn: HTMLButtonElement) => void decide(a, self, btn, 'approve', note.value);
    let approveText = a.kind === 'merge' ? 'Approve and merge' : 'Approve';
    let body: (HTMLElement | false | '')[];

    if (a.kind === 'lineup') {
      const editors = ((d.agents as LineupAgent[] | undefined) ?? []).map(agentEditor);
      approve = (btn) => {
        const agents = editors.map((e) => e.get()).filter((x): x is LineupAgent => x !== null);
        if (!agents.length) {
          err.textContent = 'Keep at least one agent, or reject the lineup.';
          err.hidden = false;
          return;
        }
        void decide(a, self, btn, 'approve', note.value, agents);
      };
      approveText = 'Approve and spawn';
      body = [typeof d.note === 'string' && d.note && h('p', { class: 'lineup-note' }, d.note), ...editors.map((e) => e.el)];
    } else if (a.kind === 'access') {
      // detail { kind, summary, agent_id, command, cwd, reason, paths, host ... }: show every string (and string list), skip objects
      const rows: HTMLElement[] = [];
      for (const [k, v] of Object.entries(d)) {
        if (k === 'summary' || k === 'agent_id') continue;
        const val = typeof v === 'string' ? v : Array.isArray(v) && v.every((x) => typeof x === 'string') ? v.join('\n') : null;
        if (!val) continue;
        rows.push(h('dt', {}, k.replace(/_/g, ' ')), h('dd', {}, val));
      }
      body = [rows.length > 0 && h('dl', { class: 'access-detail' }, ...rows)];
    } else {
      const detail = Object.keys(d).length ? JSON.stringify(d, null, 1) : '';
      body = [detail && h('pre', { class: 'detail' }, detail)];
    }

    const ok: HTMLButtonElement = h('button', { class: 'primary', onclick: () => approve(ok) }, approveText);
    const no: HTMLButtonElement = h('button', { class: 'danger', onclick: () => void decide(a, self, no, 'reject', note.value) }, 'Reject');
    const head =
      a.kind === 'access'
        ? [
            h('div', { class: 'row between' }, h('strong', { class: 'access-label' }, 'Agent asks for access'), h('span', { class: 'badge' }, a.kind)),
            h('div', {}, typeof d.summary === 'string' && d.summary ? d.summary : a.title),
            h('div', { class: 'sub muted' }, `${a.title}, ${ago(a.created_at)} ago` + (a.run_id ? `, run ${a.run_id}` : '')),
          ]
        : [
            h('div', { class: 'row between' }, h('strong', {}, a.title), h('span', { class: 'badge' }, a.kind)),
            h('div', { class: 'sub muted' }, `requested by ${a.requested_by}, ${ago(a.created_at)} ago` + (a.run_id ? `, run ${a.run_id}` : '')),
          ];
    setKids(self, ...head, ...body, h('div', { class: 'row' }, note, ok, no), err);
    return self;
  }

  return {
    title: 'Pending approvals',
    el,
    update() {
      const pending = state.approvals.filter((a) => !decided.has(a.id));
      const want = new Set(pending.map((a) => a.id));
      for (const [id, c] of cards) {
        if (!want.has(id) && !c.classList.contains('busy')) {
          c.remove();
          cards.delete(id);
        }
      }
      let prev: Element | null = null;
      for (const a of pending) {
        let c = cards.get(a.id);
        if (!c) cards.set(a.id, (c = card(a)));
        if (prev ? prev.nextElementSibling !== c : list.firstElementChild !== c) {
          if (prev) prev.after(c);
          else list.prepend(c);
        }
        prev = c;
      }
      if (!cards.size) list.replaceChildren(empty);
      else empty.remove();
    },
  };
}
