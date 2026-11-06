// The 3D view: conductor at the centre, one tilted orbit per account, one node per agent.
// Lazy-loaded (three.js is only fetched when the scene is first shown). Reads the same store as the list.
import * as THREE from 'three';
import { OrbitControls } from 'three/addons/controls/OrbitControls.js';
import { EffectComposer } from 'three/addons/postprocessing/EffectComposer.js';
import { RenderPass } from 'three/addons/postprocessing/RenderPass.js';
import { UnrealBloomPass } from 'three/addons/postprocessing/UnrealBloomPass.js';
import { OutputPass } from 'three/addons/postprocessing/OutputPass.js';
import { CSS2DObject, CSS2DRenderer } from 'three/addons/renderers/CSS2DRenderer.js';
import { accountById, select, state, type Account, type Run } from './store';
import { currentStep, stepProgress } from './ui/runs';
import { ENDED } from './util';

const reduced = matchMedia('(prefers-reduced-motion: reduce)').matches;
const C = { ink: 0x121317, paper: 0xf2f1ec, mute: 0xa9adb9, brass: 0xffcd1c, hot: 0xff8a3d, bad: 0xf0524a, rule: 0x30343e };
const OVERVIEW = { pos: new THREE.Vector3(0, 10, 17), target: new THREE.Vector3(0, 0, 0) };
const IDLE_ROTATE_MS = 20_000;

type AgentObj = {
  run: Run;
  body: THREE.Mesh<THREE.BufferGeometry, THREE.MeshStandardMaterial>;
  holder: THREE.Group; // faces the camera: progress arc + track
  arc: THREE.Mesh<THREE.BufferGeometry, THREE.MeshBasicMaterial>;
  track: THREE.Mesh<THREE.BufferGeometry, THREE.MeshBasicMaterial>;
  arcKey: string;
  edge: THREE.Line<THREE.BufferGeometry, THREE.LineBasicMaterial>;
  label: CSS2DObject;
  angle: number;
  seed: number;
  kind: string;
};
type AcctObj = { group: THREE.Group; ring: THREE.LineLoop; radius: number; label: CSS2DObject; key: string; phase: number };

export interface SceneCtl {
  setVisible(v: boolean): void;
  update(): void;
}

