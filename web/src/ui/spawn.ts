import { api, errText, type SpawnBody } from '../api';
import { refresh, select, setOverlay, state } from '../store';
import { busy, h, setKids, toast } from '../util';
import type { Panel } from './overlay';

const lines = (s: string) => s.split(/[\n,]/).map((x) => x.trim()).filter(Boolean);

export function spawnPanel(): Panel {
  const out = h('div', { class: 'spawn-out', 'aria-live': 'polite' });
  const err = h('div', { class: 'err', role: 'alert', hidden: true });

  const field = (id: string, label: string, control: HTMLElement, hint?: string) =>
    h('div', { class: 'field' }, h('label', { for: id }, label), control, hint && h('small', { class: 'muted' }, hint));

  const account = h('select', { id: 'sp-account' }, ...state.accounts.map((a) => h('option', { value: a.id }, `${a.label || a.id} (${a.used}/${a.max_concurrent})${a.enabled ? '' : ' - disabled'}`)));
  const kind = h('input', { id: 'sp-kind', placeholder: 'default: account kind' });
  const model = h('input', { id: 'sp-model', list: 'sp-models', placeholder: 'default: account model' });
  const models = h('datalist', { id: 'sp-models' });
  const effort = h('input', { id: 'sp-effort', list: 'sp-efforts', placeholder: 'optional' });
  const role = h('input', { id: 'sp-role', list: 'sp-roles', required: true, placeholder: 'e.g. implementer' });
  const roles = h('datalist', { id: 'sp-roles' });
  const callsign = h('input', { id: 'sp-callsign', placeholder: 'optional' });
  const task = h('textarea', { id: 'sp-task', rows: 4, required: true });
  const base = h('input', { id: 'sp-base', placeholder: 'default: current branch' });
  const paths = h('textarea', { id: 'sp-paths', rows: 2, placeholder: 'one per line' });
  const forbidden = h('textarea', { id: 'sp-forb', rows: 2, placeholder: 'one per line' });
  const askBefore = h('textarea', { id: 'sp-ask', rows: 2, placeholder: 'one per line' });
  const steps = h('div', { class: 'steps' });

  const syncAccount = () => {
    const a = state.accounts.find((x) => x.id === account.value);
    setKids(models, ...(a?.models ?? []).map((m) => h('option', { value: m })));
    setKids(roles, ...(a?.roles ?? []).map((m) => h('option', { value: m })));
  };
  const free = state.accounts.find((a) => a.enabled && !a.rate_limited && a.used < a.max_concurrent);
  if (free) account.value = free.id; // start on an account that can take an agent now
  account.addEventListener('change', syncAccount);
  syncAccount();

  const addStep = () => {
    const row = h(
      'div',
      { class: 'step-row' },
      h('input', { 'aria-label': 'Step title', placeholder: 'Step title', 'data-f': 'title' }),
      h('input', { 'aria-label': 'Acceptance', placeholder: 'Acceptance check', 'data-f': 'acc' }),
      h('button', { type: 'button', 'aria-label': 'Remove step', onclick: () => row.remove() }, 'x'),
    );
    steps.append(row);
    row.querySelector('input')?.focus();
  };

  function body(dry: boolean): SpawnBody {
    const plan = [...steps.querySelectorAll<HTMLElement>('.step-row')]
      .map((r) => ({ title: r.querySelector<HTMLInputElement>('[data-f=title]')!.value.trim(), acceptance: r.querySelector<HTMLInputElement>('[data-f=acc]')!.value.trim() }))
      .filter((s) => s.title)
      .map((s, i) => ({ id: 's' + (i + 1), ...s }));
    const b: SpawnBody = { role: role.value.trim(), task: task.value.trim(), dry_run: dry };
    const opt = (k: 'account' | 'kind' | 'model' | 'effort' | 'callsign' | 'base_branch', v: string) => {
      if (v.trim()) b[k] = v.trim();
    };
    opt('account', account.value);
    opt('kind', kind.value);
    opt('model', model.value);
    opt('effort', effort.value);
    opt('callsign', callsign.value);
    opt('base_branch', base.value);
    if (plan.length) b.plan = plan;
    const sc = { paths: lines(paths.value), forbidden: lines(forbidden.value), ask_before: lines(askBefore.value) };
    if (sc.paths.length || sc.forbidden.length || sc.ask_before.length) b.scope = sc;
    return b;
  }

  // Spawning can take a few seconds (worktree, launch): the buttons say so and take one click.
  async function submit(dry: boolean): Promise<void> {
    const btn = dry ? dryBtn : spawnBtn;
    await busy(btn, dry ? 'Writing brief...' : 'Spawning...', async () => {
      err.hidden = true;
      setKids(out);
      try {
        const r = await api.spawn(body(dry));
        if (dry) {
          setKids(out, h('h3', {}, `Brief for ${r.callsign} (${r.account_id}, ${r.model || r.kind})`), h('pre', { class: 'brief' }, r.brief ?? '(no brief returned)'));
          out.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
          return;
        }
        toast(`Spawned ${r.callsign} on ${r.account_id}`);
        await refresh();
        select(r.run_id);
        setOverlay(null);
      } catch (e) {
        err.textContent = errText(e);
        err.hidden = false;
        err.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
      }
    }, [form]);
  }

  const dryBtn: HTMLButtonElement = h('button', { type: 'button', onclick: () => form.reportValidity() && void submit(true) }, 'Dry run: show brief');
  const spawnBtn: HTMLButtonElement = h('button', { type: 'submit', class: 'primary' }, 'Spawn');

  const form: HTMLFormElement = h(
    'form',
    { class: 'spawn', onsubmit: (e: Event) => { e.preventDefault(); void submit(false); } },
    h('div', { class: 'grid2' }, field('sp-account', 'Account', account), field('sp-role', 'Role', role), field('sp-kind', 'Kind', kind), field('sp-model', 'Model', model), field('sp-effort', 'Effort', effort), field('sp-callsign', 'Callsign', callsign)),
    models,
    roles,
    h('datalist', { id: 'sp-efforts' }, ...['low', 'medium', 'high'].map((v) => h('option', { value: v }))),
    field('sp-task', 'Task', task),
    h('fieldset', {}, h('legend', {}, 'Plan (optional)'), steps, h('button', { type: 'button', onclick: addStep }, 'Add step')),
    h('fieldset', {}, h('legend', {}, 'Scope (optional)'), field('sp-paths', 'Allowed paths', paths), field('sp-forb', 'Forbidden paths', forbidden), field('sp-ask', 'Ask before touching', askBefore)),
    field('sp-base', 'Base branch', base),
    err,
    h('div', { class: 'row' }, dryBtn, spawnBtn),
    out,
  );
  return { title: 'Spawn an agent', el: form };
}
