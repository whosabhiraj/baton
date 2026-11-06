// The 3D view as a rail yard: every agent is a straight track of stations (its plan steps). Tracks branch
// off their account's spine; spines drop from one trunk that leads from the conductor. Account bands stand
// side by side on wide screens and stack on narrow ones. A puck sits at the current station and slides on
// when a step completes. Black ground, flat unlit colour, no bloom.
// Lazy-loaded (three.js is only fetched when the map is first shown). Reads the same store as the list.
import * as THREE from 'three';
import { OrbitControls } from 'three/addons/controls/OrbitControls.js';
import { CSS2DObject, CSS2DRenderer } from 'three/addons/renderers/CSS2DRenderer.js';
import { accountById, select, state, type Account, type Run } from './store';
import { currentStep } from './ui/runs';
import { bullet, ENDED, lineColor, PAL, STATUS_COLOR } from './util';

const reduced = matchMedia('(prefers-reduced-motion: reduce)').matches;
const STEP = 1.3; // station spacing along a track
const ROW = 1.1; // track spacing inside a band
const TRUNK_X = -0.7; // the trunk every track branches off
const FIRST_X = 0.45; // first station
const LABEL_X = TRUNK_X - 0.45;
const LABEL_W = 3.4; // room left of a spine for agent labels
const TRUNK_Z = -1.4;
const HEAD_GAP = 1; // account heading to its first track
const LINGER_MS = 120_000; // finished agents stay this long (or while selected)
const IDLE_REFRAME_MS = 15_000;
const DIR = new THREE.Vector3(0, 0.8, 0.72).normalize(); // overview camera direction
const GROUND = new THREE.Color(PAL.ground);

type Mat = THREE.MeshBasicMaterial;
type AgentObj = {
  run: Run;
  group: THREE.Group; // the track; slides to its place when the order changes
  at: THREE.Vector3 | null; // where it is going (null: not placed yet)
  rail: THREE.Mesh;
  stations: { disc: THREE.Mesh; ring: THREE.Mesh }[];
  marker: THREE.Group;
  status: THREE.Mesh<THREE.BufferGeometry, Mat>;
  focus: THREE.Mesh<THREE.BufferGeometry, Mat>;
  hit: THREE.Mesh;
  mats: Record<'line' | 'dim' | 'fill' | 'warn' | 'top' | 'side' | 'status' | 'focus', Mat>;
  label: CSS2DObject;
  here: CSS2DObject;
  xWant: number;
  seed: number;
};
type BandObj = { spine: THREE.Mesh<THREE.BufferGeometry, Mat>; label: CSS2DObject | null };

export interface SceneCtl {
  setVisible(v: boolean): void;
  update(): void;
}

/** Unlit colour that dims toward the ground instead of going transparent (no sorting, fog-friendly). */
function mat(hex: string): Mat {
  const m = new THREE.MeshBasicMaterial({ color: hex });
  m.userData.base = new THREE.Color(hex);
  return m;
}
const setBase = (m: Mat, hex: string) => (m.userData.base as THREE.Color).set(hex);
const tint = (m: Mat, k: number) => m.color.copy(m.userData.base as THREE.Color).lerp(GROUND, 1 - k);

const flatPlane = (along: 'x' | 'z') => {
  const g = new THREE.PlaneGeometry(1, 1).rotateX(-Math.PI / 2);
  return along === 'x' ? g.translate(0.5, 0, 0) : g.translate(0, 0, 0.5);
};
const flatRing = (r0: number, r1: number) => new THREE.RingGeometry(r0, r1, 48).rotateX(-Math.PI / 2);
const G = {
  rail: flatPlane('x'),
  spine: flatPlane('z'),
  disc: new THREE.CircleGeometry(0.15, 32).rotateX(-Math.PI / 2),
  ring: flatRing(0.115, 0.165),
  puck: new THREE.CylinderGeometry(0.19, 0.19, 0.22, 32).translate(0, 0.11, 0),
  status: flatRing(0.27, 0.32),
  focus: flatRing(0.42, 0.45),
};