export function mountScene(host: HTMLElement): SceneCtl | null {
  let renderer: THREE.WebGLRenderer;
  try {
    renderer = new THREE.WebGLRenderer({ antialias: true, powerPreference: 'high-performance' });
  } catch {
    return null; // no WebGL: caller keeps the list view
  }
  renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));
  renderer.toneMapping = THREE.ACESFilmicToneMapping;
  renderer.toneMappingExposure = 0.95;
  host.replaceChildren(renderer.domElement);
  const labels = new CSS2DRenderer();
  labels.domElement.className = 'scene-labels';
  host.append(labels.domElement, Object.assign(document.createElement('p'), { className: 'scene-hint', textContent: 'Drag to orbit, scroll to zoom, click an agent. L switches to the list, T shows the timeline.' }));

  const scene = new THREE.Scene();
  scene.background = new THREE.Color(C.ink);
  scene.fog = new THREE.FogExp2(C.ink, 0.016);
  const camera = new THREE.PerspectiveCamera(48, 1, 0.1, 400);
  camera.position.copy(OVERVIEW.pos);
  const controls = new OrbitControls(camera, renderer.domElement);
  controls.enableDamping = true;
  controls.minDistance = 3;
  controls.maxDistance = 60;
  controls.autoRotateSpeed = 0.35;
  controls.target.copy(OVERVIEW.target);

  const composer = new EffectComposer(renderer);
  composer.addPass(new RenderPass(scene, camera));
  const bloom = new UnrealBloomPass(new THREE.Vector2(1, 1), 0.55, 0.45, 0.42);
  composer.addPass(bloom);
  composer.addPass(new OutputPass());

  scene.add(new THREE.AmbientLight(0x9fb0d8, 0.5));
  const key = new THREE.DirectionalLight(0xfff1dc, 1.4);
  key.position.set(6, 12, 8);
  scene.add(key);
  const coreLight = new THREE.PointLight(0xfff6e0, 26, 30, 1.6);
  scene.add(coreLight);
  scene.add(starfield());

  // conductor: a warm core inside a slow wire shell
  const core = new THREE.Mesh(new THREE.IcosahedronGeometry(0.85, 4), new THREE.MeshStandardMaterial({ color: 0xb9b8b2, emissive: C.paper, emissiveIntensity: 0.25, roughness: 0.6, metalness: 0.1 }));
  const shell = new THREE.Mesh(new THREE.IcosahedronGeometry(1.4, 1), new THREE.MeshBasicMaterial({ color: C.paper, wireframe: true, transparent: true, opacity: 0.09 }));
  scene.add(core, shell);
  const condLabel = label('lbl lbl-cond');
  condLabel.position.set(0, -1.55, 0);
  condLabel.center.set(0.5, 0);
  scene.add(condLabel);

  const accts = new Map<string, AcctObj>();
  const agents = new Map<string, AgentObj>();
  const geoms: Record<string, THREE.BufferGeometry> = {
    claude: new THREE.IcosahedronGeometry(0.36, 0),
    codex: new THREE.OctahedronGeometry(0.42, 0),
    opencode: new THREE.DodecahedronGeometry(0.37, 0),
    generic: new THREE.SphereGeometry(0.33, 24, 16),
  };
  const trackGeo = new THREE.TorusGeometry(0.66, 0.018, 6, 64);

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
      group.rotation.set(0.12 + ((i * 0.37) % 0.5) - 0.25, 0, ((i * 0.53) % 0.4) - 0.2);
      const pts = Array.from({ length: 160 }, (_, j) => {
        const t = (j / 160) * Math.PI * 2;
        return new THREE.Vector3(Math.cos(t) * radius, 0, Math.sin(t) * radius);
      });
      const geo = new THREE.BufferGeometry().setFromPoints(pts);
      const color = new THREE.Color(a.rate_limited ? C.bad : a.color || C.mute);
      const mat = a.rate_limited
        ? new THREE.LineDashedMaterial({ color, dashSize: 0.35, gapSize: 0.25, transparent: true, opacity: 0.75 })
        : new THREE.LineBasicMaterial({ color, transparent: true, opacity: a.enabled ? 0.42 : 0.12 });
      const ring = new THREE.LineLoop(geo, mat);
      if (a.rate_limited) ring.computeLineDistances();
      group.add(ring);
      const lbl = label('lbl lbl-acct');
      lbl.position.set(radius * Math.cos(-0.5), 0, radius * Math.sin(-0.5) + 0.2);
      group.add(lbl);
      scene.add(group);
      group.updateMatrixWorld(true);
      o = { group, ring, radius, label: lbl, key: k, phase: i * 1.7 };
      accts.set(a.id, o);
      updateAcctLabel(o, a);
    });
    for (const [id, o] of accts) if (!seen.has(id)) (dropAcct(o), accts.delete(id));
  }

  function updateAcctLabel(o: AcctObj, a: Account): void {
    const el = o.label.element;
    const sig = JSON.stringify([a.label, a.used, a.max_concurrent, a.rate_limited, a.enabled, a.color, state.accountFilter]);
    if (el.dataset.sig === sig) return;
    el.dataset.sig = sig;
    el.style.setProperty('--c', a.color || '#a9adb9');
    el.classList.toggle('off', !a.enabled || (!!state.accountFilter && state.accountFilter !== a.id));
    el.replaceChildren(
      span('name', a.label || a.id),
      span('use', `${a.used}/${a.max_concurrent}`),
      ...(a.rate_limited ? [span('flag', 'rate limited')] : []),
    );
  }

  function dropAcct(o: AcctObj): void {
    scene.remove(o.group);
    o.ring.geometry.dispose();
    (o.ring.material as THREE.Material).dispose();
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
      const kind = geoms[r.kind ?? ''] ? (r.kind as string) : 'generic';
      if (o && o.kind !== kind) (dropAgent(o), agents.delete(r.run_id), (o = undefined));
      if (!o) {
        const body = new THREE.Mesh(geoms[kind]!, new THREE.MeshStandardMaterial({ roughness: 0.35, metalness: 0.15, flatShading: kind !== 'generic', transparent: true }));
        body.userData.runId = r.run_id;
        const track = new THREE.Mesh(trackGeo, new THREE.MeshBasicMaterial({ color: C.rule, transparent: true, opacity: 0.55 }));
        const arc = new THREE.Mesh(new THREE.BufferGeometry(), new THREE.MeshBasicMaterial({ transparent: true }));
        arc.rotation.z = Math.PI / 2;
        arc.scale.x = -1; // fill clockwise from the top
        const holder = new THREE.Group();
        holder.add(track, arc);
        const edge = new THREE.Line(new THREE.BufferGeometry().setFromPoints(new Array(24).fill(0).map(() => new THREE.Vector3())), new THREE.LineBasicMaterial({ transparent: true }));
        const lbl = label('lbl lbl-agent');
        lbl.position.set(0, -0.8, 0); // holder space: always just below the node on screen
        lbl.center.set(0.5, 0);
        holder.add(lbl);
        scene.add(body, holder, edge);
        o = { run: r, body, holder, arc, track, arcKey: '', edge, label: lbl, angle: NaN, seed: hash(r.run_id), kind };
        agents.set(r.run_id, o);
      }
      o.run = r;
      const a = accountById(r.account_id);
      const col = new THREE.Color(a?.color || C.paper);
      o.body.material.color.copy(col);
      o.edge.material.color.copy(col);
      o.arc.material.color.copy(col).multiplyScalar(1.25);
      const { done, total } = stepProgress(r);
      const ak = `${done}/${total}`;
      if (ak !== o.arcKey) {
        o.arcKey = ak;
        o.arc.geometry.dispose();
        o.arc.geometry = total && done ? new THREE.TorusGeometry(0.66, 0.045, 6, 64, (Math.PI * 2 * done) / total) : new THREE.BufferGeometry();
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
    el.className = `lbl lbl-agent st-${r.status}${state.selected === r.run_id ? ' sel' : ''}`;
    el.style.setProperty('--c', a?.color || '#f2f1ec');
    el.replaceChildren(
      span('cs', r.callsign || r.agent_id),
      ...(r.open_questions.length ? [span('q', '?')] : []),
      span('st', r.status.replace('_', ' ')),
      span('more', [r.role, r.model].filter(Boolean).join(' · ') + ' — ' + currentStep(r)),
    );
  }

  function dropAgent(o: AgentObj): void {
    o.holder.remove(o.label);
    scene.remove(o.body, o.holder, o.edge);
    o.body.material.dispose();
    o.arc.geometry.dispose();
    o.arc.material.dispose();
    o.track.material.dispose();
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
  const grey = new THREE.Color(0x6b7080);
  const COL = { brass: new THREE.Color(C.brass), hot: new THREE.Color(C.hot), bad: new THREE.Color(C.bad), paper: new THREE.Color(C.paper) };
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
        o.body.position.copy(tmp);
        o.holder.position.copy(tmp);
      });
    }
  }

  function styleAgent(o: AgentObj, t: number): void {
    const r = o.run;
    const m = o.body.material;
    const ended = ENDED.has(r.status);
    const sel = state.selected === r.run_id;
    const dimmed = !!state.accountFilter && state.accountFilter !== r.account_id;
    const wave = (f: number) => (reduced ? 0.5 : 0.5 + 0.5 * Math.sin(t * f + o.seed));
    let glow = 0.38;
    let emissive = m.color;
    let scale = 1;
    switch (r.status) {
      case 'waiting': emissive = COL.brass; glow = 0.35 + 0.65 * wave(3.2); break;
      case 'blocked': emissive = COL.hot; glow = reduced ? 0.6 : 0.25 + (Math.sin(t * 23 + o.seed) > 0.3 ? 0.6 : 0.05); break;
      case 'paused': m.color.lerp(grey, 0.75); glow = 0.12; break;
      case 'stalled': glow = 0.15 + 0.25 * wave(0.8); break;
      case 'rate_limited': emissive = COL.bad; glow = 0.45; break;
      case 'starting': glow = 0.3 + 0.4 * wave(5); break;
      case 'done': emissive = COL.paper; glow = 0.22; scale = 0.8; break;
      case 'failed': case 'killed': emissive = COL.bad; glow = 0.3; scale = 0.75; break;
      case 'exited': m.color.lerp(grey, 0.6); glow = 0.08; scale = 0.7; break;
    }
    m.emissive.copy(emissive);
    m.emissiveIntensity = glow;
    m.opacity = dimmed ? 0.18 : 1;
    const s = scale * (sel ? 1.3 : 1) * (hovered === r.run_id ? 1.12 : 1);
    o.body.scale.setScalar(s);
    if (!reduced && !ended) o.body.rotation.set(t * 0.3 + o.seed, t * 0.45 + o.seed, 0);
    o.holder.scale.setScalar(s);
    o.holder.quaternion.copy(camera.quaternion);
    o.arc.material.opacity = dimmed ? 0.1 : ended ? 0.45 : 1;
    o.track.material.opacity = dimmed ? 0.08 : 0.5;
    o.label.element.style.opacity = dimmed ? '0.25' : ended && !sel ? '0.55' : '1';
    // edge: a gentle arc from the conductor to the agent
    curve.v0.set(0, 0, 0);
    curve.v2.copy(o.body.position);
    mid.copy(o.body.position).multiplyScalar(0.5);
    mid.y += 1.2 + o.body.position.length() * 0.08;
    curve.v1.copy(mid);
    const pos = o.edge.geometry.attributes.position as THREE.BufferAttribute;
    for (let i = 0; i < pos.count; i++) {
      curve.getPoint(i / (pos.count - 1), tmp);
      pos.setXYZ(i, tmp.x, tmp.y, tmp.z);
    }
    pos.needsUpdate = true;
    o.edge.geometry.computeBoundingSphere();
    o.edge.material.opacity = dimmed ? 0.03 : sel ? 0.85 : ended ? 0.06 : r.status === 'waiting' ? 0.55 : 0.22;
  }

  function cameraWork(now: number, dt: number): void {
    if (state.selected !== lastSelected) {
      lastSelected = state.selected;
      const o = state.selected ? agents.get(state.selected) : undefined;
      if (o) {
        const dir = tmp.copy(camera.position).sub(o.body.position).normalize();
        const p1 = o.body.position.clone().add(dir.multiplyScalar(9)).add(new THREE.Vector3(0, 2, 0));
        flight = { p0: camera.position.clone(), t0v: controls.target.clone(), p1, t1v: o.body.position.clone(), start: now, dur: reduced ? 1 : 900 };
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
      if (f) flight.t1v.copy(f.body.position); // the agent keeps moving during the flight
      camera.position.lerpVectors(flight.p0, flight.p1, e);
      controls.target.lerpVectors(flight.t0v, flight.t1v, e);
      if (k >= 1) flight = null;
    } else if (f) {
      const delta = tmp.copy(f.body.position).sub(controls.target);
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
    const beat = reduced ? 0.5 : 0.5 + 0.5 * Math.sin(t * (busy ? 3 : 1.2));
    core.material.emissiveIntensity = state.conductor.state === 'off' ? 0.08 : (busy ? 0.42 : 0.22) + beat * (busy ? 0.3 : 0.08);
    core.material.emissive.lerp(busy ? COL.brass : COL.paper, 0.08);
    core.scale.setScalar(1 + (reduced ? 0 : beat * (busy ? 0.05 : 0.02)));
    coreLight.intensity = 14 + core.material.emissiveIntensity * 16;
    if (!reduced) shell.rotation.set(t * 0.05, t * 0.08, 0);
    cameraWork(now, dt);
    composer.render();
    labels.render(scene, camera);
  }

  // ---------- input ----------

  const ray = new THREE.Raycaster();
  const ndc = new THREE.Vector2();
  function pick(ev: PointerEvent): string | null {
    const rect = renderer.domElement.getBoundingClientRect();
    ndc.set(((ev.clientX - rect.left) / rect.width) * 2 - 1, -((ev.clientY - rect.top) / rect.height) * 2 + 1);
    ray.setFromCamera(ndc, camera);
    const hits = ray.intersectObjects([...agents.values()].map((o) => o.body), false);
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
    composer.setSize(w, h);
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

function starfield(): THREE.Points {
  const n = 1400;
  const pos = new Float32Array(n * 3);
  for (let i = 0; i < n; i++) {
    const v = new THREE.Vector3().randomDirection().multiplyScalar(70 + Math.random() * 90);
    pos.set([v.x, v.y, v.z], i * 3);
  }
  const g = new THREE.BufferGeometry();
  g.setAttribute('position', new THREE.BufferAttribute(pos, 3));
  return new THREE.Points(g, new THREE.PointsMaterial({ color: 0xa9adb9, size: 0.3, sizeAttenuation: true, transparent: true, opacity: 0.4, fog: false, depthWrite: false }));
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
