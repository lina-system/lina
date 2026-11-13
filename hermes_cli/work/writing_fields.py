"""The dashboard's writing rules per locale: one row per field, the refused patterns and the rewritten examples.
Checks, ``work fields``, refusals, ``docs/work/writing-guide.<locale>.md`` and LLM prompts all come from these
tables. Field names are the Korean ones in the code (``guard_text("왜", ...)``); ``label`` is what en shows."""

from __future__ import annotations

KO_GUIDE = {
    "title": "대시보드에 보이는 글 쓰는 법 (에이전트 공통)",
    "lead": ("**읽는 사람은 주인이다.** 주인은 그 일의 맥락을 모르는 채로 요약 화면에서 처음 본다고 생각하고 쓴다. "
             "일하면서 쓰던 말(파일, 명령, 기기, 이슈 번호, 내부 도구 이름)을 그대로 옮기지 않는다. "
             "업무 도구(Linear, Jira, 노션 한국어판)의 할 일처럼 쓴다."),
    "keep_out": ("이슈·PR 번호(`#19`)", "작업 번호(`OT-…`, `T…`, `G…`)", "기기 이름(`devices.workers`의 이름)",
                 "명령과 옵션(`lina worker send`, `--omo`)", "파일·경로·커밋", "영어 코드 말(`breaker`, `verdict`)",
                 "내부 도구 이름: `장부`는 쓰지 않는다. `작업판`은 `대시보드`로, `LCM`은 `기억 압축`으로, "
                 "`omo`는 `작업`으로 쓴다."),
    "products": ("GitHub", "Claude", "OpenAI", "Gemini", "Slack", "Telegram", "NAS"),
    "english_ok": ("Codex", "Grok", "Cursor", "Antigravity", "magpie", "Synology", "iPhone", "Mac"),
}
# (rule, regex). re.ASCII: a Korean particle right after a name (ctl에서, T12를) still ends the word.
KO_BANNED = (
    ("이슈·PR 번호", r"#\d+"),
    ("작업 번호", r"\b(?:OT|OG|OS)-[A-Z0-9]+|\b[TG]\d{2,}\b"),
    ("명령과 옵션", r"\blina (?:worker|work)\b| --[a-z]"),
    ("경로", r"/Users/|/home/|~/"),
    ("내부 이름", r"장부|작업판|\bomo\b|\bLCM\b"),
    ("괄호", r"[()]"),
)
KO_DEVICE_RULE = "기기 이름"
KO_TITLE_TAG = ("앞머리 꼬리표", r"^[^\s:]+:\s")
# The rewritten titles of the guide: (before, title, why).
KO_EXAMPLES = (
    ("주문 관문 #19: breaker 재확인·결과 비밀값 거르기", "주문 관문 안전장치 점검", "실주문을 켜도 승인 없이 주문이 나가지 않게"),
    ("LCM 수정 받아 전체 배포·전후 측정", "기억 압축 수정 배포와 효과 측정", "대화가 길어져도 답이 느려지지 않게"),
    ("장부: 댓글·답글·완료 표시", "대시보드 댓글과 완료 표시", "대시보드에서 바로 의견을 남기고 끝을 표시하게"),
    ("lina worker send: 질문 대기 중 답 넣기", "질문하며 멈춘 작업에 답 넣기", "작업이 사람 답을 기다리며 멈춰 있지 않게"),
    ("OT-ABC 시험 작업 알림 확인", "작업 완료 알림 점검", "작업이 끝나면 맡은 에이전트에게 빠짐없이 알려지게"),
    ("대시보드: 목표 함께 완료 묻기 상자 다듬기", "목표 완료 확인 창 다듬기", "열린 할 일이 남은 목표를 실수로 닫지 않게"),
)
_E = KO_EXAMPLES


def _f(name, where, required, max_, check, shape, content, cli, good, bad, source, banned=()):
    return {"name": name, "where": where, "required": required, "max": max_, "check": check, "shape": shape,
            "content": content, "banned": banned, "cli": cli, "good": good, "bad": bad, "source": source}


