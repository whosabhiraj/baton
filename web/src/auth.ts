import { h } from './util';

const KEY = 'baton.token';

/** Sheet named by #open=... (e.g. the conductor chat: #open=chat), read once by initToken. */
export let startSheet = '';

/** Read #token=... (and #open=...) from the URL once, store the token, strip the fragment. */
export function initToken(): string {
  const p = new URLSearchParams(location.hash.replace(/^#/, ''));
  const t = p.get('token');
  startSheet = p.get('open') ?? '';
  if (t) localStorage.setItem(KEY, t);
  if (t || startSheet) history.replaceState(null, '', location.pathname + location.search);
  return getToken();
}

export function getToken(): string {
  return localStorage.getItem(KEY) ?? '';
}

export function setToken(t: string): void {
  localStorage.setItem(KEY, t.trim());
}

/** Full-screen paste box. Calls onSave after the token is stored. */
export function loginBox(msg: string, onSave: () => void): HTMLElement {
  const input = h('input', { id: 'tok', type: 'password', autocomplete: 'off', spellcheck: 'false', required: true });
  const form = h(
    'form',
    {
      class: 'login',
      onsubmit: (e: Event) => {
        e.preventDefault();
        if (!input.value.trim()) return;
        setToken(input.value);
        onSave();
      },
    },
    h('h1', {}, 'Baton'),
    h('p', { role: 'alert' }, msg),
    h('label', { for: 'tok' }, 'Admin token (printed when the hub starts)'),
    input,
    h('button', { type: 'submit', class: 'primary' }, 'Save token'),
  );
  queueMicrotask(() => input.focus());
  return h('div', { class: 'login-wrap' }, form);
}
