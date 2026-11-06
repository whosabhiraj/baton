// Tiny DOM + formatting helpers shared by the UI modules.

type Child = Node | string | number | null | undefined | false;

export function h<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  attrs: Record<string, unknown> = {},
  ...kids: Child[]
): HTMLElementTagNameMap[K] {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === undefined || v === null) continue;
    if (k.startsWith('aria-')) el.setAttribute(k, String(v)); // aria states need "true"/"false"
    else if (v === false) continue;
    else if (k === 'class') el.className = String(v);
    else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v as EventListener);
    else if (k === 'value' || k === 'checked' || k === 'disabled' || k === 'hidden') (el as unknown as Record<string, unknown>)[k] = v;
    else el.setAttribute(k, v === true ? '' : String(v));
  }
  add(el, kids);
  return el;
}

function add(el: Element, kids: Child[]): void {
  for (const c of kids) {
    if (c === null || c === undefined || c === false) continue;
    el.append(typeof c === 'object' ? c : String(c));
  }
}

export function setKids(el: Element, ...kids: Child[]): void {
  el.replaceChildren();
  add(el, kids);
}

/** Re-render `el` only when `sig` changed since last time. Returns true if it ran. */
const sigs = new WeakMap<Element, string>();
export function renderIf(el: Element, sig: string, fn: () => void): boolean {
  if (sigs.get(el) === sig) return false;
  sigs.set(el, sig);
  fn();
  return true;
}

export const ENDED = new Set(['done', 'failed', 'killed', 'exited']);

export function ago(ts: string | undefined, now = Date.now()): string {
  if (!ts) return '-';
  const t = Date.parse(ts);
  if (Number.isNaN(t)) return '-';
  const s = Math.max(0, Math.round((now - t) / 1000));
  if (s < 60) return s + 's';
  if (s < 3600) return Math.floor(s / 60) + 'm';
  if (s < 86400) return Math.floor(s / 3600) + 'h';
  return Math.floor(s / 86400) + 'd';
}

export function clock(ts: string): string {
  const d = new Date(ts);
  return Number.isNaN(d.getTime()) ? ts : d.toLocaleTimeString([], { hour12: false });
}

export function num(n: unknown): string {
  const v = typeof n === 'number' ? n : 0;
  if (v >= 1e6) return (v / 1e6).toFixed(1) + 'M';
  if (v >= 1e3) return (v / 1e3).toFixed(1) + 'k';
  return String(Math.round(v));
}

export function usageText(u: Record<string, unknown> | undefined): string {
  if (!u) return 'no usage yet';
  const cost = typeof u.cost_usd === 'number' ? ' / $' + u.cost_usd.toFixed(2) : '';
  return `${num(u.input_tokens)} in · ${num(u.output_tokens)} out${cost.replace(' / ', ' · ')}`;
}

/** Account route bullet: a roundel in the line colour with a 1-2 letter code (like a route sign). */
export function bullet(a: { id: string; label?: string; color?: string } | undefined, fallback = '?'): HTMLElement {
  const name = (a?.label || a?.id || fallback).replace(/\(.*?\)/g, '').trim();
  const words = name.split(/[\s_-]+/).filter(Boolean);
  const code = (words.length > 1 ? words.map((w) => w[0]).join('').slice(0, 2) : name.slice(0, 1)).toUpperCase();
  const el = h('span', { class: 'bullet', 'aria-hidden': 'true' }, code);
  const c = a?.color || '#a9adb9';
  el.style.background = c;
  el.style.color = luminance(c) > 0.45 ? '#16171b' : '#f2f1ec';
  return el;
}

function luminance(hex: string): number {
  const m = /^#?([0-9a-f]{6})$/i.exec(hex.trim());
  if (!m) return 0.5;
  const n = parseInt(m[1]!, 16);
  return (0.299 * ((n >> 16) & 255) + 0.587 * ((n >> 8) & 255) + 0.114 * (n & 255)) / 255;
}

export function dot(color: string | undefined): HTMLElement {
  const d = h('span', { class: 'dot', 'aria-hidden': 'true' });
  d.style.background = color || 'var(--muted)';
  return d;
}

/** True when a keyboard event comes from somewhere the user is typing. */
export function typing(e: Event): boolean {
  const t = e.target;
  if (!(t instanceof HTMLElement)) return false;
  return t.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(t.tagName);
}
