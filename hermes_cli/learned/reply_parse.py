"""Record the owner's OK / fix / drop answers to a sent card in verdicts.jsonl.

`learned reply "<text>"` parses numbered verdicts (parsed_by "reply"); text it
cannot fully parse records nothing and exits 3. `learned verdict <item>` records
a bot-interpreted verdict (parsed_by "bot") for an item id or a card line id.
Each record: {at, day, n, item, verdict: ok|fix|drop, text?, parsed_by}.
"""
from datetime import datetime, timezone
import json
import re
import time
from hermes_cli.learned.locale import word

UNPARSED, NO_CARD, UNKNOWN = 3, 1, 2
WORDS = ("ok", "drop", "fix")
OK = r"(?:okay|ok|(?:ㅇㅋ)+|오케이|오키)"
VERBS = (("ok", OK), ("drop", r"(?:drop|remove|지움|지워(?:줘|주세요|요)?|빼(?:줘|주세요|요)?|삭제)"),
         ("fix", r"(?:fix|change|고침|고쳐(?:줘|주세요|요)?|수정)"))
END = r"\s*[.!~]*(?=\s*(?:[,，]|$)|\s+\d)"
NUMBER = r"\d+\s*번?(?:\s*[-~]\s*\d+\s*번?)?"
NUMBERS = NUMBER + r"(?:(?:\s*[,，·]\s*|\s+)" + NUMBER + r")*"
ALL = re.compile(r"(?:(?:all|rest|다|전부|모두|나머지는|나머진|나머지)\s*)?(?:다\s*)?" + OK + END, re.IGNORECASE)
ITEM = re.compile(r"(?P<numbers>" + NUMBERS + r")\s*(?:은|는)?\s*(?:"
                  + "|".join(f"(?P<{name}>{verb})" for name, verb in VERBS) + ")", re.IGNORECASE)
FIX_TEXT = re.compile(r"\s*[:：]\s*|\s+")
SEPARATOR = re.compile(r"[\s,，]*")
HEADER = re.compile(r"\s*(?:>\s*)?(?:Learned|배운\s*것)\s*(\d{1,2})-(\d{1,2})(?=[ \t:·]|\n|$)")


class Unparsed(ValueError):
    pass


def records(path):
    try:
        return [json.loads(line) for line in path.read_text(encoding="utf-8-sig").splitlines() if line.strip()]
    except FileNotFoundError:
        return []


def cards(state):
    found = []
    for path in sorted((state / "cards").glob("????-??-??.json"), reverse=True):
        card = json.loads(path.read_text(encoding="utf-8-sig"))
        if card.get("day") == path.stem:
            found.append(card)
    return found


def numbers(text):
    """[(n, named)]: a number written alone is named; numbers inside a range are not."""
    result = []
    for part in re.findall(r"\d+\s*번?(?:\s*[-~]\s*\d+)?", text):
        low, ranged, high = re.sub(r"[\s번]", "", part).replace("~", "-").partition("-")
        low, high = int(low), int(high or low)
        if low > high:
            raise Unparsed(part)
        result.extend((n, not ranged) for n in range(low, high + 1))
    return result


def strongest(verdicts):
    """One (verdict, text) from equally strong ones: fix or drop beats ok; fix and drop together are ambiguous."""
    found = set(verdicts)
    changes = {value for value in found if value[0] != "ok"}
    if len(found) > 1 and len(changes) != 1:
        raise Unparsed("ambiguous")
    return changes.pop() if changes else found.pop()


def item(line, pos):
    match = ITEM.match(line, pos)
    if not match:
        return None
    verdict = next(name for name, _ in VERBS if match.group(name))
    boundary = (FIX_TEXT if verdict == "fix" else re.compile(END)).match(line, match.end())
    return (match, verdict, boundary.end()) if boundary else None


def starts_verdict(line, pos):
    return bool(ALL.match(line, pos) or item(line, pos))


def parse_line(line):
    """Return (whole, [(numbers, verdict, text)]) for one line, or raise Unparsed."""
    whole, given, pos = False, [], 0
    while True:
        pos = SEPARATOR.match(line, pos).end()
        if pos == len(line):
            return whole, given
        match = ALL.match(line, pos)
        if match:
            whole, pos = True, match.end()
            continue
        found = item(line, pos)
        if not found:
            raise Unparsed(line[pos:])
        match, verdict, pos = found
        text = None
        if verdict == "fix":
            # Fix text runs to the end of the line or to a comma that starts the next verdict,
            # so "5 고쳐 사과, 배 둘 다, 6 지워" keeps "사과, 배 둘 다" whole.
            end = next((pos + comma.start() for comma in re.finditer(r"[,，]", line[pos:])
                        if starts_verdict(line, SEPARATOR.match(line, pos + comma.end()).end())), len(line))
            text, pos = line[pos:end].strip(), end
            if not text:
                raise Unparsed("empty fix")
        given.append((numbers(match.group("numbers")), verdict, text))


def parse(text, count):
    """Map each card line number to (named, verdict, text).

    A line named by its own number beats 다/나머지/a range covering it. One line named
    twice with different verdicts is unparsed; unnamed coverings combine by strongest().
    """
    whole, given = False, {}
    for line in text.splitlines():
        line_whole, line_given = parse_line(line.strip())
        whole = whole or line_whole
        for found, verdict, value in line_given:
            for n, named in found:
                if not 1 <= n <= count:
                    raise Unparsed(str(n))
                given.setdefault(n, []).append((named, verdict, value))
    if whole:
        for n in range(1, count + 1):
            given.setdefault(n, []).append((False, "ok", None))
    if not given:
        raise Unparsed("nothing")
    chosen = {}
    for n, entries in given.items():
        named = {(verdict, value) for flag, verdict, value in entries if flag}
        if len(named) > 1:
            raise Unparsed(str(n))
        chosen[n] = (True,) + named.pop() if named else (False,) + strongest(
            [(verdict, value) for _, verdict, value in entries])
    return chosen


def resolve(card, chosen):
    """Per item {item: (n, verdict, text)}: an item on several lines takes its most named line's verdict."""
    lines = {line["n"]: line for line in card["lines"]}
    entries = {}
    for n in sorted(chosen):
        for item in lines[n]["ids"]:
            entries.setdefault(item, []).append((n,) + chosen[n])
    result = {}
    for item, found in entries.items():
        top = max(named for _, named, _, _ in found)
        found = [(n, verdict, value) for n, named, verdict, value in found if named == top]
        verdict, value = strongest([(verdict, value) for _, verdict, value in found])
        result[item] = next(entry for entry in found if entry[1:] == (verdict, value))
    return result


def split_header(text):
    """("-MM-DD" or None, verdict text). A card header counts only at the start of the reply,
    optionally after '>': a line holding only the quoted header ("배운 것 01-02 · …") or
    the header followed by verdicts. A header inside verdict text stays text."""
    header = HEADER.match(text)
    if not header:
        return None, text
    suffix = f"-{int(header.group(1)):02d}-{int(header.group(2)):02d}"
    first, _, others = text[header.end():].partition("\n")
    if not first.strip() or first.strip().startswith("·"):
        return suffix, others
    return suffix, first.lstrip(" \t:") + ("\n" + others if others else "")


def pick(state, suffix):
    """The header's card; else the latest sent card with lines, answered or not. An older card
    is picked only by its quoted header."""
    found = cards(state)
    if suffix:
        return next((card for card in found if card["day"].endswith(suffix)), None)
    return next((card for card in found if card["lines"]), None)
