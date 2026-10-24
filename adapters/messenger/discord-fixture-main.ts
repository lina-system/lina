// SPDX-License-Identifier: Apache-2.0

// The add-on's program with a recorded Discord SDK, used by the add-on's tests
// and by Core's end-to-end run. The driver itself is the real one; only the
// platform behind it is recorded. No SDK is imported and nothing contacts
// Discord.
import { serve } from './src/serve.ts';
import { discordDriver } from './src/discord.ts';
import { discordFixture } from './discord-fixture.ts';

const account = process.env.LINA_MESSENGER_ADDON_ACCOUNT ?? '';
const configDir = process.env.AGENT_MESSENGER_CONFIG_DIR ?? '';
if (!account || !configDir) {
  process.stderr.write('messenger add-on discord fixture: account and data directory are required\n');
  process.exit(64);
}

const fixture = discordFixture(configDir);
await serve(process.stdin, process.stdout, {
  open: async () => discordDriver(fixture.sdk, { configDir }),
}, { platform: 'discord', account });
