import { api, errText } from '../api';
import { accountById, refresh, ringOf, runById, select, state, type Run, type StoredEvent } from '../store';
import { bullet, clock, ENDED, h, lineColor, renderIf, setKids, usageText } from '../util';
import { route, stepProgress, statusBadge } from './runs';
import { I, icon } from '../icons';

const SHOW = 300;

/** One-line summary of an event payload. */
export function summarize(ev: StoredEvent): string {
  const p = ev.payload as Record<string, unknown>;
  const s = (k: string) => (typeof p[k] === 'string' ? (p[k] as string) : '');
  switch (ev.type) {
    case 'step_start': return `${s('step_id')} ${s('note')}`.trim();
    case 'step_done': return `${s('step_id')}: ${s('summary')}${s('evidence') ? ' [evidence: ' + s('evidence') + ']' : ''}`;
    case 'step_blocked': return `${s('step_id')}: ${s('reason')}`;
    case 'progress': return `${String(p.percent ?? '?')}% ${s('note')}`;
    case 'question': return s('text');
    case 'artifact': return `${s('kind')} ${s('ref')} ${s('note')}`.trim();
    case 'done': return `${s('status')}: ${s('summary')}`;
    case 'error': return s('message');
    case 'activity': return `${s('tool')} ${s('detail')}`;
    case 'exit': return `code ${String(p.code)}${s('diff_stat') ? ', ' + s('diff_stat').split('\n').pop() : ''}`;
    case 'usage': return usageText(p);
    case 'plan': return Array.isArray(p.steps) ? `${p.steps.length} steps` : 'plan confirmed';
    case 'to_agent': {
      const inner = (p.payload ?? {}) as Record<string, unknown>;
      return `${s('type')} from ${String(inner.from ?? 'hub')}: ${String(inner.text ?? inner.qid ?? '')}`.trim();
    }
    default: {
      const t = s('summary') || s('text') || s('message') || s('note') || s('detail') || s('reason');
      return t || (Object.keys(p).length ? JSON.stringify(p).slice(0, 160) : '');
    }
  }
}

export function detailPanel(host: HTMLElement): () => void {
  let builtFor = '';
  let update: (r: Run) => void = () => {};

  return () => {
    const r = runById(state.selected);
    host.hidden = !r;
    document.body.classList.toggle('has-detail', !!r);
    if (!r) {
      builtFor = '';
      host.replaceChildren();
      return;
    }
    if (builtFor !== r.run_id) {
      builtFor = r.run_id;
      update = build(host);
    }
    update(r);
  };
}

