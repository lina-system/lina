"""What the record reads outside itself, read-only: the owner's words in the agents' state.db (basis), GitHub,
a Todoist probe, the Desk and files, and an optional session spawner queue."""

from __future__ import annotations

import datetime
import json
import re
import sqlite3
import subprocess
import time
import unicodedata
from contextlib import closing
from pathlib import Path

from hermes_cli.work.common import ago, load_json, now
from hermes_cli.work.env import current
from hermes_cli.work.locale import t, word


def profile_home(profile: str) -> Path:
    from hermes_cli.profiles import get_profile_dir
    return Path(get_profile_dir(profile))


# ---------------------------------------------------------------- basis: the owner's words
def said_ts(src, fallback=0) -> int:
    """'10-26 16:11 chat' / '2026-10-26 16:11' / '10-26 12:16·12:18 …' -> epoch of the first time given."""
    m = re.search(r"(?<![\w-])(?:([0-9]{4})-)?([0-9]{1,2})-([0-9]{1,2})(?![0-9])", src or "")
    if not m:
        return fallback
    hm = re.match(r"\s+([0-9]{1,2}):([0-9]{2})(?::([0-9]{2}))?(?![\w:])", src[m.end():])
    if not hm:
        return 0
    y = int(m.group(1) or time.localtime(now()).tm_year)
    try:
        dt = datetime.datetime(y, int(m.group(2)), int(m.group(3)), int(hm.group(1)), int(hm.group(2)),
                               int(hm.group(3) or 0))
        return int(dt.timestamp())
    except (ValueError, OverflowError, OSError):
        return 0


def basis_of(r: dict) -> list[dict]:
    """Read normalized copies; never repair the append-only record while reading."""
    b = [dict(x) for x in r.get("basis") or []]
    if not b and r.get("quote"):
        src = r.get("quote_source") or r.get("said") or ""
        b = [{"text": r["quote"], "at": r.get("said_at") or said_ts(src, 0), "src": src}]
    for x in b:
        if r.get("ts") and (x.get("at") or 0) > r["ts"]:
            x["at"] = 0
        x.setdefault("verified", "unknown")
    return b


def first_said(r: dict) -> int:
    ts = [x.get("at") or 0 for x in basis_of(r) if x.get("at")]
    return min(ts) if ts else int(r.get("created") or 0)


def folded_quote(text: str) -> str:
    return "".join(c for c in text if not c.isspace() and not unicodedata.category(c).startswith("P"))


def basis_candidate_pattern(quote: str):
    # Every character removed by folded_quote is non-word or underscore. Allow that superset between an
    # alphanumeric run's characters, so punctuation inside words cannot hide a match; the fold below still decides.
    # A short suffix anchor avoids walking a long shared introduction in messages that cannot match.
    runs = re.findall(r"[^\W_]+", quote)
    anchor = max(runs, key=len)[-8:] if runs else quote[:1]
    return re.compile(r"[\W_]*".join(re.escape(c) for c in anchor))


def basis_profile(r: dict, b: dict) -> str:
    """New entries keep their own writer even after another agent edits the item."""
    agents = current().agents
    for value in (b.get("profile"), r.get("actor"), r.get("writer"), r.get("by")):
        if value in agents:
            return value
    return r.get("by", "default")


def _basis_match(db, quote: str, claimed: int):
    pattern = basis_candidate_pattern(quote)
    db.create_function("basis_candidate", 1, lambda content: bool(
        quote and isinstance(content, str) and pattern.search(content)))
    t0 = now()
    rows = db.execute("""SELECT m.content, m.timestamp, s.source
                         FROM messages AS m JOIN sessions AS s ON s.id = m.session_id
                         WHERE m.role = 'user' AND m.timestamp >= ? AND m.timestamp <= ?
                         AND basis_candidate(m.content)
                         ORDER BY m.timestamp DESC""", (t0 - 14 * 86400, t0))
    matches = ((at, source) for content, at, source in rows
               if quote in content or quote in folded_quote(content))
    return min(matches, key=lambda m: abs(m[0] - claimed) if claimed else m[0], default=None)


def verify_basis(b: dict, profile: str) -> dict:
    """Best-effort lookup in the profile's state.db; choose the closest claimed time or earliest user match."""
    b = dict(b, verified="unknown")
    quote = folded_quote(b.get("text") or "")
    try:
        uri = (profile_home(profile) / "state.db").resolve().as_uri() + "?mode=ro"
        with closing(sqlite3.connect(uri, uri=True, timeout=0.1)) as db:
            claimed = said_ts(b.get("said", b.get("src", "")))
            match = _basis_match(db, quote, claimed)
            if match is not None:
                at, source = match
                b.update(at=at, src=source or "", verified=(
                    "time_mismatch" if claimed and abs(at - claimed) > 600 else "found"))
            else:
                b["verified"] = "not_found"
    except (sqlite3.Error, OSError, ValueError):
        b["verified"] = "unknown"  # a missing, locked or foreign state.db never blocks a write
    return b


def new_basis(text: str, src, profile: str, said=None) -> dict:
    src = (src or "").strip()
    claimed = src if said is None else said.strip()
    return verify_basis({"text": text, "at": said_ts(claimed), "src": src, "said": claimed, "profile": profile},
                        profile)


def basis_line(b: dict) -> str:
    return (t("근거: {text}", text=b["text"]) + (f"  ({b['src']})" if b.get("src") else "")
            + " " + word("basis", b.get("verified") or "unknown", word("basis", "unknown")))


