"""Conservative Korean display text; structural names never become card prose."""
import difflib
import re
from hermes_cli.learned.locale import word
from hermes_cli.work.env import current


def clip(value, limit):
    """Cut at the last word boundary that fits, marked with an ellipsis."""
    if len(value) <= limit:
        return value
    cut = value[:limit - 1]
    if value[limit - 1] != " " and " " in cut.strip():
        cut = cut[:cut.rstrip().rfind(" ")]
    return cut.rstrip(" ·") + "…"


def phrase(value, fallback="", limit=28):
    value = re.split(r"[\n.!?。;,]", value.strip().strip("\"'"), maxsplit=1)[0].strip(" #*-:")
    if current().locale == "ko" and (not re.search(r"[가-힣]", value) or re.search(r"[A-Za-z/\\|_~<>\x00-\x1f]", value)):
        return fallback
    return clip(value, limit)


def content(value, fallback="", limit=28):
    """A phrase of free Korean text that may name things in Latin letters (LINA, PR).

    Mostly-Hangul phrases pass; paths, ids, markup and control characters do not.
    Structural names (skills, bots) keep the stricter phrase() rule.
    """
    value = re.split(r"\n|[.!?。;,](?=\s|$)", value.strip().strip("\"'"), maxsplit=1)[0].strip(" #*-:")
    hangul, latin = len(re.findall(r"[가-힣]", value)), len(re.findall(r"[A-Za-z]", value))
    if current().locale == "ko" and (not hangul or hangul < latin):
        return fallback
    # Path-like (a/b, ~x, /x), escapes or markup, control characters, and id-like
    # tokens: 8+ hex digits or a snake_case identifier.
    if re.search(r"\w/\w|(?:^|\s)[~/]|[\\|<>\x00-\x1f]|(?<![0-9A-Za-z])[0-9A-Fa-f]{8,}(?![0-9A-Za-z])|\w_\w",
                 value):
        return fallback
    return clip(value, limit)


def skill_name(content):
    lines = content.splitlines()
    if not lines or lines[0].strip() != "---":
        return word("s0")
    for index, line in enumerate(lines[1:], 1):
        if line.strip() == "---":
            break
        if line.startswith("description:"):
            value = line.partition(":")[2].strip()
            if value in ("|", ">", "|-", ">-", "|+", ">+"):
                value = " ".join(part.strip() for part in lines[index + 1:]
                                 if part.startswith((" ", "\t")))
            return phrase(value, word("s0"))
    return word("s0")


def net_size(before, after):
    return sum(max(i2 - i1, j2 - j1) for op, i1, i2, j1, j2 in
               difflib.SequenceMatcher(None, before.splitlines(), after.splitlines(), autojunk=False).get_opcodes()
               if op != "equal")
