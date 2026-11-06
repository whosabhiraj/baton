// Secret redaction. Applied to every event payload before it is stored or fanned out.
// Regexes are linear-time (bounded quantifiers, no nested repetition).

const R = '[REDACTED]';

// [pattern, replacement] applied in order; more specific patterns first.
const RULES: Array<[RegExp, string | ((...m: string[]) => string)]> = [
  [/-----BEGIN [A-Z ]{0,30}PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z ]{0,30}PRIVATE KEY-----|$)/g, `-----PRIVATE KEY ${R}-----`],
  [/\bbt_run_[A-Za-z0-9_-]{8,}/g, `bt_run_${R}`],
  [/\bbt_adm_[A-Za-z0-9_-]{8,}/g, `bt_adm_${R}`],
  [/\bsk-ant-[A-Za-z0-9_-]{8,}/g, `sk-ant-${R}`],
  [/\bsk-(?:proj-)?[A-Za-z0-9_-]{20,}/g, `sk-${R}`],
  [/\b(gh[pousr]_)[A-Za-z0-9]{20,}/g, (_m, p = '') => `${p}${R}`],
  [/\bgithub_pat_[A-Za-z0-9_]{20,}/g, `github_pat_${R}`],
  [/\b(?:AKIA|ASIA)[A-Z0-9]{16}\b/g, `AKIA${R}`],
  [/\bxox[abposr]-[A-Za-z0-9-]{10,}/g, `xox-${R}`],
  [/\bAIza[A-Za-z0-9_-]{30,}/g, `AIza${R}`],
  [/\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}/g, `eyJ${R}`],
  [/\b(Bearer\s+)[A-Za-z0-9._~+/=-]{8,}/gi, (_m, p = '') => `${p}${R}`],
  // key = value / key: value / "key": "value" for sensitive-looking key names
  [
    /\b((?:[A-Za-z0-9]{1,30}[_.-])?(?:api[_-]?key|secret|token|password|passwd|pwd|key)(?:[_.-][A-Za-z0-9]{1,30})?["']?\s*[:=]\s*["']?)(?!\[REDACTED\])[^\s"',;}\]]{3,200}/gi,
    (_m, p = '') => `${p}${R}`,
  ],
];

export function redactString(s: string): string {
  let out = s;
  for (const [re, rep] of RULES) out = out.replace(re, rep as string);
  return out;
}

const SENSITIVE_KEY = /^(?:[\w-]*[_-])?(?:api[_-]?key|secret|token|password|passwd|pwd|authorization)$/i;

/** Deep-walk a JSON-like value, redacting secrets in strings (and string values of sensitive keys). */
export function redact(value: unknown): unknown {
  return walk(value, 0);
}

function walk(v: unknown, depth: number): unknown {
  if (typeof v === 'string') return redactString(v);
  if (v === null || typeof v !== 'object') return v;
  if (depth > 50) return R;
  if (Array.isArray(v)) return v.map((x) => walk(x, depth + 1));
  const out: Record<string, unknown> = {};
  for (const [k, val] of Object.entries(v)) {
    out[k] = typeof val === 'string' && SENSITIVE_KEY.test(k) ? R : walk(val, depth + 1);
  }
  return out;
}
