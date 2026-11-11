"""Reads of the record: check, list, show, verify-basis, json, lint, fields, plain."""

from __future__ import annotations

import json
import subprocess
import sys
import time

from hermes_cli.work import ledger
from hermes_cli.work.common import ago, die, load_json, mask, now, paths
from hermes_cli.work.evaluate import item_name, omo_next_late
from hermes_cli.work.items import KIND_IN, find_area, get, norm_project
from hermes_cli.work.kinds import JSON_TODO, TODO, kind_now
from hermes_cli.work.ledger import projects_of
from hermes_cli.work.links import link_label
from hermes_cli.work.locale import kind, label, status, t
from hermes_cli.work.people import agent, agent_name, agents, name, owner, owner_or_agent
from hermes_cli.work.sources import basis_line, basis_of, basis_profile, first_said, verify_basis
from hermes_cli.work.view import archive_due, computed, proj_name, row_view, tree
from hermes_cli.work.writing import (
    field_of, fields, fields_markdown, fields_prompt, fields_spec, fields_text, fields_version, guide_path,
    problem_notes, record_texts, shown, table, write_problems,
)


def cmd_check(a) -> None:
    from hermes_cli.work.state import check
    recs, _ = ledger.read_all()
    for i in a.ids or []:
        get(recs, i)
    check(a.ids or None, dry=a.dry, notify=a.notify, quiet=a.quiet)


# ---------------------------------------------------------------- list
def _area_line(r: dict, recs: dict, a, by) -> str | None:
    kids = [x for x in recs.values() if x.get("parent") == r["id"] and x.get("state") == "open"]
    if (by and not any(x["by"] == by for x in kids)) or (not kids and not a.all):
        return None
    goals, memos = sum(1 for x in kids if x["kind"] == "목표"), sum(1 for x in kids if x["kind"] == "메모")
    return (f"{r['id']:5} [{kind('영역')}] {r.get('title')} — " + t("열린 목표 {n}", n=goals)
            + (t(", 메모 {n}", n=memos) if memos else ""))


def _listed(r: dict, recs: dict, a, by, ar, pj) -> bool:
    top = r if r["kind"] in ("목표", "메모") else recs.get(r.get("parent"), {})
    if (ar and top.get("parent") != ar["id"]) or (pj and pj not in projects_of(top)) or (by and r["by"] != by):
        return False
    if not a.all and r.get("state") != "open" and not (
            r["kind"] == "목표" and any(x.get("parent") == r["id"] and x.get("state") == "open" for x in recs.values())):
        return False
    return not (a.kind and kind_now(r["kind"]) != KIND_IN.get(a.kind))


def _item_line(r: dict, recs: dict, st: dict, d: int) -> str:
    c = computed(r, st)
    tag = kind(r["kind"]) + (t("(주기)") if r["kind"] == "목표" and r.get("every") else "")
    ptag = f" [{', '.join(proj_name(x) for x in projects_of(r))}]" if projects_of(r) else ""
    arch = " · " + status("archive_due") if archive_due(r, recs) else ""
    return (f"{r['id']:5} {'  ' * d}{tag} {name(r['by'])} {status(c['status'])} · {item_name(r)}{ptag} — "
            f"{c.get('line', '')}{arch} ({ago(first_said(r))})")


def cmd_list(a) -> None:
    recs, order = ledger.read_all()
    st = load_json(paths()["state"], {})
    by = owner_or_agent(a.by) if a.by else None
    ar = find_area(recs, a.area) if a.area else None
    pj = norm_project(a.project) if a.project else ""
    rows = 0
    for d, r in tree(recs, order):
        if r["kind"] == "영역":
            line = None if (ar and r["id"] != ar["id"]) else _area_line(r, recs, a, by)
            if line:
                print(line)
            continue
        if _listed(r, recs, a, by, ar, pj):
            print(_item_line(r, recs, st, d))
            rows += 1
    if not rows:
        print(t("(열린 목표·할 일 없음)"))


