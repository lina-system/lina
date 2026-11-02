"""Stable grouping and ranking for daily learning views."""
from collections import defaultdict
from hermes_cli.learned import storage, text as display
from hermes_cli.learned.locale import word
from hermes_cli.learned.fold import rank

def combine(rows, form):
    rows = sorted(rows, key=lambda row: row["id"])
    first = max(rows, key=lambda row: rank(dict(row, flags=[])))
    members = sorted({item for row in rows for item in row.get("members", [row["id"]])})
    sources = defaultdict(int)
    for row in rows:
        for source, count in row.get("sources", {row.get("source", "bot_said"): 1}).items():
            sources[source] += count
    result = dict(first, id="F" + storage.digest(form + ":" + ":".join(members))[:16],
                  form=form, members=members, count=len(rows), ops=sum(row.get("ops", 1) for row in rows),
                  flags=sorted({flag for row in rows for flag in row.get("flags", [])}),
                  rank=max(row["rank"] for row in rows), sources=dict(sources),
                  source=next(iter(sources)) if len(sources) == 1 else "mixed",
                  statuses={item: status for row in rows for item, status in
                            row.get("statuses", {row["id"]: row.get("status", "new")}).items()})
    if all("op_ids" in row for row in rows):
        result["op_ids"] = sorted({op for row in rows for op in row["op_ids"]})
        result["ops"] = len(result["op_ids"])
    result["rank"] = rank(result)
    return result


def unknown_shared(row):
    return row["bot"] == "unknown" and row.get("place", {}).get("scope") == "shared"


def folded_items(items, bot_order, seq=None):
    seq = seq or {}
    groups = defaultdict(list)
    shared = [row for row in items if unknown_shared(row)]
    for row in items:
        kind, target = row["kind"], row["target_key"]
        if kind == "conclusion":
            # New, deleted and private conclusions about the owner fold into one line per bot;
            # private ones contribute a count only.
            form, target = "conclusion", "conclusion"
        elif "private" in row["flags"]:
            form, target = "private", "private"
        elif len(shared) > 1 and unknown_shared(row):
            form, target = "shared_skills", "shared"
        else:
            form = kind
            if kind == "user":
                # Each changed user fact needs its own numbered verdict.
                target = row["id"]
        groups[(row["bot"], form, target)].append(row)
    folded, skills = [], defaultdict(list)
    for (bot, form, target), rows in groups.items():
        row = combine(rows, form)
        if form == "conclusion":
            earliest = sorted(rows, key=lambda item: (seq.get(item["id"], 0), item["id"]))
            public = [item for item in earliest if "private" not in item["flags"]]
            new = [item for item in public if "deleted" not in item["flags"]]
            deleted = [item for item in public if "deleted" in item["flags"]]
            row["counts"] = {"new": len(new), "deleted": len(deleted), "private": len(rows) - len(public)}
            # The card shows only phrases that pass content(); lead with the first one that does.
            shown_new = next((item["after"] for item in new if display.content(item.get("after", ""))), None)
            shown_deleted = next((item["before"] for item in deleted if display.content(item.get("before", ""))), None)
            row["lead"] = shown_new if shown_new is not None else shown_deleted or ""
            row["lead_deleted"] = shown_new is None and shown_deleted is not None
            row["bot_order"] = list(bot_order).index(bot) if bot in bot_order else len(bot_order)
            folded.append(row)
        elif form == "memory":
            row["net"] = {name: sum(item.get("change", "removed" if "deleted" in item["flags"] else "changed") == name
                                    for item in rows) for name in ("added", "changed", "removed")}
            row["ops"] = max((item.get("file_ops", 0) for item in rows), default=0) or row["ops"]
            row["usage"] = next((item["usage"] for item in rows if "usage" in item), {"before": None, "after": None})
            folded.append(row)
            folded.extend(combine([item], "deleted") for item in rows if "deleted" in item["flags"])
        elif form == "skill":
            row["target"] = ":".join(row["target"].split(":")[:2])
            row["net_size"] = sum(item.get("net_size", display.net_size(item.get("before", ""), item.get("after", "")))
                                  for item in rows)
            row["display_name"] = next((item["display_name"] for item in rows
                                        if item.get("display_name") not in (None, word("s0"))), word("s0"))
            skills[bot].append(row)
        else:
            folded.append(row)
    for rows in skills.values():
        if len(rows) > 3:
            folded.append(combine(rows, "skills"))
            folded.append(min(rows, key=lambda row: (-row["net_size"], row["id"])))
        else:
            folded.extend(rows)
    order = {bot: n for n, bot in enumerate(bot_order)}
    return sorted(folded, key=lambda row: (-row["rank"], order.get(row["bot"], len(order)), row["bot"], row["id"]))
