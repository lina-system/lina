// SPDX-License-Identifier: Apache-2.0

// The KakaoTalk driver over a recorded LOCO-shaped platform, used by its tests
// and by Core's headless end-to-end run. No SDK is imported and nothing
// contacts KakaoTalk.
import { serve } from './src/serve.ts';
import { openKakaoTalk } from './src/kakaotalk.ts';
import { fixtureSDK, recordedFrom } from './kakaotalk-fixture.ts';

const platform = process.env.LINA_MESSENGER_ADDON_PLATFORM ?? '';
const account = process.env.LINA_MESSENGER_ADDON_ACCOUNT ?? '';
const configDir = process.env.AGENT_MESSENGER_CONFIG_DIR ?? '';
if (!platform || !account || !configDir) {
  process.stderr.write('messenger add-on kakaotalk fixture: platform, account and data directory are required\n');
  process.exit(64);
}

const fixture = fixtureSDK(recordedFrom(configDir), configDir);
await serve(process.stdin, process.stdout, { open: async () => openKakaoTalk(fixture.sdk, { configDir }) }, { platform, account });
