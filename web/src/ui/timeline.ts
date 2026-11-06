// Bottom timeline: one swim lane per agent with step segments and event ticks; drag to scrub
// a replay playhead (lanes then show each agent's step and status at that moment).
import { api } from '../api';
import { accountById, eventHooks, resetHooks, select, state, type Run, type StoredEvent } from '../store';
import { clock, ENDED, h } from '../util';
import { visibleRuns } from './runs';

type Seg = { id: string; start: number; end?: number; blocked: boolean };
type Tick = { t: number; type: string };
type Lane = { seen: Set<number>; evs: StoredEvent[]; segs: Map<string, Seg>; ticks: Tick[]; status: { t: number; s: string }[]; dirty: boolean };

const WINDOWS = [
  ['5m', 5 * 60_000],
  ['15m', 15 * 60_000],
  ['1h', 60 * 60_000],
  ['all', 0],
] as const;
const TICK_TYPES = new Set(['question', 'artifact', 'error', 'rate_limited', 'killed', 'to_agent', 'done', 'stalled', 'paused']);
const GUTTER = 132;
const LANE = 26;
const AXIS = 22;

const lanes = new Map<string, Lane>();
const fetched = new Set<string>();

function lane(id: string): Lane {
  let l = lanes.get(id);
  if (!l) lanes.set(id, (l = { seen: new Set(), evs: [], segs: new Map(), ticks: [], status: [], dirty: false }));
  return l;
}

function ingest(ev: StoredEvent): void {
  const l = lane(ev.run_id);
  if (l.seen.has(ev.seq)) return;
  l.seen.add(ev.seq);
  l.evs.push(ev);
  l.dirty = true;
}
eventHooks.add(ingest);
resetHooks.add(() => {
  lanes.clear();
  fetched.clear();
});

/** Rebuild segments, ticks and the status history of a lane from its events (in seq order). */
function rebuild(l: Lane): void {
  if (!l.dirty) return;
  l.dirty = false;
  l.evs.sort((a, b) => a.seq - b.seq);
  l.segs.clear();
  l.ticks = [];
  l.status = [];
  let cur = 'starting';
  let waiting = false;
  let paused = false;
  const set = (t: number, s: string) => {
    if (s !== cur) l.status.push({ t, s: (cur = s) });
  };
  for (const e of l.evs) {
    const t = Date.parse(e.ts);
    const p = e.payload as Record<string, unknown>;
    const step = typeof p.step_id === 'string' ? p.step_id : '';
    if (TICK_TYPES.has(e.type)) l.ticks.push({ t, type: e.type === 'to_agent' ? 'to_agent:' + String(p.type) : e.type });
    if (ENDED.has(cur)) continue;
    switch (e.type) {
      case 'step_start': {
        const s = l.segs.get(step);
        if (s) (s.end = undefined), (s.blocked = false);
        else l.segs.set(step, { id: step, start: t, blocked: false });
        break;
      }
      case 'step_done':
      case 'step_blocked': {
        const s = l.segs.get(step) ?? { id: step, start: t, blocked: false };
        s.end = t;
        s.blocked = e.type === 'step_blocked';
        l.segs.set(step, s);
        break;
      }
    }
    if (e.type === 'question' && p.blocking !== false) waiting = true;
    if (e.type === 'to_agent' && p.type === 'answer') waiting = false;
    if (e.type === 'paused') paused = true;
    if (e.type === 'resumed') paused = false;
    if (e.type === 'done') set(t, p.status === 'failed' ? 'failed' : 'done');
    else if (e.type === 'killed') set(t, 'killed');
    else if (e.type === 'exit') set(t, 'exited');
    else if (paused) set(t, 'paused');
    else if (e.type === 'rate_limited') set(t, 'rate_limited');
    else if (e.type === 'stalled') set(t, 'stalled');
    else if (e.type === 'step_blocked') set(t, 'blocked');
    else if (waiting) set(t, 'waiting');
    else if (e.type !== 'heartbeat' && e.type !== 'usage') set(t, 'running');
  }
}

function statusAt(l: Lane, t: number): string {
  let s = 'starting';
  for (const x of l.status) if (x.t <= t) s = x.s;
  return s;
}

const STATUS_COLOR: Record<string, string> = {
  running: '#5cc8a8', starting: '#5cc8a8', waiting: '#d2a85a', blocked: '#e8844a', rate_limited: '#e8844a', stalled: '#e8844a',
  paused: '#a99bea', done: '#ece6d6', failed: '#e5626a', killed: '#e5626a', exited: '#8b94aa',
};
const TICK_COLOR = (type: string) =>
  type === 'question' ? '#d2a85a' : type === 'artifact' ? '#ece6d6' : type.startsWith('to_agent') ? '#9fb4e0' : type === 'done' ? '#5cc8a8' : type === 'paused' ? '#a99bea' : '#e5626a';

