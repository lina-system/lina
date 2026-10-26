import importlib.machinery
import importlib.util
import io
import json
import os
import subprocess
import tempfile
import unittest
from contextlib import redirect_stdout
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
PATH = os.path.join(os.path.dirname(HERE), 'hermes-cua-driver')
loader = importlib.machinery.SourceFileLoader('hermes_cua_driver', PATH)
spec = importlib.util.spec_from_loader('hermes_cua_driver', loader)
w = importlib.util.module_from_spec(spec)
loader.exec_module(w)


class WrapperTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.gate = os.path.join(self.tmp.name, 'cua.sock')
        mock.patch.object(w, 'GATE', self.gate).start()
        mock.patch.object(w, 'DRIVER', '/bin/echo').start()
        self.addCleanup(mock.patch.stopall)
        self.addCleanup(self.tmp.cleanup)

    def test_manifest_points_mcp_back_at_the_wrapper(self):
        upstream = {'binary_version': '0.38.0', 'mcp_invocation': {'command': '/x/cua-driver', 'args': ['mcp']},
                    'subcommands': [{'name': 'mcp'}]}
        done = subprocess.CompletedProcess([], 0, json.dumps(upstream), '')
        out = io.StringIO()
        with mock.patch.object(w.subprocess, 'run', return_value=done) as run, redirect_stdout(out):
            self.assertEqual(w.main(['manifest']), 0)
        self.assertEqual(run.call_args[0][0], ['/bin/echo', 'manifest'])
        data = json.loads(out.getvalue())
        self.assertEqual(data['mcp_invocation'], {'command': w.SELF, 'args': ['mcp']})
        self.assertEqual(data['binary_version'], '0.38.0')
        self.assertEqual(data['subcommands'], upstream['subcommands'])

    def test_manifest_failure_passes_through(self):
        done = subprocess.CompletedProcess([], 3, '', 'boom\n')
        with mock.patch.object(w.subprocess, 'run', return_value=done):
            self.assertEqual(w.main(['manifest']), 3)

    def test_mcp_execs_driver_against_the_gate_and_drops_caller_endpoints(self):
        open(self.gate, 'w').close()
        with mock.patch.object(w.os, 'execv', side_effect=SystemExit(0)) as ex:
            with self.assertRaises(SystemExit):
                w.main(['mcp', '--no-overlay', '--socket', '/tmp/other.sock', '--direct', '--socket=/x', '--embedded'])
        path, argv = ex.call_args[0]
        self.assertEqual(path, '/bin/echo')
        self.assertEqual(argv, ['/bin/echo', 'mcp', '--embedded', '--socket', self.gate, '--no-overlay'])

    def test_mcp_remote_host_runs_the_proxy_on_that_mac(self):
        with mock.patch.object(w, 'HOST', 'agent-mac'), \
                mock.patch.object(w.os, 'execvp', side_effect=SystemExit(0)) as ex:
            with self.assertRaises(SystemExit):
                w.main(['mcp', '--no-overlay', '--socket', '/x'])
        prog, argv = ex.call_args[0]
        self.assertEqual(prog, 'ssh')
        self.assertEqual(argv[-2], 'agent-mac')
        self.assertEqual(argv[-1], "'/Applications/LINA Helper.app/Contents/Helpers/cua-driver' mcp --embedded --socket "
                                   '"$HOME/Library/Application Support/LINA Helper/run/cua.sock" --no-overlay')

    def test_bad_remote_host_refused(self):
        with mock.patch.object(w, 'HOST', '-oProxyCommand=x'), mock.patch.object(w.os, 'execvp') as ex:
            self.assertEqual(w.main(['mcp']), 69)
        ex.assert_not_called()

    def test_mcp_without_gate_fails_clearly(self):
        with mock.patch.object(w.os, 'execv') as ex:
            self.assertEqual(w.main(['mcp']), 69)
        ex.assert_not_called()

    def test_other_verbs_pass_through(self):
        with mock.patch.object(w.os, 'execv', side_effect=SystemExit(0)) as ex:
            with self.assertRaises(SystemExit):
                w.main(['status', '--socket', '/s'])
        self.assertEqual(ex.call_args[0][1], ['/bin/echo', 'status', '--socket', '/s'])

    def test_missing_driver(self):
        with mock.patch.object(w, 'DRIVER', os.path.join(self.tmp.name, 'nope')):
            self.assertEqual(w.main(['mcp']), 69)


if __name__ == '__main__':
    unittest.main()
