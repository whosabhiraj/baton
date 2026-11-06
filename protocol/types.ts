// BATON/1 protocol types — the single source of truth.
// JSON Schemas in protocol/schema/*.json are generated from this file (npm run schemas).
// See protocol/PROTOCOL.md for the prose contract.
import { z } from 'zod';

export const PROTOCOL_VERSION = 1;

// ---------------------------------------------------------------------------
// Payloads: agent → hub
// ---------------------------------------------------------------------------

export const PlanStep = z.object({
  id: z.string().min(1),
  title: z.string().min(1),
  acceptance: z.string().min(1),
});
export type PlanStep = z.infer<typeof PlanStep>;

export const HelloPayload = z.looseObject({
  model: z.string().optional(),
  cwd: z.string().optional(),
  brief_ack: z.literal(true),
});

/** Either a full (possibly revised) plan, or `{ confirm: true }` (hub fills in the conductor's steps). */
export const PlanPayload = z.union([
  z.looseObject({
    steps: z.array(PlanStep).min(1),
    reason: z.string().optional(),
  }),
  z.looseObject({
    confirm: z.literal(true),
    steps: z.array(PlanStep).optional(), // filled in by the hub
  }),
]);

export const StepStartPayload = z.looseObject({
  step_id: z.string().min(1),
  note: z.string().optional(),
});

export const StepDonePayload = z.looseObject({
  step_id: z.string().min(1),
  summary: z.string().min(1),
  evidence: z.string().optional(),
});

export const StepBlockedPayload = z.looseObject({
  step_id: z.string().min(1),
  reason: z.string().min(1),
});

export const ProgressPayload = z.looseObject({
  percent: z.number().min(0).max(100),
  note: z.string(),
});

export const ArtifactKind = z.enum(['file', 'diff', 'commit', 'url', 'log']);
export const ArtifactPayload = z.looseObject({
  kind: ArtifactKind,
  ref: z.string().min(1),
  note: z.string().optional(),
});

export const QuestionPayload = z.looseObject({
  qid: z.string().min(1),
  text: z.string().min(1),
  options: z.array(z.string()).optional(),
  blocking: z.boolean(),
});

export const HeartbeatPayload = z.looseObject({});

export const DoneStatus = z.enum(['success', 'partial', 'failed']);
export const DonePayload = z.looseObject({
  status: DoneStatus,
  summary: z.string().min(1),
  followups: z.string().optional(),
});

export const ErrorPayload = z.looseObject({
  message: z.string().min(1),
  fatal: z.boolean(),
});

/** Payload schema per agent-sendable type. */
export const AgentPayloads = {
  hello: HelloPayload,
  plan: PlanPayload,
  step_start: StepStartPayload,
  step_done: StepDonePayload,
  step_blocked: StepBlockedPayload,
  progress: ProgressPayload,
  artifact: ArtifactPayload,
  question: QuestionPayload,
  heartbeat: HeartbeatPayload,
  done: DonePayload,
  error: ErrorPayload,
} as const;
export type AgentMessageType = keyof typeof AgentPayloads;
export const AGENT_TYPES = Object.keys(AgentPayloads) as AgentMessageType[];

// ---------------------------------------------------------------------------
// Payloads: hub-generated (agents may never send these)
// ---------------------------------------------------------------------------

export const ActivityPayload = z.looseObject({
  tool: z.enum(['edit', 'shell', 'read', 'test', 'search', 'web', 'other']),
  detail: z.string(),
  raw_kind: z.string().optional(),
});

export const ExitPayload = z.looseObject({
  code: z.number().nullable(),
  signal: z.string().nullable(),
  diff_stat: z.string().nullable(),
  duration_s: z.number(),
});

export const RateLimitedPayload = z.looseObject({ detail: z.string() });
export const ReasonPayload = z.looseObject({ reason: z.string().optional() });

export const StalledPayload = z.looseObject({
  last_seen: z.string(), // ISO timestamp of the last event from the agent
  timeout_s: z.number(),
});

export const HubPayloads = {
  activity: ActivityPayload,
  exit: ExitPayload,
  rate_limited: RateLimitedPayload,
  killed: ReasonPayload,
  paused: ReasonPayload,
  resumed: ReasonPayload,
  stalled: StalledPayload,
  // `to_agent` records a hub→agent inbox delivery in the run's event log (see Amendments).
  to_agent: z.looseObject({
    n: z.number().int().positive(),
    type: z.string(),
    payload: z.record(z.string(), z.unknown()),
  }),
} as const;
export type HubMessageType = keyof typeof HubPayloads;
export const HUB_ONLY_TYPES = Object.keys(HubPayloads) as HubMessageType[];

// ---------------------------------------------------------------------------
// Envelope
// ---------------------------------------------------------------------------

/** Where an event entered the hub. */
export const EventSource = z.enum(['cli', 'spool', 'hub', 'adapter']);

