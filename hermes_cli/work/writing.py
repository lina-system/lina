"""The writing rules a dashboard text is checked against before it is written (``writing_fields`` holds the tables)."""

from __future__ import annotations

import hashlib
import json
import re
import sys
from pathlib import Path

from hermes_cli.work.common import die, mask
from hermes_cli.work.env import current
from hermes_cli.work.locale import lang, t
from hermes_cli.work.writing_fields import TABLES

ENGLISH_WORD, ENGLISH_RUN = r"[A-Za-z][A-Za-z0-9]*", r"[A-Za-z][A-Za-z0-9]*(?:[ \t]+[A-Za-z][A-Za-z0-9]*){2,}"
# Words that look like a deadline in a title, why or note; `work` points at --due, never sets it.
DATE_WORD = {
    "ko": re.compile(r"\d{1,2}-\d{1,2}(?!\d)|\d{1,2}월\s*\d{1,2}일|\d{1,2}월\s*말|(?:다음|이번)\s*주|이번\s*달|월말"
                     r"|마감|까지|오늘|내일|모레|[월화수목금토일]요일"),
    "en": re.compile(r"(?i)\b\d{1,2}-\d{1,2}(?!\d)|\b(?:by|until|due|deadline|today|tomorrow|tonight|next week|"
                     r"this week|end of month|monday|tuesday|wednesday|thursday|friday|saturday|sunday)\b"),
}


def table() -> dict:
    return TABLES[lang()]


def guide_path() -> Path:
    return Path(__file__).resolve().parents[2] / "docs" / "work" / f"writing-guide.{lang()}.md"


def products() -> tuple[str, ...]:
    extra = (current().section("work").get("writing") or {}) if isinstance(
        current().section("work").get("writing"), dict) else {}
    return tuple(dict.fromkeys(list(table()["guide"]["products"]) + [str(p) for p in extra.get("products") or []]))


def english_ok() -> tuple[str, ...]:
    return tuple(dict.fromkeys([p for p in products() if p.isascii()] + list(table()["guide"]["english_ok"])))


def banned() -> tuple[tuple[str, str], ...]:
    """The refused patterns; device names come from ``devices.workers`` and ``work.writing.devices``."""
    devices = current().devices
    rules = list(table()["banned"])
    if devices:
        rules.insert(2, (table()["device_rule"], r"\b(?:" + "|".join(re.escape(d) for d in devices) + r")\b"))
    return tuple(rules)


def fields() -> tuple[dict, ...]:
    return table()["fields"]


def field_by(name: str) -> dict:
    return next(f for f in fields() if f["name"] == name)


def shown(f: dict) -> str:
    return f.get("label") or f["name"]


def field_of(name: str) -> dict:
    key = (name or "").replace(" ", "").lower()
    for f in fields():
        if key in (f["name"].replace(" ", ""), shown(f).replace(" ", "").lower()):
            return f
    die(t("없는 칸: {name} — 칸 이름: {names}", name=name, names=", ".join(shown(f) for f in fields())))


def _english_spans(text: str) -> list[tuple]:
    out = []
    run = re.search(ENGLISH_RUN, text)
    if run:
        out.append((table()["warn"], t("영어 단어 셋 이상 이어짐"), run.group(0), run.start(), run.end()))
    ok = {w.lower() for w in english_ok()}
    odd = [m for m in re.finditer(ENGLISH_WORD, text) if m.group(0).lower() not in ok]
    if odd:
        out.append((table()["warn"], t("허용 목록 밖 영어 단어"), ", ".join(dict.fromkeys(m.group(0) for m in odd)),
                    odd[0].start(), odd[0].end()))
    return out


def write_spans(name: str, text: str) -> list[tuple]:
    """[(level, rule, matched, start, end)] for one text of one field; a warn-only field downgrades every rule."""
    f, text = field_by(name), (text or "").strip()
    refuse, out = table()["refuse"], []
    if len(text) > f["max"]:
        out.append((refuse, t("길이 {n}자 넘음", n=f["max"]), t("{n}자", n=len(text)), f["max"], f["max"]))
    for rule, rx in banned() + tuple(f["banned"]):
        m = re.search(rx, text, re.ASCII)
        if m:
            out.append((refuse, rule, m.group(0).strip(), m.start(), m.end()))
    if table()["english"]:
        out += _english_spans(text)
    if f["check"] == table()["warn"]:
        out = [(table()["warn"],) + p[1:] for p in out]
    return out


def write_problems(name: str, text: str) -> list[tuple]:
    return [p[:3] for p in write_spans(name, text)]


def excerpt(text: str, start: int, end: int, span: int = 20) -> str:
    a, b = max(0, start - span), min(len(text), end + span)
    return ("…" if a else "") + text[a:b] + ("…" if b < len(text) else "")