# ---------------------------------------------------------------- show
def _show_head(r: dict) -> None:
    if r.get("by") == owner():
        print(t("{id} {kind} — 맡은 사람 {owner}, 요청한 에이전트 {who}", id=r["id"], kind=kind(r["kind"]),
                owner=name(owner()), who=agent_name(r.get("from"), t("없음")))
              + (t(", 기한 {due}", due=r["due"]) if r.get("due") else ""))
        return
    every = t("(주기, {every})", every=r["every"]) if r.get("every") else ""
    print(f"{r['id']} {kind(r['kind'])}{every}" + (t(" (약속)") if r.get("promise") else "")
          + " — " + t("맡은 에이전트 {who}", who=name(r["by"]))
          + (t(", 맡은 세션 {s}", s=r["session"]) if r.get("session") else "")
          + (t(", 마감일 {due}", due=r["due"]) if r.get("due") else ""))


def _show_fields(r: dict, recs: dict) -> None:
    for key, template in (("title", "이름: {v}"), ("why", "왜: {v}")):
        if r.get(key):
            print(t(template, v=r[key]))
    for b in basis_of(r):
        print(basis_line(b))
    for key, template in (("decide", "주인이 정할 것: {v}"), ("done", "끝 조건: {v}"), ("check", "검사: {v}")):
        if r.get(key):
            print(t(template, v=r[key]))
    if r.get("parent"):
        p = recs.get(r["parent"], {})
        pp = recs.get(p.get("parent"), {}) if p.get("kind") == "목표" else {}
        print(t("속한 곳: ") + (f"{item_name(pp)} › " if pp else "") + f"{r['parent']} {item_name(p)}")
    if projects_of(r):
        print(t("프로젝트(Desk): {names}", names=", ".join(projects_of(r)))
              + (t("  → 보관할 것(4_Archive)") if archive_due(r, recs) else ""))


def _show_state(r: dict, recs: dict, c: dict) -> None:
    print(t("상태: {state} — {line}", state=status(c["status"]), line=c.get("line", "")))
    for x in c.get("reasons", [])[1:]:
        print(f"  · {x}")
    if c.get("check"):
        ch = c["check"]
        print(t("마지막 검사: rc={rc} {ago} 전 — {out}", rc=ch.get("rc"), ago=ago(ch.get("at")),
                out=ch.get("out", "")[:300]))
    if c.get("check_line"):
        print(c["check_line"])
    if c.get("manager"):
        print(t("맡은 세션: {s}", s=c["manager"]))
    w = r.get("wait") or {}
    if w.get("hold"):
        print(t("보류: {on}", on=w.get("on") or ""))
    elif w.get("item"):
        print(t("기다림: {id} {name}이 끝나면 풀림", id=w["item"], name=item_name(recs.get(w["item"], {}))))
    elif w:
        print(t("기다림: {on} — {day}까지", on=w.get("on"), day=time.strftime("%Y-%m-%d", time.localtime(w.get("next", 0)))))
    if c.get("todos"):
        print(t("이슈:"))
        for td in c["todos"]:
            print(f"  - {td['label']}: {td['state']}  ({td['link']})")
    elif r.get("links"):
        print(t("이슈:"), ", ".join(r["links"]))


def _show_history(r: dict) -> None:
    for e in r.get("evidence") or []:
        print(t("증거: {ref} ({who}, {ago} 전)", ref=e["ref"], who=name(e.get("by")), ago=ago(e["ts"])))
    for n in (r.get("notes") or [])[-8:]:
        print(t("메모 {ago} 전 {who}: {text}", ago=ago(n["ts"]), who=name(n.get("by")), text=n["text"]))
    for cm in r.get("comments") or []:
        state = t("열림") if cm.get("status") == "open" else t("해결")
        print(t("댓글 {cid} ({state}) {ago} 전 {who}: {text}", cid=cm["cid"], state=state, ago=ago(cm["at"]),
                who=name(cm.get("by")), text=cm["text"]))
        for x in cm.get("replies") or []:
            print(f"  ↳ {name(x.get('by'))} [{x.get('result')}] " + t("{ago} 전: {text}", ago=ago(x.get("at")),
                                                                      text=x.get("text")))
    if r.get("state") == "done":
        print(t("끝난 근거: {by} — {ev}", by=r.get("done_by"), ev=r.get("done_evidence", "")[:300]))
    if r.get("state") == "dropped":
        print(t("내린 말: {q}", q=r.get("dropped_quote")))


