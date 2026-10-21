# SPDX-License-Identifier: Apache-2.0
"""Capture the built connection surface through a Linux PTY, then render with Freeze.

Only standard-library Python is required. The caller supplies both binaries.
State observations and socket reads establish ordering; there are no sleeps.
"""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import pty
import re
import select
import socket
import struct
import subprocess
import tempfile
import termios
import time

ROOT = Path(__file__).resolve().parents[4]
SGR = re.compile(rb'\x1b\[[0-?]*[ -/]*[@-~]')
CLEAR = re.compile(rb'\x1b\[(?:0)?J')


def capture(binary, theme, profile, ascii_only, width, scenario):
    with tempfile.TemporaryDirectory(prefix='lina-brand-') as directory:
        path = directory + '/core.sock'
        listener = socket.socket(socket.AF_UNIX)
        if scenario != 'stopped':
            listener.bind(path)
            listener.listen()
            listener.settimeout(5)
        master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', 24, width, 0, 0))
        attrs = termios.tcgetattr(slave)
        attrs[3] &= ~termios.ECHO
        termios.tcsetattr(slave, termios.TCSANOW, attrs)
        env = {'PATH': os.environ['PATH'], 'TERM': 'xterm' if profile == '16' else 'xterm-256color', 'LINA_THEME': theme}
        if profile == 'truecolor':
            env['COLORTERM'] = 'truecolor'
        if profile == 'none':
            env['NO_COLOR'] = '1'
        if ascii_only:
            env['LINA_GLYPHS'] = 'ascii'
        process = subprocess.Popen([binary, 'tui', '--socket', path], stdin=slave, stdout=slave, stderr=slave, env=env)
        os.close(slave)
        data = bytearray()
        frames = {}
        conn = None

        def until(marker, trailing_lines=3):
            deadline = time.monotonic() + 5
            while marker not in SGR.sub(b'', data) or SGR.sub(b'', data).split(marker, 1)[-1].count(b'\n') < trailing_lines:
                if not select.select([master], [], [], max(0, deadline - time.monotonic()))[0]:
                    raise RuntimeError(f'timeout waiting for {marker!r}')
                data.extend(os.read(master, 65536))

        def frame():
            # Freeze understands SGR but does not execute cursor-up/erase.
            # Keep the complete last draw emitted by the real binary. No text,
            # spacing, glyph or colour bytes within that draw are changed.
            return bytes(data) if profile == 'none' else CLEAR.split(data)[-1]

        try:
            if scenario != 'stopped':
                conn, _ = listener.accept()
                conn.settimeout(5)
                with conn.makefile('rb') as stream:
                    request = json.loads(stream.readline())
                until(b'to LINA.')
                frames['connecting'] = frame()
                fixture = 'unsupported_combination' if scenario == 'refused' else 'welcome'
                envelope = json.loads((ROOT / f'protocol/fixtures/client/1.0/{fixture}.json').read_text())
                envelope['reply_to'] = request['params']['message_id']
                if scenario == 'failed':
                    conn.sendall(b'{invalid json}\n')
                else:
                    conn.sendall((json.dumps({'jsonrpc': '2.0', 'id': request['id'], 'result': envelope}) + '\n').encode())
                if scenario == 'live':
                    until(b'q + Enter  Quit', 1 if profile == 'none' else 3)
                    frames['connected'] = frame()
                    os.write(master, b'q\n')
            while True:
                if not select.select([master], [], [], 5)[0]:
                    raise RuntimeError('exit timed out')
                try:
                    chunk = os.read(master, 65536)
                    if not chunk:
                        break
                    data.extend(chunk)
                except OSError as error:
                    if error.errno != 5:  # Linux PTY EOF is EIO.
                        raise
                    break
            code = process.wait(timeout=5)
            assert code == {'live': 0, 'refused': 78, 'stopped': 69, 'failed': 70}[scenario], code
            frames['quit' if scenario == 'live' else scenario] = frame()
            if profile == 'none':
                assert b'\x1b' not in data
            if ascii_only:
                assert all(byte < 128 for byte in data)
            if profile == '16':
                assert not any(code in data for code in [b'38;2;', b'48;2;', b'38;5;', b'48;5;'])
            if profile == '256':
                assert not any(code in data for code in [b'38;2;', b'48;2;'])
            return frames, {key: value for key, value in env.items() if key != 'PATH'}, code
        finally:
            if process.poll() is None:
                process.kill()
                process.wait()
            if conn is not None:
                conn.close()
            os.close(master)
            listener.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True)
    parser.add_argument('--freeze', required=True)
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    output = Path(args.output)
    output.mkdir(parents=True, exist_ok=True)
    cases = [(theme, 'truecolor', False, 72, state) for theme in ['dark', 'light'] for state in ['live', 'refused', 'stopped', 'failed']]
    cases += [('dark', profile, True, 72, 'live') for profile in ['16', '256', 'none']]
    cases += [('high-contrast', 'truecolor', False, 72, 'live'), ('light', 'truecolor', True, 32, 'live')]
    evidence = {'renderer': subprocess.check_output([args.freeze, '--version'], text=True).strip(),
                'binary_sha256': hashlib.sha256(Path(args.binary).read_bytes()).hexdigest(),
                'terminal': 'Linux PTY, canonical input, echo disabled', 'cases': []}
    for theme, profile, ascii_only, width, scenario in cases:
        frames, env, code = capture(args.binary, theme, profile, ascii_only, width, scenario)
        selected = frames if scenario == 'live' and profile == 'truecolor' and theme != 'high-contrast' and width == 72 else {
            'connected' if scenario == 'live' else scenario: frames['connected' if scenario == 'live' else scenario]}
        background = json.loads((ROOT / f'design/tokens/{theme}.json').read_text())['color']['canvas']['$value']['hex']
        names = []
        for state, content in selected.items():
            name = f'{theme}-{state}' if profile == 'truecolor' and width == 72 else f'{theme}-{profile}-{width}-{state}'
            names.append(name + '.png')
            with tempfile.TemporaryDirectory(prefix='lina-frame-') as temp:
                source = Path(temp) / 'frame.ansi'
                source.write_bytes(content)
                subprocess.run([args.freeze, str(source), '--language', 'ansi', '--background', background,
                                *(['--theme', 'github'] if theme == 'light' else []),
                                '--padding', '0', '--margin', '0', '--border.radius', '0',
                                '--font.size', '14', '--line-height', '1.4', '--output', str(output / (name + '.png'))], check=True)
        evidence['cases'].append({'environment': env, 'columns': width, 'rows': 24,
                                  'scenario': scenario, 'exit': code, 'renders': names})
    (output / 'capture.json').write_text(json.dumps(evidence, indent=2) + '\n')


if __name__ == '__main__':
    main()
