// Conductor chat sheet (A17/A18): the same conversation as the terminal chat, live over
// GET /api/conductor/stream. Built when the sheet opens; the stream closes with it.
import { api, errText } from '../api';
import { getToken } from '../auth';
import { I, icon } from '../icons';
import { accountById, state } from '../store';
import { h, renderIf, setKids } from '../util';
import type { Panel } from './overlay';

export interface ConductorEntry {
  n: number;
  ts: string;
  dir: 'in' | 'out' | 'hub';
  from?: 'human' | 'hub';
  text?: string;
  event?: string;
  msg?: Record<string, unknown>;
}

type Kind = 'you' | 'hub' | 'conductor' | 'tool' | 'error' | 'meta';
export interface Line {
  kind: Kind;
  text: string;
}

function clip(s: string, n: number): string {
  s = s.trim();
  const i = s.indexOf('\n');
  if (i >= 0) s = s.slice(0, i) + ' ...';
  return s.length > n ? s.slice(0, n) + '...' : s;
}

const PRIO = ['agent_id', 'account', 'role', 'model', 'effort', 'qid', 'mode', 'file_path', 'pattern'];

function toolSummary(name: string, input: Record<string, unknown> = {}): string {
  name = name.replace(/^mcp__baton__/, '');
  const keys = Object.keys(input).sort((a, b) => {
    const pa = PRIO.indexOf(a), pb = PRIO.indexOf(b);
    if ((pa < 0) !== (pb < 0)) return pa < 0 ? 1 : -1;
    return pa < 0 ? a.localeCompare(b) : pa - pb;
  });
  const parts: string[] = [];
  for (const k of keys) {
    if (parts.length === 3) {
      parts.push('...');
      break;
    }
    const v = input[k];
    if (typeof v === 'string') parts.push(`${k}=${clip(v, 40)}`);
    else if (typeof v === 'number' || typeof v === 'boolean') parts.push(`${k}=${v}`);
  }
  return parts.length ? `${name} ${parts.join(' ')}` : name;
}

function resultText(c: unknown): string {
  if (typeof c === 'string') return c;
  if (Array.isArray(c)) return c.map((p) => (p && typeof p === 'object' && 'text' in p ? String((p as { text: unknown }).text) : '')).join(' ');
  return JSON.stringify(c);
}

interface Block {
  type?: string;
  text?: string;
  name?: string;
  input?: Record<string, unknown>;
  is_error?: boolean;
  content?: unknown;
}

/** Mirrors internal/chat.Render. */
export function renderEntry(e: ConductorEntry): Line[] {
  if (e.dir === 'in') return [{ kind: e.from === 'hub' ? 'hub' : 'you', text: e.text ?? '' }];
  if (e.dir === 'hub') {
    const bad = e.event === 'error' || e.event === 'exited';
    return [{ kind: bad ? 'error' : 'meta', text: `conductor ${e.event}: ${e.text ?? ''}` }];
  }
  const m = (e.msg ?? {}) as { type?: string; subtype?: string; is_error?: boolean; result?: string; duration_ms?: number; total_cost_usd?: number; text?: string; message?: { content?: Block[] } };
  const out: Line[] = [];
  switch (m.type) {
    case 'assistant':
      for (const b of m.message?.content ?? []) {
        if (b.type === 'text' && b.text?.trim()) out.push({ kind: 'conductor', text: b.text.trim() });
        else if (b.type === 'tool_use') out.push({ kind: 'tool', text: toolSummary(b.name ?? '?', b.input) });
      }
      break;
    case 'user':
      for (const b of m.message?.content ?? []) if (b.type === 'tool_result' && b.is_error) out.push({ kind: 'error', text: clip(resultText(b.content), 300) });
      break;
    case 'result': {
      if (m.is_error || (m.subtype && m.subtype !== 'success')) return [{ kind: 'error', text: 'turn failed: ' + (m.subtype ?? '') + (m.result ? ': ' + clip(m.result, 300) : '') }];
      let s = ((m.duration_ms ?? 0) / 1000).toFixed(1) + 's';
      if (m.total_cost_usd && m.total_cost_usd > 0) s += ` · $${m.total_cost_usd.toFixed(4)}`;
      out.push({ kind: 'meta', text: s });
      break;
    }
    case 'raw':
      out.push({ kind: 'meta', text: clip(m.text ?? '', 300) });
  }
  return out;
}

interface Info {
  running: boolean;
  wanted: boolean;
  account?: string;
  model?: string;
  effort?: string;
  last_error?: string;
  problem?: string;
}

