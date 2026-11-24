"""English and Korean command copy and protected-topic vocabulary."""

from __future__ import annotations

import re
import unicodedata
from typing import Final

from hermes_cli.work.env import current

# English word boundaries avoid matching terms like "fact" inside "factory".
# Korean protected terms can carry particles and appear in compounds.
_PROTECTED_EN: Final = re.compile(
    r"\b(?:report\w*|(?:dis)?honest\w*|truth\w*|accurac\w*|accurate\w*|permissions?"
    r"|approv\w*|authori[sz]\w*|verif\w*|evidence\w*|facts?|factual\w*|consent\w*"
    r"|check(?:s|ed|ing)?|instruct(?:ions?|ional|ed|ing)?|owner['’]s\s+(?:directions?|orders?)"
    r"|safe(?:ty|guards?)?|secrets?|credentials?|passwords?)\b"
)
_PROTECTED_KO: Final = re.compile(
    r"보고|정직|거짓|진실|정확|권한|승인|허가|검증|확인|사실|안전|지시|지침|비밀|암호"
)


def protected_rule(label: str) -> str | None:
    """Return the audit rule for a protected label in either language."""
    normalized = unicodedata.normalize("NFKC", label).casefold()
    if _PROTECTED_EN.search(normalized) or _PROTECTED_KO.search(normalized):
        return "protected-topic"
    return None

