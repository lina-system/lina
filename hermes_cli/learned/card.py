"""Korean, eight-line stdout cards. No senders and no verdict mutations."""
import importlib.util
import json
import os
from pathlib import Path
import re
import shlex
import signal
import subprocess


from hermes_cli.work.env import current
from hermes_cli.learned.locale import word
from hermes_cli.learned import text as _text

FLAG_KEYS = {"regressed":"s33","no_basis":"s34","time_mismatch":"s35","self_closed":"s36","deleted":"s37","content_lost":"s38"}
def totals():
    return ((word("s18"), ("conclusion",)), (word("s40"), ("skill",)),
            (word("s41"), ("rule", "soul")), (word("s42"), ("memory",)),
            (word("s22"), ("user",)), (word("s43"), ("ledger",)))
CONCLUSION_LINES = 2

def bot_name(bot, phrase):
    return word("s39") if bot == "unknown" else current().agents.get(bot, phrase(bot, bot, limit=10))


def place_label(place, phrase):
    """Name a rule file from where it lives, e.g. 루미 행동 규칙(SOUL.self) or 공용 스킬 <description>."""
    skill = phrase(place.get("name", ""), "")
    if place["scope"] == "shared":
        return word("s4") + (skill if skill and skill != word("s0") else word("s3"))
    owner = bot_name(place["bot"], phrase)
    if place["what"] == "skill":
        return owner + word("s5") + (skill if skill and skill != word("s0") else word("s3"))
    label = owner + (word("s6") if place["what"] == "soul" else word("s7"))
    return label + ("(" + place["file"] + ")" if place.get("file") else "")


def provenance(row):
    sources = row["sources"]
    if len(sources) == 1:
        return word("s8") if "seen" in sources else word("s9")
    return word("s10").format(sources.get("seen", 0), sum(n for key, n in sources.items() if key != "seen"))


def summary(row, phrase):
    form = row["form"]
    if form == "private":
        return word("s11").format(row["count"])
    if form == "memory":
        net, usage = row["net"], row["usage"]
        percent = lambda value: "?" if value is None else f"{value:g}"
        return word("s12").format(
            row["ops"], net["added"], net["changed"], net["removed"], percent(usage["before"]), percent(usage["after"]))
    if form == "skills":
        return word("s13").format(row["count"], row["ops"])
    if form == "skill":
        return phrase(row.get("display_name", ""), word("s0")) + word("s14").format(row["ops"])
    if form == "deleted":
        return word("s15") + _text.content(row.get("before", ""), word("s16"))
    if form == "shared_skills":
        return word("s17").format(row["count"])
    if form == "conclusion":
        counts = row["counts"]
        parts = [word("conclusion_count").format(title, counts[key]) for title, key in
                 ((word("s18"), "new"), (word("s19"), "deleted"), (word("s20"), "private")) if counts[key]]
        lead = _text.content(row.get("lead", ""), "")
        if lead:
            parts.append(lead + (word("s21") if row.get("lead_deleted") else ""))
        return " · ".join(parts)
    content = row.get("before", "") if "deleted" in row["flags"] else row.get("after", "")
    if row.get("place"):
        lead = _text.content(content, "")
        return place_label(row["place"], phrase) + (" · " + lead if lead else "")
    label = {"user": word("s22"), "rule": word("s23"), "soul": word("s24"),
             "conclusion": word("s18"), "ledger": word("s25")}[form]
    return label + " · " + _text.content(content, word("s26").format(row["count"]))