def cmd_show(a) -> None:
    recs, _ = ledger.read_all()
    r = get(recs, a.id)
    st = load_json(paths()["state"], {})
    c = computed(r, st)
    _show_head(r)
    _show_fields(r, recs)
    _show_state(r, recs, c)
    _show_history(r)
    for k in [x for x in recs.values() if x.get("parent") == r["id"]
              and (x.get("state") == "open" or r["kind"] != "영역")]:
        kc = computed(k, st)
        print(f"  ⊃ {k['id']} {kind(k['kind'])} {item_name(k)} — {name(k['by'])}, {status(kc['status'])}: "
              f"{kc.get('line', '')}")


# ---------------------------------------------------------------- verify-basis, json
def cmd_verify_basis(a) -> None:
    recs, order = ledger.read_all()
    records = [get(recs, a.id)] if a.id else [recs[i] for i in order]
    verified: dict = {}  # identical lookups share one result within this run only; never persisted
    for r in records:
        if not a.id and not a.all and r.get("state") != "open":
            continue
        for b in basis_of(r):
            profile = basis_profile(r, b)
            key = (profile, json.dumps(b, sort_keys=True, ensure_ascii=False))
            result = verified.get(key)
            if result is None:
                result = verify_basis(b, profile)
                if result["verified"] != "unknown":
                    verified[key] = result
            print(r["id"] + " " + basis_line(result))


def first_writers() -> dict:
    w = {}
    for r in ledger.rows(ops=("add", "seed-writer")):
        if r.get("op") == "add" and r.get("id") not in w:
            w[r["id"]] = r.get("writer") or "?"
        if r.get("op") == "seed-writer":
            w[r["id"]] = r.get("writer")
    return w


def json_item(r: dict, recs: dict, st: dict, fw: dict, show_all: bool) -> dict:
    from hermes_cli.work.cmd_edit import sprint_card
    i, x = r["id"], dict(r)
    if (x.get("sprint") or {}).get("items"):
        x["sprint_card"] = sprint_card(x, recs)
    x["computed"] = computed(x, st)
    x["basis"] = basis_of(r)
    x["first_said"] = first_said(r)
    if x["kind"] == "목표":
        v = row_view(r, x["computed"], recs)
        v["stage_since"] = (st.get("stage_at") or {}).get(i) or x["first_said"]
        x["view"] = v
    if kind_now(x["kind"]) == TODO:
        x["kind"] = JSON_TODO
    x["by_ko"] = name(x["by"])
    if x["by"] == owner():  # only the owner's own to-dos carry these, so older json output stays the same
        x["owner"] = owner()
        x["from_ko"] = agent_name(x.get("from"), "")
    x["link_labels"] = [link_label(link) for link in x.get("links") or []]
    x["archive_due"] = archive_due(r, recs)
    x["projects"] = projects_of(r)
    x["project"] = x["projects"][0] if x["projects"] else ""
    x["added_by"] = fw.get(i, "?")
    if x.get("omo_next_at"):
        x["omo_next_late"] = omo_next_late(r)
    x["comments"] = x.get("comments") or []
    x["last_reply_at"] = max((y.get("at") or 0 for c in x["comments"] for y in c.get("replies") or []
                              if y.get("by") != owner()), default=None)
    if not show_all and x["by"] != owner():
        x.pop("notes", None)  # the board shows the asking agent's notes on the owner's own to-dos
    return x


def cmd_json(a) -> None:
    from hermes_cli.work.sessions import session_list
    recs, order = ledger.read_all()
    fw = first_writers()
    st = load_json(paths()["state"], {})
    items = []
    for i in order:
        r = recs[i]
        closed_at = int(r.get("done_at") or r.get("dropped_at") or r.get("ts") or 0)
        if r.get("state") != "open" and not a.all and now() - closed_at > 3 * 86400:
            continue
        items.append(json_item(r, recs, st, fw, a.all))
    out = {"fields": fields_version(), "now": now(), "checked_at": st.get("at"), "dv_ok": st.get("dv_ok"),
           "items": items, "sessions": session_list(recs), "version": 3, "bots": dict(agents()),
           "status_ko": {k: v for k, v in label("status").items() if not k.startswith("due_")}}
    json.dump(out, sys.stdout, ensure_ascii=False)
    print()


