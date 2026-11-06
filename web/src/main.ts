import '@fontsource-variable/mona-sans';
import '@fontsource-variable/jetbrains-mono';
import './style.css';
import { setUnauthorizedHandler } from './api';
import { initToken, loginBox, startSheet } from './auth';
import { connect, disconnect } from './stream';
import { emit, refresh, select, setOverlay, state, subscribe } from './store';
import { accountsRail } from './ui/accounts';
import { detailPanel } from './ui/detail';
import { overlayHost } from './ui/overlay';
import { runsList, visibleRuns } from './ui/runs';
import { timelinePanel } from './ui/timeline';
import { topbar } from './ui/topbar';
import { h, typing } from './util';

const app = document.getElementById('app')!;

function start(): void {
  app.replaceChildren();
  state.conn = 'connecting';

  const bar = h('header', { id: 'topbar' });
  const rail = h('aside', { id: 'rail', 'aria-label': 'Accounts' });
  const list = h('section', { id: 'list', 'aria-label': 'Agents' });
  // The three.js scene is loaded on first show; it reads the same store as the list.
  const scene = h('section', { id: 'scene', 'aria-label': 'Scene (press L for the list)', hidden: true });
  let sceneCtl: import('./scene').SceneCtl | null | undefined; // undefined = not loaded yet
  let sceneLoading = false;
  const syncScene = () => {
    const want = state.view === 'scene';
    if (want && sceneCtl === undefined && !sceneLoading) {
      sceneLoading = true;
      void import('./scene').then((m) => {
        sceneCtl = m.mountScene(scene);
        if (!sceneCtl) {
          state.view = 'list';
          state.notices.push('3D scene unavailable (no WebGL); showing the list.');
        }
        emit();
      });
    }
    sceneCtl?.setVisible(want);
    sceneCtl?.update();
  };
  const timeline = h('section', { id: 'timeline', 'aria-label': 'Timeline', hidden: true });
  const detail = h('aside', { id: 'detail', 'aria-label': 'Selected agent', hidden: true });
  const overlay = h('div', { id: 'overlay', hidden: true });
  app.append(bar, h('div', { id: 'layout' }, rail, h('main', { id: 'main' }, list, scene, timeline), detail), overlay);

  const toggleView = () => {
    state.view = state.view === 'list' ? 'scene' : 'list';
    localStorage.setItem('baton.view', state.view);
    emit();
  };
  state.view = localStorage.getItem('baton.view') === 'list' ? 'list' : 'scene';
  const toggleTimeline = () => {
    state.timeline = !state.timeline;
    localStorage.setItem('baton.timeline', state.timeline ? '1' : '0');
    emit();
  };
  const parts = [topbar(bar, toggleView, toggleTimeline), accountsRail(rail), runsList(list), timelinePanel(timeline), detailPanel(detail), overlayHost(overlay)];
  const paint = () => {
    if (state.conn === 'auth') return needLogin('The hub rejected the token. Paste a valid admin token.');
    list.hidden = state.view !== 'list';
    scene.hidden = state.view !== 'scene';
    document.body.classList.toggle('view-scene', state.view === 'scene');
    parts.forEach((p) => p());
    syncScene();
  };
  const unsub = subscribe(paint);
  const tick = window.setInterval(emit, 3000); // ages and events/min

  function needLogin(msg: string): void {
    unsub();
    clearInterval(tick);
    disconnect();
    document.removeEventListener('keydown', onKey);
    document.body.classList.remove('has-detail');
    app.replaceChildren(loginBox(msg, () => {
      void refresh();
      start();
    }));
  }
  setUnauthorizedHandler(() => {
    state.conn = 'auth';
    emit();
  });

  const onKey = (e: KeyboardEvent) => {
    if (e.ctrlKey || e.metaKey || e.altKey) return;
    if (e.key === 'Escape') {
      if (state.overlay) setOverlay(null);
      else if (state.selected) select(null);
      return;
    }
    if (typing(e)) return;
    const k = e.key.toLowerCase();
    if (k === 's' && !state.overlay) {
      e.preventDefault();
      setOverlay('spawn');
    } else if (k === 'c' && !state.overlay) {
      e.preventDefault();
      setOverlay('chat');
    } else if (k === 'l') toggleView();
    else if (k === 't') toggleTimeline();
    else if ((e.key === 'ArrowDown' || e.key === 'ArrowUp') && !state.overlay) {
      const rows = visibleRuns();
      if (!rows.length) return;
      e.preventDefault();
      const i = rows.findIndex((r) => r.run_id === state.selected);
      const next = e.key === 'ArrowDown' ? (i + 1) % rows.length : (i <= 0 ? rows.length - 1 : i - 1);
      const id = rows[next]!.run_id;
      select(id);
      requestAnimationFrame(() => document.querySelector<HTMLElement>(`tr[data-id="${CSS.escape(id)}"]`)?.focus());
    }
  };
  document.addEventListener('keydown', onKey);

  connect();
  void refresh();
  if (startSheet === 'chat' || startSheet === 'approvals' || startSheet === 'spawn' || startSheet === 'config') setOverlay(startSheet);
  emit();
}

if (initToken()) start();
else {
  app.replaceChildren(
    loginBox('No token yet. Open the URL the hub printed, or paste the admin token.', () => start()),
  );
}
