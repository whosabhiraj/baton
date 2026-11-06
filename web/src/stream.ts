// SSE connection (A12): hello carries the hub instance, reset means "refetch everything".
import { getToken } from './auth';
import { emit, fullReload, onEvent, onNotice, refresh, state, type StoredEvent } from './store';

let es: EventSource | null = null;
let retry: number | undefined;
let seenInstance = '';

export function connect(): void {
  disconnect();
  if (!getToken()) return;
  state.conn = 'connecting';
  emit();
  const src = new EventSource('/api/stream?token=' + encodeURIComponent(getToken()));
  es = src;

  src.onopen = () => {
    state.conn = 'live';
    emit();
  };
  src.addEventListener('hello', (e) => {
    const inst = parse<{ instance?: string }>(e)?.instance ?? '';
    state.conn = 'live';
    if (seenInstance && inst && inst !== seenInstance) void fullReload(); // hub restarted
    else void refresh();
    if (inst) seenInstance = inst;
  });
  src.addEventListener('reset', () => void fullReload());
  src.addEventListener('baton', (e) => {
    const ev = parse<StoredEvent>(e);
    if (ev) onEvent(ev);
  });
  src.addEventListener('hub', (e) => {
    const n = parse<{ type?: string }>(e);
    if (n) onNotice(n as { type?: string } & Record<string, unknown>);
  });
  src.onerror = () => {
    if (state.conn !== 'auth') state.conn = 'down';
    state.connNote = 'stream interrupted, retrying';
    emit();
    // A closed source (e.g. 401) does not retry by itself. Probe state to tell auth from outage.
    if (src.readyState === EventSource.CLOSED) {
      void refresh();
      window.clearTimeout(retry);
      retry = window.setTimeout(() => {
        if (state.conn !== 'auth') connect();
      }, 3000);
    }
  };
}

export function disconnect(): void {
  window.clearTimeout(retry);
  es?.close();
  es = null;
}

function parse<T>(e: Event): T | null {
  try {
    return JSON.parse((e as MessageEvent<string>).data) as T;
  } catch {
    return null;
  }
}