export function mountScene(host: HTMLElement): SceneCtl | null {
  let renderer: THREE.WebGLRenderer;
  try {
    renderer = new THREE.WebGLRenderer({ antialias: true, powerPreference: 'high-performance' });
  } catch {
    return null; // no WebGL: caller keeps the list view
  }
  renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));
  host.replaceChildren(renderer.domElement);
  const labels = new CSS2DRenderer();
  labels.domElement.className = 'scene-labels';
  host.append(labels.domElement, Object.assign(document.createElement('p'), { className: 'scene-hint', textContent: 'Drag to orbit, scroll to zoom, click an agent. L switches to the list, T shows the timeline.' }));

  const scene = new THREE.Scene();
  scene.background = GROUND.clone();
  const fog = new THREE.Fog(PAL.ground, 30, 90);
  scene.fog = fog;
  const camera = new THREE.PerspectiveCamera(34, 1, 0.1, 300);
  const controls = new OrbitControls(camera, renderer.domElement);
  controls.enableDamping = true;
  controls.minDistance = 3;
  controls.maxDistance = 90;
  controls.minPolarAngle = 0.2;
  controls.maxPolarAngle = 1.3; // never under the ground
  const overview = { pos: new THREE.Vector3(0, 20, 18), target: new THREE.Vector3() };
  let framed = false;

  // conductor: the terminus at the head of the trunk
  const condMats = { top: mat(PAL.text), side: mat('#8f8d88'), ring: mat(PAL.text) };
  const conductor = new THREE.Group();
  conductor.add(
    new THREE.Mesh(new THREE.CylinderGeometry(0.34, 0.34, 0.3, 48).translate(0, 0.15, 0), [condMats.side, condMats.top, condMats.side]),
    new THREE.Mesh(flatRing(0.5, 0.54), condMats.ring),
  );
  const condLabel = label('lbl lbl-cond', -0.75, 0, [1, 0.5]);
  conductor.add(condLabel);
  const trunkMat = mat(PAL.line2);
  const trunk = new THREE.Mesh(G.rail, trunkMat); // across the columns
  trunk.scale.z = 0.05;
  const drops: THREE.Mesh[] = []; // one neutral spine per column, from the trunk down
  scene.add(conductor, trunk);

  const bands = new Map<string, BandObj>();
  const agents = new Map<string, AgentObj>();

  // ---------- sync with the store ----------

  function band(id: string, a: Account | undefined): BandObj {
    let b = bands.get(id);
    if (!b) {
      const spine = new THREE.Mesh(G.spine, mat(PAL.text3));
      spine.scale.x = 0.07;
      spine.position.y = 0.004;
      b = { spine, label: a ? label('lbl lbl-acct', 0, 0, [0, 0.5]) : null };
      scene.add(spine, ...(b.label ? [b.label] : []));
      bands.set(id, b);
    }
    const off = !!a && (!a.enabled || (!!state.accountFilter && state.accountFilter !== a.id));
    setBase(b.spine.material, a?.rate_limited ? PAL.warn : lineColor(a?.color));
    tint(b.spine.material, off ? 0.3 : 1);
    if (a && b.label) {
      const el = b.label.element;
      const sig = JSON.stringify([a.label, a.used, a.max_concurrent, a.rate_limited, a.enabled, a.color, off]);
      if (el.dataset.sig !== sig) {
        el.dataset.sig = sig;
        el.classList.toggle('off', off);
        el.replaceChildren(bullet(a), span('name', a.label || a.id), span('use', `${a.used}/${a.max_concurrent}`), ...(a.rate_limited ? [span('flag', 'rate limited')] : []));
      }
    }
    return b;
  }

  function dropBand(b: BandObj): void {
    scene.remove(b.spine, ...(b.label ? [b.label] : []));
    b.spine.material.dispose();
  }

  function syncRuns(): void {
    const seen = new Set<string>();
    const now = Date.now();
    for (const r of state.runs) {
      const gone = ENDED.has(r.status) && r.ended_at && now - Date.parse(r.ended_at) > LINGER_MS && state.selected !== r.run_id;
      if (gone) continue;
      seen.add(r.run_id);
      const fresh = !agents.has(r.run_id);
      const o = agents.get(r.run_id) ?? makeAgent(r);
      o.run = r;
      const a = accountById(r.account_id);
      const line = lineColor(a?.color);
      setBase(o.mats.line, line);
      setBase(o.mats.dim, '#' + new THREE.Color(line).lerp(GROUND, 0.5).getHexString());
      setBase(o.mats.status, STATUS_COLOR[r.status] ?? PAL.text3);
      layStations(o);
      if (fresh) o.marker.position.x = o.xWant;
      agentLabel(o);
    }
    for (const [id, o] of agents) if (!seen.has(id)) (dropAgent(o), agents.delete(id));
  }

  function makeAgent(r: Run): AgentObj {
    const mats = { line: mat(PAL.text3), dim: mat(PAL.line2), fill: mat(PAL.ground), warn: mat(PAL.warn), top: mat(PAL.text), side: mat('#8f8d88'), status: mat(PAL.text3), focus: mat(PAL.text) };
    const group = new THREE.Group();
    const rail = new THREE.Mesh(G.rail, mats.line);
    rail.position.set(TRUNK_X, 0.002, 0);
    rail.scale.z = 0.045;
    const puck = new THREE.Mesh(G.puck, [mats.side, mats.top, mats.side]);
    const status = new THREE.Mesh(G.status, mats.status);
    const focus = new THREE.Mesh(G.focus, mats.focus);
    status.position.y = focus.position.y = 0.012;
    const marker = new THREE.Group();
    marker.add(puck, status, focus);
    const hit = new THREE.Mesh(G.rail, new THREE.MeshBasicMaterial({ colorWrite: false, depthWrite: false }));
    hit.position.x = LABEL_X - 2.6;
    hit.scale.z = ROW * 0.9;
    hit.userData.runId = r.run_id;
    const lbl = label('lbl lbl-agent', LABEL_X, 0, [1, 0.5]);
    const here = label('lbl lbl-here', 0, 0, [0.5, 1]);
    here.position.y = 0.5; // just above the puck
    marker.add(here);
    group.add(rail, marker, hit, lbl);
    scene.add(group);
    const o: AgentObj = { run: r, group, at: null, rail, stations: [], marker, status, focus, hit, mats, label: lbl, here, xWant: FIRST_X, seed: hash(r.run_id) };
    agents.set(r.run_id, o);
    return o;
  }

  /** Stations follow the plan: done filled, pending hollow, blocked in the warn colour; the puck marks the current one. */
  function layStations(o: AgentObj): void {
    const r = o.run;
    const plan = r.plan ?? [];
    const n = Math.max(1, plan.length);
    while (o.stations.length > n) {
      const s = o.stations.pop()!;
      o.group.remove(s.disc, s.ring);
    }
    while (o.stations.length < n) {
      const i = o.stations.length;
      const disc = new THREE.Mesh(G.disc, o.mats.fill);
      const ring = new THREE.Mesh(G.ring, o.mats.dim);
      disc.position.set(FIRST_X + i * STEP, 0.006, 0);
      ring.position.set(FIRST_X + i * STEP, 0.008, 0);
      o.group.add(disc, ring);
      o.stations.push({ disc, ring });
    }
    const states = plan.map((p) => r.step_states?.[p.id] ?? 'pending');
    let here = states.findIndex((s) => s === 'active' || s === 'blocked');
    if (here < 0) here = states.length && states.every((s) => s === 'done') ? states.length - 1 : Math.max(0, states.findIndex((s) => s !== 'done'));
    o.stations.forEach((s, i) => {
      const st = states[i] ?? 'pending';
      s.disc.material = st === 'done' || i === here ? o.mats.line : st === 'blocked' ? o.mats.warn : o.mats.fill;
      s.ring.visible = st !== 'done' && st !== 'blocked' && i !== here;
    });
    o.rail.scale.x = FIRST_X - TRUNK_X + (n - 1) * STEP;
    o.hit.scale.x = o.rail.scale.x + 3.4;
    o.xWant = FIRST_X + here * STEP;
  }

  function agentLabel(o: AgentObj): void {
    const r = o.run;
    const sel = state.selected === r.run_id;
    const el = o.label.element;
    const sig = JSON.stringify([r.callsign, r.status, r.open_questions.length, sel, r.role, r.model]);
    if (el.dataset.sig !== sig) {
      el.dataset.sig = sig;
      el.className = `lbl lbl-agent st-${r.status}${sel ? ' sel' : ''}${hovered === r.run_id ? ' hover' : ''}`;
      const top = document.createElement('span');
      top.className = 'top';
      top.append(...(r.open_questions.length ? [span('q', '?' + (r.open_questions.length > 1 ? r.open_questions.length : ''))] : []), span('cs', r.callsign || r.agent_id), span('st', r.status.replace('_', ' ')));
      el.replaceChildren(top, span('more', [r.role, r.model].filter(Boolean).join(' · ')));
    }
    o.here.element.textContent = currentStep(r);
  }

  function dropAgent(o: AgentObj): void {
    o.label.removeFromParent(); // a CSS2D label leaves the DOM only when it is removed itself
    o.here.removeFromParent();
    scene.remove(o.group);
    for (const m of Object.values(o.mats)) m.dispose();
    (o.hit.material as THREE.Material).dispose();
    if (hovered === o.run.run_id) hovered = null;
  }

  function syncConductor(): void {
    const c = state.conductor;
    const el = condLabel.element;
    const sig = JSON.stringify(c);
    if (el.dataset.sig === sig) return;
    el.dataset.sig = sig;
    el.className = 'lbl lbl-cond cond-' + c.state;
    el.replaceChildren(span('cs', 'conductor'), span('st', [c.state, c.model].filter(Boolean).join(' · ')));
  }

  /** Bands in account order, live tracks first in each band. Bands are dealt into columns (shortest
   *  first); the column count is whichever shows the yard largest. Then frames the overview. */
  function arrange(): void {
    const buckets = new Map<string, AgentObj[]>();
    let steps = 1;
    for (const o of agents.values()) {
      const k = o.run.account_id ?? '';
      if (!buckets.has(k)) buckets.set(k, []);
      buckets.get(k)!.push(o);
      steps = Math.max(steps, o.stations.length);
    }
    const live = (o: AgentObj) => Number(ENDED.has(o.run.status));
    const rows = [...state.accounts.map((a) => a.id), ...[...buckets.keys()].filter((k) => !accountById(k))].map((id) => ({
      id,
      a: accountById(id),
      list: (buckets.get(id) ?? []).sort((x, y) => live(x) - live(y) || x.run.created_at.localeCompare(y.run.created_at)),
    }));
    const endX = FIRST_X + (steps - 1) * STEP;
    const pitch = LABEL_W + (FIRST_X - TRUNK_X) + (steps - 1) * STEP + 1.2;
    const deal = (cols: number) => {
      const depth = new Array<number>(cols).fill(0);
      const at = rows.map((r) => {
        const c = depth.indexOf(Math.min(...depth));
        const head = depth[c]!;
        depth[c] = head + (r.a ? HEAD_GAP : 0.3) + Math.max(0, r.list.length - 1) * ROW + (r.list.length ? 0.25 : 0) + 0.9;
        return { c, head };
      });
      return { cols, depth, at, w: (cols - 1) * pitch + endX + 0.6 - (LABEL_X - LABEL_W), d: Math.max(...depth) - TRUNK_Z + 0.8 };
    };
    // judged by the window's shape, so opening the agent panel does not reshuffle the yard
    const shape = innerWidth / innerHeight;
    let plan = deal(1);
    for (let n = 2; n <= Math.min(3, rows.length); n++) {
      const p = deal(n);
      if (fit(p.w, p.d, shape) < fit(plan.w, plan.d, shape)) plan = p;
    }
    const { cols, depth } = plan;
    const colX = (c: number) => (c - (cols - 1) / 2) * pitch;
    rows.forEach(({ id, a, list }, i) => {
      const { c, head } = plan.at[i]!;
      const cx = colX(c);
      const b = band(id, a);
      let z = head + (a ? HEAD_GAP : 0.3);
      for (const o of list) {
        const fresh = !o.at || !framed;
        o.at = (o.at ?? new THREE.Vector3()).set(cx, 0, z);
        if (fresh) o.group.position.copy(o.at); // new tracks appear in place
        z += ROW;
      }
      const end = list.length ? z - ROW + 0.25 : z;
      b.spine.position.x = TRUNK_X + cx;
      b.spine.position.z = head + 0.3;
      b.spine.scale.z = Math.max(0.2, end - head - 0.3);
      b.label?.position.set(TRUNK_X + cx + 0.2, 0, head + 0.15); // a heading over its tracks
    });
    for (const [id, b] of bands) if (!rows.some((r) => r.id === id)) (dropBand(b), bands.delete(id));
    // the trunk across the column heads, the conductor in its middle, a neutral drop down each column
    conductor.position.set(TRUNK_X, 0, TRUNK_Z);
    trunk.position.set(TRUNK_X + colX(0), 0.001, TRUNK_Z);
    trunk.scale.x = Math.max(0.001, colX(cols - 1) - colX(0));
    while (drops.length < cols) {
      const d = new THREE.Mesh(G.spine, trunkMat);
      d.scale.x = 0.05;
      drops.push(d);
      scene.add(d);
    }
    drops.forEach((d, c) => {
      d.visible = c < cols;
      d.position.set(TRUNK_X + colX(c), 0.001, TRUNK_Z);
      d.scale.z = Math.max(0.1, (depth[c] ?? 0) - 0.9 - TRUNK_Z);
    });
    // overview: fit the yard in the viewport; labels keep their pixel size, so give them room in pixels too
    const x1 = colX(cols - 1) + endX + 0.6, z0 = TRUNK_Z - 0.8, z1 = Math.max(...depth);
    let x0 = colX(0) + LABEL_X - LABEL_W;
    let dist = fit(x1 - x0, z1 - z0);
    const perUnit = host.clientWidth / (2 * dist * Math.tan(Math.atan(Math.tan(THREE.MathUtils.degToRad(camera.fov) / 2) * camera.aspect)));
    if (LABEL_W * perUnit < 170) {
      x0 = colX(0) + LABEL_X - 170 / perUnit;
      dist = fit(x1 - x0, z1 - z0);
    }
    overview.target.set((x0 + x1) / 2, 0, (z0 + z1) / 2);
    overview.pos.copy(overview.target).addScaledVector(DIR, dist);
    fog.near = dist * 1.2;
    fog.far = dist * 3;
    if (!framed && agents.size) {
      framed = true;
      camera.position.copy(overview.pos);
      controls.target.copy(overview.target);
    }
  }

  /** Camera distance (along DIR) that fits a w x d patch of ground. */
  function fit(w: number, d: number, aspect = camera.aspect): number {
    const vf = THREE.MathUtils.degToRad(camera.fov) / 2;
    const hf = Math.atan(Math.tan(vf) * aspect);
    return Math.max(8, Math.max(w / 2 / Math.tan(hf), (d * 0.82) / 2 / Math.tan(vf)) * 1.08);
  }

  // ---------- frame ----------

  const timer = new THREE.Timer();
  timer.connect(document);
  const tmp = new THREE.Vector3();
  const followAt = new THREE.Vector3();
  let lastInput = -Infinity;
  let flight: { p0: THREE.Vector3; t0v: THREE.Vector3; p1: THREE.Vector3; t1v: THREE.Vector3; start: number; dur: number } | null = null;
  let following: string | null = null;
  let lastSelected: string | null = null;
  let hovered: string | null = null;

  // a selected agent is framed with its label and whole track
  const spanOf = (o: AgentObj) => FIRST_X + (o.stations.length - 1) * STEP - (LABEL_X - LABEL_W * 0.7);
  const focusOf = (o: AgentObj, out: THREE.Vector3) => out.set(o.group.position.x + LABEL_X - LABEL_W * 0.7 + spanOf(o) / 2, 0, o.group.position.z);

  function styleAgent(o: AgentObj, t: number, dt: number): void {
    const r = o.run;
    const ended = ENDED.has(r.status);
    const sel = state.selected === r.run_id;
    const hov = hovered === r.run_id;
    const dimmed = !!state.accountFilter && state.accountFilter !== r.account_id;
    const k = dimmed ? 0.18 : ended && !sel ? 0.42 : 1;
    if (o.at) o.group.position.lerp(o.at, reduced ? 1 : Math.min(1, dt * 6));
    o.marker.position.x += (o.xWant - o.marker.position.x) * (reduced ? 1 : Math.min(1, dt * 3));
    for (const m of [o.mats.dim, o.mats.warn, o.mats.top, o.mats.side]) tint(m, k);
    tint(o.mats.line, k * (sel || hov ? 1 : 0.85));
    const wait = r.status === 'waiting' && !dimmed;
    tint(o.mats.status, wait && !reduced ? 0.55 + 0.45 * Math.sin(t * 1.6 + o.seed) : k);
    o.focus.visible = !dimmed && (sel || hov);
    tint(o.mats.focus, sel ? 1 : 0.45);
    o.here.element.classList.toggle('on', !dimmed && !ended && (sel || hov));
    o.label.element.style.opacity = dimmed ? '0.25' : ended && !sel ? '0.6' : '1';
  }

  function cameraWork(now: number, dt: number): void {
    const sel = state.selected ? agents.get(state.selected) : undefined;
    if (state.selected !== lastSelected && (sel || !state.selected)) {
      // (a finished agent joins the yard on the next sync once selected: wait for it)
      lastSelected = state.selected;
      const o = sel;
      const dur = reduced ? 1 : 900;
      if (o) {
        const t1 = focusOf(o, new THREE.Vector3());
        flight = { p0: camera.position.clone(), t0v: controls.target.clone(), p1: t1.clone().addScaledVector(DIR, fit(spanOf(o) + 3, 5)), t1v: t1, start: now, dur };
        following = o.run.run_id;
      } else {
        flight = { p0: camera.position.clone(), t0v: controls.target.clone(), p1: overview.pos.clone(), t1v: overview.target.clone(), start: now, dur };
        following = null;
      }
    } else if (!flight && !following && now - lastInput > IDLE_REFRAME_MS && controls.target.distanceTo(overview.target) > 0.5) {
      // the fleet grew or shrank while nobody was steering: reframe
      flight = { p0: camera.position.clone(), t0v: controls.target.clone(), p1: overview.pos.clone(), t1v: overview.target.clone(), start: now, dur: reduced ? 1 : 1400 };
    }
    const f = following ? agents.get(following) : undefined;
    if (flight) {
      const k = Math.min(1, (now - flight.start) / flight.dur);
      const e = 1 - Math.pow(1 - k, 3);
      if (f) {
        // the track can move during the flight
        const d = focusOf(f, tmp).sub(flight.t1v);
        flight.t1v.add(d);
        flight.p1.add(d);
      }
      camera.position.lerpVectors(flight.p0, flight.p1, e);
      controls.target.lerpVectors(flight.t0v, flight.t1v, e);
      if (k >= 1) (flight = null), f && focusOf(f, followAt);
    } else if (f) {
      // ride along when the track moves; the user's own orbit and pan stay
      const delta = focusOf(f, tmp).sub(followAt);
      followAt.add(delta);
      controls.target.add(delta);
      camera.position.add(delta);
    }
    controls.update(dt);
  }

  function frame(): void {
    timer.update();
    const dt = Math.min(timer.getDelta(), 0.1);
    const t = timer.getElapsed();
    const now = performance.now();
    for (const o of agents.values()) styleAgent(o, t, dt);
    const busy = state.conductor.state === 'thinking' || state.conductor.state === 'acting';
    setBase(condMats.ring, busy ? PAL.signal : PAL.text);
    tint(condMats.ring, busy && !reduced ? 0.6 + 0.4 * Math.sin(t * 1.6) : 1);
    cameraWork(now, dt);
    renderer.render(scene, camera);
    labels.render(scene, camera);
  }

  // ---------- input ----------

  const ray = new THREE.Raycaster();
  const ndc = new THREE.Vector2();
  function pick(ev: PointerEvent): string | null {
    const rect = renderer.domElement.getBoundingClientRect();
    ndc.set(((ev.clientX - rect.left) / rect.width) * 2 - 1, -((ev.clientY - rect.top) / rect.height) * 2 + 1);
    ray.setFromCamera(ndc, camera);
    const hits = ray.intersectObjects([...agents.values()].map((o) => o.hit), false);
    return (hits[0]?.object.userData.runId as string | undefined) ?? null;
  }
  let down: { x: number; y: number } | null = null;
  const el = renderer.domElement;
  el.addEventListener('pointerdown', (e) => {
    down = { x: e.clientX, y: e.clientY };
    lastInput = performance.now();
  });
  el.addEventListener('pointerup', (e) => {
    if (down && Math.hypot(e.clientX - down.x, e.clientY - down.y) < 5) {
      const id = pick(e);
      if (id) select(id);
    }
    down = null;
  });
  el.addEventListener('pointermove', (e) => {
    if (e.buttons) return;
    const id = pick(e);
    if (id !== hovered) {
      if (hovered) agents.get(hovered)?.label.element.classList.remove('hover');
      hovered = id;
      if (id) agents.get(id)?.label.element.classList.add('hover');
      el.style.cursor = id ? 'pointer' : '';
    }
  });
  el.addEventListener('wheel', () => (lastInput = performance.now()), { passive: true });
  controls.addEventListener('start', () => {
    lastInput = performance.now();
    if (flight) {
      flight = null; // the user takes over
      const f = following ? agents.get(following) : undefined;
      if (f) focusOf(f, followAt);
    }
  });

  // ---------- lifecycle ----------

  let visible = false;
  const resize = () => {
    const w = Math.max(1, host.clientWidth);
    const h = Math.max(1, host.clientHeight);
    camera.aspect = w / h;
    camera.updateProjectionMatrix();
    renderer.setSize(w, h);
    labels.setSize(w, h);
    arrange();
  };
  new ResizeObserver(resize).observe(host);
  const run = () => renderer.setAnimationLoop(visible && !document.hidden ? frame : null);
  document.addEventListener('visibilitychange', run);

  const ctl: SceneCtl = {
    setVisible(v) {
      if (v === visible) return;
      visible = v;
      if (v) (resize(), ctl.update());
      run();
    },
    update() {
      if (!visible) return;
      syncRuns();
      arrange();
      syncConductor();
    },
  };
  return ctl;
}

function label(cls: string, x: number, z: number, center: [number, number]): CSS2DObject {
  const el = document.createElement('div');
  el.className = cls;
  const o = new CSS2DObject(el);
  o.position.set(x, 0, z);
  o.center.set(...center);
  return o;
}

function span(cls: string, text: string): HTMLElement {
  const s = document.createElement('span');
  s.className = cls;
  s.textContent = text;
  return s;
}

function hash(s: string): number {
  let h = 0;
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) | 0;
  return (Math.abs(h) % 1000) / 160;
}
