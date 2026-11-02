"""Only collector-owned files are writable. Cursor commits happen last."""
import fcntl  # windows-footgun: ok — learned runs on the POSIX hub
import hashlib
import json
import os
from pathlib import Path


def digest(value):
    if isinstance(value, str):
        value = value.encode("utf-8")
    return hashlib.sha256(value).hexdigest()


def read_json(path, default):
    try:
        return json.loads(path.read_text(encoding="utf-8-sig"))
    except FileNotFoundError:
        return default


def atomic(path, text):
    temp = path.with_name(path.name + ".tmp")
    with temp.open("w", encoding="utf-8") as out:
        os.chmod(temp, 0o600)
        out.write(text)
        out.flush()
        os.fsync(out.fileno())
    temp.replace(path)


class State:
    def __init__(self, root):
        self.root = root
        self.days = {}

    def __enter__(self):
        self.root.mkdir(parents=True, exist_ok=True, mode=0o700)
        os.chmod(self.root, 0o700)
        self.lock = (self.root / ".lock").open("a")
        os.chmod(self.root / ".lock", 0o600)
        fcntl.flock(self.lock, fcntl.LOCK_EX)
        for name in ("blobs", "snap", "items"):
            (self.root / name).mkdir(exist_ok=True, mode=0o700)
            (self.root / name).chmod(0o700)
        self.cursor = read_json(self.root / "cursor.json", {})
        return self

    def __exit__(self, *args):
        fcntl.flock(self.lock, fcntl.LOCK_UN)
        self.lock.close()

    def blob(self, text):
        sha = digest(text)
        path = self.root / "blobs" / sha
        if not path.exists():
            atomic(path, text)
        return sha

    def content(self, sha):
        return (self.root / "blobs" / sha).read_text(encoding="utf-8-sig") if sha else ""

    def put(self, item):
        day = item["day"]
        if day not in self.days:
            path = self.root / "items" / (day + ".jsonl")
            self.days[day] = {r["id"]: r for r in (
                json.loads(line) for line in path.read_text(encoding="utf-8-sig").splitlines())} if path.exists() else {}
        self.days[day][item["id"]] = item

    def remove(self, day, id_):
        # Used when a previously unconfirmed tool call returns a failure.
        if day not in self.days:
            path = self.root / "items" / (day + ".jsonl")
            self.days[day] = {r["id"]: r for r in (
                json.loads(line) for line in path.read_text(encoding="utf-8-sig").splitlines())} if path.exists() else {}
        self.days[day].pop(id_, None)

    def lines(self, path, *, ready=None):
        """Resume streams, stopping before incomplete or not-yet-ready records.

        Exhaust the iterator to save the accepted prefix and its fingerprint.
        A false readiness result leaves that entire line for the next run.
        """
        key = digest(str(path))
        cursors = self.cursor.setdefault("streams", {})
        previous = cursors.get(key, {})
        try:
            source = path.open("rb")
        except FileNotFoundError:
            return
        with source:
            offset = previous.get("offset", 0)
            source.seek(max(0, offset - 128))
            if digest(source.read(min(128, offset))) != previous.get("tail"):
                offset = 0
            source.seek(offset)
            while True:
                start = source.tell()
                line = source.readline()
                if not line or not line.endswith(b"\n"):
                    source.seek(start)
                    break
                try:
                    row = json.loads(line)
                except (ValueError, UnicodeDecodeError):
                    # A complete malformed record is an error, not a silently lost change.
                    raise ValueError("invalid source JSON line") from None
                if not isinstance(row, dict):
                    raise ValueError("source record must be an object")
                if ready is not None and not ready(row):
                    source.seek(start)
                    break
                yield row, digest(line)
            offset = source.tell()
            source.seek(max(0, offset - 128))
            cursors[key] = {"offset": offset, "tail": digest(source.read(min(128, offset)))}

    def commit(self):
        for day, items in self.days.items():
            atomic(self.root / "items" / (day + ".jsonl"), "".join(
                json.dumps(item, ensure_ascii=False) + "\n" for item in sorted(items.values(), key=lambda r: r["id"])))
        atomic(self.root / "cursor.json", json.dumps(self.cursor, ensure_ascii=False))
