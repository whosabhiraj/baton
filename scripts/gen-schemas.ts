// Generates protocol/schema/*.json from protocol/types.ts (zod is the source of truth).
import { writeFileSync, mkdirSync, readdirSync, rmSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { z } from 'zod';
import * as T from '../protocol/types.ts';

const outDir = join(dirname(fileURLToPath(import.meta.url)), '..', 'protocol', 'schema');
mkdirSync(outDir, { recursive: true });
for (const f of readdirSync(outDir)) if (f.endsWith('.json')) rmSync(join(outDir, f));

const schemas: Record<string, z.ZodType> = {
  'envelope.incoming': T.IncomingEnvelope,
  'envelope.stored': T.StoredEvent,
  'inbox-message': T.InboxMessage,
  'run-record': T.RunRecord,
};
for (const [t, s] of Object.entries(T.AgentPayloads)) schemas[`agent.${t}`] = s;
for (const [t, s] of Object.entries(T.HubPayloads)) schemas[`hub.${t}`] = s;
for (const [t, s] of Object.entries(T.InboxPayloads)) schemas[`inbox.${t}`] = s;

for (const [name, schema] of Object.entries(schemas)) {
  const json = { $id: `baton/1/${name}`, title: name, ...z.toJSONSchema(schema, { io: 'input', unrepresentable: 'any' }) };
  writeFileSync(join(outDir, `${name}.json`), JSON.stringify(json, null, 2) + '\n');
}
console.log(`wrote ${Object.keys(schemas).length} schemas to protocol/schema/`);
