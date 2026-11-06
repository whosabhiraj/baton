import { I, icon } from '../icons';
// Modal host: builds the panel once when it opens (so typed text survives live updates).
import { setOverlay, state, type Overlay } from '../store';
import { h } from '../util';
import { approvalsPanel } from './approvals';
import { chatPanel } from './chat';
import { configPanel } from './config';
import { spawnPanel } from './spawn';

export interface Panel {
  title: string;
  el: HTMLElement;
  update?: () => void;
  close?: () => void; // called when the sheet closes (streams, timers)
}

const BUILD: Record<Exclude<Overlay, null>, () => Panel> = {
  spawn: spawnPanel,
  approvals: approvalsPanel,
  config: configPanel,
  chat: chatPanel,
};

export function overlayHost(host: HTMLElement): () => void {
  let shown: Overlay = null;
  let panel: Panel | null = null;
  let opener: HTMLElement | null = null;

  return () => {
    if (state.overlay !== shown) {
      if (shown === null) opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      shown = state.overlay;
      host.replaceChildren();
      panel?.close?.();
      panel = null;
      host.hidden = shown === null;
      if (shown === null) {
        opener?.focus();
        return;
      }
      panel = BUILD[shown]();
      const box = h(
        'div',
        { class: 'sheet', role: 'dialog', 'aria-modal': 'true', 'aria-label': panel.title },
        h('div', { class: 'sheet-head' }, h('h2', {}, panel.title), h('button', { class: 'icon-btn', 'aria-label': 'Close', title: 'Close (Esc)', onclick: () => setOverlay(null) }, icon(I.close))),
        panel.el,
      );
      host.append(h('div', { class: 'backdrop', onclick: () => setOverlay(null) }), box);
      queueMicrotask(() => box.querySelector<HTMLElement>('input:not([type=hidden]), select, textarea, button.primary')?.focus());
    }
    panel?.update?.();
  };
}
