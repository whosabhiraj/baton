// A small Markdown renderer for the conductor's replies in the chat sheet. Same subset and rules
// as the terminal chat (internal/chat/markdown.go): bold, italic, inline code, strike, links,
// headings, lists, quotes, rules, fenced code. Anything else shows as written. Builds DOM nodes
// only (never innerHTML), so the text can't inject markup. Links open only for http(s)/mailto.
import { h } from './util';

const FENCE = /^\s*(```+|~~~+)/;
const HEADING = /^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$/;
const BULLET = /^(\s*)[-*+]\s+(.*)$/;
const ORDERED = /^(\s*)(\d{1,3}[.)])\s+(.*)$/;
const QUOTE = /^\s{0,3}>\s?(.*)$/;
const HR = /^\s{0,3}(?:(?:-\s*){3,}|(?:\*\s*){3,}|(?:_\s*){3,})$/;

/** Renders text as block elements inside one container. */
export function renderMarkdown(text: string): HTMLElement {
  const root = h('div', { class: 'md' });
  const lines = text.replace(/\r\n/g, '\n').split('\n');
  for (let i = 0; i < lines.length; i++) {
    const ln = lines[i] ?? '';
    const fence = FENCE.exec(ln);
    if (fence) {
      // fenced code: verbatim; ends at the matching fence or the end of the text
      const mark = fence[1] ?? '```';
      const code: string[] = [];
      for (i++; i < lines.length && !(lines[i] ?? '').trim().startsWith(mark); i++) code.push(lines[i] ?? '');
      root.append(h('pre', { class: 'md-pre' }, h('code', {}, code.join('\n'))));
      continue;
    }
    let m: RegExpExecArray | null;
    if (HR.test(ln)) root.append(h('hr', { class: 'md-hr' }));
    else if ((m = HEADING.exec(ln))) root.append(h('div', { class: 'md-h md-h' + (m[1] ?? '#').length }, ...inline(m[2] ?? '')));
    else if ((m = BULLET.exec(ln))) root.append(item(m[1] ?? '', '•', m[2] ?? ''));
    else if ((m = ORDERED.exec(ln))) root.append(item(m[1] ?? '', m[2] ?? '', m[3] ?? ''));
    else if ((m = QUOTE.exec(ln))) root.append(h('div', { class: 'md-q' }, ...inline(m[1] ?? '')));
    else if (!ln.trim()) root.append(h('div', { class: 'md-blank' }));
    else root.append(h('div', { class: 'md-p' }, ...inline(ln)));
  }
  return root;
}

/** A list item: the marker in its own column, so wrapped lines hang under the text. */
function item(lead: string, marker: string, body: string): HTMLElement {
  const level = Math.min(Math.floor(lead.replace(/\t/g, '    ').length / 2), 6);
  return h('div', { class: 'md-li', style: `--ind:${level * 2}ch` }, h('span', { class: 'md-mark', 'aria-hidden': 'true' }, marker), h('span', {}, ...inline(body)));
}

// ---- inline ----

/** Parses one line's inline markup. Unmatched markers stay as written. */
export function inline(s: string): Node[] {
  const out: Node[] = [];
  let cur = '';
  const flush = () => {
    if (cur) out.push(document.createTextNode(cur));
    cur = '';
  };
  for (let i = 0; i < s.length; ) {
    const c = s[i] ?? '';
    if (c === '\\' && i + 1 < s.length && isPunct(s[i + 1] ?? '')) {
      cur += s[i + 1];
      i += 2;
      continue;
    }
    if (c === '`') {
      const n = run(s, i, '`');
      const j = s.indexOf('`'.repeat(n), i + n);
      if (j >= 0) {
        flush();
        let code = s.slice(i + n, j);
        if (code.length > 2 && code.startsWith(' ') && code.endsWith(' ')) code = code.slice(1, -1);
        out.push(h('code', { class: 'md-code' }, code));
        i = j + n;
        continue;
      }
      cur += s.slice(i, i + n);
      i += n;
      continue;
    }
    if (c === '[') {
      const l = link(s.slice(i));
      if (l) {
        flush();
        const kids = inline(l.txt);
        if (/^(https?:|mailto:)/i.test(l.url)) out.push(h('a', { href: l.url, target: '_blank', rel: 'noopener noreferrer', title: l.url }, ...kids));
        else {
          out.push(...kids);
          if (l.url !== l.txt) out.push(h('span', { class: 'md-url' }, ` (${l.url})`));
        }
        i += l.n;
        continue;
      }
    }
    if (c === '*' || c === '_' || c === '~') {
      const e = emphasis(s, i);
      if (e) {
        flush();
        out.push(e.node);
        i += e.n;
        continue;
      }
      const n = run(s, i, c); // an unmatched run stays as written
      cur += s.slice(i, i + n);
      i += n;
      continue;
    }
    cur += c;
    i++;
  }
  flush();
  return out;
}

/** Parses **bold**, __bold__, *italic*, _italic_ or ~~strike~~ starting at s[i]. */
function emphasis(s: string, i: number): { node: Node; n: number } | null {
  const c = s[i] ?? '';
  const n = run(s, i, c);
  if ((c === '~' && n !== 2) || n > 3) return null;
  const open = i + n;
  if (open >= s.length || s[open] === ' ') return null; // "* item" or "a * b" is not emphasis
  if (c === '_' && isWordEnd(s.slice(0, i))) return null; // snake_case_names
  const marker = c.repeat(n);
  for (let from = open; from < s.length; ) {
    const j = s.indexOf(marker, from);
    if (j < 0) return null;
    const end = j + n;
    let ok = j > open && s[j - 1] !== ' ' && run(s, j, c) === n;
    if (ok && c === '_' && isWordStart(s.slice(end))) ok = false;
    if (ok) {
      const kids = inline(s.slice(open, j));
      let node: HTMLElement;
      if (c === '~') node = h('s', {}, ...kids);
      else if (n === 1) node = h('em', {}, ...kids);
      else if (n === 2) node = h('strong', {}, ...kids);
      else node = h('strong', {}, h('em', {}, ...kids));
      return { node, n: end - i };
    }
    from = j + run(s, j, c);
  }
  return null;
}

/** Parses [text](url) at the start of s; n is the characters consumed. */
function link(s: string): { txt: string; url: string; n: number } | null {
  const close = s.indexOf('](');
  if (close < 1 || s.slice(1, close).includes(']')) return null;
  const end = s.indexOf(')', close + 2);
  if (end < 0) return null;
  const url = s.slice(close + 2, end);
  if (!url || /[ \t]/.test(url)) return null;
  return { txt: s.slice(1, close), url, n: end + 1 };
}

function run(s: string, i: number, c: string): number {
  let n = 0;
  while (s[i + n] === c) n++;
  return n;
}

const isPunct = (c: string) => c !== '' && '!"#$%&\'()*+,-./:;<=>?@[\\]^_`{|}~'.includes(c);
const isWordEnd = (s: string) => /[\p{L}\p{N}]$/u.test(s);
const isWordStart = (s: string) => /^[\p{L}\p{N}]/u.test(s);