# ---------------------------------------------------------------- lint, fields, plain
def cmd_lint(a) -> None:
    recs, order = ledger.read_all()
    who = agent(a.by) if a.by else None
    rows = []
    for i in order:
        r = recs[i]
        if (a.open and r.get("state") != "open") or (who and r.get("by") != who):
            continue
        for fname, text in record_texts(r):
            probs = write_problems(fname, text)
            if probs:
                rows.append(f"{i} · {fname} · {', '.join(f'{lv} {rule}' for lv, rule, _ in probs)} · {mask(text)}")
    if not rows:
        print(t("규칙에 어긋난 글 없음"))
        return
    print(t("id · 칸 · 걸린 규칙 · 지금 글"))
    print("\n".join(rows))
    print(t("{n}곳 · 고치는 법: work fields <칸> · {guide}", n=len(rows), guide=guide_path()))


def cmd_fields(a) -> None:
    if a.markdown:
        sys.stdout.write(fields_markdown())
    elif a.json:
        json.dump(dict(fields_spec(), version=fields_version()), sys.stdout, ensure_ascii=False)
        print()
    elif a.prompt:
        print(fields_prompt(field_of(a.prompt)))
    elif a.check is not None:
        if not a.name:
            die(t("--check에는 칸 이름이 필요하다: work fields <칸> --check \"<글>\""))
        f = field_of(a.name)
        probs = write_problems(f["name"], a.check)
        for lv, note in problem_notes(f["name"], a.check):
            print(f"{lv} {note}")
        if not probs:
            print(t("통과"))
        raise SystemExit(1 if any(p[0] == table()["refuse"] for p in probs) else 0)
    elif a.name:
        print(fields_text(field_of(a.name)))
    else:
        print("\n\n".join(fields_text(f) for f in fields()))
        print("\n" + t("가이드: {guide} · 버전 {v}", guide=guide_path(), v=fields_version()))


def plain_answer(out: str) -> str:
    lines = [line.strip() for line in (out or "").splitlines() if line.strip()]
    return lines[0].strip("\"'“”‘’`").strip() if lines else ""


def _generate(cmd: list[str], prompt: str, timeout: float) -> tuple[str, str]:
    try:
        p = subprocess.run(cmd, input=prompt, capture_output=True, text=True, encoding="utf-8",
                           timeout=timeout, check=False)
        return plain_answer(p.stdout), (t("생성기 종료 코드 {rc}", rc=p.returncode) if p.returncode else "")
    except (OSError, subprocess.TimeoutExpired) as e:
        return "", t("생성기 실행 실패: {e}", e=e)


def cmd_plain(a) -> None:
    """Ask a generator (stdin: prompt, stdout: one line) for a field's text, rewrite once when refused, else drop it."""
    f = field_of(a.name)
    cmd = a.cmd[1:] if a.cmd[:1] == ["--"] else a.cmd
    if not cmd:
        die(t("생성기 명령이 필요하다: work plain <칸> [--context \"<맥락>\"] -- <명령…>"))
    context = a.context if a.context is not None else ("" if sys.stdin.isatty() else sys.stdin.read())
    prompt = fields_prompt(f) + ("\n\n" + t("맥락:") + f"\n{context.strip()}" if context.strip() else "")
    for attempt in (1, 2):
        text, failed = _generate(cmd, prompt, a.timeout)
        if failed or not text:
            rules = failed or t("빈 답")
        else:
            notes = problem_notes(f["name"], text)
            bad = [note for lv, note in notes if lv == table()["refuse"]]
            if not bad:
                for _, note in notes:
                    print("work: " + t("경고 — {name}: {note}", name=shown(f), note=note), file=sys.stderr)
                print(mask(text))
                return
            rules = "; ".join(bad)
        print("work: " + t("{n}번째 답 걸림 — {name}: {rules}", n=attempt, name=shown(f), rules=rules), file=sys.stderr)
        prompt += "\n\n" + t("앞서 쓴 글 \"{text}\"은 규칙에 걸렸다: {rules}. 규칙을 지켜 다시 쓴다.", text=mask(text),
                             rules=rules)
    print("work: " + t("버림 — {name} 칸은 비워 둔다", name=shown(f)), file=sys.stderr)
