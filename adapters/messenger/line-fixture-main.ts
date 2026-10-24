// SPDX-License-Identifier: Apache-2.0

// The LINE driver with a recorded synthetic LINE platform, used by the add-on's
// tests and by Core's end-to-end run. The module below has the pinned SDK's
// shape, so the real driver runs unchanged; nothing here imports the SDK,
// contacts LINE or reads a session.
//
// The script is JSON in the add-on's data directory (`script.json`):
//
//	{ "credentials": { "account_id", "auth_token", "device" },
//	  "revoked": false, "chats": [...], "messages": [...],
//	  "profile": { "mid", "display_name" } }
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { serve } from './src/serve.ts';
import { lineDriver } from './src/line.ts';

type Credentials = { account_id: string; auth_token: string; device: string };
type Script = {
  credentials?: Credentials;
  revoked?: boolean;
  profile?: { mid: string; display_name?: string };
  chats?: unknown[];
  messages?: unknown[];
};

function recorded(configDir: string): Script {
  try {
    return JSON.parse(readFileSync(join(configDir, 'script.json'), 'utf8')) as Script;
  } catch {
    return {};
  }
}

function sdk(script: Script) {
  class LineCredentialManager {
    async getAccount(): Promise<Credentials | null> {
      return script.credentials ?? null;
    }
  }
  class LineClient {
    async login(): Promise<unknown> {
      if (script.revoked) throw Object.assign(new Error('synthetic revoked session'), { code: 'AUTHENTICATION_FAILED' });
      return this;
    }
    async loginWithQR(): Promise<unknown> {
      throw new Error('synthetic QR sign-in is not part of this fixture');
    }
    async getProfile() {
      return script.profile ?? { mid: script.credentials?.account_id ?? 'me', display_name: 'Me' };
    }
    async getChats() {
      return script.chats ?? [];
    }
    async getMessages() {
      return script.messages ?? [];
    }
    async *streamEvents() {
      // The fixture delivers no live events.
    }
    close(): void {}
  }
  return { LineClient, LineCredentialManager };
}

const platform = process.env.LINA_MESSENGER_ADDON_PLATFORM ?? '';
const account = process.env.LINA_MESSENGER_ADDON_ACCOUNT ?? '';
const configDir = process.env.AGENT_MESSENGER_CONFIG_DIR ?? '';
if (!platform || !account || !configDir) {
  process.stderr.write('messenger add-on LINE fixture: platform, account and data directory are required\n');
  process.exit(64);
}

await serve(process.stdin, process.stdout, {
  open: async () => lineDriver(sdk(recorded(configDir)), { configDir }),
}, { platform, account });
