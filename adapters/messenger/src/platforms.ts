// SPDX-License-Identifier: Apache-2.0

// The platform registry. A platform's driver maps this add-on's read operations
// onto the pinned SDK's reads; each one ships with its own source issue
// (#1071 KakaoTalk, #1085 Discord, #1086 WhatsApp, #1087 LINE), because each
// needs its own recorded fixtures and read allowlist. Until a driver is
// registered, an account of that platform is refused as `unsupported_platform`
// and only that source fails.
import { Refusal, type Driver } from './protocol.ts';
import type { DriverFactory, DriverOptions } from './driver.ts';
import { discordDriver } from './discord.ts';

export type PlatformName = 'kakaotalk' | 'discord' | 'whatsapp' | 'line';

export const PLATFORMS: Record<PlatformName, true> = { kakaotalk: true, discord: true, whatsapp: true, line: true };

export type Registration = {
  // open starts the platform client for one account. It receives the pinned
  // SDK module and the account's data directory.
  open: (sdk: unknown, options: DriverOptions) => Promise<Driver>;
  // load imports the pinned SDK slice for this platform. Kept beside the
  // driver so nothing else can import a platform module.
  load: () => Promise<unknown>;
};

const DRIVERS: Partial<Record<PlatformName, Registration>> = {
  // The SDK slice is loaded on first open, because the platform's module is the
  // only one that may touch it.
  discord: { load: () => import('agent-messenger/discord'), open: (sdk, options) => discordDriver(sdk, options) },
};

export function registered(platform: string): platform is PlatformName {
  return Object.hasOwn(PLATFORMS, platform) && Object.hasOwn(DRIVERS, platform);
}

export async function openDriver(platform: string, options: DriverOptions): Promise<Driver> {
  if (!Object.hasOwn(PLATFORMS, platform)) throw new Refusal('unsupported_platform', 'unknown platform');
  const registration = DRIVERS[platform as PlatformName];
  if (!registration) throw new Refusal('unsupported_platform', 'no driver for this platform');
  const sdk = await registration.load();
  return registration.open(sdk, options);
}

export type { DriverFactory };
