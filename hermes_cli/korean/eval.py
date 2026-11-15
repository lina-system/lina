"""``lina eval korean``: every registered Korean check scored on the labeled set in the store (lina#377).

The set lives in ``memory.korean_eval_texts`` (migration 0017) and never in the repository: sentences the owner
flagged, each with the flagged phrase, and clean texts a person marked "nothing to fix" (the owner, or a reviewer
agent of another model family than the writer's). Everything this module prints or stores outside that table is a
count or an interval; no sentence, phrase or other text of the set reaches the output (docs/korean.md).
"""

from __future__ import annotations

import hashlib
import json
import math
from collections import Counter
from collections.abc import Callable, Iterator
from dataclasses import asdict, dataclass, field
from pathlib import Path

Span = tuple[int, int]
# A check takes one text and returns the character spans it flags ([start, end) offsets); empty means clean.
Check = Callable[[str], list[Span]]

CHECKS: dict[str, Check] = {}

# Below this many clean texts a false-alarm rate is too wide to decide anything (lina#377).
SMOKE_BELOW = 50
DEFAULT_MAX_FALSE_ALARM = 0.10
_Z = 1.96

FLAGGED_COLUMNS = ("sentence", "phrase", "agent", "source")
CLEAN_COLUMNS = ("text", "marked_by", "marker_family", "writer_family", "agent", "source")


def register(name: str, check: Check) -> None:
    """Add *check* to the checks ``lina eval korean`` scores."""
    if name in CHECKS:
        raise ValueError(f"a Korean check named {name!r} is already registered")
    CHECKS[name] = check


@dataclass(frozen=True)
class WordPair:
    avoid: str
    use: str


def word_list_pairs(conn) -> list[WordPair]:
    """Read word-only samples backed by stored pairs through the evaluator's owner connection.

    Each stored pair contributes its entry's avoid word as both sentence and phrase, preserving
    this seam's WordPair contract without exposing private sentences outside the store.
    """
    from hermes_cli.korean import words
    from hermes_cli.korean.checks import register_checks

    entries = words.entries(conn)
    register_checks(entries)
    stored = words.pairs(conn)
    return [WordPair(entry.avoid, entry.use) for entry in entries for _ in stored.get(entry.entry_id, [])]



@dataclass(frozen=True)
class LabeledText:
    kind: str
    text: str
    source: str
    phrase: str | None = None
    marked_by: str | None = None
    marker_family: str | None = None
    writer_family: str | None = None
    agent_id: str | None = None


@dataclass
class ImportResult:
    kind: str
    lines: int = 0
    imported: int = 0
    already: int = 0
    refused: list[tuple[int, str]] = field(default_factory=list)


def _lines(path: Path) -> Iterator[tuple[int, str | None]]:
    """(line number, text) per line of *path*; text is None for a line that is not UTF-8."""
    data = path.read_bytes().removeprefix(b"\xef\xbb\xbf")
    raw = data.split(b"\n")
    if raw and raw[-1] == b"":
        raw.pop()
    for number, line in enumerate(raw, start=1):
        try:
            yield number, line.decode("utf-8").removesuffix("\r")
        except UnicodeDecodeError:
            yield number, None


def _fields(line: str, columns: tuple[str, ...]) -> list[str] | str:
    fields = [value.strip() for value in line.split("\t")]
    if len(fields) > len(columns):
        return f"{len(fields)} columns, at most {len(columns)} expected ({', '.join(columns)})"
    return fields + [""] * (len(columns) - len(fields))


def parse_flagged(line: str, source: str) -> LabeledText | str:
    """One flagged line, or the reason it cannot be imported (the reason never quotes the line)."""
    fields = _fields(line, FLAGGED_COLUMNS)
    if isinstance(fields, str):
        return fields
    sentence, phrase, agent, own_source = fields
    if not sentence:
        return "no sentence"
    if not phrase:
        return "no flagged phrase"
    if phrase not in sentence:
        return "the flagged phrase is not in the sentence"
    return LabeledText("flagged", sentence, own_source or source, phrase=phrase, agent_id=agent or None)


