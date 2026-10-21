# SPDX-License-Identifier: Apache-2.0
"""Capture the built connection surface through a Linux PTY and render its cells.

Requires Pillow and pyte (versions in evidence/brand/README.md). State observations
and socket reads establish ordering; there are no sleeps or synthetic UI strings.
"""
import argparse
import fcntl
import hashlib
from importlib.metadata import version
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

from PIL import Image, ImageDraw, ImageFont
import pyte

ROOT = Path(__file__).resolve().parents[4]
SGR = re.compile(rb'\x1b\[[0-?]*[ -/]*[@-~]')
ROWS = 16


def capture(binary, theme, profile, ascii_only, width, scenario):
    with tempfile.TemporaryDirectory(prefix='lina-brand-') as directory:
        path = directory + '/core.sock'
        listener = socket.socket(socket.AF_UNIX)
        if scenario != 'stopped':
            listener.bind(path)
            listener.listen()
            listener.settimeout(5)
        master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', ROWS, width, 0, 0))
        original_mode = termios.tcgetattr(slave)
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

        try:
            if scenario != 'stopped':
                conn, _ = listener.accept()
                conn.settimeout(5)
                with conn.makefile('rb') as stream:
                    request = json.loads(stream.readline())
                until(b'to LINA.')
                frames['connecting'] = bytes(data)
                fixture = 'unsupported_combination' if scenario == 'refused' else 'welcome'
                envelope = json.loads((ROOT / f'protocol/fixtures/client/1.0/{fixture}.json').read_text())
                envelope['reply_to'] = request['params']['message_id']
                if scenario == 'failed':
                    conn.sendall(b'{invalid json}\n')
                else:
                    conn.sendall((json.dumps({'jsonrpc': '2.0', 'id': request['id'], 'result': envelope}) + '\n').encode())
                if scenario == 'live':
                    until(b'[q] Quit', 1 if profile == 'none' else 3)
                    frames['connected'] = bytes(data)
                    os.write(master, b'q')
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
            assert termios.tcgetattr(master) == original_mode, 'terminal mode was not restored'
            frames['quit' if scenario == 'live' else scenario] = bytes(data)
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


def render(content, theme, profile, width, fonts, destination):
    # Replay the complete PTY stream, including cursor-up, erase and CRLF. The
    # image is exactly the terminal grid: no theme frame, padding or crop.
    screen = pyte.Screen(width, ROWS)
    pyte.Stream(screen).feed(content.decode())
    tokens = json.loads((ROOT / f'design/tokens/{theme}.json').read_text())['color']
    canvas, text = (tokens[role]['$value']['hex'] for role in ('canvas', 'text'))
    palette = dict(zip(
        ['black', 'red', 'green', 'brown', 'blue', 'magenta', 'cyan', 'white',
         'brightblack', 'brightred', 'brightgreen', 'brightbrown', 'brightblue', 'brightmagenta', 'brightcyan', 'brightwhite'],
        ['#000000', '#800000', '#008000', '#808000', '#000080', '#800080', '#008080', '#c0c0c0',
         '#808080', '#ff0000', '#00ff00', '#ffff00', '#0000ff', '#ff00ff', '#00ffff', '#ffffff']))

    def color(value, default):
        return default if value == 'default' else palette.get(value, '#' + value)

    def terminal_color(index):
        if index < 16:
            return list(palette.values())[index]
        if index >= 232:
            return '#' + f'{8 + (index - 232) * 10:02x}' * 3
        value, levels = index - 16, [0, 95, 135, 175, 215, 255]
        return '#' + ''.join(f'{levels[channel]:02x}' for channel in (value // 36, value // 6 % 6, value % 6))

    if profile in ('16', '256'):
        canvas, text = (terminal_color(tokens[role]['$extensions']['lina.terminal']['ansi' + profile]) for role in ('canvas', 'text'))
    cell_width = round(fonts[0].getlength('M'))
    cell_height = sum(fonts[0].getmetrics()) + 4
    image = Image.new('RGB', (width * cell_width, ROWS * cell_height), canvas)
    draw = ImageDraw.Draw(image)
    for y in range(ROWS):
        for x in range(width):
            cell = screen.buffer[y][x]
            fg, bg = color(cell.fg, text), color(cell.bg, canvas)
            if cell.reverse:
                fg, bg = bg, fg
            left, top = x * cell_width, y * cell_height
            draw.rectangle((left, top, left + cell_width - 1, top + cell_height - 1), fill=bg)
            draw.text((left, top + 2), cell.data, font=fonts[bool(cell.bold)], fill=fg)
    image.save(destination)
    # A light render must have the same canvas at every edge, with no white band.
    expected = image.getpixel((0, 0))
    assert expected == tuple(int(canvas[i:i+2], 16) for i in (1, 3, 5))
    assert all(image.getpixel((x, y)) == expected for x, y in (
        [(x, 0) for x in range(image.width)] + [(x, image.height - 1) for x in range(image.width)]
        + [(0, y) for y in range(image.height)] + [(image.width - 1, y) for y in range(image.height)]))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True)
    parser.add_argument('--font', default='/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf')
    parser.add_argument('--bold-font', default='/usr/share/fonts/truetype/dejavu/DejaVuSansMono-Bold.ttf')
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    output = Path(args.output)
    output.mkdir(parents=True, exist_ok=True)
    fonts = [ImageFont.truetype(path, 20) for path in (args.font, args.bold_font)]
    cases = [(theme, 'truecolor', False, 72, state) for theme in ['dark', 'light', 'high-contrast'] for state in ['live', 'refused', 'stopped', 'failed']]
    cases += [('dark', profile, True, 72, 'live') for profile in ['16', '256', 'none']]
    cases += [('light', 'truecolor', True, 32, 'live')]
    evidence = {'renderer': {name: version(name) for name in ['Pillow', 'pyte', 'wcwidth']},
                'fonts': [{'name': Path(path).name, 'sha256': hashlib.sha256(Path(path).read_bytes()).hexdigest()} for path in (args.font, args.bold_font)],
                'binary_sha256': hashlib.sha256(Path(args.binary).read_bytes()).hexdigest(),
                'terminal': 'Linux PTY; application manages raw input and restores original mode; single q without Enter',
                'cases': []}
    for theme, profile, ascii_only, width, scenario in cases:
        frames, env, code = capture(args.binary, theme, profile, ascii_only, width, scenario)
        selected = frames if scenario == 'live' and profile == 'truecolor' and width == 72 else {
            'connected' if scenario == 'live' else scenario: frames['connected' if scenario == 'live' else scenario]}
        names = []
        for state, content in selected.items():
            name = f'{theme}-{state}' if profile == 'truecolor' and width == 72 else f'{theme}-{profile}-{width}-{state}'
            names.append(name + '.png')
            render(content, theme, profile, width, fonts, output / (name + '.png'))
        evidence['cases'].append({'environment': env, 'columns': width, 'rows': ROWS,
                                  'scenario': scenario, 'exit': code, 'terminal_restored': True, 'renders': names})
    (output / 'capture.json').write_text(json.dumps(evidence, indent=2) + '\n')


if __name__ == '__main__':
    main()
