import { accountById, eventsPerMin, setOverlay, state } from '../store';
import { ENDED, h, renderIf, setKids } from '../util';
import { I, icon } from '../icons';

const CONN_TEXT = { connecting: 'connecting', live: 'live', down: 'offline, retrying', auth: 'not signed in' } as const;

export function topbar(host: HTMLElement, toggleView: () => void, toggleTimeline: () => void): () => void {
  const status = h('span', { class: 'pill', 'aria-live': 'polite' });
  const stats = h('div', { class: 'stats' });
  const actions = h('div', { class: 'actions' });
  const notices = h('div', { class: 'notices', role: 'status' });
  setKids(host, h('span', { class: 'brand' }, h('span', { class: 'brand-mark', 'aria-hidden': 'true' }), 'Baton'), status, stats, actions);
  host.after(notices);

  return () => {
    status.className = 'pill conn-' + state.conn;
    status.textContent = CONN_TEXT[state.conn];
    status.title = state.connNote;

    const live = state.runs.filter((r) => !ENDED.has(r.status));
    const active = state.runs.filter((r) => r.status === 'running').length;
    const c = state.conductor;
    const cacct = accountById(c.account);
    const cText = c.state === 'off' ? 'off' : [c.state, cacct?.label ?? c.account, c.model].filter(Boolean).join(' / ');
    const epm = eventsPerMin();

    renderIf(stats, JSON.stringify([c, cText, state.mode, state.runs.length, live.length, active, epm]), () =>
      setKids(
        stats,
        h('span', { class: 'stat cond cond-' + c.state }, h('i', { class: 'cdot', 'aria-hidden': 'true' }), h('span', { class: 'k' }, 'conductor'), ' ', h('span', { class: 'v' }, cText)),
        stat('mode', state.mode || '-'),
        stat('agents', `${state.runs.length} total / ${active} active`),
        stat('events/min', String(epm)),
      ),
    );

    const pending = state.approvals.length;
    renderIf(actions, JSON.stringify([state.configOk, state.configIssues, pending, state.view, state.timeline]), () =>
      setKids(
        actions,
        !state.configOk &&
          h(
            'button',
            { class: 'warn', title: state.configIssues.map((i) => i.msg).join('\n'), onclick: () => setOverlay('config') },
            `Config problems (${state.configIssues.length})`,
          ),
        h('button', { onclick: () => setOverlay('approvals'), class: pending ? 'quiet has' : 'quiet' }, icon(I.approvals), 'Approvals', h('span', { class: 'count' }, String(pending))),
        h('button', { class: 'quiet', onclick: () => setOverlay('config') }, icon(I.config), 'Config'),
        h('button', { class: 'quiet', onclick: toggleTimeline, title: 'Show or hide the timeline (T)', 'aria-pressed': state.timeline }, icon(I.timeline), 'Timeline'),
        h('button', { class: 'quiet', onclick: toggleView, title: 'Switch between map and list (L)' }, icon(state.view === 'list' ? I.scene : I.list), state.view === 'list' ? 'Map' : 'List'),
        h('button', { class: 'primary', onclick: () => setOverlay('spawn'), title: 'Spawn an agent (S)' }, icon(I.spawn), 'Spawn agent'),
      ),
    );

    renderIf(notices, state.notices.join('|'), () => {
      setKids(notices, ...state.notices.map((n) => h('div', { class: 'notice' }, n)));
      notices.hidden = state.notices.length === 0;
    });
  };
}

function stat(label: string, value: string): HTMLElement {
  return h('span', { class: 'stat' }, h('span', { class: 'k' }, label), ' ', h('span', { class: 'v' }, value));
}
