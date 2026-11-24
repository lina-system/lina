"""Payload discovery must keep unrelated manifests off the repeated lookup path."""

import io
import json
import os
from pathlib import Path
from unittest.mock import patch

from pm.environments import payload_venv, store_root


def test_adjacent_manifest_is_read_once_and_rechecked_after_replacement(tmp_path: Path) -> None:
    checkout = tmp_path / "checkout"
    checkout.mkdir()
    adjacent = tmp_path / "manifest.json"
    data = {"repo": "siblingx", "venv": "shipped-env", "store": "shipped-tools",
            "inventory": ["unrelated"] * 10_000}
    adjacent.write_text(json.dumps(data), encoding="utf-8-sig")

    with patch("io.open", wraps=io.open) as opened:
        for _ in range(3):
            assert payload_venv(checkout) is None
            store_root(checkout, honor_runtime_override=False)

    adjacent_opens = [call for call in opened.call_args_list if call.args[0] == adjacent]
    assert len(adjacent_opens) <= 1, f"read the unrelated manifest {len(adjacent_opens)} times"

    # Keep both size and mtime: publishing a replacement must invalidate by file identity.
    previous = adjacent.stat()
    replacement = tmp_path / "replacement.json"
    replacement.write_text(json.dumps({**data, "repo": checkout.name}), encoding="utf-8-sig")
    os.utime(replacement, ns=(previous.st_atime_ns, previous.st_mtime_ns))
    replacement.replace(adjacent)

    assert payload_venv(checkout) == tmp_path / "shipped-env"
    assert store_root(checkout, honor_runtime_override=False) == tmp_path / "shipped-tools"
