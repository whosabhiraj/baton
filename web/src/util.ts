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

/** Runs fn with btn showing it is working: disabled, a spinner, `label` as its text (and every
 *  control in `lock` disabled too). A second click while it runs does nothing. The button gets
 *  its old content back afterwards (controls inside a `[data-done]` element stay disabled).
 *  Returns false when it was already busy. */
export async function busy(btn: HTMLButtonElement, label: string, fn: () => Promise<unknown>, lock: Element[] = []): Promise<boolean> {
  if (btn.dataset.busy) return false;
  btn.dataset.busy = '1';
  const kids = [...btn.childNodes];
  const locked = [btn, ...lock.flatMap((el) => [el, ...el.querySelectorAll('button, input, select, textarea')])].filter(
    (el): el is HTMLButtonElement => 'disabled' in el && !(el as HTMLButtonElement).disabled,
  );
  locked.forEach((el) => (el.disabled = true));
  btn.setAttribute('aria-busy', 'true');
  btn.replaceChildren(h('i', { class: 'spin', 'aria-hidden': 'true' }), label);
  try {
    await fn();
  } finally {
    delete btn.dataset.busy;
    btn.removeAttribute('aria-busy');
    btn.replaceChildren(...kids);
    locked.forEach((el) => (el.disabled = !!el.closest('[data-done]'))); // fn may mark its work done
  }
  return true;
}

let toasts: HTMLElement | null = null;
/** A short message in the corner that goes away by itself: what just happened. */
export function toast(text: string, kind: 'ok' | 'bad' | 'info' = 'ok', ms = 4000): void {
  if (!toasts?.isConnected) {
    toasts = h('div', { class: 'toasts', role: 'status', 'aria-live': 'polite' });
    document.body.append(toasts);
  }
  const t = h('div', { class: 'toast toast-' + kind }, text);
  toasts.append(t);
  while (toasts.children.length > 4) toasts.firstElementChild?.remove();
  window.setTimeout(() => {
    t.classList.add('gone');
    window.setTimeout(() => t.remove(), 200);
  }, ms);
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

/** The palette, for the canvas and the 3D map (style.css :root holds the same values). */
export const PAL = {
  ground: '#0a0a0b', panel: '#0f0f11', raise: '#17171a', line: '#222225', line2: '#2f2f33',
  text: '#e8e6e1', text2: '#9a9892', text3: '#7f7d78',
  signal: '#d9b45a', go: '#86b596', warn: '#c98f66', stop: '#d27a72', hold: '#a29cc8',
} as const;

export const STATUS_COLOR: Record<string, string> = {
  running: PAL.go, starting: PAL.go, waiting: PAL.signal, blocked: PAL.warn, rate_limited: PAL.warn, stalled: PAL.warn,
  paused: PAL.hold, done: PAL.text2, failed: PAL.stop, killed: PAL.stop, exited: PAL.text3,
};

/** An account's configured colour as a quiet line colour: same hue, low chroma, even lightness,
 *  so loud config colours read as identifiers, not decoration. Mixed in OKLab. */
const toned = new Map<string, string>();
export function lineColor(hex: string | undefined): string {
  const m = /^#?([0-9a-f]{6})$/i.exec((hex ?? '').trim());
  if (!m) return PAL.text3;
  const hit = toned.get(m[1]!);
  if (hit) return hit;
  const n = parseInt(m[1]!, 16);
  const lin = (c: number) => ((c /= 255) <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
  const [r, g, b] = [lin((n >> 16) & 255), lin((n >> 8) & 255), lin(n & 255)];
  const l = Math.cbrt(0.4122214708 * r + 0.5363325363 * g + 0.0514459929 * b);
  const mm = Math.cbrt(0.2119034982 * r + 0.6806995451 * g + 0.1073969566 * b);
  const s = Math.cbrt(0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b);
  let L = 0.2104542553 * l + 0.793617785 * mm - 0.0040720468 * s;
  let A = 1.9779984951 * l - 2.428592205 * mm + 0.4505937099 * s;
  let B = 0.0259040371 * l + 0.7827717662 * mm - 0.808675766 * s;
  const C = Math.hypot(A, B);
  const k = C ? Math.min(C * 0.5, 0.07) / C : 0;
  L = 0.7 + (L - 0.7) * 0.3;
  A *= k;
  B *= k;
  const l3 = (L + 0.3963377774 * A + 0.2158037573 * B) ** 3;
  const m3 = (L - 0.1055613458 * A - 0.0638541728 * B) ** 3;
  const s3 = (L - 0.0894841775 * A - 1.291485548 * B) ** 3;
  const out = (c: number) => Math.round(Math.min(1, Math.max(0, c <= 0.0031308 ? 12.92 * c : 1.055 * c ** (1 / 2.4) - 0.055)) * 255).toString(16).padStart(2, '0');
  const res = '#' + out(4.0767416621 * l3 - 3.3077115913 * m3 + 0.2309699292 * s3) + out(-1.2684380046 * l3 + 2.6097574011 * m3 - 0.3413193965 * s3) + out(-0.0041960771 * l3 - 0.7034186147 * m3 + 1.707614701 * s3);
  toned.set(m[1]!, res);
  return res;
}

/** Account route bullet: a small roundel in the (toned) line colour with a 1-2 letter code. */
export function bullet(a: { id: string; label?: string; color?: string } | undefined, fallback = '?'): HTMLElement {
  const name = (a?.label || a?.id || fallback).replace(/\(.*?\)/g, '').trim();
  const words = name.split(/[\s_-]+/).filter(Boolean);
  const code = (words.length > 1 ? words.map((w) => w[0]).join('').slice(0, 2) : name.slice(0, 1)).toUpperCase();
  const el = h('span', { class: 'bullet', 'aria-hidden': 'true' }, code);
  el.style.background = lineColor(a?.color);
  return el;
}

/** True when a keyboard event comes from somewhere the user is typing. */
export function typing(e: Event): boolean {
  const t = e.target;
  if (!(t instanceof HTMLElement)) return false;
  return t.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(t.tagName);
}
