"""Exercise output-transform routing through the real compute hosts."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from tests.tui_gateway._turn_host_parity_support import SID, Reply, gateway, loopback_model, stored_rows
from tests.tui_gateway.test_rust_host_rollback import FIXTURE_SECRETS, home, rust_binary, store_dsn, workdir
from tests.tui_gateway.test_turn_host_parity import _config, _event, _host_frame, _host_pid

__all__ = ["home", "rust_binary", "store_dsn", "workdir"]

pytestmark = pytest.mark.platforms("linux")

RAW = "Untransformed model reply."
REWRITE = "Rewritten plugin reply."


def _install_transform(home: Path, *, hold_stream: bool) -> None:
    plugin = home / "plugins" / "output-transform"
    plugin.mkdir(parents=True)
    (plugin / "plugin.yaml").write_text(
        'name: output-transform\nversion: "0.1.0"\ndescription: Output transform fixture\n',
        encoding="utf-8",
    )
    (plugin / "__init__.py").write_text(
        "def register(ctx):\n"
        '    ctx.register_hook("transform_llm_output", rewrite'
        f", hold_stream={hold_stream!r})\n\n"
        "def rewrite(*, response_text, **kwargs):\n"
        f"    return {REWRITE!r} if response_text == {RAW!r} else None\n",
        encoding="utf-8",
    )
    path = home / "config.yaml"
    config = json.loads(path.read_text(encoding="utf-8"))
    config["plugins"] = {"enabled": ["output-transform"]}
    path.write_text(json.dumps(config), encoding="utf-8")


@pytest.mark.parametrize(
    "plugin_profile,hold_stream",
    [("served", False), ("served", True), ("launch", True)],
    ids=["ordinary-transform", "held-transform", "unrelated-profile"],
)
def test_output_transform_routes_by_served_profile(
    workdir: Path, home: Path, rust_binary: Path, store_dsn: str,
    plugin_profile: str, hold_stream: bool,
) -> None:
    served = home / "profiles" / "served"
    served.mkdir(parents=True)
    with loopback_model((Reply(text=RAW),)) as model:
        _config(home, workdir, model.url, "rust")
        _config(served, workdir, model.url, "rust")
        transforms = plugin_profile == "served"
        _install_transform(served if transforms else home, hold_stream=hold_stream)

        with gateway(home, rust_binary, {}, store_dsn, FIXTURE_SECRETS) as client:
            client.rpc("session.create", cwd=str(workdir), profile="served")
            submission = client.rpc("prompt.submit", session_id=SID, text="Reply to this.")
            complete = _event(client, "message.complete")["payload"]
            assert complete["status"] == "complete", complete
            deltas = [
                frame["params"]["payload"]
                for frame in client.sink.frames()
                if frame.get("method") == "event" and frame["params"]["type"] == "message.delta"
            ]
            if transforms and hold_stream:
                assert not deltas, f"Held transform exposed model deltas: {deltas}"
            expected = REWRITE if transforms else RAW
            assert complete["text"] == expected
            if transforms:
                assert not (served / "state" / f"dashboard-rust-host-{SID}.json").exists()
                if submission.get("turn_isolation"):
                    _host_pid(home, "python", rust_binary)
                    _host_frame(client, "turn.end")
            else:
                assert submission.get("turn_isolation"), submission
                _host_pid(served, "rust", rust_binary)
                _host_frame(client, "turn.end")
            assert [row["content"] for row in stored_rows(served) if row["role"] == "assistant"] == [expected]
        assert len(model.requests) == 1
        assert not model.errors
