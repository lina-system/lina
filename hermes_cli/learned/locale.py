"""Learning card words and message-source markers in Korean and English."""
from hermes_cli.work.env import current

WORDS = {
  "s0": {
    "ko": "스킬 하나",
    "en": "one skill"
  },
  "s1": {
    "ko": "비공개 변경",
    "en": "Private change"
  },
  "s2": {
    "ko": "봇",
    "en": "agent"
  },
  "s3": {
    "ko": "하나",
    "en": "one"
  },
  "s4": {
    "ko": "공용 스킬 ",
    "en": "Shared skill "
  },
  "s5": {
    "ko": " 스킬 ",
    "en": " skill "
  },
  "s6": {
    "ko": " 행동 규칙",
    "en": " behavior rules"
  },
  "s7": {
    "ko": " 규칙 문서",
    "en": " rule document"
  },
  "s8": {
    "ko": "바뀜",
    "en": "Changed"
  },
  "s9": {
    "ko": "봇 말",
    "en": "Reported"
  },
  "s10": {
    "ko": "바뀜 {}건·봇 말 {}건",
    "en": "Changed {} · reported {}"
  },
  "s11": {
    "ko": "비공개 {}건",
    "en": "Private {}"
  },
  "s12": {
    "ko": "기억 {}번 고쳐 씀, 추가 {}·고침 {}·삭제 {} (용량 {}%→{}%)",
    "en": "Memory: {} edits, +{} / ~{} / -{} ({}%→{}%)"
  },
  "s13": {
    "ko": "스킬 {}개 ({}번 고침)",
    "en": "{} skills ({} edits)"
  },
  "s14": {
    "ko": " · {}번 고침",
    "en": " · {} edits"
  },
  "s15": {
    "ko": "기억 삭제 · ",
    "en": "Memory removed · "
  },
  "s16": {
    "ko": "항목 하나",
    "en": "one entry"
  },
  "s17": {
    "ko": "공용 스킬 {}개 바뀜",
    "en": "{} shared skills changed"
  },
  "s18": {
    "ko": "새로 안 것",
    "en": "Conclusions"
  },
  "s19": {
    "ko": "지움",
    "en": "Removed"
  },
  "s20": {
    "ko": "비공개",
    "en": "Private"
  },
  "s21": {
    "ko": "(지움)",
    "en": "(removed)"
  },
  "s22": {
    "ko": "사용자 사실",
    "en": "User facts"
  },
  "s23": {
    "ko": "규칙 문서",
    "en": "Rule document"
  },
  "s24": {
    "ko": "봇 행동 규칙",
    "en": "Agent behavior rules"
  },
  "s25": {
    "ko": "장부 기록",
    "en": "Work record"
  },
  "s26": {
    "ko": "{}건 확인",
    "en": "{} changes"
  },
  "s27": {
    "ko": "어제 고침·지움 {}건 반영됨 / {}건 아직",
    "en": "Yesterday: {} applied / {} pending"
  },
  "s28": {
    "ko": "오늘 봇들이 바꾼 것 {}건",
    "en": "Agents changed {} items today"
  },
  "s29": {
    "ko": ", 봇 말 {}건",
    "en": ", {} reported"
  },
  "s30": {
    "ko": ", 걸리는 건 없음",
    "en": ", nothing flagged"
  },
  "s31": {
    "ko": "배운 것 ",
    "en": "Learned "
  },
  "s32": {
    "ko": "답: 다 ㅇㅋ · 번호 고침: 내용 · 번호 지움",
    "en": "Reply: all ok · N fix: text · N drop"
  },
  "s33": {
    "ko": "같은 실수 다시",
    "en": "Repeated mistake"
  },
  "s34": {
    "ko": "근거 없음",
    "en": "No basis"
  },
  "s35": {
    "ko": "시각 다름",
    "en": "Time mismatch"
  },
  "s36": {
    "ko": "스스로 닫음",
    "en": "Self-closed"
  },
  "s37": {
    "ko": "삭제",
    "en": "Deleted"
  },
  "s38": {
    "ko": "내용 잃음",
    "en": "Content lost"
  },
  "s39": {
    "ko": "누가 고쳤는지 모름",
    "en": "Unknown writer"
  },
  "s40": {
    "ko": "스킬",
    "en": "Skills"
  },
  "s41": {
    "ko": "규칙",
    "en": "Rules"
  },
  "s42": {
    "ko": "기억",
    "en": "Memory"
  },
  "s43": {
    "ko": "장부",
    "en": "Work"
  },
  "s44": {
    "ko": "뜻과 숫자를 유지해 한국어 요약 배열만 반환",
    "en": "Return only an English summary array, preserving meaning and numbers"
  },
  "s45": {
    "ko": "learned: 답할 카드 없음",
    "en": "learned: no sent card to answer"
  },
  "s46": {
    "ko": "learned: 알아듣지 못함, 기록 안 함 (배운 것 {})",
    "en": "learned: unparsed; nothing recorded (Learned {})"
  },
  "s47": {
    "ko": "learned: fix에는 --text가 필요함",
    "en": "learned: fix requires --text"
  },
  "s48": {
    "ko": " (봇 해석)",
    "en": " (agent interpreted)"
  },
  "s49": {
    "ko": "learned: 보낸 카드에 없는 항목",
    "en": "learned: item absent from sent cards"
  },
  "s50": {
    "ko": "learned: 날짜는 YYYY-MM-DD",
    "en": "learned: day must be YYYY-MM-DD"
  },
  "s51": {
    "ko": "learned: 보낸 카드 {}에 {}번 줄 없음, 기록 안 함",
    "en": "learned: no line {} on sent card {}; nothing recorded"
  },
  "s52": {
    "ko": "learned: 다른 줄의 고침·지움과 겹침, 기록 안 함",
    "en": "learned: conflicting line decisions; nothing recorded"
  },
  "s53": {
    "ko": "learned: 다른 줄의 고침·지움 유지, 기록 안 함",
    "en": "learned: keeping other line decisions; nothing recorded"
  },
  "s54": {
    "ko": " (봇 해석{})",
    "en": " (agent interpreted{})"
  },
  "s55": {
    "ko": ", {}건은 다른 줄 판정 유지",
    "en": ", {} items retain other line decisions"
  },
  "s56": {
    "ko": "배운 것 {}: {}",
    "en": "Learned {}: {}"
  },
  "s57": {
    "ko": "고침",
    "en": "Fix"
  }
}
WORDS["conclusion_count"] = {"ko": "{} {}개", "en": "{} {}"}


def word(key: str) -> str:
    return WORDS[key][current().locale]


def basis_prefixes(kind: str) -> tuple[str, ...]:
    return {"cron": ("[cron", "[Cron", "[크론", "[Scheduled"),
            "checker": ("[장부 검사기", "[checker"),
            "bot": ("Message from 🤖", "[Grok 수집", "[collector")}[kind]
