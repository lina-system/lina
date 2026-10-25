import importlib.machinery
import importlib.util
import io
import json
import os
import shlex
import subprocess
import tempfile
import unittest

PATH = os.path.join(os.path.dirname(__file__), '..', 'lina-helper')
loader = importlib.machinery.SourceFileLoader('lina_helper', PATH)
spec = importlib.util.spec_from_loader('lina_helper', loader)
lh = importlib.util.module_from_spec(spec)
loader.exec_module(lh)

OK = b'{"v":1,"ok":true,"result":{}}\n'


class Result:
    def __init__(self, stdout=b'', stderr=b'', returncode=0):
        self.stdout, self.stderr, self.returncode = stdout, stderr, returncode


class Fake:
    def __init__(self, result=None, exc=None):
        self.result, self.exc, self.calls = result or Result(OK), exc, []

    def __call__(self, cmd, **kw):
        self.calls.append((cmd, kw))
        if self.exc:
            raise self.exc
        return self.result


def run(argv, fake, env=None):
    out = io.StringIO()
    code = lh.main(argv, runner=fake, env=env or {}, stdout=out)
    return code, json.loads(out.getvalue())


class CommandTests(unittest.TestCase):
    def test_builds_fixed_ssh_command(self):
        self.assertEqual(lh.command('agent-mac', 'status'), [
            'ssh', '-T', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=8', '-o', 'ServerAliveInterval=5',
            '-o', 'ServerAliveCountMax=2', 'agent-mac', "'/Applications/LINA Helper.app/Contents/Helpers/lina-helperctl' status"])

    def test_hostile_args_are_quoted(self):
        for value in ['a b', "it's", '"q"', '$(touch /tmp/x)', 'x\ny', '`id`', ';rm -rf /', '--out=/etc/passwd']:
            remote = lh.command('h', 'input.keys', ['text=' + value])[-1]
            self.assertEqual(shlex.split(remote), [lh.CTL, 'input.keys', '--arg', 'text=' + value])

    def test_out_appends_dash(self):
        remote = lh.command('h', 'screenshot', ['display=main'], out=True)[-1]
        self.assertEqual(shlex.split(remote), [lh.CTL, 'screenshot', '--arg', 'display=main', '--out', '-'])

    def test_host_rejected(self):
        for host in ['', None, '-oProxyCommand=x', 'a b', 'a;b', 'h\n', 'a' * 129, 'u@h', '.h']:
            with self.assertRaises(lh.UsageError):
                lh.command(host, 'status')

    def test_action_rejected(self):
        for action in ['', 'rm', 'status; id', 'Status', 'shell']:
            with self.assertRaises(lh.UsageError):
                lh.command('h', action)

    def test_bad_arg_and_out_scope(self):
        for arg in ['novalue', '=v', 'k k=v', '-x=1']:
            with self.assertRaises(lh.UsageError):
                lh.command('h', 'ax.tree', [arg])
        with self.assertRaises(lh.UsageError):
            lh.command('h', 'status', out=True)


class MainTests(unittest.TestCase):
    def test_success(self):
        fake = Fake()
        code, value = run(['--host', 'h', 'status'], fake)
        self.assertEqual((code, value['ok']), (0, True))
        self.assertEqual(fake.calls[0][1]['timeout'], 30)
        self.assertIs(fake.calls[0][1]['stdin'], subprocess.DEVNULL)

    def test_host_from_env(self):
        fake = Fake()
        self.assertEqual(run(['status'], fake, {'LINA_HELPER_SSH_HOST': 'envhost'})[0], 0)
        self.assertEqual(fake.calls[0][0][-2], 'envhost')

    def test_missing_host(self):
        fake = Fake()
        code, value = run(['status'], fake)
        self.assertEqual((code, value['error']['code'], fake.calls), (1, 'bad_request', []))

    def test_bad_action_not_run(self):
        fake = Fake()
        code, value = run(['--host', 'h', 'sudo'], fake)
        self.assertEqual((code, value['error']['code'], fake.calls), (1, 'bad_request', []))

    def test_remote_error_passthrough(self):
        err = b'{"v":1,"ok":false,"error":{"code":"permission_missing","detail":"screen_recording"}}\n'
        code, value = run(['--host', 'h', 'status'], Fake(Result(err, b'', 1)))
        self.assertEqual((code, value['error']['code']), (1, 'permission_missing'))

    def test_timeout(self):
        fake = Fake(exc=subprocess.TimeoutExpired('ssh', 30))
        code, value = run(['--host', 'h', 'status'], fake)
        self.assertEqual((code, value), (1, {'v': 1, 'ok': False, 'error': {'code': 'transport_timeout'}}))
        self.assertEqual(len(fake.calls), 1)

    def test_screenshot_timeout_is_45(self):
        fake = Fake(exc=subprocess.TimeoutExpired('ssh', 45))
        run(['--host', 'h', 'screenshot'], fake)
        self.assertEqual(fake.calls[0][1]['timeout'], 45)

    def test_oversized_and_invalid(self):
        big = b'{"v":1,"ok":true,"pad":"' + b'a' * (128 << 20) + b'"}'
        for raw in [big, b'not json', b'', b'[]', b'{"v":2,"ok":true}', b'{"v":1,"ok":"yes"}']:
            code, value = run(['--host', 'h', 'status'], Fake(Result(raw)))
            self.assertEqual((code, value['error']['code']), (1, 'invalid_remote_response'), raw[:20])

    def test_large_ax_tree_response_is_accepted(self):
        node = {'path': '0/1', 'role': 'AXStaticText', 'title': 't' * 512, 'value': 'v' * 512}
        raw = json.dumps({'v': 1, 'ok': True, 'result': {'elements': [node] * 2000}}).encode()
        self.assertGreater(len(raw), 2 << 20)
        code, value = run(['--host', 'h', 'ax.tree', '--arg', 'app=frontmost'], Fake(Result(raw)))
        self.assertEqual((code, len(value['result']['elements'])), (0, 2000))

    def test_ok_with_nonzero_exit_is_invalid(self):
        code, value = run(['--host', 'h', 'status'], Fake(Result(OK, b'', 255)))
        self.assertEqual((code, value['error']['code']), (1, 'invalid_remote_response'))