def problem_notes(name: str, text: str) -> list[tuple]:
    """[(level, 'rule "hit" · …up to 20 chars either side…')] read from the masked text, so a diagnostic never shows a
    credential or the whole input. Decisions still come from write_problems."""
    from hermes_cli.work.locale import label
    shown_text = mask((text or "").strip())
    seen = {p[1]: p for p in write_spans(name, shown_text)}
    out = []
    too_long = t("길이 {n}자 넘음", n=field_by(name)["max"])
    for lv, rule, hit in write_problems(name, text):
        if rule == too_long:
            cut = min(field_by(name)["max"], len(shown_text))
            out.append((lv, f"{rule} {hit} · {excerpt(shown_text, cut, cut)}"))
        elif rule in seen:
            _, _, h, s, e = seen[rule]
            out.append((lv, f"{rule} \"{h}\" · {excerpt(shown_text, s, e)}"))
        else:
            masked = label("masked")
            s = max(shown_text.find(masked), 0)
            out.append((lv, f"{rule} \"{masked}\" · {excerpt(shown_text, s, s + 4)}"))
    return out


def field_row(f: dict) -> str:
    return f"| {shown(f)} | {t('{n}자', n=f['max'])} | {f['shape']} | {f['content']} |"


def field_brief(f: dict) -> str:
    return t("{name} — {where} · {required} · {max}자까지 · 걸리면 {check}", name=shown(f), where=f["where"],
             required=f["required"], max=f["max"], check=f["check"])


def guard_text(name: str, text: str) -> None:
    """Warn on stderr; refuse (exit 2, nothing written) when a refusing rule hits."""
    text = (text or "").strip()
    if not text:
        return
    f, notes = field_by(name), problem_notes(name, text)
    for _, note in [n for n in notes if n[0] == table()["warn"]]:
        print("work: " + t("경고 — {name}: {note} · 가이드 {guide}", name=shown(f), note=note, guide=guide_path()),
              file=sys.stderr)
    bad = [n for n in notes if n[0] == table()["refuse"]]
    if bad:
        die(t("글 쓰기 규칙에 걸려 쓰지 않았다 — {name}", name=shown(f)) + "\n"
            + "".join("  " + t("걸린 규칙: {note}", note=note) + "\n" for _, note in bad)
            + "  " + t("이 칸: {shape} · {max}자까지 · 들어갈 것: {content}", shape=f["shape"], max=f["max"],
                       content=f["content"]) + "\n"
            + "  " + t("고쳐 쓴 예: \"{bad}\" → \"{good}\"", bad=f["bad"][0], good=f["good"][0]) + "\n"
            + "  " + t("가이드: {guide} · work fields {name}", guide=guide_path(), name=shown(f)))


def missing_field(name: str, flag: str):
    f = field_by(name)
    die(t("새 {where}에는 {flag} \"<{name}>\" 이 필요하다", where=f["where"], flag=flag, name=shown(f))
        + "\n  " + t("가이드: {row}", row=field_row(f)) + "\n  " + t("예: \"{good}\"", good=f["good"][0])
        + "\n  " + t("가이드 문서: {guide} · work fields {name}", guide=guide_path(), name=shown(f)))


def record_texts(r: dict) -> list[tuple[str, str]]:
    """(field, text) pairs a record shows on the dashboard; notes and replies are history, not listed."""
    kind, out = r.get("kind"), []
    if kind in ("목표", "할 일"):
        out.append(("목표 이름" if kind == "목표" else "할 일 제목", r.get("title")))
        out.append(("완료 조건", r.get("done")))
    if kind == "할 일":
        out.append(("왜", r.get("why")))
    out.append(("결정 문장", r.get("decide")))
    out += [("고를 답", c) for c in r.get("decide_choices") or []]
    w = r.get("wait") or {}
    out.append(("멈춤 이유" if w.get("hold") else "대기 문장", w.get("on")))
    return [(n, s.strip()) for n, s in out if isinstance(s, str) and s.strip()]


def fields_spec() -> dict:
    tb = table()
    return {"fields": [{k: list(v) if isinstance(v, tuple) else v for k, v in f.items()} for f in fields()],
            "banned": [list(b) for b in banned()], "title_tag": list(tb["title_tag"]), "english_ok": list(english_ok()),
            "english_word": ENGLISH_WORD if tb["english"] else None, "english_run": ENGLISH_RUN if tb["english"] else None}


def fields_version() -> str:
    blob = json.dumps(fields_spec(), ensure_ascii=False, sort_keys=True).encode()
    return hashlib.sha256(blob).hexdigest()[:12]


def md_code(rx: str) -> str:
    return "`" + rx.replace("|", "\\|") + "`"


