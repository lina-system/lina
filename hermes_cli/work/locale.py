"""Labels and messages of ``lina work`` per locale (``work.locale``: ko or en).

The record itself keeps its data words whatever the locale (kinds ``영역/목표/할 일/메모``, ``done_by`` and the
schema's state codes), so a store written under one locale reads the same under the other. Messages are written in
Korean in the code and looked up in ``locale_en.MESSAGES`` for en; a template missing there falls back to Korean.
"""

from __future__ import annotations

LABELS: dict[str, dict[str, object]] = {
    "ko": {
        "owner_word": "주인",
        "kind": {"영역": "영역", "목표": "목표", "할 일": "할 일", "메모": "메모"},
        "status": {"done": "끝남", "dropped": "내림", "ok": "정상", "bad": "이상", "check_left": "검사 남음",
                   "open": "열림", "working": "하는 중", "waiting": "기다림", "stalled": "멈춤", "unknown": "모름",
                   "memo": "메모", "archive_due": "보관할 것", "evidence_wait": "증거 확인 필요",
                   "due_over": "마감일 지남", "due_today": "오늘 마감"},
        "stage": ("정하는 중", "하는 중", "검사 남음", "끝"),
        "health": {"green": "순조", "yellow": "늦어짐 위험", "red": "멈춤", "grey": "소식 없음"},
        "kanban": {"running": "도는 중", "blocked": "막힘", "done": "끝남", "archived": "닫힘", "triage": "열림",
                   "todo": "열림", "scheduled": "열림", "ready": "열림", "review": "열림"},
        "basis": {"found": "원문 확인", "not_found": "원문 못 찾음", "time_mismatch": "시각 다름",
                  "unknown": "원문 확인 불가"},
        "codes": {"planned": "예정", "in_progress": "진행 중", "waiting": "대기", "stopped": "중단됨", "done": "완료",
                  "decision": "결정 필요", "feedback": "피드백", "review": "검토 필요", "hold": "보류",
                  "blocked_by_item": "진행 대기", "no_response": "응답 없음", "failed": "실패",
                  "result_missing": "결과 누락"},
        "masked": "[가림]",
        "ago": ("분", "시간", "일"),
    },
    "en": {
        "owner_word": "owner",
        "kind": {"영역": "area", "목표": "goal", "할 일": "to-do", "메모": "memo"},
        "status": {"done": "done", "dropped": "dropped", "ok": "ok", "bad": "failing", "check_left": "check left",
                   "open": "open", "working": "working", "waiting": "waiting", "stalled": "stalled",
                   "unknown": "unknown", "memo": "memo", "archive_due": "to archive",
                   "evidence_wait": "evidence to confirm", "due_over": "past due", "due_today": "due today"},
        "stage": ("deciding", "doing", "check left", "done"),
        "health": {"green": "on track", "yellow": "at risk", "red": "stalled", "grey": "no news"},
        "kanban": {"running": "running", "blocked": "blocked", "done": "done", "archived": "closed",
                   "triage": "open", "todo": "open", "scheduled": "open", "ready": "open", "review": "open"},
        "basis": {"found": "source found", "not_found": "source not found", "time_mismatch": "time differs",
                  "unknown": "source not checked"},
        "codes": {"planned": "planned", "in_progress": "in progress", "waiting": "waiting", "stopped": "stopped",
                  "done": "done", "decision": "decision needed", "feedback": "feedback", "review": "review needed",
                  "hold": "on hold", "blocked_by_item": "waiting on an item", "no_response": "no response",
                  "failed": "failed", "result_missing": "result missing"},
        "masked": "[masked]",
        "ago": ("m", "h", "d"),
    },
}
# Reply results are stored in Korean (the record's data words); en accepts its own words for them.
REPLY_RESULTS = ("반영함", "답변 필요", "반영 안 함")
REPLY_ALIASES = {"applied": "반영함", "needs-answer": "답변 필요", "wont-apply": "반영 안 함"}
STAGES = LABELS["ko"]["stage"]


def lang() -> str:
    from hermes_cli.work.env import current
    return current().locale


def label(name: str, locale: str | None = None):
    return LABELS[locale or lang()][name]


def word(table: str, key: str, default: str | None = None) -> str:
    return label(table).get(key, key if default is None else default)


def kind(k: str) -> str:
    return word("kind", k)


def status(code: str) -> str:
    return word("status", code, "")


def stage(ko_stage: str) -> str:
    return dict(zip(STAGES, label("stage"))).get(ko_stage, ko_stage)


def t(template: str, **kw) -> str:
    """The message *template* (Korean, ``str.format`` fields) in the current locale, filled with *kw*."""
    if lang() == "en":
        from hermes_cli.work.locale_en import MESSAGES
        template = MESSAGES.get(template, template)
    return template.format(**kw) if kw else template


def josa(name: str, pair: str) -> str:
    """*name* plus the Korean particle of *pair* (``이/가``, ``은/는``, ``을/를``, ``과/와``) its last sound takes;
    under en the bare name."""
    if lang() == "en":
        return name
    with_final, without = pair.split("/")
    last = name[-1:] if name else ""
    if "가" <= last <= "힣":
        return name + (with_final if (ord(last) - 0xAC00) % 28 else without)
    return name + without
