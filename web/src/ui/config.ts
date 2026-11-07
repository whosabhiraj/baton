import { api, errText } from '../api';
import type { ApiConfig } from '../protocol.gen';
import { refresh } from '../store';
import { h, setKids } from '../util';
import type { Panel } from './overlay';

type Kind = 'text' | 'number' | 'bool' | { select: string[] };
const FIELDS: { key: string; label: string; kind: Kind; hint?: string }[] = [
  { key: 'mode', label: 'Mode', kind: { select: ['user', 'propose', 'auto'] }, hint: 'who decides spawns and merges' },
  { key: 'approvals.merge', label: 'Approve merges', kind: 'bool' },
  { key: 'approvals.spawn', label: 'Approve spawns', kind: 'bool' },
  { key: 'max_agents_auto', label: 'Max agents (auto mode)', kind: 'number' },
  { key: 'heartbeat_timeout_s', label: 'Heartbeat timeout (s)', kind: 'number' },
  { key: 'ask_timeout_s', label: 'Ask timeout (s)', kind: 'number' },
  { key: 'poll_interval_s', label: 'Poll interval (s)', kind: 'number' },
  { key: 'idle_stop_min', label: 'Idle stop (min, 0 = never)', kind: 'number' },
  { key: 'open_browser', label: 'Open browser on start', kind: 'bool' },
  { key: 'worktrees.cleanup', label: 'Worktree cleanup', kind: { select: ['keep', 'remove_on_merge', 'remove_after_days'] } },
  { key: 'worktrees.days', label: 'Cleanup after days', kind: 'number' },
  { key: 'runs_keep_days', label: 'Keep run history (days, 0 = forever)', kind: 'number', hint: 'baton gc removes older ended runs' },
];

function dig(o: unknown, path: string): unknown {
  return path.split('.').reduce<unknown>((v, k) => (v && typeof v === 'object' ? (v as Record<string, unknown>)[k] : undefined), o);
}

export function configPanel(): Panel {
  const form = h('form', { class: 'cfg' });
  const issues = h('ul', { class: 'issues', role: 'status' });
  const err = h('div', { class: 'err', role: 'alert', hidden: true });
  const info = h('p', { class: 'muted' });
  let current: ApiConfig.ApiConfig | null = null;
  const inputs = new Map<string, HTMLInputElement | HTMLSelectElement>();

  function show(c: ApiConfig.ApiConfig): void {
    current = c;
    const hub = (c.config as { hub?: unknown }).hub;
    setKids(info, `Files: ${[c.global_path, c.project_path].filter(Boolean).join(' and ') || '(none)'} - version ${c.version}${c.ok ? '' : ' - files have errors, last good config in effect'}`);
    setKids(issues, ...c.issues.map((i) => h('li', {}, `${i.file ? i.file + ': ' : ''}${i.path} ${i.msg}${i.line ? ' (line ' + i.line + ')' : ''}`)));
    inputs.clear();
    setKids(
      form,
      ...FIELDS.map((f) => {
        const v = dig(hub, f.key);
        const id = 'cfg-' + f.key.replace('.', '-');
        let ctl: HTMLInputElement | HTMLSelectElement;
        if (typeof f.kind === 'object') {
          ctl = h('select', { id }, ...f.kind.select.map((o) => h('option', { value: o, selected: o === v }, o)));
          ctl.value = typeof v === 'string' ? v : f.kind.select[0]!;
        } else if (f.kind === 'bool') ctl = h('input', { id, type: 'checkbox', checked: v === true });
        else ctl = h('input', { id, type: f.kind, value: v === undefined ? '' : String(v) });
        inputs.set(f.key, ctl);
        return h('div', { class: f.kind === 'bool' ? 'field inline' : 'field' }, h('label', { for: id }, f.label), ctl, f.hint && h('small', { class: 'muted' }, f.hint));
      }),
      err,
      h('button', { type: 'submit', class: 'primary' }, 'Save changes'),
    );
  }

  async function load(): Promise<void> {
    try {
      show(await api.config());
    } catch (e) {
      setKids(info, errText(e));
    }
  }

  form.addEventListener('submit', (e) => {
    e.preventDefault();
    void (async () => {
      err.hidden = true;
      const changes: Record<string, unknown> = {};
      for (const f of FIELDS) {
        const ctl = inputs.get(f.key);
        if (!ctl) continue;
        const old = dig((current?.config as { hub?: unknown } | undefined)?.hub, f.key);
        let val: unknown;
        if (f.kind === 'bool') val = (ctl as HTMLInputElement).checked;
        else if (f.kind === 'number') {
          if (ctl.value.trim() === '') continue;
          val = Number(ctl.value);
        } else val = ctl.value;
        if (val !== old) changes[f.key] = val;
      }
      if (!Object.keys(changes).length) {
        err.textContent = 'Nothing changed.';
        err.hidden = false;
        return;
      }
      try {
        show(await api.patchConfig(changes));
        await refresh();
      } catch (ex) {
        await load(); // fields before the bad one stay applied, so show the real state
        err.textContent = errText(ex);
        err.hidden = false;
      }
    })();
  });

  void load();
  return { title: 'Hub configuration', el: h('div', {}, info, issues, form) };
}