export function timelinePanel(host: HTMLElement): () => void {
  let win = 15 * 60_000;
  let playhead: number | null = null; // null = live
  let playing = false;
  let lastFrame = 0;
  const canvas = h('canvas', { 'aria-label': 'Timeline of agent steps and events. Drag to scrub.' });
  const wrap = h('div', { class: 'tl-canvas' }, canvas);
  const readout = h('span', { class: 'tl-readout mono' });
  const playBtn = h('button', { type: 'button', onclick: () => togglePlay() }, 'Play');
  const liveBtn = h('button', { type: 'button', onclick: () => ((playhead = null), (playing = false), draw()) }, 'Live');
  const winSel = h('select', { 'aria-label': 'Time window', onchange: () => ((win = Number(winSel.value)), draw()) }, ...WINDOWS.map(([l, ms]) => h('option', { value: String(ms), selected: ms === win }, l)));
  host.replaceChildren(h('div', { class: 'tl-head' }, h('h2', {}, 'Timeline'), readout, h('span', { class: 'grow' }), winSel, playBtn, liveBtn), wrap);

  let rows: Run[] = [];
  let range: [number, number] = [0, 1];
  const xOf = (t: number, w: number) => GUTTER + ((t - range[0]) / (range[1] - range[0])) * (w - GUTTER - 10);
  const tOf = (x: number, w: number) => range[0] + ((x - GUTTER) / (w - GUTTER - 10)) * (range[1] - range[0]);

  function togglePlay(): void {
    playing = !playing;
    if (playing && (playhead === null || playhead >= Date.now() - 500)) playhead = range[0];
    lastFrame = performance.now();
    if (playing) requestAnimationFrame(step);
    draw();
  }
  function step(now: number): void {
    if (!playing || playhead === null) return;
    playhead += ((now - lastFrame) / 1000) * ((range[1] - range[0]) / 40); // the window plays in ~40 s
    lastFrame = now;
    if (playhead >= Date.now()) (playhead = null), (playing = false);
    draw();
    if (playing) requestAnimationFrame(step);
  }

  let drag = false;
  const scrub = (e: PointerEvent) => {
    const r = canvas.getBoundingClientRect();
    if (e.clientX - r.left < GUTTER) return;
    playhead = Math.min(Date.now(), Math.max(range[0], tOf(e.clientX - r.left, r.width)));
    playing = false;
    draw();
  };
  canvas.addEventListener('pointerdown', (e) => {
    const r = canvas.getBoundingClientRect();
    if (e.clientX - r.left < GUTTER) {
      const run = rows[Math.floor((e.clientY - r.top) / LANE)];
      if (run) select(run.run_id);
      return;
    }
    drag = true;
    canvas.setPointerCapture(e.pointerId);
    scrub(e);
  });
  canvas.addEventListener('pointermove', (e) => drag && scrub(e));
  canvas.addEventListener('pointerup', () => (drag = false));
  new ResizeObserver(() => draw()).observe(wrap);

  function load(): void {
    for (const r of state.runs) {
      if (fetched.has(r.run_id)) continue;
      fetched.add(r.run_id);
      api.events(r.run_id, 0).then((x) => (x.events.forEach(ingest), draw()), () => fetched.delete(r.run_id));
    }
  }

  function draw(): void {
    if (host.hidden) return;
    rows = [...visibleRuns()].sort((a, b) => a.created_at.localeCompare(b.created_at));
    rows.forEach((r) => rebuild(lane(r.run_id)));
    const now = Date.now();
    const first = Math.min(now - 60_000, ...rows.map((r) => Date.parse(r.created_at)));
    range = win ? [now - win, now] : [first, now];
    const w = wrap.clientWidth;
    const hgt = Math.max(rows.length, 1) * LANE + AXIS;
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    canvas.width = w * dpr;
    canvas.height = hgt * dpr;
    canvas.style.width = w + 'px';
    canvas.style.height = hgt + 'px';
    const g = canvas.getContext('2d')!;
    g.setTransform(dpr, 0, 0, dpr, 0, 0);
    g.clearRect(0, 0, w, hgt);
    const at = playhead ?? now;

    // time axis
    const span = range[1] - range[0];
    const stepMs = [15e3, 30e3, 60e3, 120e3, 300e3, 600e3, 900e3, 1800e3, 3600e3].find((s) => span / s <= 8) ?? 7200e3;
    g.font = '10.5px "JetBrains Mono Variable", monospace';
    g.textBaseline = 'middle';
    for (let t = Math.ceil(range[0] / stepMs) * stepMs; t <= range[1]; t += stepMs) {
      const x = xOf(t, w);
      g.fillStyle = '#1c2438';
      g.fillRect(x, 0, 1, hgt - AXIS);
      g.fillStyle = '#5f6880';
      g.fillText(clock(new Date(t).toISOString()).slice(0, 5), x - 14, hgt - AXIS / 2);
    }

    rows.forEach((r, i) => {
      const l = lane(r.run_id);
      const y = i * LANE;
      const color = accountById(r.account_id)?.color || '#ece6d6';
      const sel = state.selected === r.run_id;
      if (sel) (g.fillStyle = 'rgba(255,255,255,.035)'), g.fillRect(0, y, w, LANE);
      const born = Date.parse(r.created_at);
      const st = playhead === null ? r.status : at < born ? 'unborn' : statusAt(l, at);
      // gutter: status dot + callsign
      g.fillStyle = STATUS_COLOR[st] ?? '#2a3148';
      g.beginPath();
      g.arc(10, y + LANE / 2, 3.5, 0, Math.PI * 2);
      g.fill();
      g.fillStyle = sel ? '#ffffff' : '#ece6d6';
      g.font = 'italic 16px "Instrument Serif", serif';
      g.fillText(clip(g, r.callsign || r.agent_id, GUTTER - 26), 20, y + LANE / 2);
      // lifetime
      const died = r.ended_at ? Date.parse(r.ended_at) : now;
      g.fillStyle = '#263049';
      g.fillRect(Math.max(GUTTER, xOf(born, w)), y + LANE / 2 - 0.5, Math.max(0, xOf(Math.min(died, at), w) - Math.max(GUTTER, xOf(born, w))), 1);
      // step segments
      const plan = new Map((r.plan ?? []).map((s) => [s.id, s.title]));
      g.font = '11px "Instrument Sans Variable", sans-serif';
      for (const s of l.segs.values()) {
        if (s.start > at) continue;
        const end = Math.min(s.end ?? died, at);
        const x0 = Math.max(GUTTER, xOf(s.start, w));
        const x1 = xOf(end, w);
        if (x1 < GUTTER || x1 - x0 < 1) continue;
        const open = s.end === undefined || s.end > at;
        g.fillStyle = s.blocked && !open ? '#e8844a' : color;
        g.globalAlpha = open ? 0.55 : 0.9;
        roundRect(g, x0, y + 6, x1 - x0, LANE - 12, 3);
        g.globalAlpha = 1;
        const title = plan.get(s.id) ?? s.id;
        if (x1 - x0 > 46) {
          g.fillStyle = '#0d1220';
          g.fillText(clip(g, title, x1 - x0 - 10), x0 + 5, y + LANE / 2 + 0.5);
        }
      }
      // event ticks
      for (const k of l.ticks) {
        if (k.t > at || k.t < range[0]) continue;
        const x = xOf(k.t, w);
        g.fillStyle = TICK_COLOR(k.type);
        g.fillRect(x - 1, y + 2, 2, 5);
      }
    });

    // playhead
    if (playhead !== null) {
      const x = xOf(playhead, w);
      g.fillStyle = '#d2a85a';
      g.fillRect(x - 1, 0, 2, hgt - AXIS);
      readout.textContent = `replay ${clock(new Date(playhead).toISOString())}`;
      readout.classList.add('on');
    } else {
      readout.textContent = 'live';
      readout.classList.remove('on');
    }
    playBtn.textContent = playing ? 'Pause' : 'Play';
    liveBtn.disabled = playhead === null;
    if (!rows.length) {
      g.fillStyle = '#8b94aa';
      g.font = '13px "Instrument Sans Variable", sans-serif';
      g.fillText('No agents yet.', GUTTER, LANE / 2);
    }
  }

  let tick: number | undefined;
  return () => {
    host.hidden = !state.timeline;
    document.body.classList.toggle('has-timeline', state.timeline);
    if (!state.timeline) return void (window.clearInterval(tick), (tick = undefined));
    load();
    if (tick === undefined) tick = window.setInterval(() => playhead === null && draw(), 1000);
    if (!playing) draw();
  };
}

function clip(g: CanvasRenderingContext2D, s: string, max: number): string {
  if (g.measureText(s).width <= max) return s;
  while (s.length > 1 && g.measureText(s + '…').width > max) s = s.slice(0, -1);
  return s + '…';
}

function roundRect(g: CanvasRenderingContext2D, x: number, y: number, w: number, hh: number, r: number): void {
  g.beginPath();
  g.roundRect(x, y, w, hh, Math.min(r, w / 2));
  g.fill();
}
