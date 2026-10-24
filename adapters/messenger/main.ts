// SPDX-License-Identifier: Apache-2.0

// main starts the add-on for one account. Core passes the platform, the
// account's own data directory (0700, outside backups) and the account id; the
// add-on keeps its sign-in session there and nowhere else.
import { serve } from './src/serve.ts';
import { openDriver } from './src/platforms.ts';

const platform = process.env.LINA_MESSENGER_ADDON_PLATFORM ?? '';
const account = process.env.LINA_MESSENGER_ADDON_ACCOUNT ?? '';
const configDir = process.env.AGENT_MESSENGER_CONFIG_DIR ?? '';

if (!platform || !account || !configDir) {
  process.stderr.write('messenger add-on: platform, account and data directory are required\n');
  process.exit(64);
}

await serve(process.stdin, process.stdout, {
  open: name => openDriver(name, { configDir }),
}, { platform, account });
