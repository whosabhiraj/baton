// The 3D view as a transit map in 3D: the conductor is the interchange roundel, each account a flat ring line,
// each agent a station puck on its line. Same flat palette as the list; no glow, no bloom.
// Lazy-loaded (three.js is only fetched when the scene is first shown). Reads the same store as the list.
import * as THREE from 'three';
import { OrbitControls } from 'three/addons/controls/OrbitControls.js';
import { mergeGeometries } from 'three/addons/utils/BufferGeometryUtils.js';
import { CSS2DObject, CSS2DRenderer } from 'three/addons/renderers/CSS2DRenderer.js';
import { accountById, select, state, type Account, type Run } from './store';
import { currentStep, stepProgress } from './ui/runs';
import { bullet, ENDED } from './util';

const reduced = matchMedia('(prefers-reduced-motion: reduce)').matches;
// the 2D tokens (style.css :root)
const C = { ground: 0x16171b, text: 0xf2f1ec, mute: 0xa9adb9, signal: 0xffcd1c, stop: 0xf0524a, line: 0x30343e, enamel: 0x1d2b78, edge: 0x7d8290 };
// status core colours: identical to the 2D .badge colours
const STATUS: Record<string, number> = {
  running: 0x3cc489, starting: 0x3cc489, waiting: 0xffcd1c,
  blocked: 0xff8a3d, rate_limited: 0xff8a3d, stalled: 0xff8a3d,
  paused: 0xa597ff, done: 0xf2f1ec, failed: 0xf0524a, killed: 0xf0524a, exited: 0x7d8290,
};
const OVERVIEW = { pos: new THREE.Vector3(0, 23, 14), target: new THREE.Vector3(0, 0, 0) };
const IDLE_ROTATE_MS = 20_000;
const PUCK_R = 0.42;

type Mat = THREE.MeshBasicMaterial;
type AgentObj = {
  run: Run;
  group: THREE.Group; // sits on the map at the agent's station
  puck: THREE.Mesh;
  puckMats: [Mat, Mat]; // side, top
  coreMats: [Mat, Mat];
  halo: THREE.Mesh<THREE.BufferGeometry, Mat>;
  focus: THREE.Mesh<THREE.BufferGeometry, Mat>;
  track: THREE.Mesh<THREE.BufferGeometry, Mat>;
  arc: THREE.Mesh<THREE.BufferGeometry, Mat>;
  arcKey: string;
  edge: THREE.Line<THREE.BufferGeometry, THREE.LineBasicMaterial>;
  label: CSS2DObject;
  angle: number;
  seed: number;
  kind: string;
};
type AcctObj = { group: THREE.Group; ring: THREE.Mesh<THREE.BufferGeometry, Mat>; radius: number; label: CSS2DObject; key: string; phase: number };

export interface SceneCtl {
  setVisible(v: boolean): void;
  update(): void;
}

const flat = <T extends THREE.Object3D>(o: T, y = 0): T => ((o.rotation.x = -Math.PI / 2), (o.position.y = y), o);

