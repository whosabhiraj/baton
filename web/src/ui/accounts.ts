import { api, errText } from '../api';
import { emit, refresh, state } from '../store';
import { bullet, h, lineColor, renderIf, setKids, usageText } from '../util';

/** Capacity as a mini line: one station per slot, the first `used` filled. */
function slots(a: { used: number; max_concurrent: number; color?: string }): HTMLElement {
  const max = Math.max(0, Math.min(a.max_concurrent, 12));
  const el = h(
    'span',
    { class: 'slots-row', role: 'img', 'aria-label': `${a.used} of ${a.max_concurrent} slots in use` },
    h('span', { class: 'route slots' }, ...Array.from({ length: max }, (_, i) => h('i', { class: 'stn' + (i < a.used ? ' stn-done' : '') }))),
    `${a.used}/${a.max_concurrent}`,
  );
  (el.firstChild as HTMLElement).style.setProperty('--c', lineColor(a.color));
  return el;
}

export function accountsRail(host: HTMLElement): () => void {
  const err = h('div', { class: 'err', role: 'alert', hidden: true });
  const list = h('div', { class: 'acct-list' });
  setKids(host, h('h2', {}, 'Accounts'), list, err);

  async function toggle(id: string, enabled: boolean): Promise<void> {
    err.hidden = true;
    try {
      await api.patchAccount(id, { enabled });
    } catch (e) {
      err.textContent = errText(e);
      err.hidden = false;
    }
    await refresh();
  }

  return () => {
    renderIf(list, JSON.stringify([state.accounts, state.accountFilter]), () => {
      if (!state.accounts.length) return setKids(list, h('p', { class: 'muted' }, 'No accounts configured.'));
      setKids(
        list,
        ...state.accounts.map((a) => {
          const on = state.accountFilter === a.id;
          const main = h(
            'button',
            {
              class: 'acct-main',
              'aria-pressed': on,
              title: on ? 'Click to show all agents' : 'Click to show only this account\'s agents',
              onclick: () => {
                state.accountFilter = on ? null : a.id;
                emit();
              },
            },
            h('span', { class: 'row' }, bullet(a), h('strong', {}, a.label || a.id), h('span', { class: 'muted kind' }, a.kind)),
            h(
              'span',
              { class: 'row sub' },
              slots(a),
              a.rate_limited && h('span', { class: 'flag' }, 'rate limited'),
              !a.enabled && h('span', { class: 'flag off' }, 'disabled'),
            ),
            h('span', { class: 'sub muted usage', title: usageText(a.usage) }, usageText(a.usage)),
          );
          const cb = h('input', {
            type: 'checkbox',
            checked: a.enabled,
            'aria-label': `Enable ${a.label || a.id}`,
            onchange: () => void toggle(a.id, cb.checked),
          });
          return h('div', { class: 'acct' + (on ? ' on' : '') }, main, h('label', { class: 'toggle', title: a.enabled ? 'Enabled: new agents may use it' : 'Disabled: no new agents' }, cb));
        }),
      );
    });
  };
}