KO_FIELDS = (
    _f("목표 이름", "목표", "꼭", 25, "거절", "결과를 말하는 명사구", "주인이 바라는 결과. 주인의 말에서 가져온다",
       "work add 목표 --title · work edit --title", (_E[2][1], _E[1][1]), (_E[2][0], _E[1][0]), "가이드",
       (KO_TITLE_TAG,)),
    _f("완료 조건", "목표·할 일", "꼭", 60, "경고", "확인할 수 있는 상태 한 문장", "무엇이 보이거나 되면 끝인지",
       "work add --done · work edit --done",
       ("실주문을 켜도 승인 없는 주문이 하나도 나가지 않는다", "대시보드에서 댓글을 달면 맡은 에이전트에게 알림이 간다"),
       ("breaker verdict 재확인 완료", "#19 닫힘"), "지어냄"),
    _f("할 일 제목", "할 일", "꼭", 25, "거절", "대상 + 하는 일(…추가, …수정, …정리, …점검, …조사, …배포)",
       "이 할 일이 바꾸는 것", "work add 할 일 --title · work edit --title", (_E[0][1], _E[4][1]),
       (_E[0][0], _E[4][0]), "가이드", (KO_TITLE_TAG,)),
    _f("왜", "할 일", "꼭", 40, "거절", "한 문장, `~게` 또는 `~려고`로 끝냄",
       "이게 끝나면 주인에게 무엇이 좋아지나. 목표를 모르는 사람이 읽어도 알 수 있게",
       "work add 할 일 --why · work edit --why", (_E[0][2], _E[4][2]),
       ("실주문 전에 breaker 재확인하려고 (#19)", "omo 알림이 제대로 오게"), "가이드(좋은 예) · 지어냄(나쁜 예)"),
    _f("진행 메모", "메모", "선택", 60, "경고", "`~했다. 다음은 ~.`", "한 일과 다음 할 일", "work note · work add --note",
       ("안전장치 시험을 돌렸다. 다음은 실주문 설정 점검.", "알림 누락 두 건을 고쳤다. 다음은 하루 지켜보기."),
       ("lina worker send 돌림", "PR #19 머지함 (CI green)"), "지어냄"),
    _f("결정 문장", "결정", "선택", 70, "거절", "질문은 `~할까?`, 요청은 `~해 줘`", "무엇을 정하거나 해야 하나",
       "work wait --decide · work edit --decide",
       ("실주문 안전장치 기준을 지금대로 둘까?", "증권 앱에서 주문 승인을 한 번 눌러 줘"),
       ("breaker 기준 (3회) 유지?", "OT-ABC1101 결과 보고 --omo 켤까?"), "지어냄"),
    _f("진행 대기", "결정", "선택", 40, "거절", "`정해지면 ~`", "정해지면 무엇이 움직이나", "대시보드가 쓴다 · work plain",
       ("정해지면 실주문 설정을 바꾼다", "정해지면 남은 할 일을 닫는다"),
       ("정해지면 ~/bin에서 재시작 (수동)", "정해지면 #21 진행"), "지어냄"),
    _f("고를 답", "결정", "선택", 15, "거절", "2~4개, 추천 하나", "질문일 때만", "work wait --choice",
       ("지금대로 두기", "다음 주로 미루기"), ("유지 (추천)", "OT-ABC 보고 나서 정하기"), "지어냄"),
    _f("대기 문장", "대기", "기다릴 때 꼭", 50, "거절", "`~를 기다림`", "무엇을, 누구를, 언제까지", "work wait --on --next",
       ("빌드 기기 재부팅을 오늘 밤까지 기다림", "인증 메일을 금요일까지 기다림"),
       ("lina worker 재부팅 기다림", "~/Desk 동기화 기다림"), "지어냄"),
    _f("멈춤 이유", "대기", "선택", 50, "거절", "이유 + 풀리는 조건", "누가 무엇을 하면 풀리나", "work wait --hold --on",
       ("주문 한도가 없어 멈춤. 주인이 한도를 정하면 풀림", "NAS가 꺼져 멈춤. NAS가 켜지면 풀림"),
       ("/home 디스크 다운", "LCM 오류 (원인 모름)"), "지어냄"),
    _f("답글", "답글", "선택", 60, "경고", "한 일 + 확인한 것", "결과 칩(반영함·반영 안 함·답변 필요)과 맞게", "work reply",
       ("알림 문구를 고쳤다. 시험 알림이 에이전트에게 간 것을 봤다.", "말씀대로 미뤘다. 다음 주 월요일에 다시 묻겠다."),
       ("fixed in PR #19", "작업판 수정 (abc123 커밋)"), "지어냄"),
    _f("마감일", "목표·할 일", "선택", 10, "거절", "YYYY-MM-DD", "언제까지 끝내야 하나. 주인이나 요청에서 나온 날짜만",
       "work add --due · work edit --due", ("2026-11-05", "2026-11-30"), ("11-05", "다음 주 금요일"), "지어냄",
       (("날짜 모양", r"^(?!\d{4}-\d{2}-\d{2}$).+"),)),
)

