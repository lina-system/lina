"""Clock, output, masking, json files and the write lock of ``lina work``."""

from __future__ import annotations

import fcntl  # windows-footgun: ok — the work record runs on the POSIX hub (launchd/systemd) only
import json
import re
import sys
import time
from pathlib import Path

from hermes_cli.work.env import current
from hermes_cli.work.locale import label, t


def now() -> int:
    return int(time.time())


def iso(ts=None) -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%S%z", time.localtime(ts or time.time()))


def ago(ts) -> str:
    if not ts:
        return "?"
    m, h, d = label("ago")
    s = max(0, now() - int(ts))
    if s < 3600:
        return f"{s // 60}{m}"
    if s < 86400:
        return f"{s // 3600}{h}" + (f" {s % 3600 // 60}{m}" if s < 6 * 3600 and s % 3600 >= 60 else "")
    return f"{s // 86400}{d}"


def die(msg: str, code: int = 2):
    print(f"work: {msg}", file=sys.stderr)
    raise SystemExit(code)


def warn(msg: str) -> None:
    print(f"work: {msg}", file=sys.stderr)


SECRET_RE = [re.compile(p) for p in (
    r"gh[pousr]_[A-Za-z0-9]{20,}", r"github_pat_[A-Za-z0-9_]{20,}", r"sk-[A-Za-z0-9_-]{16,}", r"xox[abprs]-[\w-]{10,}",
    r"AKIA[0-9A-Z]{16}", r"eyJ[\w-]{15,}\.[\w-]{10,}\.[\w-]{10,}", r"ops_[A-Za-z0-9_-]{20,}",
    r"(?i)(?:token|secret|passw(?:or)?d|api[_-]?key|authorization)\s*[=:]\s*\S+",
    r"(?<![0-9a-f])(?![0-9a-f]{40}(?![0-9a-f]))[A-Za-z0-9+/_-]{48,}={0,2}")]


def mask(s) -> str:
    s = s or ""
    for r in SECRET_RE:
        s = r.sub(label("masked"), s)
    return s


def load_json(p, default):
    try:
        return json.loads(Path(p).read_text(encoding="utf-8-sig"))
    except (OSError, ValueError):
        return default


def save_json(p, obj) -> None:
    p = Path(p)
    p.parent.mkdir(parents=True, exist_ok=True)
    tmp = p.with_suffix(".tmp")
    tmp.write_text(json.dumps(obj, ensure_ascii=False, indent=1), encoding="utf-8")
    tmp.replace(p)


def paths() -> dict[str, Path]:
    d = current().root
    return {"dir": d, "state": d / "state.json", "notify": d / "notify.json", "log": d / "checker.log",
            "lock": d / ".lock", "freeze": d / "freeze", "db": d / "work.db", "notices": d / "notices"}


def freeze_wait() -> float:
    return float(current().section("work").get("freeze_wait_s") or 1800)


class Lock:
    """One writer at a time; a frozen record (``work freeze``) makes writers wait, never fail, until ``thaw``."""

    def __enter__(self):
        p = paths()
        p["dir"].mkdir(parents=True, exist_ok=True)
        deadline, told = time.monotonic() + freeze_wait(), False
        while True:
            while p["freeze"].exists():
                if time.monotonic() >= deadline:
                    die(t("기록을 옮기는 중이라 {s}초 기다렸지만 안 풀렸다 ({path})", s=int(freeze_wait()),
                          path=p["freeze"]), 75)
                if not told:
                    warn(t("기록을 옮기는 중이다. 끝날 때까지 기다린다"))
                    told = True
                time.sleep(1)
            self.f = open(p["lock"], "w", encoding="utf-8")
            fcntl.flock(self.f, fcntl.LOCK_EX)
            if not p["freeze"].exists():
                return self
            fcntl.flock(self.f, fcntl.LOCK_UN)
            self.f.close()

    def __exit__(self, *a):
        fcntl.flock(self.f, fcntl.LOCK_UN)
        self.f.close()


def writer_profile() -> str:
    """The profile whose shell writes this line (the active profile), not the item's owner."""
    from hermes_cli.profiles import get_active_profile_name
    return get_active_profile_name() or "default"