COPY: Final = {
    "conclusion": ("[관계일지] {event} | 반응 {feeling} {note} | 근거 {ids} | 강도 {strength}",
                   "[Journal] {event} | reaction {feeling} {note} | evidence {ids} | strength {strength}"),
    "invalid_line": ("{field}는 비어 있지 않은 한 줄, 최대 {limit}자이며 '|'를 넣을 수 없다",
                     "{field} must be one nonempty line, at most {limit} characters, without '|'"),
    "feeling": ("--feeling은 공백 없는 한 단어다", "--feeling must be one word without whitespace"),
    "ids": ("--messages에 쉼표로 나눈 메시지 uid가 필요하다", "--messages requires comma-separated message uids"),
    "missing_ids": ("이 봇의 대화에 없는 uid: {ids}", "Not a message of this agent: {ids}"),
    "forgotten_ids": ("잊은 메시지는 인용할 수 없다: {ids}", "Forgotten messages cannot be cited: {ids}"),
    "foreign_speaker": ("다른 발화자의 메시지는 인용할 수 없다: {ids}",
                        "Cannot cite messages attributed to another speaker: {ids}"),
    "pref": ("--pref-target, --pref-kind, --pref-dir, --pref-route는 함께 준다",
             "--pref-target, --pref-kind, --pref-dir and --pref-route go together"),
    "owner_required": ("인용한 줄에 {owner} 메시지가 없다", "Citations contain no message from {owner}"),
    "database": ("state.db를 읽지 못했다: {error}", "Cannot read state.db: {error}"),
    "store": ("메모리 저장소를 읽지 못했다: {error}", "Cannot read the memory store: {error}"),
    "import_files": ("파일: 복사 {copied}, 같음 {same}, 건너뜀 {skipped}",
                     "Files: copied {copied}, unchanged {same}, skipped {skipped}"),
    "import_skipped": ("  건너뜀 {path}: {reason}", "  skipped {path}: {reason}"),
    "import_journal": ("일지: {lines}줄 읽음, {written}줄 씀, {present}줄 이미 있음, {unreadable}줄 못 읽음",
                       "Journal: {lines} lines read, {written} rows written, {present} already present, "
                       "{unreadable} unreadable"),
    "import_dry": ("(--dry-run: 아무것도 쓰지 않았다. 숫자는 쓸 양이다)", "(--dry-run: nothing written; counts are what would be)"),
    "import_ledger": ("장부: {records}건 읽음, {written}건 씀, {present}건 이미 있음, 오류 {errors}건",
                      "Ledger: {records} records read, {written} written, {present} already present, errors: {errors}"),
    "import_unmapped": ("  {line}행: 옮긴 LCM에 없는 근거 {ids}", "  line {line}: evidence not in the moved LCM rows {ids}"),
    "import_unreadable": ("  {line}행을 못 읽는다: {reason}", "  line {line} unreadable: {reason}"),
    "date": ("날짜는 YYYY-MM-DD다: {value}", "Date must be YYYY-MM-DD: {value}"),
    "future_review": ("미래 날짜 {value}는 검토할 수 없다 (오늘 {today})",
                      "Cannot review a future date {value} (today is {today})"),
    "time": ("시각을 못 읽는다: {value} (ISO 또는 7d)", "Cannot parse time: {value} (ISO or 7d)"),
    "limit": ("--limit은 1 이상이다", "--limit must be at least 1"),
    "candidates": ("## {owner} 대화 {day} ({count}줄)", "## {owner} conversations {day} ({count} lines)"),
    "reply": ("답", "Reply"), "no_reply": ("답 없음", "No reply"),
    "no_candidates": ("(그날 {owner} 대화 없음)", "(No conversations from {owner} that day)"),
    "journal": ("## 관계일지 {day} ({count}줄)", "## Journal {day} ({count} lines)"),
    "no_journal": ("(그날 일지 없음)", "(No journal that day)"),
    "soul": ("## 지금 SOUL.self.md ({count}자)", "## Current SOUL.self.md ({count} characters)"),
    "no_soul": ("(SOUL.self.md 없음)", "(No SOUL.self.md)"),
    "changes": ("## 최근 변경 {count}건", "## Recent changes: {count}"),
    "removal": ("지울 후보: ", "Removal candidates: "),
    "refusal": ("거절: {rule} — {detail}", "Refused: {rule} - {detail}"),
    "length": ("{count}자다. 1500자를 {overflow}자 넘었다", "{count} characters; exceeds 1500 by {overflow}"),
    "machine-prefix": ("기계가 넣은 접두어가 든 줄은 넣을 수 없다", "Machine-prefixed lines are forbidden"),
    "line-length": ("200자를 넘는 줄이 있다 ({count}자)", "A line exceeds 200 characters ({count})"),
    "line-count": ("{count}줄이 바뀌었다. 한 번에 2줄까지다", "{count} lines changed; at most 2 per apply"),
    "protected-topic": ("보고·정직·권한·검증·지시·안전·비밀에 관한 줄은 바꿀 수 없다",
                        "Reporting, honesty, permissions, verification, instructions, safety and secrecy lines cannot change"),
    "evidence-suffix": ("바뀐 줄에 (YYYY-MM-DD, 근거 <journal ts>)가 없다",
                         "Changed line needs (YYYY-MM-DD, evidence <journal ts>)"),
    "evidence-ts": ("일지에 없거나 미래 시각인 근거다: {value}", "Unknown or future journal timestamp: {value}"),
    "applied": ("적용: {added}줄 더하고 {removed}줄 지웠다 (백업 {backup})",
                "Applied: {added} added, {removed} removed (backup {backup})"),
    "unchanged": ("적용: 바뀐 줄 없음", "Applied: no changed lines"),
    "no_backup": ("되돌릴 백업이 없다", "No backup to restore"),
    "undo": ("되돌림: {backup}", "Restored: {backup}"),
    "forget_owner_required": ("취향을 잊는 요청은 소유자만 할 수 있어요.", "Only the owner can ask me to forget a taste."),
    "forget_target_required": ("어느 에이전트의 어떤 취향을 잊을지 알려 주세요.", "Which agent and taste should I forget?"),
    "forget_done": ("그 취향은 잊었어요.", "I’ve forgotten that taste."),
    "weekly": ("마음 주간: {path}", "Heart weekly: {path}"),
    "weekly_title": ("# 봇 마음 주간 {day}", "# Agent heart weekly {day}"),
    "weekly_intro": ("{start} ~ {end} 사이 봇 {count}의 관계일지와 자체 서술 변경입니다. 일지 사건 문장은 싣지 않았습니다.",
                     "{start} ~ {end}: journal counts and self-note changes for {count} agents. Journal text is omitted."),
    "inactive": ("- 아직 안 켬.", "- Not enabled yet."),
    "count": ("- 관계일지 {count}줄", "- Journal: {count} lines"),
    "paths": ("(대화 중 {turn}, 회고 {review})", " (turn {turn}, review {review})"),
    "applies": (". 변경 {count}건", ". Changes: {count}"),
    "zero": (", 바뀐 줄 없음 {count}건", ", unchanged {count}"),
    "refused_count": (", 거절 {count}건", ", refused {count}"),
    "undo_count": (", 되돌림 {count}건", ", undone {count}"),
    "removed": ("- 뺀 줄: {line}", "- Removed: {line}"),
    "added": ("- 더한 줄: {line}", "- Added: {line}"),
    "rules": ("- 거절한 규칙: {rules}.", "- Refused rules: {rules}."),
    "headings": ("## 효과 있었던 것\n\n## 안 된 것\n\n## LINA에 옮길 것",
                 "## What worked\n\n## What did not work\n\n## Bring into LINA"),
    "no_stats": ("해석 통계 없음.", "No insight statistics."),
    "accuracy": ("정확도 {value}%", "Accuracy {value}%"),
    "no_accuracy": ("정확도 아직 없음", "No accuracy yet"),
    "stats": ("해석 {total}건 중 열림 {opened}, 닫힘 {closed}. 답 {answered}건 — 맞다 {correct}, 틀리다 {incorrect}, 모르겠다 {unknown}. {accuracy}.",
              "Insights {total}: open {opened}, closed {closed}. Answers {answered}: correct {correct}, incorrect {incorrect}, unknown {unknown}. {accuracy}."),
}


def text(key: str, **values: str | int) -> str:
    return COPY[key][0 if current().locale == "ko" else 1].format(**values)