/** What an agent (CLI or hand-written spool line) sends. `seq` is ignored by the hub (see Amendments). */
export const IncomingEnvelope = z.looseObject({
  v: z.literal(1),
  run_id: z.string().min(1),
  agent_id: z.string().min(1),
  seq: z.number().int().optional(),
  ts: z.string().optional(), // ISO 8601; hub fills it if missing
  type: z.string().min(1),
  payload: z.record(z.string(), z.unknown()).default({}),
  msg_id: z.string().min(1).optional(), // client-generated UUID; required from the CLI
});
export type IncomingEnvelope = z.infer<typeof IncomingEnvelope>;

/** What the hub stores in events.jsonl and fans out. */
export const StoredEvent = z.looseObject({
  v: z.literal(1),
  run_id: z.string(),
  agent_id: z.string(),
  seq: z.number().int().positive(), // hub-assigned, per run, strictly increasing
  ts: z.string(),
  type: z.string(),
  payload: z.record(z.string(), z.unknown()),
  msg_id: z.string(), // client UUID, or `spool@<byteOffset>` / `hub-<uuid>`
  src: EventSource,
  client_seq: z.number().int().optional(),
});
export type StoredEvent = z.infer<typeof StoredEvent>;

// ---------------------------------------------------------------------------
// Hub → agent inbox messages ($BATON_RUN_DIR/inbox/<n>.json)
// ---------------------------------------------------------------------------

export const InboxPayloads = {
  message: z.looseObject({ from: z.enum(['conductor', 'human']), text: z.string().min(1) }),
  answer: z.looseObject({ qid: z.string().min(1), text: z.string().min(1) }),
  plan_update: z.looseObject({ steps: z.array(PlanStep).min(1) }),
  pause: ReasonPayload,
  resume: ReasonPayload,
  stop: ReasonPayload,
} as const;
export type InboxMessageType = keyof typeof InboxPayloads;
export const INBOX_TYPES = Object.keys(InboxPayloads) as InboxMessageType[];

export const InboxMessage = z.looseObject({
  v: z.literal(1),
  n: z.number().int().positive(), // 1, 2, 3 ... per run; also the file name (zero-padded)
  ts: z.string(),
  type: z.string(),
  payload: z.record(z.string(), z.unknown()),
});
export type InboxMessage = z.infer<typeof InboxMessage>;

// ---------------------------------------------------------------------------
// Run record (runs/<run_id>/run.json)
// ---------------------------------------------------------------------------

export const RunStatus = z.enum([
  'starting', 'running', 'waiting', 'blocked', 'paused', 'stalled',
  'rate_limited', 'done', 'failed', 'killed', 'exited',
]);
export type RunStatus = z.infer<typeof RunStatus>;

export const RunRecord = z.looseObject({
  run_id: z.string(),
  agent_id: z.string(),
  callsign: z.string().optional(),
  account_id: z.string().optional(),
  role: z.string().optional(),
  kind: z.string().optional(),
  model: z.string().optional(),
  status: RunStatus,
  created_at: z.string(),
  ended_at: z.string().optional(),
  token_sha256: z.string(), // the plaintext per-run token is never stored
  token_revoked: z.boolean(),
  plan: z.array(PlanStep).default([]), // the conductor's plan (used by `plan --confirm`)
});
export type RunRecord = z.infer<typeof RunRecord>;

// ---------------------------------------------------------------------------
// Validation helpers
// ---------------------------------------------------------------------------

export function isHubOnlyType(type: string): boolean {
  return (HUB_ONLY_TYPES as string[]).includes(type);
}

/**
 * Validate the payload of an agent-sent message.
 * Known agent types are checked strictly; unknown (non-hub) types pass (forward compatibility).
 */
export function validateAgentPayload(
  type: string,
  payload: unknown,
): { ok: true; payload: Record<string, unknown> } | { ok: false; code: string; error: string } {
  if (isHubOnlyType(type)) {
    return { ok: false, code: 'HUB_ONLY_TYPE', error: `type "${type}" can only be generated by the hub` };
  }
  const schema = (AgentPayloads as Record<string, z.ZodType>)[type];
  if (!schema) {
    const r = z.record(z.string(), z.unknown()).safeParse(payload ?? {});
    return r.success ? { ok: true, payload: r.data } : { ok: false, code: 'INVALID_PAYLOAD', error: 'payload must be an object' };
  }
  const r = schema.safeParse(payload ?? {});
  if (!r.success) return { ok: false, code: 'INVALID_PAYLOAD', error: z.prettifyError(r.error) };
  return { ok: true, payload: r.data as Record<string, unknown> };
}

export function validateInboxPayload(
  type: string,
  payload: unknown,
): { ok: true; payload: Record<string, unknown> } | { ok: false; code: string; error: string } {
  const schema = (InboxPayloads as Record<string, z.ZodType>)[type];
  if (!schema) return { ok: false, code: 'UNKNOWN_INBOX_TYPE', error: `unknown inbox type "${type}"` };
  const r = schema.safeParse(payload ?? {});
  if (!r.success) return { ok: false, code: 'INVALID_PAYLOAD', error: z.prettifyError(r.error) };
  return { ok: true, payload: r.data as Record<string, unknown> };
}