def parse_clean(line: str, source: str) -> LabeledText | str:
    """One clean line, or the reason it cannot be imported (the reason never quotes the line)."""
    fields = _fields(line, CLEAN_COLUMNS)
    if isinstance(fields, str):
        return fields
    text, marked_by, marker_family, writer_family, agent, own_source = fields
    marked_by, marker_family, writer_family = marked_by.lower(), marker_family.lower(), writer_family.lower()
    if not text:
        return "no text"
    if marked_by == "owner":
        if marker_family:
            return "an owner mark takes no reviewer model family"
    elif marked_by == "reviewer":
        if not (marker_family and writer_family):
            return "a reviewer mark needs the reviewer's model family and the writer's"
        if marker_family == writer_family:
            return "the reviewer's model family is the writer's; only another family's mark counts"
    else:
        return "marked_by must be owner or reviewer"
    return LabeledText("clean", text, own_source or source, marked_by=marked_by, marker_family=marker_family or None,
                       writer_family=writer_family or None, agent_id=agent or None)


_PARSERS: dict[str, Callable[[str, str], LabeledText | str]] = {"flagged": parse_flagged, "clean": parse_clean}


def _sha256(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def _insert(conn, row: LabeledText) -> str | None:
    """'imported', 'already', or the reason the row conflicts with the set."""
    digest = _sha256(row.text)
    other = conn.execute("SELECT kind FROM memory.korean_eval_texts WHERE text_sha256 = %s AND kind <> %s LIMIT 1",
                         (digest, row.kind)).fetchone()
    if other:
        return f"the same text is already labeled {other[0]}"
    inserted = conn.execute(
        "INSERT INTO memory.korean_eval_texts (kind, text, text_sha256, phrase, marked_by, marker_family, "
        "writer_family, agent_id, source) VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s) "
        "ON CONFLICT DO NOTHING RETURNING text_id",
        (row.kind, row.text, digest, row.phrase, row.marked_by, row.marker_family, row.writer_family, row.agent_id,
         row.source)).fetchone()
    return "imported" if inserted else "already"


def import_file(conn, kind: str, path: Path, *, source: str) -> ImportResult:
    """Import every line of a TSV file of *kind* in one transaction: each line is imported, already in the set,
    or refused with a reason; none is dropped silently."""
    parse = _PARSERS[kind]
    result = ImportResult(kind)
    with conn.transaction():
        for number, line in _lines(path):
            result.lines += 1
            if line is None:
                result.refused.append((number, "not UTF-8"))
                continue
            if not line.strip():
                result.refused.append((number, "empty line"))
                continue
            row = parse(line, source)
            outcome = row if isinstance(row, str) else _insert(conn, row)
            if outcome == "imported":
                result.imported += 1
            elif outcome == "already":
                result.already += 1
            else:
                result.refused.append((number, outcome))
    return result


def import_lines(result: ImportResult, path: Path) -> list[str]:
    head = (f"{path}: {result.lines} {result.kind} lines: {result.imported} imported, {result.already} already in "
            f"the set, {len(result.refused)} refused.")
    return [head, *(f"  line {number}: {reason}" for number, reason in result.refused)]



@dataclass(frozen=True)
class LabeledSet:
    flagged: list[tuple[str, str]]  # (sentence, flagged phrase), one per flagged phrase
    clean: list[str]
    clean_by: dict[str, int]
    pairs: int

    @property
    def sentences(self) -> int:
        return len({sentence for sentence, _ in self.flagged})

    def counts(self) -> dict[str, int]:
        return {"flagged_sentences": self.sentences, "flagged_phrases": len(self.flagged), "clean": len(self.clean),
                "word_list_pairs": self.pairs, **{f"clean_{who}": n for who, n in sorted(self.clean_by.items())}}


def load_set(conn) -> LabeledSet:
    flagged, clean, clean_by = [], [], Counter()
    for kind, text, phrase, marked_by in conn.execute(
            "SELECT kind, text, phrase, marked_by FROM memory.korean_eval_texts ORDER BY text_id"):
        if kind == "flagged":
            flagged.append((text, phrase))
        else:
            clean.append(text)
            clean_by[marked_by] += 1
    pairs = word_list_pairs(conn)
    flagged.extend((pair.avoid, pair.avoid) for pair in pairs)
    return LabeledSet(flagged, clean, dict(clean_by), len(pairs))


@dataclass(frozen=True)
class Score:
    sentences_caught: int
    sentences: int
    phrases_caught: int
    phrases: int
    false_alarms: int
    clean: int


def _covers(spans: list[Span], sentence: str, phrase: str) -> bool:
    """Whether a flagged span overlaps any occurrence of *phrase* in *sentence*."""
    start = sentence.find(phrase)
    while start >= 0:
        end = start + len(phrase)
        if any(a < end and start < b for a, b in spans):
            return True
        start = sentence.find(phrase, start + 1)
    return False


def score(check: Check, labeled: LabeledSet) -> Score:
    spans = {sentence: check(sentence) for sentence, _ in labeled.flagged}
    return Score(
        sentences_caught=sum(1 for found in spans.values() if found),
        sentences=len(spans),
        phrases_caught=sum(1 for sentence, phrase in labeled.flagged if _covers(spans[sentence], sentence, phrase)),
        phrases=len(labeled.flagged),
        false_alarms=sum(1 for text in labeled.clean if check(text)),
        clean=len(labeled.clean),
    )


def wilson(k: int, n: int, z: float = _Z) -> tuple[float, float]:
    """The Wilson score interval of k successes in n trials (95% for the default z); n must be positive."""
    p = k / n
    denominator = 1 + z * z / n
    centre = (p + z * z / (2 * n)) / denominator
    half = z * math.sqrt(p * (1 - p) / n + z * z / (4 * n * n)) / denominator
    return max(0.0, centre - half), min(1.0, centre + half)


def _measure(k: int, n: int) -> str:
    if n == 0:
        return "0/0 (no texts)"
    low, high = wilson(k, n)
    return f"{k}/{n}  {100 * k / n:.1f}%  95% CI {100 * low:.1f}-{100 * high:.1f}%"


_MEASURES = (("flagged sentences caught", "sentences_caught", "sentences"),
             ("flagged phrases caught", "phrases_caught", "phrases"),
             ("false alarms on clean texts", "false_alarms", "clean"))


def _change(now: dict[str, int], then: dict[str, int] | None, k_key: str, n_key: str) -> str:
    if then is None:
        return "  (not in the baseline)"
    k, n = then[k_key], then[n_key]
    if not n or not now[n_key]:
        return f"  (baseline {k}/{n})"
    points = 100 * (now[k_key] / now[n_key] - k / n)
    return f"  (baseline {k}/{n}, {points:+.1f} pts)"


def report(labeled: LabeledSet, scores: dict[str, Score], baseline: tuple[str, dict] | None) -> list[str]:
    counts = labeled.counts()
    by = ", ".join(f"{who} {n}" for who, n in sorted(labeled.clean_by.items()))
    lines = [f"Korean set: {counts['flagged_sentences']} flagged sentences with {counts['flagged_phrases']} flagged "
             f"phrases ({labeled.pairs} word-list pairs), {counts['clean']} clean texts" + (f" ({by})." if by else ".")]
    if len(labeled.clean) < SMOKE_BELOW:
        lines.append(f"smoke only: {len(labeled.clean)} clean texts, fewer than {SMOKE_BELOW}; no rate here may be "
                     "used as a gate.")
    then_checks = None
    if baseline is not None:
        name, saved = baseline
        then = saved["set"]
        lines.append(f"Compared with baseline {name}: then {then['flagged_sentences']} flagged sentences, "
                     f"{then['flagged_phrases']} flagged phrases, {then['clean']} clean texts.")
        then_checks = saved["checks"]
    if not scores:
        lines.append("No Korean check is registered yet (the checks are lina#381); only the set is counted.")
    for name, result in scores.items():
        now = asdict(result)
        lines.append(f"check {name}")
        for label, k_key, n_key in _MEASURES:
            change = "" if then_checks is None else _change(now, then_checks.get(name), k_key, n_key)
            lines.append(f"  {label:<28} {_measure(now[k_key], now[n_key])}{change}")
    return lines


def gate(labeled: LabeledSet, scores: dict[str, Score], max_false_alarm: float) -> tuple[int, str]:
    """(exit code, line): 2 when the set cannot gate, 1 when a check's false-alarm upper bound is above the limit."""
    if len(labeled.clean) < SMOKE_BELOW:
        return 2, (f"gate: refused: smoke only ({len(labeled.clean)} clean texts, fewer than {SMOKE_BELOW}); no rate "
                   "is used as a gate.")
    if not scores:
        return 2, "gate: refused: no Korean check is registered, so there is nothing to gate."
    over = [f"{name} {100 * wilson(s.false_alarms, s.clean)[1]:.1f}%" for name, s in scores.items()
            if wilson(s.false_alarms, s.clean)[1] > max_false_alarm]
    if over:
        return 1, (f"gate: failed: false alarms on clean texts may exceed {100 * max_false_alarm:.1f}% "
                   f"(upper 95% bound): {', '.join(over)}.")
    return 0, (f"gate: passed: every check's false alarms on clean texts are at most {100 * max_false_alarm:.1f}% "
               "(upper 95% bound).")



def _baseline_counts(labeled: LabeledSet, scores: dict[str, Score]) -> dict:
    return {"set": labeled.counts(), "checks": {name: asdict(result) for name, result in scores.items()}}


def find_baseline(conn, name: str | None) -> tuple[str, dict] | None:
    """The named baseline, else the newest; None when none is saved."""
    if name is None:
        row = conn.execute("SELECT name, counts FROM memory.korean_eval_baselines "
                           "ORDER BY created_at DESC, name LIMIT 1").fetchone()
    else:
        row = conn.execute("SELECT name, counts FROM memory.korean_eval_baselines WHERE name = %s",
                           (name,)).fetchone()
    return (row[0], row[1]) if row else None


def save_baseline(conn, name: str, labeled: LabeledSet, scores: dict[str, Score]) -> None:
    conn.execute("INSERT INTO memory.korean_eval_baselines (name, counts) VALUES (%s, %s)",
                 (name, json.dumps(_baseline_counts(labeled, scores))))



def evaluate(conn, *, out: Callable[[str], None] = print, baseline_name: str | None = None,
             save_as: str | None = None, use_gate: bool = False,
             max_false_alarm: float = DEFAULT_MAX_FALSE_ALARM) -> int:
    if save_as is not None and find_baseline(conn, save_as) is not None:
        out(f"eval korean: a baseline named {save_as} already exists; a baseline is never replaced, pick another name.")
        return 1
    baseline = find_baseline(conn, baseline_name)
    if baseline_name is not None and baseline is None:
        out(f"eval korean: no baseline named {baseline_name}.")
        return 1
    labeled = load_set(conn)
    scores = {name: score(check, labeled) for name, check in CHECKS.items()}
    for line in report(labeled, scores, baseline):
        out(line)
    if save_as is not None:
        save_baseline(conn, save_as, labeled, scores)
        conn.commit()
        out(f"Saved baseline {save_as} (counts only).")
    if not use_gate:
        return 0
    code, line = gate(labeled, scores, max_false_alarm)
    out(line)
    return code


def run(args) -> int:
    """The CLI entry: ``lina eval korean [import --flagged FILE --clean FILE]``."""
    import psycopg

    from hermes_state_store import StoreUnavailableError, _owner_connection
    try:
        with _owner_connection() as conn:
            if args.korean_action == "import":
                return _import(conn, args)
            return evaluate(conn, baseline_name=args.baseline, save_as=args.save_baseline, use_gate=args.gate,
                            max_false_alarm=args.max_false_alarm)
    except StoreUnavailableError as exc:
        print(f"eval korean: {exc}")
    except psycopg.errors.UndefinedTable:
        print("eval korean: the store has no Korean set yet; run `lina store upgrade` (migration 0017).")
    return 1


def _import(conn, args) -> int:
    files = [(kind, path) for kind, path in (("flagged", args.flagged), ("clean", args.clean)) if path is not None]
    if not files:
        print("eval korean import: give --flagged FILE, --clean FILE or both.")
        return 1
    refused = 0
    for kind, path in files:
        result = import_file(conn, kind, path, source=args.source)
        refused += len(result.refused)
        for line in import_lines(result, path):
            print(line)
    return 1 if refused else 0