class JsonArgTests(unittest.TestCase):
    def test_typed_args_reach_the_cli_as_one_word(self):
        fake = Fake()
        code, _ = run(['--host', 'h', 'ax.tree', '--arg', 'app=frontmost', '--json', '{"depth": 2}'], fake)
        self.assertEqual(code, 0)
        remote = shlex.split(fake.calls[0][0][-1])
        self.assertEqual(remote[remote.index('--json') + 1], '{"depth":2}')

    def test_non_object_json_is_rejected_locally(self):
        for bad in ['[1]', 'nope', '2']:
            fake = Fake()
            code, value = run(['--host', 'h', 'ax.tree', '--json', bad], fake)
            self.assertEqual((code, value['error']['code'], fake.calls), (1, 'bad_request', []))


class OutTests(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.dir.cleanup)
        self.path = os.path.join(self.dir.name, 'shot.png')

    def test_writes_bytes_and_reads_json_from_stderr(self):
        png = b'\x89PNG\r\n\x1a\n\x00\xff'
        fake = Fake(Result(png, b'log line\n' + OK))
        code, value = run(['--host', 'h', 'screenshot', '--out', self.path], fake)
        self.assertEqual((code, value['ok']), (0, True))
        with open(self.path, 'rb') as f:
            self.assertEqual(f.read(), png)
        self.assertEqual(shlex.split(fake.calls[0][0][-1])[-2:], ['--out', '-'])

    def test_refuses_overwrite_without_running(self):
        with open(self.path, 'wb') as f:
            f.write(b'keep')
        fake = Fake(Result(b'new', OK))
        code, value = run(['--host', 'h', 'screenshot', '--out', self.path], fake)
        self.assertEqual((code, value['error']['code'], fake.calls), (1, 'bad_request', []))
        with open(self.path, 'rb') as f:
            self.assertEqual(f.read(), b'keep')

    def test_failure_writes_nothing(self):
        err = b'{"v":1,"ok":false,"error":{"code":"timeout"}}\n'
        code, _ = run(['--host', 'h', 'screenshot', '--out', self.path], Fake(Result(b'', err, 1)))
        self.assertEqual(code, 1)
        self.assertFalse(os.path.exists(self.path))

    def test_invalid_stderr_or_empty_body(self):
        for res in [Result(b'png', b'garbage'), Result(b'png', b''), Result(b'', OK)]:
            code, value = run(['--host', 'h', 'screenshot', '--out', self.path], Fake(res))
            self.assertEqual((code, value['error']['code']), (1, 'invalid_remote_response'))
            self.assertFalse(os.path.exists(self.path))

    def test_missing_directory_is_structured_error(self):
        path = os.path.join(self.dir.name, 'missing', 'shot.png')
        code, value = run(['--host', 'h', 'screenshot', '--out', path], Fake(Result(b'png', OK)))
        self.assertEqual((code, value['error']['code']), (1, 'output_unwritable'))

    def test_write_failure_removes_partial_file(self):
        real_open = open

        class Full:
            def __init__(self, f):
                self.f = f

            def __enter__(self):
                return self

            def __exit__(self, *exc):
                self.f.close()

            def write(self, _):
                raise OSError(28, 'No space left on device')

        def failing_open(path, mode='r', *a, **kw):
            f = real_open(path, mode, *a, **kw)
            return Full(f) if mode == 'xb' else f
        setattr(lh, 'open', failing_open)
        self.addCleanup(lambda: delattr(lh, 'open'))
        code, value = run(['--host', 'h', 'screenshot', '--out', self.path], Fake(Result(b'png', OK)))
        self.assertEqual((code, value['error']['code']), (1, 'output_unwritable'))
        self.assertFalse(os.path.exists(self.path))

    def test_out_rejected_for_other_actions(self):
        code, value = run(['--host', 'h', 'status', '--out', self.path], Fake())
        self.assertEqual((code, value['error']['code']), (1, 'bad_request'))


if __name__ == '__main__':
    unittest.main()