EN_GUIDE = {
    "title": "How to write what the dashboard shows (every agent)",
    "lead": ("**The reader is the owner.** Write as if they see it for the first time on a summary screen, without "
             "the context of the work. Do not carry over working words (files, commands, devices, issue numbers, "
             "internal tool names). Write like a task in a work tracker (Linear, Jira)."),
    "keep_out": ("issue and PR numbers (`#19`)", "task ids (`OT-…`, `T…`, `G…`)", "device names (`devices.workers`)",
                 "commands and options (`lina worker send`, `--omo`)", "files, paths, commits",
                 "internal tool names: say `dashboard`, `memory compression`, `job`"),
    "products": ("GitHub", "Claude", "OpenAI", "Gemini", "Slack", "Telegram", "NAS"),
    "english_ok": (),
}
EN_BANNED = (
    ("issue or PR number", r"#\d+"),
    ("task id", r"\b(?:OT|OG|OS)-[A-Z0-9]+|\b[TG]\d{2,}\b"),
    ("command or option", r"\blina (?:worker|work)\b| --[a-z]"),
    ("path", r"/Users/|/home/|~/"),
    ("internal name", r"\bledger\b|\bomo\b|\bLCM\b"),
    ("parentheses", r"[()]"),
)
EN_DEVICE_RULE = "device name"
EN_TITLE_TAG = ("leading tag", r"^[^\s:]+:\s")
EN_EXAMPLES = (
    ("Order gate #19: recheck breaker, filter secrets", "Check the order gate safeguards",
     "so no order goes out without approval"),
    ("Take the LCM fix, deploy to all, measure", "Ship the memory compression fix and measure it",
     "so long chats do not slow answers down"),
    ("ledger: comments, replies, done mark", "Dashboard comments and done marks",
     "so feedback and done marks happen right on the dashboard"),
    ("lina worker send: answer while it waits", "Answer jobs that stop on a question",
     "so a job never sits waiting for a person"),
    ("OT-ABC test job notice check", "Check job end notices", "so the agent always hears when a job ends"),
    ("dashboard: polish the close-goal box", "Polish the goal close dialog",
     "so a goal with open to-dos is not closed by mistake"),
)
_N = EN_EXAMPLES


def _label(fields, labels):
    return tuple(dict(f, label=lab) for f, lab in zip(fields, labels))


