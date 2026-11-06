import { api, errText } from '../api';
import { refresh, state } from '../store';
import { ago, h, renderIf, setKids } from '../util';
import type { Panel } from './overlay';

export function approvalsPanel(): Panel {
  const list = h('div', { class: 'approvals' });
  const err = h('div', { class: 'err', role: 'alert', hidden: true });
  const el = h('div', {}, list, err);

  async function decide(id: string, verb: 'approve' | 'reject', note: string): Promise<void> {
    err.hidden = true;
    try {
      await api.decide(id, verb, note.trim());
    } catch (e) {
      err.textContent = errText(e);
      err.hidden = false;
    }
    await refresh();
  }

  return {
    title: 'Pending approvals',
    el,
    update() {
      renderIf(list, JSON.stringify(state.approvals), () => {
        if (!state.approvals.length) return setKids(list, h('p', { class: 'muted' }, 'Nothing is waiting for approval.'));
        setKids(
          list,
          ...state.approvals.map((a) => {
            const note = h('input', { type: 'text', 'aria-label': 'Optional note', placeholder: 'Optional note' });
            const detail = a.detail && Object.keys(a.detail).length ? JSON.stringify(a.detail, null, 1) : '';
            return h(
              'section',
              { class: 'approval' },
              h('div', { class: 'row between' }, h('strong', {}, a.title), h('span', { class: 'badge' }, a.kind)),
              h('div', { class: 'sub muted' }, `requested by ${a.requested_by}, ${ago(a.created_at)} ago` + (a.run_id ? `, run ${a.run_id}` : '')),
              detail && h('pre', { class: 'detail' }, detail),
              h(
                'div',
                { class: 'row' },
                note,
                h('button', { class: 'primary', onclick: () => void decide(a.id, 'approve', note.value) }, 'Approve'),
                h('button', { class: 'danger', onclick: () => void decide(a.id, 'reject', note.value) }, 'Reject'),
              ),
            );
          }),
        );
      });
    },
  };
}
