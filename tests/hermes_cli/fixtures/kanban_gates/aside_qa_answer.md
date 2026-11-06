## (A) Defects
- None.
## (B) Verified
- PASS: status, open the screen (sidebar entry) — The sidebar line reads `기기 3 · 정상 1 · 경고 1 · 꺼짐 1`. The `경고 1` span has class is-orange and the `꺼짐 1` span has class is-red. On the page itself `경고` computes to orange rgb(255,159,10) and `꺼짐` to red rgb(255,69,58).
- PASS: status, phone entry point — At 390 wide the first screen has the link `기기 3 · 정상 1 · 경고 1 · 꺼짐 1`. Clicking it went to `#/status`.
- PASS: status, `/app/#/status` address — I loaded it directly and the screen drew.
- PASS: status-machines — The list `기기` has 3 rows. 모래톱 reads `정상 · 작업 2/4`. 솔방울 reads `경고 · 디스크 · 작업 1/4`. 물레방아 reads `꺼짐 · 연결 끊김 · CPU — · 메모리 — · 디스크 — · 작업 —/2`.
- PASS: status-meters — The first two rows show three bars each and the third row shows none, as expected. The count of 6 bars in all is from the screenshot, which I did not measure. My automated bar count read 3, 5 and 0, which I put down to the heuristic picking up extra elements.
- PASS: status-quota — The list `쿼터` shows Claude `72% · 시간당 +10% · 3시간 후` with a red `주의`, ChatGPT `31% · 시간당 +1% · 2일 후`, and 가온셈 `5% · 12일 후`. The rate title reads `최근 5시간 평균`.
- PASS: status-quota, last rows — `Cursor · 계정 없음` and `OCX · 모델 3개 켜짐 · 연결 끊김: 물레방아` come last. Both have the same left and right inset as the service rows (240 to 1280 at 1280 wide, 16 to 374 at 390 wide). Both are 44 px tall. 물레방아 is the `꺼짐` machine.
- PASS: status-accounts — Accounts start folded. Clicking Claude shows `계정 1 · 72% · 시간당 +10% · 3시간 후 · 주의` and `계정 2 · 다시 로그인 필요`, which is red with no numbers. Clicking 가온셈 shows `계정 1 · 5% · 12일 후` and `계정 2 · 일시 중지 · 26일 후`.
- PASS: status-usage, `오늘` — Total `$3.42 · 토큰 1.2만`. `모델별` is haru `$1.85 · 6,200`, saeol `$1.12 · 4,200`, moru `$0.45 · 2,080`. `기기별` is 모래톱 `$2.05 · 7,000`, 솔방울 `$1.01 · 3,800`, 물레방아 `$0.36 · 1,680`.
- PASS: status-usage, `7일` — The button is pressed. Total `$18.40 · 토큰 8.6만`. `기기별` is 모래톱 `$11.20 · 4.9만`, 솔방울 `$5.40 · 2.6만`, 물레방아 `$1.80 · 1.1만`.
- PASS: status-usage, `30일` — Total `$63.10 · 토큰 42만`. `모델별` is haru `$34.20 · 19만`, saeol `$19.90 · 14.8만`, moru `$9.00 · 8.2만`.
- PASS: status-usage, return to `오늘` — Pressing it again restores the first reading.
- PASS: status-usage, phone sizing — At 390 wide each range control is 44 px tall. No text is under 13 px.
- PASS: status-checks — Two open rows: `백업 회선 · 응답 없음` with red `실패` and `12분 전`, then `대기 봇 · 소식이 끊김` with orange `오래됨` and `3시간 전`. A single `정상 3` follows and no passing check has its own row.
- PASS: status-checks, layout — Text in the `점검` rows ends at 1264 at 1280 wide, the same as the `기기` rows.
- PASS: Warning rule and wording — `주의` shows only on Claude and its account 1. Resets read `3시간 후`, `2일 후` and `12일 후`, and no line has an empty part.
- PASS: status-ocx — OCX is the last row of the quota list and names only the down link, 물레방아.
- PASS: status-line — The sum is correct. The sidebar text for `경고 1` and `꺼짐 1` computes white on this page, likely because the link is active, so the colours are only in the class names.
## (C) Suggestions
- status-accounts: The ChatGPT row has an empty-text nested list item (64 px tall) in the DOM. Hiding it with `hidden` or removing it would avoid a stray empty list item for assistive tech.
- status-line: The orange and red on `경고 1` and `꺼짐 1` do not show while the 기기 link is the active page. Contrast there could be checked.
- status, `ChatGPT` and other service rows: In the accessibility tree the row is a generic element rather than a button, so the fold is hard to find for screen-reader users (aria-expanded was not checked).
## (D) Map drift
- status: The entry point `/app/#/status` is dropped from the URL on load (it shows `/app/?source=synthetic`) but the status screen still draws.
- status, Accounts step: The map says the signed-out account reads in the "warning colour" in Intended behavior and "red" in the driving step. The screen uses red.