EN_FIELDS = _label((
    _f("목표 이름", "goal", "required", 50, "refuse", "a noun phrase naming the result",
       "the result the owner wants, taken from their words", "work add goal --title · work edit --title",
       (_N[2][1], _N[1][1]), (_N[2][0], _N[1][0]), "guide", (EN_TITLE_TAG,)),
    _f("완료 조건", "goal, to-do", "required", 100, "warn", "one sentence about a state you can check",
       "what is seen or works when it is done", "work add --done · work edit --done",
       ("No order goes out without approval, even with live orders on", "A dashboard comment reaches the agent"),
       ("breaker verdict recheck done", "#19 closed"), "made up"),
    _f("할 일 제목", "to-do", "required", 50, "refuse", "object + action (add, fix, tidy, check, research, ship)",
       "what this to-do changes", "work add to-do --title · work edit --title", (_N[0][1], _N[4][1]),
       (_N[0][0], _N[4][0]), "guide", (EN_TITLE_TAG,)),
    _f("왜", "to-do", "required", 70, "refuse", "one sentence starting with `so`",
       "what gets better for the owner when it is done, readable without the goal",
       "work add to-do --why · work edit --why", (_N[0][2], _N[4][2]),
       ("to recheck the breaker before live orders (#19)", "so omo notices arrive"), "guide · made up"),
    _f("진행 메모", "note", "optional", 100, "warn", "`Did …. Next: ….`", "what was done and what comes next",
       "work note · work add --note",
       ("Ran the safeguard test. Next: check the live order settings.", "Fixed two missed notices. Next: watch a day."),
       ("ran lina worker send", "merged PR #19 (CI green)"), "made up"),
    _f("결정 문장", "decision", "optional", 110, "refuse", "a question `…?` or a request `please …`",
       "what has to be decided or done", "work wait --decide · work edit --decide",
       ("Keep the order safeguard limits as they are?", "Please approve the order once in the broker app"),
       ("keep breaker limit (3)?", "turn on --omo after OT-ABC1101?"), "made up"),
    _f("진행 대기", "decision", "optional", 70, "refuse", "`once decided, …`", "what moves once it is decided",
       "the dashboard writes it · work plain",
       ("Once decided, the live order settings change", "Once decided, the remaining to-dos close"),
       ("once decided, restart in ~/bin (manual)", "once decided, go on with #21"), "made up"),
    _f("고를 답", "decision", "optional", 30, "refuse", "2 to 4 answers, one recommended", "for questions only",
       "work wait --choice", ("Keep as is", "Postpone a week"), ("keep (recommended)", "decide after OT-ABC"),
       "made up"),
    _f("대기 문장", "wait", "required when waiting", 80, "refuse", "`Waiting for …`", "what, whom, until when",
       "work wait --on --next", ("Waiting for the build machine reboot tonight", "Waiting for the sign-up mail by Friday"),
       ("waiting for lina worker reboot", "waiting for ~/Desk sync"), "made up"),
    _f("멈춤 이유", "wait", "optional", 80, "refuse", "reason + what releases it", "who does what to release it",
       "work wait --hold --on", ("Stopped: no order limit. Released when the owner sets one",
                                 "Stopped: the NAS is off. Released when it is back"),
       ("/home disk down", "LCM error (cause unknown)"), "made up"),
    _f("답글", "reply", "optional", 100, "warn", "what was done + what was checked",
       "matches the result chip (applied, won't apply, needs answer)", "work reply",
       ("Fixed the notice text. Saw the test notice reach the agent.", "Postponed as asked. Will ask again Monday."),
       ("fixed in PR #19", "ledger fix (commit abc123)"), "made up"),
    _f("마감일", "goal, to-do", "optional", 10, "refuse", "YYYY-MM-DD",
       "when it must be done; only a date from the owner or the request", "work add --due · work edit --due",
       ("2026-11-05", "2026-11-30"), ("11-05", "next Friday"), "made up",
       (("date shape", r"^(?!\d{4}-\d{2}-\d{2}$).+"),)),
), ("goal title", "done condition", "to-do title", "why", "progress note", "decision", "on decision",
    "answer choice", "wait", "hold reason", "reply", "due date"))

TABLES = {
    "ko": {"guide": KO_GUIDE, "banned": KO_BANNED, "device_rule": KO_DEVICE_RULE, "title_tag": KO_TITLE_TAG,
           "examples": KO_EXAMPLES, "fields": KO_FIELDS, "refuse": "거절", "warn": "경고", "english": True},
    "en": {"guide": EN_GUIDE, "banned": EN_BANNED, "device_rule": EN_DEVICE_RULE, "title_tag": EN_TITLE_TAG,
           "examples": EN_EXAMPLES, "fields": EN_FIELDS, "refuse": "refuse", "warn": "warn", "english": False},
}