def rewrite(command, summaries):
    """llm_once transport: one stdin prompt, JSON stdout (bare/fenced/result).

    The configured command is a quoted executable/argv, never evaluated as shell.
    Only already masked display summaries cross this boundary. Any error or
    invalid answer keeps the deterministic text; process groups bound children.
    """
    from hermes_cli.process_identity import spawn_owned, stop_owned_group_survivors
    from tools.environments.local import served_profile_child_env
    from hermes_constants import get_hermes_home
    process = None
    try:
        process = spawn_owned(shlex.split(command), purpose="learned card rewrite",
                              stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                              stderr=subprocess.DEVNULL, text=True, encoding="utf-8", errors="replace",
                              env=served_profile_child_env(target_home=get_hermes_home(), inherit_credentials=False))
        prompt = json.dumps({"purpose": "LEARNED_CARD", "instruction": word("s44"),
                             "summaries": summaries}, ensure_ascii=False)
        output, _ = process.communicate(prompt, timeout=30)
        if process.returncode:
            return summaries
        def parse(text):
            return json.loads(re.sub(r"^```(?:json)?\s*|\s*```$", "", text.strip()))
        value = parse(output)
        if isinstance(value, dict) and isinstance(value.get("result"), str):
            value = parse(value["result"])
        if not isinstance(value, list) or len(value) != len(summaries):
            return summaries
        if any(not isinstance(text, str) or _text.content(text, "", limit=48) != text
               or len(text) > 48 for text in value):
            return summaries
        return value
    except subprocess.TimeoutExpired:
        stop_owned_group_survivors(process.pid, proc=process, grace=0)
        process.wait(timeout=5)
    except (OSError, ValueError, TypeError):
        pass
    finally:
        if process is not None:
            stop_owned_group_survivors(process.pid, proc=process, grace=0)
    return summaries


def render(view, phrase, command=""):
    carry = view["carry"]
    carried = (word("s27").format(carry["applied"], carry["pending"])
               if sum(carry.values()) else "")
    flagged = any(set(row["flags"]) - {"private"} for row in view["items"])
    if not flagged:
        seen = sum(row.get("source") == "seen" for row in view["items"])
        said = len(view["items"]) - seen
        text = word("s28").format(seen)
        if said:
            text += word("s29").format(said)
        text += word("s30")
        if carried:
            text += " · " + carried
        return text[:80], []
    shown = [row for row in view["folded"] if row["kind"] != "ledger" or set(row["flags"]) - {"private"}]
    # Conclusions rank low, but the owner reviews them first: reserve their lines.
    # The most reviewable come first: new public conclusions, then deletions, then bot order.
    reserved = sorted((row for row in shown if row["form"] == "conclusion"), key=lambda row: (
        -row["counts"]["new"], -row["counts"]["deleted"], row["bot_order"], row["bot"]))[:CONCLUSION_LINES]
    rest = [row for row in shown if row not in reserved][:6 - len(reserved)]
    selected = [row for row in shown if row in reserved or row in rest]
    summaries = [summary(row, phrase) for row in selected]
    if command:
        indexes = [n for n, row in enumerate(selected) if row["form"] != "private"]
        if indexes:
            for n, text in zip(indexes, rewrite(command, [summaries[n] for n in indexes])):
                summaries[n] = text
    header, tail = word("s31") + view["day"][5:], (" · " + carried if carried else "")
    for title, kinds in totals():
        total = sum(row["kind"] in kinds for row in view["items"])
        part = f" · {title} {total}"
        if total and len(header + part + tail) <= 80:
            header += part
    lines = [(header + tail)[:80]]
    for n, (row, text) in enumerate(zip(selected, summaries), 1):
        bot = bot_name(row["bot"], phrase)
        prefix = f"{n}. {bot} · {provenance(row)} · "
        # A conclusion line already counts its deletions as 지움.
        flags = "·".join(word(label) for flag, label in FLAG_KEYS.items() if flag in row["flags"]
                           and not (flag == "deleted" and row["form"] == "conclusion"))
        suffix = " · " + flags if flags and row["form"] != "private" else ""
        budget = 80 - len(prefix) - len(suffix)
        lines.append(prefix + _text.clip(text, budget) + suffix)
    lines.append(word("s32"))
    return "\n".join(lines), [row["id"] for row in selected]