function build(host: HTMLElement): (r: Run) => void {
  const header = h('header', { class: 'd-head' });
  const progress = h('div', { class: 'd-progress' });
  const plan = h('ol', { class: 'plan' });
  const questions = h('div', { class: 'questions' });
  const err = h('div', { class: 'err', role: 'alert', hidden: true });
  const artifacts = h('ul', { class: 'artifacts' });
  const diff = h('pre', { class: 'diff', hidden: true });
  const filter = h('select', { 'aria-label': 'Filter events by type', onchange: () => rerenderEvents() });
  const evBox = h('ol', { class: 'events', 'aria-label': 'Event stream' });
  let cur: Run | undefined;

  const fail = (e: unknown) => {
    err.textContent = errText(e);
    err.hidden = false;
  };
  const act = async (fn: () => Promise<unknown>) => {
    err.hidden = true;
    try {
      await fn();
    } catch (e) {
      fail(e);
    }
    await refresh();
  };

  // controls (static: keeps typed text across updates)
  const msg = h('input', { type: 'text', 'aria-label': 'Message to the agent', placeholder: 'Message to the agent' });
  const sendBtn = h('button', { type: 'submit' }, icon(I.send), 'Send');
  const msgForm = h(
    'form',
    {
      class: 'row grow',
      onsubmit: (e: Event) => {
        e.preventDefault();
        const t = msg.value.trim();
        if (!t || !cur) return;
        void act(async () => {
          await api.message(cur!.run_id, t);
          msg.value = '';
        });
      },
    },
    msg,
    sendBtn,
  );
  const pauseBtn = h('button', { type: 'button', onclick: () => cur && void act(() => api.control(cur!.run_id, cur!.status === 'paused' ? 'resume' : 'pause')) }, icon(I.pause), 'Pause');
  const stopBtn = h('button', { type: 'button', onclick: () => cur && void act(() => api.control(cur!.run_id, 'stop')) }, icon(I.stop), 'Stop');
  const killBtn = h(
    'button',
    {
      type: 'button',
      class: 'danger',
      onclick: () => {
        if (cur && window.confirm(`Kill ${cur.callsign || cur.agent_id}? The process is terminated immediately.`)) void act(() => api.control(cur!.run_id, 'kill'));
      },
    },
    icon(I.kill),
    'Kill',
  );
  const controls = h('div', { class: 'controls' }, msgForm, h('div', { class: 'row' }, pauseBtn, stopBtn, killBtn));

  host.replaceChildren(
    header,
    progress,
    h('h3', {}, 'Plan'),
    plan,
    questions,
    controls,
    err,
    h('h3', {}, 'Artifacts'),
    artifacts,
    diff,
    h('div', { class: 'row between' }, h('h3', {}, 'Events'), filter),
    evBox,
  );

  let evSig = '';
  function rerenderEvents(): void {
    evSig = '';
    if (cur) renderEvents(cur);
  }

  function renderEvents(r: Run): void {
    const ring = ringOf(r.run_id);
    const want = filter.value;
    const sig = `${r.run_id}|${ring.length}|${ring[ring.length - 1]?.seq}|${want}`;
    if (sig === evSig) return;
    evSig = sig;
    const types = [...new Set(ring.map((e) => e.type))].sort();
    renderIf(filter, types.join(','), () => {
      setKids(filter, h('option', { value: '' }, 'all types'), ...types.map((t) => h('option', { value: t }, t)));
      filter.value = types.includes(want) ? want : '';
    });
    const shown = ring.filter((e) => !want || e.type === want).slice(-SHOW).reverse();
    setKids(
      evBox,
      ...(shown.length
        ? shown.map((e) => h('li', { class: 'ev ev-' + e.type }, h('time', {}, clock(e.ts)), h('span', { class: 'etype' }, e.type), h('span', { class: 'etext' }, summarize(e))))
        : [h('li', { class: 'muted' }, 'No events loaded.')]),
    );
  }

  return (r) => {
    cur = r;
    const ended = ENDED.has(r.status) || !!r.ended_at;
    const a = accountById(r.account_id);
    const ring = ringOf(r.run_id);
    const branch = r.branch;

    renderIf(header, JSON.stringify([r.callsign, r.status, r.model, r.role, a?.label, a?.color, branch]), () =>
      setKids(
        header,
        h('div', { class: 'row between' }, h('h2', { class: 'callsign' }, r.callsign || r.agent_id), h('button', { class: 'icon-btn', 'aria-label': 'Close panel', title: 'Close (Esc)', onclick: () => select(null) }, icon(I.close))),
        h('div', { class: 'row' }, statusBadge(r.status), h('span', { class: 'row acct-cell' }, bullet(a, r.account_id), a?.label || r.account_id || '')),
        h(
          'dl',
          { class: 'meta' },
          ...[
            ['agent', r.agent_id],
            ['run', r.run_id],
            ['role', r.role],
            ['kind', r.kind],
            ['model', r.model],
            ['branch', typeof branch === 'string' ? branch : ''],
          ]
            .filter(([, v]) => v)
            .flatMap(([k, v]) => [h('dt', {}, k), h('dd', {}, v)]),
        ),
      ),
    );

    const { done, total } = stepProgress(r);
    renderIf(progress, JSON.stringify([done, total, r.usage, r.step_states, a?.color]), () =>
      setKids(
        progress,
        total > 0 && h('div', { class: 'row' }, route(r, a?.color, 'big'), h('span', { class: 'num' }, `${done} of ${total} steps`)),
        h('div', { class: 'num muted sub' }, usageText(r.usage as Record<string, unknown> | undefined)),
      ),
    );

    const evidence = new Map<string, string>();
    for (const e of ring) if (e.type === 'step_done') evidence.set(String(e.payload.step_id), String(e.payload.evidence ?? e.payload.summary ?? ''));
    plan.style.setProperty('--c', lineColor(a?.color));
    renderIf(plan, JSON.stringify([r.plan, r.step_states, [...evidence]]), () =>
      setKids(
        plan,
        ...((r.plan ?? []).length
          ? (r.plan ?? []).map((s) => {
              const st = r.step_states?.[s.id] ?? 'pending';
              return h(
                'li',
                { class: 'step-' + st },
                h('span', { class: 'sbadge' }, st),
                h('strong', {}, s.title),
                s.acceptance && h('div', { class: 'sub muted' }, 'Accept: ' + s.acceptance),
                evidence.get(s.id) && h('div', { class: 'sub' }, 'Evidence: ' + evidence.get(s.id)),
              );
            })
          : [h('li', { class: 'muted' }, 'No plan yet.')]),
      ),
    );

    renderIf(questions, JSON.stringify(r.open_questions.map((q) => q.qid)) + ended, () =>
      setKids(questions, ...r.open_questions.map((q) => questionBox(r, q, ended, act))),
    );

    msg.disabled = sendBtn.disabled = ended;
    const paused = r.status === 'paused';
    if (pauseBtn.dataset.p !== String(paused)) {
      pauseBtn.dataset.p = String(paused);
      pauseBtn.replaceChildren(icon(paused ? I.play : I.pause), paused ? 'Resume' : 'Pause');
    }
    pauseBtn.disabled = ended;
    stopBtn.disabled = ended;
    killBtn.disabled = ended;

    const arts = ring.filter((e) => e.type === 'artifact');
    renderIf(artifacts, arts.map((e) => e.seq).join(','), () =>
      setKids(
        artifacts,
        ...(arts.length
          ? arts.map((e) => h('li', {}, h('span', { class: 'etype' }, String(e.payload.kind)), ' ', h('code', {}, String(e.payload.ref)), e.payload.note ? ' ' + String(e.payload.note) : ''))
          : [h('li', { class: 'muted' }, 'None.')]),
      ),
    );

    const exit = [...ring].reverse().find((e) => e.type === 'exit');
    const stat = exit && typeof exit.payload.diff_stat === 'string' ? exit.payload.diff_stat : '';
    diff.hidden = !stat;
    diff.textContent = stat ? 'Diff stat\n' + stat : '';

    renderEvents(r);
  };
}

function questionBox(r: Run, q: Run['open_questions'][number], ended: boolean, act: (fn: () => Promise<unknown>) => Promise<void>): HTMLElement {
  const input = h('input', { type: 'text', 'aria-label': 'Answer', placeholder: 'Your answer', disabled: ended });
  const send = (text: string) => {
    if (text.trim()) void act(() => api.answer(r.run_id, q.qid, text.trim()));
  };
  return h(
    'form',
    { class: 'question', onsubmit: (e: Event) => { e.preventDefault(); send(input.value); } },
    h('div', {}, h('strong', {}, q.blocking ? 'Blocking question' : 'Question'), ` (${q.qid})`),
    h('p', {}, q.text),
    (q.options?.length ?? 0) > 0 && h('div', { class: 'row wrap' }, ...(q.options ?? []).map((o) => h('button', { type: 'button', disabled: ended, onclick: () => send(o) }, o))),
    h('div', { class: 'row' }, input, h('button', { type: 'submit', class: 'primary', disabled: ended }, 'Answer')),
  );
}