function puckGeo(kind: string): THREE.BufferGeometry {
  const h = 0.16;
  const g =
    kind === 'codex'
      ? new THREE.CylinderGeometry(PUCK_R * 1.12, PUCK_R * 1.12, h, 4).rotateY(Math.PI / 4)
      : kind === 'opencode'
        ? new THREE.CylinderGeometry(PUCK_R * 1.05, PUCK_R * 1.05, h, 6)
        : new THREE.CylinderGeometry(PUCK_R, PUCK_R, h, 40);
  return g.translate(0, h / 2, 0);
}

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
  scene.background = new THREE.Color(C.ground);
  const camera = new THREE.PerspectiveCamera(45, 1, 0.1, 400);
  camera.position.copy(OVERVIEW.pos);
  const controls = new OrbitControls(camera, renderer.domElement);
  controls.enableDamping = true;
  controls.minDistance = 3;
  controls.maxDistance = 60;
  controls.autoRotateSpeed = 0.35;
  controls.target.copy(OVERVIEW.target);

  const basic = (color: number, opacity = 1): Mat => new THREE.MeshBasicMaterial({ color, transparent: true, opacity, depthWrite: opacity === 1 });
  const ringGeo = (r0: number, r1: number) => new THREE.RingGeometry(r0, r1, 64);

  // map texture: a faint dot grid on the ground plane, sized to the outer line
  const dots = new THREE.Points(new THREE.BufferGeometry(), new THREE.PointsMaterial({ color: C.line, size: 2.5, sizeAttenuation: false, fog: false }));
  dots.position.y = -0.02;
  scene.add(dots);
  let gridR = 0;
  function sizeGrid(maxR: number): void {
    const R = Math.ceil(maxR + 5);
    if (R === gridR) return;
    gridR = R;
    const pts: number[] = [];
    for (let x = -R; x <= R; x++) for (let z = -R; z <= R; z++) if (x * x + z * z <= R * R) pts.push(x, 0, z);
    dots.geometry.dispose();
    dots.geometry = new THREE.BufferGeometry().setAttribute('position', new THREE.Float32BufferAttribute(pts, 3));
  }

  // conductor: the interchange roundel from the logo (white ring round an enamel centre)
  const condMats = { side: basic(0xb8b7b2), top: basic(C.text), inner: basic(C.enamel), pip: basic(C.text), ring: basic(C.text) };
  const roundel = new THREE.Group();
  const cyl = (r: number, h: number, mats: Mat[]) => new THREE.Mesh(new THREE.CylinderGeometry(r, r, h, 48).translate(0, h / 2, 0), mats);
  const disc = (r: number, m: Mat, y: number) => flat(new THREE.Mesh(new THREE.CircleGeometry(r, 48), m), y);
  roundel.add(cyl(1, 0.22, [condMats.side, condMats.top, condMats.side]), disc(0.64, condMats.inner, 0.225), disc(0.2, condMats.pip, 0.23));
  roundel.add(flat(new THREE.Mesh(ringGeo(1.18, 1.3), condMats.ring), 0.01));
  scene.add(roundel);
  const condLabel = label('lbl lbl-cond');
  condLabel.position.set(0, 0, 1.7);
  condLabel.center.set(0.5, 0);
  scene.add(condLabel);

  const accts = new Map<string, AcctObj>();
  const agents = new Map<string, AgentObj>();
  const pucks: Record<string, THREE.BufferGeometry> = { claude: puckGeo('claude'), codex: puckGeo('codex'), opencode: puckGeo('opencode'), generic: puckGeo('generic') };
  const coreGeo = new THREE.CylinderGeometry(PUCK_R * 0.55, PUCK_R * 0.55, 0.22, 32).translate(0, 0.11, 0);
  const haloGeo = ringGeo(0.76, 0.85);
  const focusGeo = ringGeo(0.9, 0.98);
  const trackGeo = ringGeo(0.6, 0.67);

  // ---------- sync with the store ----------

  function syncAccounts(): void {
    const seen = new Set<string>();
    state.accounts.forEach((a, i) => {
      seen.add(a.id);
      const radius = 4.2 + i * 2.4;
      const k = JSON.stringify([radius, a.color, a.rate_limited, a.enabled]);
      let o = accts.get(a.id);
      if (o && o.key === k) return void updateAcctLabel(o, a);
      if (o) dropAcct(o);
      const group = new THREE.Group();
      group.rotation.set(((i * 0.37) % 0.1) - 0.05, 0, ((i * 0.53) % 0.1) - 0.05); // near-flat: reads as a map
      const w = 0.06;
      const geo = a.rate_limited
        ? mergeGeometries(Array.from({ length: 48 }, (_, j) => new THREE.RingGeometry(radius - w, radius + w, 6, 1, (j / 48) * Math.PI * 2, (Math.PI * 2 * 0.6) / 48)))
        : new THREE.RingGeometry(radius - w, radius + w, 160);
      const ring = flat(new THREE.Mesh(geo, basic(a.rate_limited ? C.stop : new THREE.Color(a.color || C.mute).getHex(), a.enabled ? 0.85 : 0.2)), 0.005);
      group.add(ring);
      const lbl = label('lbl lbl-acct');
      lbl.position.set(radius * Math.cos(-0.5), 0, radius * Math.sin(-0.5));
      group.add(lbl);
      scene.add(group);
      group.updateMatrixWorld(true);
      o = { group, ring, radius, label: lbl, key: k, phase: i * 1.7 };
      accts.set(a.id, o);
      updateAcctLabel(o, a);
    });
    for (const [id, o] of accts) if (!seen.has(id)) (dropAcct(o), accts.delete(id));
    sizeGrid(Math.max(0, ...[...accts.values()].map((o) => o.radius)));
  }

  function updateAcctLabel(o: AcctObj, a: Account): void {
    const el = o.label.element;
    const off = !a.enabled || (!!state.accountFilter && state.accountFilter !== a.id);
    o.ring.material.opacity = off ? 0.2 : 0.85;
    const sig = JSON.stringify([a.label, a.used, a.max_concurrent, a.rate_limited, a.enabled, a.color, state.accountFilter]);
    if (el.dataset.sig === sig) return;
    el.dataset.sig = sig;
    el.classList.toggle('off', off);
    el.replaceChildren(
      bullet(a),
      span('name', a.label || a.id),
      span('use', `${a.used}/${a.max_concurrent}`),
      ...(a.rate_limited ? [span('flag', 'rate limited')] : []),
    );
  }

  function dropAcct(o: AcctObj): void {
    o.group.remove(o.label);
    scene.remove(o.group);
    o.ring.geometry.dispose();
    o.ring.material.dispose();
  }

  function syncRuns(): void {
    const seen = new Set<string>();
    const now = Date.now();
    for (const r of state.runs) {
      // the map shows the live fleet: finished agents linger 2 minutes (or while selected), then leave
      const gone = ENDED.has(r.status) && r.ended_at && now - Date.parse(r.ended_at) > 120_000 && state.selected !== r.run_id;
      if (gone) continue;
      seen.add(r.run_id);
      let o = agents.get(r.run_id);
      const kind = pucks[r.kind ?? ''] ? (r.kind as string) : 'generic';
      if (o && o.kind !== kind) (dropAgent(o), agents.delete(r.run_id), (o = undefined));
      if (!o) {
        const puckMats: [Mat, Mat] = [basic(C.mute), basic(C.mute)];
        const coreMats: [Mat, Mat] = [basic(C.mute), basic(C.mute)];
        const puck = new THREE.Mesh(pucks[kind]!, [puckMats[0], puckMats[1], puckMats[0]]);
        puck.userData.runId = r.run_id;
        const core = new THREE.Mesh(coreGeo, [coreMats[0], coreMats[1], coreMats[0]]);
        const halo = flat(new THREE.Mesh(haloGeo, basic(C.signal, 0)), 0.02);
        const focus = flat(new THREE.Mesh(focusGeo, basic(C.text, 0)), 0.03);
        const track = flat(new THREE.Mesh(trackGeo, basic(C.line)), 0.02);
        const arc = flat(new THREE.Mesh(new THREE.BufferGeometry(), basic(C.mute)), 0.025);
        const group = new THREE.Group();
        group.add(puck, core, halo, focus, track, arc);
        const lbl = label('lbl lbl-agent');
        lbl.position.set(0, 0, 0.95); // just below the station on screen
        lbl.center.set(0.5, 0);
        group.add(lbl);
        const edge = new THREE.Line(new THREE.BufferGeometry().setFromPoints(new Array(24).fill(0).map(() => new THREE.Vector3())), new THREE.LineBasicMaterial({ transparent: true, depthWrite: false }));
        scene.add(group, edge);
        o = { run: r, group, puck, puckMats, coreMats, halo, focus, track, arc, arcKey: '', edge, label: lbl, angle: NaN, seed: hash(r.run_id), kind };
        agents.set(r.run_id, o);
      }
      o.run = r;
      const a = accountById(r.account_id);
      const line = new THREE.Color(a?.color || C.mute);
      o.puckMats[1].color.copy(line);
      o.puckMats[0].color.copy(line).multiplyScalar(0.62);
      o.arc.material.color.copy(line);
      o.edge.userData.line = line;
      const core = new THREE.Color(STATUS[r.status] ?? C.mute);
      o.coreMats[1].color.copy(core);
      o.coreMats[0].color.copy(core).multiplyScalar(0.62);
      const { done, total } = stepProgress(r);
      const ak = `${done}/${total}`;
      if (ak !== o.arcKey) {
        o.arcKey = ak;
        o.arc.geometry.dispose();
        const len = total ? (Math.PI * 2 * done) / total : 0;
        // fills clockwise from the top
        o.arc.geometry = len ? new THREE.RingGeometry(0.58, 0.69, 64, 1, Math.PI / 2 - len, len) : new THREE.BufferGeometry();
        o.track.visible = total > 0;
      }
      agentLabel(o, a);
    }
    for (const [id, o] of agents) if (!seen.has(id)) (dropAgent(o), agents.delete(id));
  }

  function agentLabel(o: AgentObj, a: Account | undefined): void {
    const r = o.run;
    const el = o.label.element;
    const sig = JSON.stringify([r.callsign, r.status, r.open_questions.length, state.selected === r.run_id, r.role, r.model, currentStep(r), a?.color]);
    if (el.dataset.sig === sig) return;
    el.dataset.sig = sig;
    el.className = `lbl lbl-agent st-${r.status}${state.selected === r.run_id ? ' sel' : ''}${hovered === r.run_id ? ' hover' : ''}`;
    el.replaceChildren(
      span('cs', r.callsign || r.agent_id),
      ...(r.open_questions.length ? [span('q', '?')] : []),
      span('st', r.status.replace('_', ' ')),
      span('more', [r.role, r.model].filter(Boolean).join(' · ') + ' — ' + currentStep(r)),
    );
  }

  function dropAgent(o: AgentObj): void {
    o.group.remove(o.label);
    scene.remove(o.group, o.edge);
    for (const m of [...o.puckMats, ...o.coreMats, o.halo.material, o.focus.material, o.track.material, o.arc.material]) m.dispose();
    o.arc.geometry.dispose();
    o.edge.geometry.dispose();
    o.edge.material.dispose();
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

  // ---------- frame ----------

  const timer = new THREE.Timer();
  timer.connect(document);
  const tmp = new THREE.Vector3();
  const mid = new THREE.Vector3();
  const curve = new THREE.QuadraticBezierCurve3(new THREE.Vector3(), new THREE.Vector3(), new THREE.Vector3());
  const EDGE = new THREE.Color(C.edge);
  const SIGNAL = new THREE.Color(C.signal);
  let lastInput = performance.now();
  let flight: { p0: THREE.Vector3; t0v: THREE.Vector3; p1: THREE.Vector3; t1v: THREE.Vector3; start: number; dur: number } | null = null;
  let following: string | null = null;
  let lastSelected: string | null = null;
  let hovered: string | null = null;

  function placeAgents(t: number, dt: number): void {
    const byAcct = new Map<string, AgentObj[]>();
    for (const o of agents.values()) {
      const k = o.run.account_id ?? '';
      if (!byAcct.has(k)) byAcct.set(k, []);
      byAcct.get(k)!.push(o);
    }
    for (const [aid, list] of byAcct) {
      const acct = accts.get(aid);
      list.sort((x, y) => x.run.created_at.localeCompare(y.run.created_at));
      list.forEach((o, i) => {
        const radius = acct?.radius ?? 3;
        const drift = reduced ? 0 : t * (0.5 / radius);
        const want = (acct?.phase ?? 0) + (i / list.length) * Math.PI * 2 + drift;
        if (Number.isNaN(o.angle)) o.angle = want;
        else o.angle += Math.atan2(Math.sin(want - o.angle), Math.cos(want - o.angle)) * Math.min(1, dt * 2.5);
        tmp.set(Math.cos(o.angle) * radius, 0, Math.sin(o.angle) * radius);
        if (acct) acct.group.localToWorld(tmp);
        o.group.position.copy(tmp);
      });
    }
  }

  function styleAgent(o: AgentObj, t: number): void {
    const r = o.run;
    const ended = ENDED.has(r.status);
    const sel = state.selected === r.run_id;
    const hov = hovered === r.run_id;
    const dimmed = !!state.accountFilter && state.accountFilter !== r.account_id;
    const alpha = dimmed ? 0.18 : ended ? 0.6 : 1;
    for (const m of [...o.puckMats, ...o.coreMats]) m.opacity = alpha;
    o.group.scale.setScalar(ended ? 0.78 : 1);
    o.halo.material.opacity = r.status === 'waiting' && !dimmed ? (reduced ? 0.75 : 0.35 + 0.5 * (0.5 + 0.5 * Math.sin(t * 1.4 + o.seed))) : 0;
    o.focus.material.opacity = dimmed ? 0 : sel ? 1 : hov ? 0.4 : 0;
    o.arc.material.opacity = dimmed ? 0.15 : ended ? 0.5 : 1;
    o.track.material.opacity = dimmed ? 0.2 : 1;
    o.label.element.style.opacity = dimmed ? '0.25' : ended && !sel ? '0.55' : '1';
    // edge: a thin, low arc from the conductor to the station
    const p = o.group.position;
    curve.v0.set(0, 0.2, 0);
    curve.v2.set(p.x, 0.2, p.z);
    mid.set(p.x * 0.5, 0.45 + p.length() * 0.05, p.z * 0.5);
    curve.v1.copy(mid);
    const pos = o.edge.geometry.attributes.position as THREE.BufferAttribute;
    for (let i = 0; i < pos.count; i++) {
      curve.getPoint(i / (pos.count - 1), tmp);
      pos.setXYZ(i, tmp.x, tmp.y, tmp.z);
    }
    pos.needsUpdate = true;
    o.edge.geometry.computeBoundingSphere();
    const em = o.edge.material;
    if (sel) (em.color.copy(o.edge.userData.line ?? EDGE), (em.opacity = 0.9));
    else if (r.status === 'waiting') (em.color.copy(SIGNAL), (em.opacity = dimmed ? 0.1 : 0.6));
    else (em.color.copy(EDGE), (em.opacity = dimmed ? 0.05 : ended ? 0.12 : 0.3));
  }

  function cameraWork(now: number, dt: number): void {
    if (state.selected !== lastSelected) {
      lastSelected = state.selected;
      const o = state.selected ? agents.get(state.selected) : undefined;
      if (o) {
        const dir = tmp.copy(camera.position).sub(o.group.position).normalize();
        const p1 = o.group.position.clone().add(dir.multiplyScalar(9)).add(new THREE.Vector3(0, 2, 0));
        flight = { p0: camera.position.clone(), t0v: controls.target.clone(), p1, t1v: o.group.position.clone(), start: now, dur: reduced ? 1 : 900 };
        following = o.run.run_id;
      } else {
        flight = { p0: camera.position.clone(), t0v: controls.target.clone(), p1: OVERVIEW.pos.clone(), t1v: OVERVIEW.target.clone(), start: now, dur: reduced ? 1 : 900 };
        following = null;
      }
    }
    const f = following ? agents.get(following) : undefined;
    if (flight) {
      const k = Math.min(1, (now - flight.start) / flight.dur);
      const e = k < 0.5 ? 4 * k * k * k : 1 - Math.pow(-2 * k + 2, 3) / 2;
      if (f) flight.t1v.copy(f.group.position); // the agent keeps moving during the flight
      camera.position.lerpVectors(flight.p0, flight.p1, e);
      controls.target.lerpVectors(flight.t0v, flight.t1v, e);
      if (k >= 1) flight = null;
    } else if (f) {
      const delta = tmp.copy(f.group.position).sub(controls.target);
      controls.target.add(delta);
      camera.position.add(delta);
    }
    controls.autoRotate = !reduced && !following && now - lastInput > IDLE_ROTATE_MS;
    controls.update(dt);
  }

  function frame(): void {
    timer.update();
    const dt = Math.min(timer.getDelta(), 0.1);
    const t = timer.getElapsed();
    const now = performance.now();
    placeAgents(t, dt);
    for (const o of agents.values()) styleAgent(o, t);
    const busy = state.conductor.state === 'thinking' || state.conductor.state === 'acting';
    condMats.ring.color.setHex(busy ? C.signal : C.text);
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
    const hits = ray.intersectObjects([...agents.values()].map((o) => o.puck), false);
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
    if (flight) flight = null; // the user takes over
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
      syncAccounts();
      syncRuns();
      syncConductor();
    },
  };
  return ctl;
}

function label(cls: string): CSS2DObject {
  const el = document.createElement('div');
  el.className = cls;
  return new CSS2DObject(el);
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
