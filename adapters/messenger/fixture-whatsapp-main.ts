// SPDX-License-Identifier: Apache-2.0

// The add-on's program with a synthetic WhatsApp link, used by Core's
// end-to-end run. It drives the real WhatsApp driver against a fake SDK that
// replays a recorded script (`whatsapp.json` in the data directory): what the
// phone pushed at link time, then live messages at the recorded offsets. No SDK
// is imported and nothing contacts a platform.
//
//	{ "me": {...}, "chats": [...], "history": [...],
//	  "live": [{ "afterMs": 300, "message": {...} }],
//	  "forbidden": false }
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { serve } from './src/serve.ts';
import { whatsappDriver } from './src/whatsapp.ts';

const platform = process.env.LINA_MESSENGER_ADDON_PLATFORM ?? '';
const account = process.env.LINA_MESSENGER_ADDON_ACCOUNT ?? '';
const configDir = process.env.AGENT_MESSENGER_CONFIG_DIR ?? '';
if (!platform || !account || !configDir) {
  process.stderr.write('messenger add-on fixture: platform, account and data directory are required\n');
  process.exit(64);
}

type Summary = {
  id: string;
  chat_id: string;
  from: string;
  from_name?: string;
  timestamp: string;
  is_outgoing: boolean;
  type: string;
  text?: string;
};

type Script = {
  me?: { id: string; name?: string; phone_number?: string };
  chats?: { id: string; name?: string; type?: string }[];
  history?: Summary[];
  live?: { afterMs: number; message: Summary }[];
  forbidden?: boolean;
};

function recorded(): Script {
  try {
    return JSON.parse(readFileSync(join(configDir, 'whatsapp.json'), 'utf8')) as Script;
  } catch {
    return {};
  }
}

const script = recorded();
const me = script.me ?? { id: '15550000000@s.whatsapp.net', name: 'Me' };
// A linked device has its credentials on disk before it reads anything; the
// fixture writes the synthetic link so sign-in is already done.
mkdirSync(join(configDir, 'whatsapp'), { recursive: true });
writeFileSync(join(configDir, 'whatsapp', 'creds.json'), '{"synthetic":true}', { mode: 0o600 });

const chats = script.chats ?? [];
const buffers = new Map<string, Summary[]>(chats.map(chat => [chat.id, []]));
for (const entry of script.history ?? []) {
  buffers.set(entry.chat_id, [...(buffers.get(entry.chat_id) ?? []), entry]);
}
// The phone pushes history once; live messages arrive while the link stays up.
// A restarted process replays both, so Core's idempotence is what keeps records
// single.
for (const event of script.live ?? []) {
  setTimeout(() => {
    buffers.set(event.message.chat_id, [...(buffers.get(event.message.chat_id) ?? []), event.message]);
  }, event.afterMs);
}

const refusal = (code: string): Error => Object.assign(new Error(code), { code });

class FakeClient {
  async login(): Promise<this> {
    return this;
  }
  async connect(): Promise<void> {
    if (script.forbidden) throw refusal('forbidden');
  }
  async connectForPairing(): Promise<{ code: string; waitForAuth: () => Promise<void> }> {
    if (script.forbidden) throw refusal('forbidden');
    return { code: 'ABCD1234', waitForAuth: () => new Promise<void>(() => {}) };
  }
  async listChats(): Promise<{ id: string; name: string; type: string }[]> {
    return chats.map(chat => ({ id: chat.id, name: chat.name ?? chat.id, type: chat.type ?? 'individual' }));
  }
  async getMessages(chat: string, limit = 25): Promise<Summary[]> {
    return (buffers.get(chat) ?? []).slice(-limit);
  }
  async getProfile(): Promise<{ id: string; name: string | null; phone_number: string | null }> {
    return { id: me.id, name: me.name ?? null, phone_number: me.phone_number ?? null };
  }
  async close(): Promise<void> {}
}

await serve(process.stdin, process.stdout, { open: async () => whatsappDriver({ WhatsAppClient: FakeClient }, { configDir, liveMs: 20 }) }, { platform, account });
