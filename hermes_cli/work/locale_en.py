"""English messages of ``lina work``, keyed by the Korean template in the code (``locale.t``)."""

MESSAGES = {
    "  → 보관할 것(4_Archive)":
        "  → To archive (4_Archive)",
    "  넘기지 못한 요청 {n}건 (다음 lina work 호출 때 다시)":
        "  {n} requests not handed off (will retry on next lina work call)",
    " (마감일 {due})":
        " (due date {due})",
    " (약속)":
        " (promise)",
    " (열린 할 일 {n}개 함께: {ids})":
        " (with {n} open to-dos: {ids})",
    " (함께 닫은 할 일 {n}개도)":
        " (also closed {n} to-dos with it)",
    " · 기록에 없는 할 일 {n}개는 뺌":
        " · excluded {n} to-dos not in record",
    "(목표 이름·할 일 제목)":
        "(goal name · to-do title)",
    "(없음)":
        "(none)",
    "(열린 목표·할 일 없음)":
        "(no open goals or to-dos)",
    "(주기)":
        "(recurring)",
    "(주기, {every})":
        "(recurring, {every})",
    ", 같은 프로젝트의 열린 목표 {ids}":
        ", open goals in the same project {ids}",
    ", 기한 {due}":
        ", deadline {due}",
    ", 마감일 {due}":
        ", due date {due}",
    ", 맡은 세션 {s}":
        ", assigned session {s}",
    ", 메모 {n}":
        ", memo {n}",
    "--by <에이전트> 가 필요하다 ({names} 또는 프로필 이름)":
        "--by <agent> is required ({names} or profile name)",
    "--check에는 칸 이름이 필요하다: work fields <칸> --check \"<글>\"":
        "--check needs a field name: work fields <field> --check \"<text>\"",
    "--choice·--recommend는 --decide \"<문장>\"과 함께 쓴다":
        "Use --choice/--recommend together with --decide \"<sentence>\"",
    "--due는 YYYY-MM-DD다: {v} · work fields 마감일":
        "--due must be YYYY-MM-DD: {v} · work fields due date",
    "--due는 목표·할 일에만 쓴다":
        "--due is only for goals and to-dos",
    "--from은 work.db 파일이다":
        "--from must be a work.db file",
    "--goal <목표 id> 또는 --todo <할 일 id>… 를 준다":
        "Provide --goal <goal id> or --todo <to-do id>…",
    "--goal은 목표 id다":
        "--goal must be a goal id",
    "--hold와 --next는 함께 쓸 수 없다":
        "--hold and --next cannot be used together",
    "--omo 는 작업 id(예: OT-ABC1031071257) 또는 next(곧 작업이 붙음)여야 한다: {v}":
        "--omo must be a job id (e.g. OT-ABC1031071257) or next (a job will be attached soon): {v}",
    "--on \"<누구·무엇>\" --next <날짜> 가 필요하다":
        "--on \"<who/what>\" --next <date> is required",
    "--on-item: 에이전트의 할 일은 주인 할 일을, 주인 할 일은 열린 할 일을 기다린다":
        "--on-item: an agent's to-do waits on an owner to-do; an owner to-do waits on an open to-do",
    "--on-item은 다른 기다림 옵션과 함께 쓰지 않는다":
        "--on-item cannot be combined with other wait options",
    "--on-item은 다른 열린 할 일 id다: {v}":
        "--on-item must be another open to-do id: {v}",
    "--quote \"<주인이 끝났다고 한 말>\" 또는 --by <에이전트> --seen \"<직접 확인한 것>\"":
        "--quote \"<owner's words saying it's done>\" or --by <agent> --seen \"<what you checked yourself>\"",
    "--quote \"<주인이 취소한 말 그대로>\" 가 필요하다. 에이전트가 스스로 내리지 않는다":
        "--quote \"<owner's exact cancellation words>\" is required. Agents do not cancel on their own",
    "--quote는 비울 수 없다. 주인이 한 말 그대로를 넣는다":
        "--quote cannot be empty. Enter the owner's exact words",
    "--quote를 바꿀 때는 --source \"<주인 말의 출처: 날짜·채널·메시지>\"가 필요하다":
        "Changing --quote requires --source \"<source of owner's words: date/channel/message>\"",
    "--recommend는 --choice 가운데 하나다":
        "--recommend must be one of the --choice options",
    "--seen \"<직접 확인한 것>\"이 필요하다({n}자 이상). 무엇을 확인했는지 남긴다":
        "--seen \"<what you checked yourself>\" is required (at least {n} chars). Record what you checked",
    "--title \"<정규화한 제목>\" 이 필요하다: 결과가 보이는 짧은 명사구, 25자 안팎. 주인의 말은 --basis로":
        "--title \"<normalized title>\" is required: a short noun phrase showing the result, about 25 chars. Put the owner's words in --basis",
    "--via board는 에이전트에게 맡기는 메모에만 쓴다":
        "--via board is only for memos assigned to an agent",
    "--why는 할 일에만 쓴다":
        "--why is only for to-dos",
    "Desk 프로젝트 폴더가 붙은 목표만 보관한다":
        "Archives only goals that have a Desk project folder attached",
    "Desk에 {dst}가 없다. 옮긴 뒤 다시":
        "{dst} is not in Desk. Move it, then retry",
    "Notion 페이지":
        "Notion page",
    "PR 열림":
        "PR open",
    "Todoist 할 일":
        "Todoist to-do",
    "[대시보드] {who} 댓글 {id} {cid}: {text} — {hint}":
        "[dashboard] {who} comment {id} {cid}: {text} — {hint}",
    "[대시보드] 맡은 항목 {n}건 — 다음 할 일":
        "[dashboard] {n} assigned items — next to-dos",
    "[대시보드] 정리 대상 목표 — 할 일을 붙이거나(work add 할 일 --goal <id>) 주인에게 물어 내린다":
        "[dashboard] Goals to clean up — attach a to-do (work add to-do --goal <id>) or ask the owner and drop them",
    "[대시보드] 주인이 '{done}' 완료를 되돌렸다.":
        "[dashboard] Owner reverted completion of '{done}'.",
    "[대시보드] 주인이 '{done}' 완료를 되돌렸다. {name}는 다시 기다린다":
        "[dashboard] Owner reverted completion of '{done}'. {name} is waiting again",
    "[대시보드] 주인이 '{done}'을 마쳤다.":
        "[dashboard] Owner finished '{done}'.",
    "[대시보드] 주인이 '{done}'을 마쳤다. {next} 이어서 하면 된다":
        "[dashboard] Owner finished '{done}'. {next} can continue",
    "[대시보드] 주인이 {id} {cid} 결정에 답함 — {hint}":
        "[dashboard] Owner answered decision {id} {cid} — {hint}",
    "[대시보드] 주인이 {id} 완료 표시를 되돌림{also}.":
        "[dashboard] Owner reverted done mark on {id}{also}.",
    "[대시보드] 주인이 {id}를 완료로 표시함{also}. 이 일로 도는 작업은 멈춰라.":
        "[dashboard] Owner marked {id} done{also}. Stop any jobs running for it.",
    "[대시보드] 주인이 맡김 {id}: {title}":
        "[dashboard] Owner assigned {id}: {title}",
    "`lina work fields --markdown > {path}`로 만든다. 손으로 고치지 말고 hermes_cli/work/writing_fields.py를 고친다.":
        "Generated with `lina work fields --markdown > {path}`. Don't edit by hand; edit hermes_cli/work/writing_fields.py instead.",
    "`work`는 글을 쓰기 전에 칸마다 아래 규칙을 본다. 거절하면 아무것도 쓰지 않는다. `work lint`는 이미 쓴 글 가운데 어긋난 것을 보여 주고, `work fields`는 칸마다 설명과 예를 보여 준다.":
        "`work` checks each field against the rules below before writing. If rejected, nothing is written. `work lint` shows existing text that breaks the rules, and `work fields` shows a description and example for each field.",
    "id · 칸 · 걸린 규칙 · 지금 글":
        "id · field · rule hit · current text",
    "ok — 검사기가 증거를 확인하면 끝남이 된다. 기계로 못 보는 증거면: {cmd}":
        "ok — becomes done once the checker confirms the evidence. If the evidence can't be machine-checked: {cmd}",
    "undo에는 비지 않은 --why가 필요하다":
        "undo requires a non-empty --why",
    "{agent}에게 알림 못 보냄 ({kind})":
        "Could not notify {agent} ({kind})",
    "{ago} 전: {text}":
        "{ago} ago: {text}",
    "{ago}째 방법·범위 안 정함":
        "Approach/scope undecided for {ago}",
    "{card}: 목표 {id}":
        "{card}: goal {id}",
    "{card}: 할 일 {ids}":
        "{card}: to-dos {ids}",
    "{id} {cid}는 이미 해결된 댓글이다":
        "{id} {cid} is an already resolved comment",
    "{id} {kind} — 맡은 사람 {owner}, 요청한 에이전트 {who}":
        "{id} {kind} — assigned to {owner}, requested by agent {who}",
    "{id} 끝남":
        "{id} done",
    "{id} 끝남(주인)":
        "{id} done (owner)",
    "{id} 다시 열림":
        "{id} reopened",
    "{id}: {rev}판으로 되돌림":
        "{id}: reverted to revision {rev}",
    "{id}: 이슈 {n}개":
        "{id}: {n} issues",
    "{id}는 {kind}이 아니다":
        "{id} is not a {kind}",
    "{id}는 내린 항목이다":
        "{id} is a dropped item",
    "{id}는 열린 메모가 아니다":
        "{id} is not an open memo",
    "{id}는 열린 할 일이 아니다":
        "{id} is not an open to-do",
    "{id}는 열린 항목이 아니다":
        "{id} is not an open item",
    "{id}는 이미 {state} 상태다":
        "{id} is already {state}",
    "{id}는 주인 할 일이다. 주인이 끝났다고 한 말로 닫는다: --quote \"<주인이 한 말>\"":
        "{id} is an owner to-do. Close it with the owner's statement that it's done: --quote \"<owner's words>\"",
    "{id}에 답한 에이전트를 --by로 준다":
        "Pass the agent that answered {id} with --by",
    "{id}에 없는 댓글: {cid}":
        "Comment not found on {id}: {cid}",
    "{id}에 없는 프로젝트: {names}":
        "Project not found on {id}: {names}",
    "{id}에 열린 할 일이 남아 있어 완료로 표시하지 않았다: {kids} — 함께 닫으려면 --with-open":
        "{id} still has open to-dos, so it was not marked done: {kids} — use --with-open to close them too",
    "{id}에 이미 주인이 정할 것이 있다: {decide} — 그 결정이 닫힌 뒤 다시":
        "{id} already has something for the owner to decide: {decide} — try again after that decision is closed",
    "{id}에 주인이 정할 것이 없다":
        "{id} has nothing for the owner to decide",
    "{id}의 마지막 기록이 주인의 완료 표시가 아니다":
        "The last record on {id} is not the owner's done mark",
    "{id}의 완료 표시 전 기록을 못 찾았다":
        "Could not find the record before the done mark on {id}",
    "{kinds}에는 증거·기다림을 붙이지 않는다. 목표나 할 일에 붙인다":
        "Evidence and waits are not attached to {kinds}. Attach them to a goal or to-do",
    "{kind}의 상위는 {want}이어야 한다":
        "The parent of {kind} must be {want}",
    "{label}에 금지된 문자나 항목 번호가 있다":
        "{label} contains forbidden characters or item numbers",
    "{label}의 길이나 단어 수가 맞지 않는다":
        "{label} has the wrong length or word count",
    "{name} — {where} · {required} · {max}자까지 · 걸리면 {check}":
        "{name} — {where} · {required} · up to {max} chars · if it fails: {check}",
    "{name}({id}): {reason} — 다음: {cmd}":
        "{name}({id}): {reason} — next: {cmd}",
    "{n}곳 · 고치는 법: work fields <칸> · {guide}":
        "{n} places · how to fix: work fields <field> · {guide}",
    "{n}번째 답 걸림 — {name}: {rules}":
        "Answer #{n} rejected — {name}: {rules}",
    "{n}시간째 진척 없음":
        "No progress for {n} hours",
    "{n}자":
        "{n} chars",
    "{repo} 마일스톤 {n}":
        "{repo} milestone {n}",
    "{run}: 작업에 아직 못 넘김, 다음 호출 때 다시 — {why}":
        "{run}: not yet handed to the job, will retry on next call — {why}",
    "{run}: 작업이 받지 않았다 — {why}":
        "{run}: the job did not accept it — {why}",
    "{run}: 할 일 {ids}을 넘긴다":
        "{run}: handing over to-dos {ids}",
    "{s}초 시간 초과":
        "Timed out after {s}s",
    "{where}주인이 완료로 표시함 ({when})":
        "{where}Owner marked done ({when})",
    "{who} 세션 {name}":
        "{who} session {name}",
    "{who} 직접 확인: {seen}":
        "{who} checked directly: {seen}",
    "{who} 확인: {seen}":
        "{who} checked: {seen}",
    "{who}, 맡은 봇":
        "{who}, assigned agent",
    "가이드 문서: {guide} · work fields {name}":
        "Guide doc: {guide} · work fields {name}",
    "가이드: {guide} · work fields {name}":
        "Guide: {guide} · work fields {name}",
    "가이드: {guide} · 버전 {v}":
        "Guide: {guide} · version {v}",
    "가이드: {row}":
        "Guide: {row}",
    "가져온 기록이 원본과 다르다. 아무것도 바꾸지 않았다":
        "The imported records differ from the source. Nothing was changed",
    "가져옴: 새 줄 {added}개 · 붙을 항목이 없어 건너뛴 줄 {skipped}개 · 작업-할 일 {pairs}개 · 저장소 {backend}":
        "Imported: {added} new lines · {skipped} lines skipped (no item to attach to) · {pairs} job-to-do pairs · store {backend}",
    "거절 규칙 | 걸리는 글(정규식, 영문 단어 경계)":
        "Rejection rule | Matching text (regex, English word boundaries)",
    "걸린 규칙: {note}":
        "Rule hit: {note}",
    "검사":
        "Check",
    "검사 거절: {why}":
        "Check rejected: {why}",
    "검사 결과 없음":
        "No check results",
    "검사 남음":
        "Checks remaining",
    "검사 못 함: ":
        "Check not run: ",
    "검사 못 함: {out}":
        "Check not run: {out}",
    "검사 미통과: ":
        "Check failed: ",
    "검사 실패":
        "Check failed",
    "검사 없음":
        "No check",
    "검사 통과":
        "Check passed",
    "결과 {path}":
        "Result {path}",
    "경고 — {name}: {note}":
        "Warning — {name}: {note}",
    "경고 — {name}: {note} · 가이드 {guide}":
        "Warning — {name}: {note} · guide {guide}",
    "고쳐 쓴 예":
        "Rewritten example",
    "고쳐 쓴 예: \"{bad}\" → \"{good}\"":
        "Rewritten example: \"{bad}\" → \"{good}\"",
    "공통":
        "Common",
    "괄호로 덧붙이지 않는다. 덧붙일 게 있으면 다른 칸에 쓴다.":
        "Don't add extras in parentheses. Put anything extra in another field.",
    "규칙에 어긋난 글 없음":
        "No text breaks the rules",
    "그 뒤에 항목이 바뀌었다":
        "The item changed after that",
    "근거: {text}":
        "Basis: {text}",
    "글 쓰기 규칙에 걸려 쓰지 않았다 — {name}":
        "Not written because it broke a writing rule — {name}",
    "글만 한 줄로 답한다. 따옴표나 설명을 붙이지 않는다.":
        "Reply with the text only, on one line. No quotes or explanations.",
    "금지: {v}":
        "Not allowed: {v}",
    "기다림 기한 지남: {on}":
        "Waiting past deadline: {on}",
    "기다림: {id} {name}이 끝나면 풀림":
        "Waiting: clears when {id} {name} is done",
    "기다림: {on} ({day}까지)":
        "Waiting: {on} (until {day})",
    "기다림: {on} ({day}까지) · 마지막 검사 {line}":
        "Waiting: {on} (until {day}) · last check {line}",
    "기다림: {on} — {day}까지":
        "Waiting: {on} — until {day}",
    "기다림: 주인 할 일":
        "Waiting: owner to-do",
    "기다림: 주인 할 일 {name}":
        "Waiting: owner to-do {name}",
    "기록: {run} · {runner} · {status}":
        "Record: {run} · {runner} · {status}",
    "기록에 없는 작업: {run}":
        "Job not in records: {run}",
    "기록을 옮기는 중이다. 끝날 때까지 기다린다":
        "Records are being moved. Waiting until it finishes",
    "기록을 옮기는 중이라 {s}초 기다렸지만 안 풀렸다 ({path})":
        "Waited {s}s for records to finish moving, but they're still locked ({path})",
    "기록의 목표: {id} {name}":
        "Goal in record: {id} {name}",
    "기록의 할 일: {ids}":
        "To-dos in record: {ids}",
    "길이 {n}자 넘음":
        "Over {n} characters",
    "길이 | 칸별 규칙 표의 길이를 넘는 글":
        "Length | text longer than the limit in the per-field rules table",
    "끊겼던 작업 넘기기 {n}건을 마저 했다":
        "Finished {n} interrupted job handoff(s)",
    "끊김":
        "Interrupted",
    "끊김({detail}) {ago} 전":
        "Interrupted ({detail}) {ago} ago",
    "끝난 근거: {by} — {ev}":
        "Basis for done: {by} — {ev}",
    "끝남":
        "Done",
    "끝남 {ago} 전":
        "Done {ago} ago",
    "끝남({by})":
        "Done ({by})",
    "끝남({by}) {ago} 전":
        "Done ({by}) {ago} ago",
    "끝남으로 바뀜:":
        "Marked done:",
    "나쁜 예: {text}":
        "Bad example: {text}",
    "나쁜 예: {text} — {rules}":
        "Bad example: {text} — {rules}",
    "날짜 형식: YYYY-MM-DD, MM-DD, +3d, +6h ({s})":
        "Date format: YYYY-MM-DD, MM-DD, +3d, +6h ({s})",
    "날짜가 보인다. 마감일이면: work edit {id} --due YYYY-MM-DD":
        "Looks like a date. If it's a due date: work edit {id} --due YYYY-MM-DD",
    "내린 말: {q}":
        "Reason for dropping: {q}",
    "내림":
        "Dropped",
    "내보냄: {n}줄 -> {dst}":
        "Exported: {n} lines -> {dst}",
    "넘길 할 일이 없다: 할 일 id나 --card <작업 카드>(그 카드에 붙은 할 일)를 준다":
        "No to-do to hand off: give a to-do id or --card <job card> (the to-dos attached to that card)",
    "넣지 않는 것:":
        "Leave out:",
    "넣지 않는 것: {rules}, 영어 코드 말. 내부 이름은 이렇게 바꾼다: 작업판→대시보드, LCM→기억 압축, omo→작업.":
        "Leave out: {rules}, English code terms. Rename internal names like this: 작업판→dashboard, LCM→memory compression, omo→job.",
    "다름":
        "Different",
    "다시 엶":
        "Reopened",
    "다시 엶: {why}":
        "Reopened: {why}",
    "다음: {cmd}":
        "Next: {cmd}",
    "닫힘":
        "Closed",
    "대기 중":
        "Pending",
    "대시보드에서 ":
        "From the dashboard ",
    "대시보드의 '{name}' 칸에 들어갈 글을 한국어로 쓴다.":
        "Write the text for the dashboard's '{name}' field in Korean.",
    "댓글 {cid} ({state}) {ago} 전 {who}: {text}":
        "Comment {cid} ({state}) {ago} ago {who}: {text}",
    "댓글 글이 비었다":
        "Comment text is empty",
    "댓글은 {goal}·{todo}에만 단다":
        "Comments can only be added to a {goal} or {todo}",
    "도는 중":
        "running",
    "도는 중 {ago}":
        "running for {ago}",
    "들어갈 것: {v}":
        "Contents: {v}",
    "링크 형식을 모름: {link} ({forms})":
        "Unknown link format: {link} ({forms})",
    "마감일 지남: {name}({id}) {due}":
        "Past due date: {name}({id}) {due}",
    "마지막 검사 {ago} 전 ({state}) — 검사기 확인 필요":
        "Last check {ago} ago ({state}) — the checker needs a look",
    "마지막 검사: rc={rc} {ago} 전 — {out}":
        "Last check: rc={rc} {ago} ago — {out}",
    "막힌 작업: {items}":
        "Blocked jobs: {items}",
    "맞는 이전 판이 없다":
        "No matching earlier version",
    "맡은 세션 {state}":
        "Assigned session {state}",
    "맡은 세션: {s}":
        "Assigned session: {s}",
    "맡은 세션이 끝났는데 검사가 안 통과":
        "Assigned session ended, but the check didn't pass",
    "맡은 에이전트 {who}":
        "Assigned agent {who}",
    "맡은 에이전트 {who}: Desk '{src}' → '{dst}' 로 옮기고 `work archive {id} --moved`":
        "Assigned agent {who}: move Desk '{src}' → '{dst}', then run `work archive {id} --moved`",
    "맡은 에이전트나 주인만 되돌린다":
        "Only the assigned agent or the owner can revert this",
    "맥락:":
        "Context:",
    "메모 {ago} 전 {who}: {text}":
        "Memo {ago} ago {who}: {text}",
    "메모 정리 안 됨 {ago}":
        "Memo not sorted out for {ago}",
    "메모를 목표로 올리거나 주인의 말로 버려야 한다":
        "The memo must be promoted to a goal or discarded on the owner's word",
    "메모에는 --basis \"<주인이 한 말>\" 이 필요하다":
        "A memo needs --basis \"<what the owner said>\"",
    "모르는 에이전트: {v}":
        "Unknown agent: {v}",
    "모름":
        "unknown",
    "모름(GitHub 못 읽음)":
        "unknown (couldn't read GitHub)",
    "모양: {shape}. 들어갈 것: {content}. 길이: {max}자 이하.":
        "Shape: {shape}. Contents: {content}. Length: {max} characters max.",
    "모양: {v}":
        "Shape: {v}",
    "목표 {id} {name}":
        "Goal {id} {name}",
    "목표 {id}로 올림":
        "Promoted to goal {id}",
    "목표에는 --area <영역> 이 필요하다":
        "A goal needs --area <area>",
    "목표에는 --basis \"<주인이 한 말>\" 이 필요하다(여러 번 가능, --said로 시각·채널. 내 약속이면 --promise와 약속 문장)":
        "A goal needs --basis \"<what the owner said>\" (repeatable; use --said for time and channel; for your own promise, use --promise with the promise text)",
    "목표에는 --check \"<읽기 전용 검사 한 줄 | evidence:무엇>\" 이 필요하다":
        "A goal needs --check \"<one-line read-only check | evidence:what>\"",
    "목표에는 --done \"<끝 조건>\" 이 필요하다(주기 목표는 --every 1d 등)":
        "A goal needs --done \"<done condition>\" (for recurring goals, use --every 1d etc.)",
    "무엇을 확인했나":
        "What was checked",
    "바로 다시 검사 못 함({kind}) — 다음 검사 때 바뀐다":
        "Can't re-check right away ({kind}) — it will update at the next check",
    "반영 안 함에는 이유 글이 있어야 한다":
        "Won't apply needs a reason",
    "버림 — {name} 칸은 비워 둔다":
        "Discarded — the {name} field is left empty",
    "보류: {on}":
        "On hold: {on}",
    "브리프 파일이 없다: {path}":
        "Brief file not found: {path}",
    "빈 답":
        "Empty answer",
    "상태: {state} — {line}":
        "State: {state} — {line}",
    "새 {where}에는 {flag} \"<{name}>\" 이 필요하다":
        "A new {where} needs {flag} \"<{name}>\"",
    "생성기 명령이 필요하다: work plain <칸> [--context \"<맥락>\"] -- <명령…>":
        "A generator command is required: work plain <field> [--context \"<context>\"] -- <command…>",
    "생성기 실행 실패: {e}":
        "Generator failed to run: {e}",
    "생성기 종료 코드 {rc}":
        "Generator exit code {rc}",
    "세션 {name}":
        "Session {name}",
    "세션 기록 없음":
        "No session record",
    "소식 없음 {ago}":
        "No updates for {ago}",
    "속한 곳: ":
        "Belongs to: ",
    "쉬운 말로 쓴다. 한 칸에는 사실 하나만 쓴다.":
        "Use plain language. One fact per field.",
    "스프린트":
        "Sprint",
    "실행 실패: {e}":
        "Run failed: {e}",
    "실행 이력 없음":
        "No run history",
    "써도 되는 영어 이름: {names}.":
        "English names you may use: {names}.",
    "쓰기·변경 명령이 섞여 있다(검사는 읽기만)":
        "Contains write or modify commands (checks must be read-only)",
    "쓰는 명령: {v}":
        "Command used: {v}",
    "아무도 안 붙은 이슈: {items}":
        "Unclaimed issues: {items}",
    "아무도 안 붙은 이슈: {items} (맡은 세션도 안 돎)":
        "Unclaimed issues: {items} (no assigned session running either)",
    "아무도 안 붙음":
        "No one assigned",
    "아직 검사 안 함":
        "Not checked yet",
    "아직 보관할 때가 아니다: 상태 {state}":
        "Not ready to archive yet: state {state}",
    "안 끝남":
        "Not done",
    "안내 — 주인이 할 일이면 주인 할 일로 만들고(work add 할 일 --by {owner} --from <나>) work wait {id} --on-item <주인 할 일 id>로 기다리면 주인이 끝낼 때 저절로 풀린다":
        "Tip — if this is for the owner, make it an owner to-do (work add 할 일 --by {owner} --from <me>) and wait with work wait {id} --on-item <owner to-do id>. It clears on its own when the owner finishes.",
    "앞서 쓴 글 \"{text}\"은 규칙에 걸렸다: {rules}. 규칙을 지켜 다시 쓴다.":
        "The earlier text \"{text}\" broke these rules: {rules}. Rewrite it so it follows them.",
    "앞선 실행이 끝났다. 남은 것을 이어서 하고 report.md를 다시 채운다.":
        "The previous run ended. Finish what remains and fill in report.md again.",
    "없는 칸: {name} — 칸 이름: {names}":
        "Unknown field: {name} — field names: {names}",
    "없는 항목: {id}":
        "No such item: {id}",
    "없음":
        "None",
    "열린 목표 {n}":
        "{n} open goals",
    "열림":
        "Open",
    "열어 둠, 주인 결정 기다림":
        "Left open, waiting for owner's decision",
    "영어 단어 셋 이상 이어짐":
        "Three or more English words in a row",
    "영역에는 --title \"<영역 경로>\" 가 필요하다(예: Dev/빌드 기기)":
        "An area needs --title \"<area path>\" (e.g. Dev/빌드 기기)",
    "영역은 {tops} 또는 한 단계 아래(예: Dev/LINA)까지다: {v}":
        "Areas must be {tops} or one level below (e.g. Dev/LINA): {v}",
    "예 출처: {source}":
        "Example source: {source}",
    "예: \"{good}\"":
        "e.g. \"{good}\"",
    "오늘 마감: {name}({id}) {due}":
        "Due today: {name}({id}) {due}",
    "옮긴 검사가 원본과 다르다. 아무것도 바꾸지 않았다":
        "The moved checks don't match the original. Nothing was changed",
    "옮김: {dst} (건너뛴 줄 {n}개)":
        "Moved: {dst} ({n} lines skipped)",
    "완료 표시 뒤 10분이 지나 되돌리지 않았다 ({ago} 전). 다시 열려면 work reopen":
        "Not undone: more than 10 minutes have passed since it was marked done ({ago} ago). To reopen it, use work reopen",
    "완료 표시는 {goal}·{todo}에만 한다":
        "Only a {goal} or {todo} can be marked done",
    "요청: {who}":
        "Requested by: {who}",
    "원본이 없다: {path}":
        "Source not found: {path}",
    "이 칸: {shape} · {max}자까지 · 들어갈 것: {content}":
        "This field: {shape} · up to {max} chars · contents: {content}",
    "이미 열려 있다":
        "Already open",
    "이미 있는 영역: {id}":
        "Area already exists: {id}",
    "이상: {out}":
        "Problem: {out}",
    "이슈 {closed}/{total} 닫힘":
        "{closed}/{total} issues closed",
    "이슈:":
        "Issues:",
    "일치":
        "Match",
    "읽는 사람은 주인이다. 그 일의 맥락을 모르는 채로 요약 화면에서 처음 본다.":
        "The reader is the owner. They see this first on the summary screen, with no context on the work.",
    "있음":
        "Yes",
    "작업 {code}":
        "Job {code}",
    "작업 {jobs}개·세션 {sessions}개 맞춤":
        "Matched {jobs} jobs · {sessions} sessions",
    "작업 {key}":
        "Job {key}",
    "작업 기록 없음":
        "No job records",
    "작업 끝남, 이 할 일의 검사 남음":
        "Job done, this to-do's checks remain",
    "작업 멈춤: {reason}":
        "Job stopped: {reason}",
    "작업 세션 {card}":
        "Job session {card}",
    "작업을 넘기지 못했다":
        "Couldn't hand off the job",
    "잠금 풀림":
        "Unlocked",
    "잠금: 쓰기는 work thaw까지 기다린다 ({path})":
        "Locked: writes wait until work thaw ({path})",
    "저장소의 {n}번째 줄이 원본과 다르다. 다른 기록이 이미 들어 있다":
        "Line {n} of the store doesn't match the original. A different record is already there",
    "전 | 제목 | 왜":
        "Ago | Title | Why",
    "정리 대상":
        "To clean up",
    "정리 전":
        "Before cleanup",
    "정상":
        "OK",
    "정상 종료":
        "Exited normally",
    "정상 종료하지 못함":
        "Did not exit normally",
    "제품·서비스 이름은 써도 된다. 예: {names}.":
        "Product and service names are allowed. e.g. {names}.",
    "종류는 영역|목표|할일|메모 (이슈는 work link로 붙인다)":
        "Type must be 영역|목표|할일|메모 (attach issues with work link)",
    "좋은 예: {text}":
        "Good example: {text}",
    "주인 할 일에는 --from <요청한 에이전트> 이 필요하다":
        "An owner to-do needs --from <requesting agent>",
    "주인 할 일에는 {flag}를 받지 않는다. 요청한 에이전트는 --from <에이전트>":
        "Owner to-dos don't accept {flag}. Give the requesting agent with --from <agent>",
    "주인은 할 일만 맡는다":
        "The owner can only take to-dos",
    "주인은 할 일만 맡는다(work add 할 일 --by {owner} --from <요청한 에이전트>)":
        "The owner can only take to-dos (work add 할 일 --by {owner} --from <requesting agent>)",
    "주인이 내림":
        "Dropped by owner",
    "주인이 할 것":
        "For the owner",
    "주인이 할 것: {what}":
        "For the owner: {what}",
    "증거 붙음 — 확인 필요":
        "Evidence attached — needs confirmation",
    "증거 없음: {what}":
        "No evidence: {what}",
    "증거 확인: {refs}":
        "Evidence confirmed: {refs}",
    "증거: {ref} ({who}, {ago} 전)":
        "Evidence: {ref} ({who}, {ago} ago)",
    "증거가 붙어 있다. 끝났으면: {cmd}":
        "Evidence is attached. If done: {cmd}",
    "증거가 붙었지만 기계로 확인 못 함 — 에이전트 직접 확인(work confirm --seen) 또는 주인의 말":
        "Evidence attached but can't be checked automatically — needs direct agent confirmation (work confirm --seen) or the owner's word",
    "지금":
        "now",
    "지금은 하는 중 항목에만 쓴다":
        "Only for items in progress right now",
    "직접 확인한 것":
        "Confirmed directly",
    "진척 없음 {ago}":
        "No progress for {ago}",
    "진행 대기: {what}":
        "Waiting to proceed: {what}",
    "처리한 뒤 work reply {id} {cid} \"<한 일>\" --result …":
        "Once handled, run work reply {id} {cid} \"<what you did>\" --result …",
    "체크리스트 없음":
        "No checklist",
    "체크리스트 항목을 먼저 넣는다":
        "Add checklist items first",
    "체크리스트는 할 일(점검)에만 단다":
        "Checklists go only on to-dos (checks)",
    "최근 진척 {ago} 전":
        "Last progress {ago} ago",
    "최신 기록 {n}개: {result}":
        "Latest {n} records: {result}",
    "출력 리다이렉트(>)는 못 쓴다":
        "Output redirection (>) isn't allowed",
    "취소됨 {ago} 전":
        "Canceled {ago} ago",
    "카드 {card}":
        "Card {card}",
    "카드에 적지 못했다({kind}) — 기록에는 붙였다":
        "Couldn't write to the card ({kind}) — added to the record instead",
    "칸 | 길이 | 모양 | 들어갈 것":
        "Field | Length | Format | Contents",
    "칸 | 쓰는 곳 | 꼭 쓰나 | 걸리면 | 쓰는 명령":
        "Field | Used in | Required | If it fails | Command",
    "칸마다 예":
        "Examples for each field",
    "칸반 링크 형식을 모름: {link} (kanban:t_<8 hex> 또는 kanban:<board>/t_<8 hex>)":
        "Unknown kanban link format: {link} (kanban:t_<8 hex> or kanban:<board>/t_<8 hex>)",
    "칸반에 없는 카드: {card} (기록에는 붙였다)":
        "Card not on kanban: {card} (added to the record)",
    "칸별 규칙":
        "Rules per field",
    "태그를 {dst}로 바꿨다":
        "Changed tag to {dst}",
    "통과":
        "Passed",
    "파일 {name}":
        "File {name}",
    "표          원본      저장소  결과":
        "Table       Source    Store   Result",
    "프로젝트(Desk): {names}":
        "Projects (Desk): {names}",
    "할 일 {done}/{total} 끝남":
        "{done}/{total} to-dos done",
    "할 일 {done}/{total} 끝남, 멈춘 할 일: {rs}":
        "{done}/{total} to-dos done, stalled to-dos: {rs}",
    "할 일 {n}개 다 끝났는데 목표 검사가 안 통과":
        "All {n} to-dos are done, but the goal check isn't passing",
    "할 일 문구 또는 --none 중 하나를 골라야 한다":
        "Choose either to-do text or --none",
    "할 일에는 --done \"<끝 조건>\" 과 --check \"<검사>\" 가 필요하다":
        "A to-do needs --done \"<done condition>\" and --check \"<check>\"",
    "할 일에는 --done \"<끝 조건>\" 이 필요하다":
        "A to-do needs --done \"<done condition>\"",
    "할 일에는 --goal <목표 id> 가 필요하다":
        "A to-do needs --goal <goal id>",
    "할 일에는 --session <맡은 주제 세션 이름> 또는 --omo <맡은 작업 id, 예: OT-ABC1031071257 | next(곧 작업이 붙음)> 이 필요하다":
        "A to-do needs --session <assigned topic session name> or --omo <assigned job id, e.g. OT-ABC1031071257 | next (a job will be attached soon)>",
    "할 일은 끝, 검사 남음":
        "To-dos done, check pending",
    "할 일은 다 닫혔는데 검사가 안 통과":
        "All to-dos are closed, but the check isn't passing",
    "할 일의 --from은 주인 할 일에만 쓴다(요청한 에이전트)":
        "--from on a to-do is only for owner to-dos (the requesting agent)",
    "합쳐짐":
        "Merged",
    "항목":
        "Items",
    "해결":
        "Resolved",
    "허용 목록 밖 영어 단어":
        "English word not on the allowlist",
    "확인({who})":
        "Confirmed ({who})",
}