def _md_head() -> list[str]:
    g = table()["guide"]
    lines = [f"# {g['title']}", "", "<!-- " + t("`lina work fields --markdown > {path}`로 만든다. 손으로 고치지 말고 "
                                              "hermes_cli/work/writing_fields.py를 고친다.",
                                              path=f"docs/work/writing-guide.{lang()}.md") + " -->",
             "", g["lead"], "", "## " + t("공통"), "- " + t("쉬운 말로 쓴다. 한 칸에는 사실 하나만 쓴다."),
             "- **" + t("넣지 않는 것:") + "**"]
    lines += [f"  - {x}" for x in g["keep_out"]]
    lines += ["- " + t("제품·서비스 이름은 써도 된다. 예: {names}.", names=", ".join(products())),
              "- " + t("괄호로 덧붙이지 않는다. 덧붙일 게 있으면 다른 칸에 쓴다."), "",
              "## " + t("칸별 규칙"), "| " + t("칸 | 길이 | 모양 | 들어갈 것") + " |", "|---|---|---|---|"]
    return lines + [field_row(f) for f in fields()]


def _md_rules() -> list[str]:
    lines = ["", "## " + t("고쳐 쓴 예"), "| " + t("전 | 제목 | 왜") + " |", "|---|---|---|"]
    lines += [f"| {b} | {s} | {w} |" for b, s, w in table()["examples"]]
    lines += ["", "## " + t("검사"), t("`work`는 글을 쓰기 전에 칸마다 아래 규칙을 본다. 거절하면 아무것도 쓰지 않는다. "
                                    "`work lint`는 이미 쓴 글 가운데 어긋난 것을 보여 주고, `work fields`는 칸마다 설명과 "
                                    "예를 보여 준다."), "",
              "| " + t("거절 규칙 | 걸리는 글(정규식, 영문 단어 경계)") + " |", "|---|---|"]
    lines += [f"| {rule} | {md_code(rx)} |" for rule, rx in banned()]
    tag = table()["title_tag"]
    lines += [f"| {tag[0]} | {md_code(tag[1])} " + t("(목표 이름·할 일 제목)") + " |",
              "| " + t("길이 | 칸별 규칙 표의 길이를 넘는 글") + " |", ""]
    if table()["english"]:
        lines += ["경고만 하는 것: 영어 단어가 셋 이상 이어질 때, 허용 목록 밖 영어 단어가 있을 때. "
                  f"허용 목록: {', '.join(english_ok())}.", ""]
    lines += ["| " + t("칸 | 쓰는 곳 | 꼭 쓰나 | 걸리면 | 쓰는 명령") + " |", "|---|---|---|---|---|"]
    return lines + [f"| {shown(f)} | {f['where']} | {f['required']} | {f['check']} | {f['cli']} |" for f in fields()]


def fields_markdown() -> str:
    lines = _md_head() + _md_rules() + ["", "## " + t("칸마다 예")]
    for f in fields():
        lines += ["", f"### {shown(f)}", t("예 출처: {source}", source=f["source"]), ""]
        lines += ["- " + t("좋은 예: {text}", text=g) for g in f["good"]]
        lines += ["- " + t("나쁜 예: {text} — {rules}", text=b,
                           rules=", ".join(p[1] for p in write_problems(f["name"], b))) for b in f["bad"]]
    return "\n".join(lines) + "\n"


def fields_text(f: dict) -> str:
    rules = [r for r, _ in banned()] + [r for r, _ in f["banned"]] + [t("길이 {n}자 넘음", n=f["max"])]
    lines = [field_brief(f), "  " + t("모양: {v}", v=f["shape"]), "  " + t("들어갈 것: {v}", v=f["content"]),
             "  " + t("쓰는 명령: {v}", v=f["cli"]), "  " + t("금지: {v}", v=", ".join(rules))]
    lines += ["  " + t("좋은 예: {text}", text=g) for g in f["good"]]
    lines += ["  " + t("나쁜 예: {text}", text=b) for b in f["bad"]]
    return "\n".join(lines)


def fields_prompt(f: dict) -> str:
    rules = ", ".join([r for r, _ in banned()] + [r for r, _ in f["banned"]])
    lines = [t("대시보드의 '{name}' 칸에 들어갈 글을 한국어로 쓴다.", name=shown(f)),
             t("읽는 사람은 주인이다. 그 일의 맥락을 모르는 채로 요약 화면에서 처음 본다."),
             t("모양: {shape}. 들어갈 것: {content}. 길이: {max}자 이하.", shape=f["shape"], content=f["content"],
               max=f["max"]),
             t("넣지 않는 것: {rules}, 영어 코드 말. 내부 이름은 이렇게 바꾼다: 작업판→대시보드, LCM→기억 압축, omo→작업.",
               rules=rules)]
    if english_ok():
        lines.append(t("써도 되는 영어 이름: {names}.", names=", ".join(english_ok())))
    lines += [t("좋은 예: {text}", text=" / ".join(f"\"{g}\"" for g in f["good"])),
              t("글만 한 줄로 답한다. 따옴표나 설명을 붙이지 않는다.")]
    return "\n".join(lines)


def date_word(text: str) -> bool:
    return bool(DATE_WORD[lang()].search(text or ""))
