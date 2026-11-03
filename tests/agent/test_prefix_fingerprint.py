"""lina#25: the opt-in prefix fingerprint capture stores hashes and lengths only, under the home's state directory."""

from __future__ import annotations

import os

from agent import prefix_fingerprint
from hermes_cli import cache_measure

SESSION = "sess-PRIVATE-4411"
TEXT = "PRIVATE-TEXT my garden faces south"


def _request(*user: str) -> dict:
    messages = [{"role": "system", "content": "You are helpful."}]
    messages += [{"role": "user", "content": text} for text in user]
    return {"model": "m", "messages": messages, "tools": [{"type": "function", "function": {"name": "todo"}}]}


def _home(monkeypatch, tmp_path, memory: str):
    home = tmp_path / "home"
    home.mkdir()
    (home / "config.yaml").write_text(f"memory:\n{memory}", encoding="utf-8")
    monkeypatch.setenv("HERMES_HOME", str(home))
    return home


def test_capture_is_off_by_default(monkeypatch, tmp_path):
    home = _home(monkeypatch, tmp_path, "  provider: ''\n")
    prefix_fingerprint.capture(_request(TEXT), SESSION)
    assert not (home / "state" / prefix_fingerprint.FILE_NAME).exists()


def test_opted_in_capture_stores_no_text_and_measures_the_kept_prefix(monkeypatch, tmp_path):
    home = _home(monkeypatch, tmp_path, "  prefix_fingerprints: true\n")
    prefix_fingerprint.capture(_request(TEXT), SESSION)
    prefix_fingerprint.capture(_request(TEXT, "and the second question"), SESSION)
    prefix_fingerprint.capture(_request("an edited first question"), SESSION)

    path = home / "state" / prefix_fingerprint.FILE_NAME
    stored = path.read_text(encoding="utf-8")
    assert TEXT not in stored and SESSION not in stored and "garden" not in stored
    assert os.stat(path).st_mode & 0o077 == 0
    keep = cache_measure.prefix_keep(cache_measure.load_fingerprints(stored.splitlines()))
    assert (keep.requests, keep.kept_whole, keep.broken) == (2, 1, 1)
