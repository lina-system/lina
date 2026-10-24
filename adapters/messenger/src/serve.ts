// SPDX-License-Identifier: Apache-2.0

// serve is the add-on's whole request loop. It reads one request per line from
// the stdin side, dispatches it through the fixed operation table in
// `protocol.ts`, and writes one response per line. Nothing else is reachable:
// a request the table does not name is refused, and a driver is only asked for
// the reads its own interface declares.
import { Refusal, frame, MAX_LINE, parseRequest, type Request } from './protocol.ts';
import { signedIn } from './driver.ts';
import type { Driver, LiveEvent } from './driver.ts';
import { author, chat as checkedChat, page as checkedPage } from './shape.ts';

const VERSION = 1;

export type Runtime = {
  // open starts the platform client for one account. It is the only seam that
  // touches the SDK; tests inject a fake there.
  open(platform: string): Promise<Driver>;
};

export type Addon = {
  platform: string;
  account: string;
};

export function serve(input: NodeJS.ReadableStream, output: NodeJS.WriteStream | { write(chunk: string): unknown }, runtime: Runtime, addon: Addon): Promise<void> {
  let driver: Driver | null = null;
  let liveStop: (() => void) | null = null;
  let buffer = '';
  let openError: string | null = null;

  const write = (value: unknown) => {
    output.write(frame(value));
  };

  const driverOrOpen = async (): Promise<Driver> => {
    if (driver) return driver;
    if (openError) throw new Refusal('source_failing', openError);
    try {
      driver = await runtime.open(addon.platform);
    } catch (error) {
      openError = describe(error);
      throw new Refusal('source_failing', openError);
    }
    if (!driver) throw new Refusal('source_failing', 'no driver');
    return driver;
  };

  const stopLive = () => {
    if (liveStop) liveStop();
    liveStop = null;
  };

  const handle = async (request: Request): Promise<unknown> => {
    switch (request.op) {
      case 'hello':
        return { version: VERSION, platform: addon.platform, account: addon.account };
      case 'capabilities': {
        // The platform's own descriptor: how far back it reads, what its cursor
        // is, and the interval and read window Core should use for it.
        const value = await driverOrOpen();
        return value.capabilities();
      }
      case 'quit':
        stopLive();
        setImmediate(() => process.exit(0));
        return { stopped: true };
      case 'stop':
        stopLive();
        return { stopped: true };
      case 'signIn': {
        const value = await driverOrOpen();
        return { state: await value.signInState() };
      }
      case 'beginSignIn': {
        const value = await driverOrOpen();
        return await value.beginSignIn();
      }
      case 'answer': {
        const value = await driverOrOpen();
        return await value.answer(request.prompt, request.value);
      }
      case 'listChats': {
        const value = await driverOrOpen();
        return { chats: (await value.listChats()).map(checkedChat) };
      }
      case 'page': {
        const value = await driverOrOpen();
        return checkedPage(await value.page(request.chat, request.cursor));
      }
      case 'members': {
        const value = await driverOrOpen();
        return { members: (await value.members(request.chat)).map(author) };
      }
      case 'whoami': {
        const value = await driverOrOpen();
        return { author: author(await value.whoami()) };
      }
      case 'live': {
        const value = await driverOrOpen();
        stopLive();
        liveStop = value.live((event: LiveEvent) => write(event));
        return { live: true };
      }
      default:
        throw new Refusal('unknown_operation', 'operation not offered');
    }
  };

  const dispatch = async (line: string) => {
    let id: string | undefined;
    try {
      const request = parseRequest(line);
      id = request.id;
      const result = await handle(request);
      write({ id, ok: true, result });
    } catch (error) {
      write({ id: id ?? null, ok: false, error: describe(error) });
    }
  };

  const { promise, resolve } = Promise.withResolvers<void>();
  input.setEncoding('utf8');
    input.on('data', (chunk: string) => {
      buffer += chunk;
      for (;;) {
        const end = buffer.indexOf('\n');
        if (end < 0) break;
        const line = buffer.slice(0, end);
        buffer = buffer.slice(end + 1);
        if (buffer.length > MAX_LINE) {
          write({ id: null, ok: false, error: 'line_too_long' });
          buffer = '';
          continue;
        }
        if (line.length > MAX_LINE) {
          write({ id: null, ok: false, error: 'line_too_long' });
          continue;
        }
        if (line.trim() !== '') void dispatch(line);
      }
      if (buffer.length > MAX_LINE) {
        write({ id: null, ok: false, error: 'line_too_long' });
        buffer = '';
      }
    });
  input.on('end', () => {
    stopLive();
    resolve();
  });
  return promise;
}

export function describe(error: unknown): string {
  if (error instanceof Refusal) return error.code;
  if (error instanceof Error) {
    // SDK messages can carry host details; the code or class is enough.
    const code: unknown = Object.hasOwn(error, 'code') ? Reflect.get(error, 'code') : undefined;
    return typeof code === 'string' && code ? `failed:${code}` : 'failed';
  }
  return 'failed';
}

export { signedIn };
