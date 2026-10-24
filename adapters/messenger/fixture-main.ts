// SPDX-License-Identifier: Apache-2.0

// The add-on's program with a recorded synthetic platform, used by its tests and
// by Core's end-to-end run. No SDK is imported and nothing contacts a platform.
import { serve } from './src/serve.ts';
import { fixtureDriver, recordedFrom } from './fixture-driver.ts';

const platform = process.env.LINA_MESSENGER_ADDON_PLATFORM ?? '';
const account = process.env.LINA_MESSENGER_ADDON_ACCOUNT ?? '';
const configDir = process.env.AGENT_MESSENGER_CONFIG_DIR ?? '';
if (!platform || !account || !configDir) {
  process.stderr.write('messenger add-on fixture: platform, account and data directory are required\n');
  process.exit(64);
}

const recorded = { ...recordedFrom(configDir), configDir };
await serve(process.stdin, process.stdout, { open: async () => fixtureDriver(recorded) }, { platform, account });