# ---------------------------------------------------------------- GitHub, Todoist, Desk
def gh_fetch(path: str, cache: dict):
    c = cache.get(path)
    if c and now() - c["at"] < c.get("ttl", 600):
        return c["data"]
    try:
        p = subprocess.run(["gh", "api", path], capture_output=True, text=True, encoding="utf-8", timeout=25, stdin=subprocess.DEVNULL,
                           check=False)
        data = json.loads(p.stdout) if p.returncode == 0 else None
    except (OSError, subprocess.SubprocessError, ValueError):
        data = None
    if not isinstance(data, dict):
        return c["data"] if c else None
    closed = data.get("state") == "closed"
    cache[path] = {"at": now(), "ttl": 6 * 3600 if closed else 600, "data": {
        k: data.get(k) for k in ("state", "title", "updated_at", "closed_at", "open_issues", "closed_issues")
    } | {"is_pr": bool(data.get("pull_request")), "merged": bool((data.get("pull_request") or {}).get("merged_at"))}}
    return cache[path]["data"]


_TODOIST_CACHE: dict[str, str] = {}


def todoist_state(tid: str) -> str:
    """done / not done / unknown from the configured read-only probe (``work.probes.todoist`` argv + id)."""
    if tid not in _TODOIST_CACHE:
        probes = current().section("work").get("probes")
        argv = probes.get("todoist") if isinstance(probes, dict) else None
        state = t("모름")
        if isinstance(argv, list) and argv:
            try:
                pr = subprocess.run([str(a) for a in argv] + [tid], capture_output=True, text=True, encoding="utf-8",
                                    timeout=45,
                                    stdin=subprocess.DEVNULL, check=False)
                state = {0: t("끝남"), 1: t("안 끝남")}.get(pr.returncode, state)
            except (OSError, subprocess.SubprocessError):
                state = t("모름")
        _TODOIST_CACHE[tid] = state
    return _TODOIST_CACHE[tid]


def desk_file(rel: str) -> Path | None:
    desk = current().desk
    return (desk / rel) if desk else None


def link_path_exists(link: str) -> bool | None:
    """desk:/file: link -> exists?; None when no Desk is configured."""
    if link.startswith("desk:"):
        p = desk_file(link[5:])
        return None if p is None else p.exists()
    return Path(link[5:]).exists()


def iso_ts(s) -> int:
    try:
        return int(datetime.datetime.fromisoformat(str(s)).timestamp())
    except ValueError:
        return 0


def verify_evidence(ref: str, ctx: dict):
    """True = machine-verified; None = cannot verify."""
    from hermes_cli.work.links import gh_repo
    if ref.startswith("gh:"):
        spec, n = ref[3:].split("#")
        o, repo = gh_repo(spec)
        d = gh_fetch(f"repos/{o}/{repo}/issues/{n}", ctx["gh"])
        if d and d.get("state") == "closed" and (not d.get("is_pr") or d.get("merged")):
            return True
        return None
    if ref.startswith(("desk:", "file:")):
        return True if link_path_exists(ref) else None
    return None


# ---------------------------------------------------------------- the session spawner queue (optional)
def queue_dir() -> Path | None:
    value = current().section("work").get("session_queue")
    return Path(str(value)).expanduser() if value else None


def _index_entry(by_name: dict, x: dict, bucket: str) -> None:
    k = (x.get("profile"), x.get("name"))
    e = by_name.setdefault(k, {"running": None, "pending": 0, "last": None, "last_exited": None, "activity": 0})
    if bucket == "running":
        e["running"] = x
    elif bucket == "pending":
        e["pending"] += 1
    else:
        if not e["last"] or (x.get("ended") or 0) > (e["last"].get("ended") or 0):
            e["last"] = x
        if x.get("state") == "exited" and (not e["last_exited"] or
                                           (x.get("ended") or 0) > (e["last_exited"].get("ended") or 0)):
            e["last_exited"] = x
    e["activity"] = max(e["activity"], int(x.get("started") or 0), int(x.get("ended") or 0),
                        int(x.get("created") or 0) if bucket == "pending" else 0)


def sessions_index():
    """the session queue -> {(profile, name): info}, {id: rec}"""
    by_name, by_id = {}, {}
    sq = queue_dir()
    for bucket in ("running", "pending", "done"):
        folder = sq / bucket if sq else None
        for p in sorted(folder.glob("*.json")) if folder and folder.exists() else []:
            x = load_json(p, None)
            if not isinstance(x, dict):
                continue
            x["_bucket"] = bucket
            by_id[x.get("id")] = x
            _index_entry(by_name, x, bucket)
    return by_name, by_id


def session_state(e):
    """-> (code, label, activity_ts). code: active|ended|dead|none"""
    if not e:
        return "none", t("세션 기록 없음"), 0
    if e["running"]:
        return "active", t("도는 중 {ago}", ago=ago(e["running"].get("started"))), e["activity"]
    if e["pending"]:
        return "active", t("대기 중"), e["activity"]
    last = e["last"] or {}
    st, rc = last.get("state"), last.get("rc")
    if st == "exited" and rc == 0:
        return "ended", t("끝남 {ago} 전", ago=ago(last.get("ended"))), e["activity"]
    if st == "cancelled":
        return "ended", t("취소됨 {ago} 전", ago=ago(last.get("ended"))), e["activity"]
    detail = (st or "?") + ("" if rc in (None, 0) else " rc=" + str(rc))
    return "dead", t("끊김({detail}) {ago} 전", detail=detail, ago=ago(last.get("ended"))), e["activity"]