export function chatPanel(): Panel {
  const status = h('div', { class: 'chat-status' });
  const log = h('div', { class: 'chat-log', role: 'log', 'aria-live': 'polite', 'aria-label': 'Conversation' });
  const input = h('textarea', { rows: 3, 'aria-label': 'Message for the conductor', placeholder: 'Message the conductor (Enter sends, Shift+Enter new line)' });
  const send = h('button', { class: 'primary', title: 'Send (Enter)' }, icon(I.send), 'Send');
  const err = h('div', { class: 'err', role: 'alert', hidden: true });
  const el = h('div', { class: 'chat' }, status, log, err, h('div', { class: 'chat-input' }, input, send));

  let es: EventSource | null = null;
  let last = 0;
  let info: Info | null = null;
  let prevKind: Kind | '' = '';

  const showErr = (msg: string) => {
    err.textContent = msg;
    err.hidden = !msg;
  };

  function append(lines: Line[]): void {
    const atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 40;
    for (const l of lines) {
      // the conductor's turn starts with its first text or tool line after a message
      const firstOfTurn = (l.kind === 'conductor' || l.kind === 'tool') && (prevKind === 'you' || prevKind === 'hub' || prevKind === '');
      if (l.kind === 'you' || l.kind === 'hub' || firstOfTurn) log.append(h('div', { class: 'chat-gap' }));
      if (l.kind === 'you') log.append(h('div', { class: 'chat-who' }, 'you'));
      if (firstOfTurn) log.append(h('div', { class: 'chat-who cond' }, 'conductor'));
      log.append(h('div', { class: 'chat-line k-' + l.kind }, l.kind === 'tool' ? '↳ ' + l.text : l.kind === 'meta' ? '· ' + l.text : l.text));
      prevKind = l.kind;
    }
    if (atBottom) log.scrollTop = log.scrollHeight;
  }

  async function loadInfo(): Promise<void> {
    try {
      const r = await api.conductor();
      info = r.conductor;
      showErr('');
    } catch (e) {
      info = null;
      showErr(errText(e));
    }
    paintStatus();
  }

  function connect(): void {
    es?.close();
    const src = new EventSource(`/api/conductor/stream?token=${encodeURIComponent(getToken())}&after=${last}`);
    es = src;
    src.addEventListener('entry', (e) => {
      try {
        const en = JSON.parse((e as MessageEvent<string>).data) as ConductorEntry;
        if (en.n <= last) return;
        last = en.n;
        append(renderEntry(en));
        if (en.dir === 'hub') void loadInfo();
      } catch {
        /* skip a bad frame */
      }
    });
    // EventSource reconnects by itself with Last-Event-ID (the entry n); nothing to do on error.
  }

  async function doSend(): Promise<void> {
    const text = input.value.trim();
    if (!text) return;
    send.disabled = true;
    try {
      await api.conductorSend(text);
      input.value = '';
      showErr('');
    } catch (e) {
      showErr(errText(e));
    } finally {
      send.disabled = false;
      input.focus();
    }
  }

  async function control(verb: 'start' | 'stop'): Promise<void> {
    try {
      await api.conductorControl(verb);
    } catch (e) {
      showErr(errText(e));
    }
    void loadInfo();
  }

  function paintStatus(): void {
    const c = state.conductor;
    const acct = accountById(info?.account ?? c.account);
    const who = [acct?.label ?? info?.account ?? c.account, info?.model ?? c.model, info?.effort].filter(Boolean).join(' · ');
    renderIf(status, JSON.stringify([c.state, who, info]), () =>
      setKids(
        status,
        h('span', { class: 'stat cond cond-' + c.state }, h('i', { class: 'cdot', 'aria-hidden': 'true' }), h('span', { class: 'v' }, c.state), who && h('span', { class: 'muted' }, ' ' + who)),
        info?.problem && !info.running ? h('span', { class: 'warn-text' }, info.problem) : null,
        info?.last_error ? h('span', { class: 'warn-text', title: info.last_error }, 'last error: ' + clip(info.last_error, 60)) : null,
        info &&
          (info.running
            ? h('button', { class: 'quiet', onclick: () => void control('stop'), title: 'Stop the conductor (agents keep running)' }, icon(I.stop), 'Stop')
            : h('button', { class: 'quiet', onclick: () => void control('start') }, icon(I.play), 'Start')),
      ),
    );
  }

  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      void doSend();
    }
  });
  send.addEventListener('click', () => void doSend());

  void loadInfo();
  connect();
  append([]);

  return {
    title: 'Conductor',
    el,
    update: paintStatus,
    close: () => es?.close(),
  };
}
