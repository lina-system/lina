"""lina-helperctl argv and response handling (ported from LINA Helper's clients/tests/test_lina_helper.py)."""

from __future__ import annotations

import json
import os
import shlex
import subprocess

import pytest

from tools.lina_helper import client as lh

OK = b'{"v":1,"ok":true,"result":{}}\n'


class Fake:
    def __init__(self, stdout=OK, stderr=b"", returncode=0, exc=None):
        self.result = subprocess.CompletedProcess([], returncode, stdout, stderr)
        self.exc, self.calls = exc, []

    def __call__(self, cmd, **kw):
        self.calls.append((cmd, kw))
        if self.exc:
            raise self.exc
        return self.result


def test_builds_fixed_ssh_command():
    assert lh.command("agent-mac", "status") == [
        "ssh", "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=8", "-o", "ServerAliveInterval=5",
        "-o", "ServerAliveCountMax=2", "agent-mac", "'/Applications/LINA Helper.app/Contents/Helpers/lina-helperctl' status"]


@pytest.mark.parametrize("value", ["a b", "it's", '"q"', "$(touch /tmp/x)", "x\ny", "`id`", ";rm -rf /", "--out=/etc/passwd"])
def test_hostile_args_are_quoted(value):
    remote = lh.command("h", "cua.lease", ["holder=" + value])[-1]
    assert shlex.split(remote) == [lh.CTL, "cua.lease", "--arg", "holder=" + value]


@pytest.mark.parametrize("host", ["", None, "-oProxyCommand=x", "a b", "a;b", "h\n", "a" * 129, "u@h", ".h"])
def test_host_rejected(host):
    with pytest.raises(lh.UsageError):
        lh.command(host, "status")


@pytest.mark.parametrize("action", ["", "rm", "status; id", "Status", "shell", "cua.call"])
def test_action_rejected(action):
    with pytest.raises(lh.UsageError):
        lh.command("h", action)


def test_bad_arg_and_out_scope():
    for arg in ["novalue", "=v", "k k=v", "-x=1"]:
        with pytest.raises(lh.UsageError):
            lh.command("h", "ax.tree", [arg])
    with pytest.raises(lh.UsageError):
        lh.command("h", "status", out=True)
    assert shlex.split(lh.command("h", "screenshot", ["display=main"], out=True)[-1])[-2:] == ["--out", "-"]


def test_success_timeouts_and_stdin():
    fake = Fake()
    assert lh.invoke("h", "status", runner=fake)[0] == 0
    assert fake.calls[0][1]["timeout"] == 30 and fake.calls[0][1]["stdin"] is subprocess.DEVNULL
    fake = Fake(exc=subprocess.TimeoutExpired("ssh", 45))
    assert lh.invoke("h", "screenshot", runner=fake)[1]["error"]["code"] == "transport_timeout"
    assert fake.calls[0][1]["timeout"] == 45 and len(fake.calls) == 1


@pytest.mark.parametrize("raw", [b'{"v":1,"ok":true,"pad":"' + b"a" * (128 << 20) + b'"}', b"not json", b"", b"[]",
                                 b'{"v":2,"ok":true}', b'{"v":1,"ok":"yes"}'])
def test_oversized_and_invalid(raw):
    code, value = lh.invoke("h", "status", runner=Fake(raw))
    assert (code, value["error"]["code"]) == (1, "invalid_remote_response")


def test_ok_with_nonzero_exit_is_invalid_and_remote_error_passes_through():
    assert lh.invoke("h", "status", runner=Fake(OK, returncode=255))[1]["error"]["code"] == "invalid_remote_response"
    err = b'{"v":1,"ok":false,"error":{"code":"permission_missing","detail":"screen_recording"}}\n'
    assert lh.invoke("h", "status", runner=Fake(err, returncode=1))[1]["error"]["code"] == "permission_missing"


def test_typed_args_reach_the_cli_as_one_word():
    fake = Fake()
    lh.invoke("h", "ax.tree", ["app=frontmost"], runner=fake, json_args='{"depth": 2}')
    remote = shlex.split(fake.calls[0][0][-1])
    assert remote[remote.index("--json") + 1] == '{"depth":2}'
    for bad in ["[1]", "nope", "2"]:
        with pytest.raises(lh.UsageError):
            lh.command("h", "ax.tree", json_args=bad)


def test_out_writes_bytes_and_never_overwrites(tmp_path):
    path = str(tmp_path / "shot.png")
    png = b"\x89PNG\r\n\x1a\n\x00\xff"
    assert lh.invoke("h", "screenshot", out=path, runner=Fake(png, b"log line\n" + OK))[0] == 0
    assert open(path, "rb").read() == png
    fake = Fake(b"new", OK)
    with pytest.raises(lh.UsageError):
        lh.invoke("h", "screenshot", out=path, runner=fake)
    assert fake.calls == [] and open(path, "rb").read() == png


def test_failed_or_invalid_screenshot_leaves_no_file(tmp_path):
    path = str(tmp_path / "shot.png")
    err = b'{"v":1,"ok":false,"error":{"code":"timeout"}}\n'
    for fake in [Fake(b"", err, 1), Fake(b"png", b"garbage"), Fake(b"png", b""), Fake(b"", OK)]:
        assert lh.invoke("h", "screenshot", out=path, runner=fake)[0] == 1
        assert not os.path.exists(path)
    missing = str(tmp_path / "missing" / "shot.png")
    assert lh.invoke("h", "screenshot", out=missing, runner=Fake(b"png", OK))[1]["error"]["code"] == "output_unwritable"


def test_large_ax_tree_response_is_accepted():
    node = {"path": "0/1", "role": "AXStaticText", "title": "t" * 512, "value": "v" * 512}
    raw = json.dumps({"v": 1, "ok": True, "result": {"elements": [node] * 2000}}).encode()
    code, value = lh.invoke("h", "ax.tree", runner=Fake(raw))
    assert (code, len(value["result"]["elements"])) == (0, 2000)
