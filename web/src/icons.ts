// Lucide icons (one stroke weight everywhere); decorative, so hidden from screen readers.
import { createElement, ChartGantt, GitBranch, Inbox, List, MessageSquare, OctagonX, Orbit, Pause, Play, Plus, Send, Settings2, Square, X, type IconNode } from 'lucide';

export const I = { chat: MessageSquare, close: X, approvals: Inbox, config: Settings2, timeline: ChartGantt, scene: Orbit, list: List, spawn: Plus, send: Send, pause: Pause, play: Play, stop: Square, kill: OctagonX, branch: GitBranch };

export function icon(node: IconNode, size = 16): SVGElement {
  const el = createElement(node, { width: size, height: size, 'stroke-width': 1.75, 'aria-hidden': 'true', class: 'ico' });
  return el;
}
