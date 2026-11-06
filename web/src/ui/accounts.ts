import { api, errText } from '../api';
import { emit, refresh, state } from '../store';
import { dot, h, renderIf, setKids, usageText } from '../util';

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
            h('span', { class: 'row' }, dot(a.color), h('strong', {}, a.label || a.id), h('span', { class: 'muted' }, a.kind)),
            h(
              'span',
              { class: 'row sub' },
              `${a.used}/${a.max_concurrent} in use`,
              a.rate_limited && h('span', { class: 'flag' }, 'rate limited'),
              !a.enabled && h('span', { class: 'flag off' }, 'disabled'),
            ),
            h('span', { class: 'sub muted' }, usageText(a.usage)),
          );
          const cb = h('input', {
            type: 'checkbox',
            checked: a.enabled,
            'aria-label': `Enable ${a.label || a.id}`,
            onchange: () => void toggle(a.id, cb.checked),
          });
          const card = h('div', { class: 'acct' + (on ? ' on' : '') }, main, h('label', { class: 'toggle', title: 'Enabled' }, cb, ' on'));
          card.style.setProperty('--accent', a.color || 'var(--muted)');
          return card;
        }),
      );
    });
  };
}
