# Raycast and Spotlight feature map

Research for [#963](https://github.com/thisisjun786/lina/issues/963), read on 2026-10-23. The question: what does a power user do with Raycast and Spotlight on a Mac, atom by atom, and for each atom should LINA's launcher keep it, replace it with a LINA feature, drop it or do it later? Where does it run, which macOS permission does it need, and how is it accepted? The sources are Raycast for Mac 2.9 (released 2026-10-22) with `@raycast/api` 2.9.0, the Raycast Store as of 2026-10-23, and Apple's documentation for macOS 26 Tahoe and macOS 27. Every row says whether its fact was observed in a current primary source (O) or inferred (I). Nothing here was measured on a Mac; the bar numbers are public figures until [the bar](#the-bar) is measured.

The decisions are copied into the Launcher section of [surfaces.md](../design/surfaces.md#launcher). This note is evidence for them, not a contract. The work is filed as issues in the LINA app repository (app#N) and in this repository (#N); see [Issues](#issues).

## Findings

1. The map has 984 atomic rows: 452 keep, 215 replace, 221 later and 96 drop. What a feature does rests on a current primary source in 901 rows and is inferred in 83. How often it is used is inferred in most rows, because Raycast keeps usage data on each Mac and publishes no per-command numbers; Store and Homebrew installs and GitHub config counts are the hard evidence.
2. The most installed Store extensions are local system tools, not AI: Kill Process (787,441 installs), Color Picker (625,764) and Google Chrome (578,453) are the top three of 3,454. The top 40 hold 52% of 16.7M installs.
3. Raycast 2 renders its launcher in a web view: a Swift/AppKit host, one React interface in WKWebView, a Node process and a Rust core. Its memory rose from about 200–300 MB (v1) to 350–450 MB, and releases 2.5 to 2.7 fixed a blank launcher and keys lost right after the hotkey.
4. macOS 15 to 27 tile windows natively: halves, fill, center and restore (Fn-⌃ keys), quarters by menu or drag. It has no thirds, almost-maximize, keyed move to the next display, adjustable gaps or layouts that open apps. Those are what people bind in Raycast after the halves.
5. No public API lets an app list or run another app's App Intents, search Mail, Notes or Messages the way Spotlight does, or take ⌘Space from Spotlight. The person's Shortcuts are the action catalog, private app data goes through plugins or the agent, and the person turns Spotlight's shortcut off by hand.
6. macOS 27 made ⌘Space "Search or Ask": Return sends the line to Siri. That is the launcher's own model (a sentence goes to the agent), so the agent conversation is the default fallback.
7. Spotlight's clipboard history (macOS 26 and 27) is off until allowed, keeps text only, at most 7 days, with no pins or ignored apps. LINA keeps its own history. Apple Passwords marks none of its copies as concealed, so the history also needs a default list of ignored apps.
8. Only system-drawn glass (NSGlassEffectView) follows the Liquid Glass setting, Reduce Transparency and Increase Contrast. Flutter passes only light or dark to its apps and has an open priority-1 bug that drops the first composing key on macOS 27. A web view needs private settings for glass and extra work for Korean input.
9. The owner kept the rule that every device action, even one the person picks, goes Core → Mac Node grant → receipt. That makes launching apps, finding files and moving windows depend on Core, which Spotlight does not. This is recorded as an [open question](#open-questions).

## Decisions

- **Panel.** The launcher panel is native Swift: an AppKit non-activating NSPanel with NSGlassEffectView, the query field, the results list, the hotkey, key handling and every system integration (menu bar extra, Services, Share sheet, App Intents, drag, VoiceOver). Agent replies and mini apps render inside the panel in a prewarmed view of the app's interface, as Raycast 2 does. Evidence: only native code gets system glass and its accessibility settings without private APIs, Korean from the first key without open upstream bugs, and Spotlight-level speed; the integrations are Swift under every option anyway. Flutter is not used for the panel. One token source feeds Swift through `scripts/tokens.mjs` (#1010). lina-app #2 measures the panel and can reverse this only with numbers that meet [the bar](#the-bar).
- **Spotlight.** The launcher replaces Spotlight as well as Raycast: apps, files and folders from the system index (NSMetadataQuery, so results match Spotlight), System Settings panes, contacts, events and reminders, definitions, calculations and conversions, web search, Quick Look (Space), reveal in Finder (⌘↩, and ⌘R as in Spotlight), open with, recent items, and Shortcuts as actions. It takes ⌘Space through a guided switch with a clean way back (app#35).
- **Keys.** Arrows, ↩, ⌘↩, ⌥↩, Tab, Esc, ⌘K and ⌘1–9 follow the Mac. Keys the Korean input method consumes (Enter, Esc, ⌥↩ for Hanja during composition) go to it first. ⌘1–9 pick results, so Spotlight's ⌘1–4 browse modes become category chips.
- **Quick answers.** Calculator, units (with 평, 근, 돈 and 만, 억, 조), currency with its rate date, time zones, dates, definitions (Dictionary Services), translation (Apple Translation, on the Mac), emoji and quick links answer in the app with no Core call. Currency rates are fetched and cached by the app. The calculator engine is open source with a compatible licence (fend or math.js).
- **Agent.** Raycast AI is replaced by the agent conversation. A sentence goes to the main conversation of the last or named agent; the short reply shows in the panel with "open in conversation". The launcher adds one key to send the selection, the Finder selection or a dropped file, read through Accessibility, never through the clipboard.
- **Device actions.** Opening apps and files, file search, Quick Look, reveal, windows, system commands, menu items, contacts, calendar, Shortcuts, scripts and selection reads run on the Mac Node under a grant from Core with a receipt, and are refused with a reason when Core or the grant is missing (owner, 10-23). What still works without Core is shown to the person (app#56).
- **Copy only.** The launcher puts items on the clipboard and the person pastes. Pasting into the front app, snippet auto-expansion and `{cursor}` inject input, which non-competition.md allows only under a seat lease, so they are `later` (owner, 10-23).
- **Clipboard history.** Off until the person turns it on, in the app (a background daemon cannot reach the person's clipboard), encrypted on that Mac, never synced and never sent to Core. It skips concealed, transient and auto-generated items and a default list of ignored apps (Passwords, Keychain Access, password managers).
- **Launcher items.** Quick links, snippets, aliases, hotkeys and favorites are the person's data, so Core stores them (#1025) and the app uses its last copy when Core is down. Raycast's quicklinks and snippets JSON exports import directly. Ranking by the person's picks and the calculator history are local display state on that Mac.
- **Windows.** macOS keeps what it already does with a shortcut (halves, fill, center, quarters, full screen, Spaces). LINA adds thirds, almost maximize, center half, restore, next display, gaps and layouts (app#67–app#69). Moving a window to another Space has no public API and is `later`.
- **Extensions.** A Store extension becomes the agent conversation, a plugin, a mini app (lina #951, app#25) or a launcher feature; only the last needs native code. See [Extensions](#extensions).

## Where each feature runs

`app`: the LINA app on the Mac, no Core. `Core`: needs agents, conversation, memory, plugins or a model. `Node`: the Mac Node under a grant from Core, with a receipt. `macOS`: macOS already does it. Combinations name every part. Counts are for `keep` rows.

| Runs | Keep rows | What runs there |
| --- | --- | --- |
| app | 257 | Panel, keys, ranking, quick answers, clipboard history, copy actions |
| Node | 73 | Opening, finding, windows, system commands, contacts, calendar, Shortcuts, scripts |
| app+Core | 68 | Launcher items stored in Core and used in the app; sending a line to an agent |
| app+Node | 47 | Results drawn by the app, effects or reads done by the Node |
| Core+Node | 5 | Selection and Finder reads that go into an agent message |
| macOS | 1 | macOS behavior LINA relies on |
| Core | 1 | Agent work, plugins, mini app hosting |

macOS permissions the kept rows need: Accessibility (windows, menu items, selected text; macOS 27 may call it "Device Control and Data Access", to be checked), Full Disk Access (Safari data, Messages, some file scopes), Files and Folders (Desktop, Documents, Downloads), Contacts, Calendars, Reminders, Automation per app (browsers, Music, Finder, System Events), "Paste from Other Apps" for clipboard history, and "Allow in the Menu Bar" on macOS 26 and later. If the Mac Node is a separate process, it needs its own grants (I).

## The bar

No primary source publishes Spotlight's or Raycast's open time, first-result time or Spotlight's memory. These are the public numbers until app#52 measures them on the owner's desk Mac (M2 Pro, macOS 27).

| Metric | Spotlight | Raycast | Source |
| --- | --- | --- | --- |
| Hotkey to visible panel | about 0.2 s (one report, Mac mini M4, macOS not stated); about 150 ms (review, no method) | about 90 ms (review, no method) | S: visca/2025, dtr/2026 (I) |
| Last key to first result | unknown | 2.9: "search 50% faster" (relative) | B: changelog (O) |
| Resident memory | about 30 MB (review, no method); about 50 MB (another report) | v2: about 350–450 MB after use (web content 120–200, Node 150–200, Swift shell about 40); v1: 200–300 MB; 2.7 and 2.8 cut it by stated percentages | N: rc/deepdive (O); S: dtr/2026, visca/2025 (I) |
| Index freshness | new files appear after about 7 s; about 11 min of indexing after login | own Rust index for names, Spotlight for content | S: eclectic/2026-02 (O for 26.2); F: rc-tech (O) |

Cold launch to first content of a minimal app on an M4 with macOS 26.6.1, as a guide to UI stacks: AppKit 193 ms and 15.5 MiB, SwiftUI 228 ms and 18.3 MiB, Flutter 259 ms and 55.0 MiB, WKWebView (Tauri) 410 ms and 73.0 MiB (N: legend, O). Warm panel-show times are unknown for every stack.

How to measure, for Spotlight, Raycast and LINA alike (N: apple/measure, legend, typometer):

1. A small signed Swift tool with Accessibility and Screen Recording posts ⌘Space, then the query: "safari" on the ABC layout, and 사파리 typed on Korean 2-Set, with gaps of 0, 8 and 30 ms.
2. It records the display at full refresh rate (ScreenCaptureKit, `minimumFrameInterval` 0) and keeps each frame's display time.
3. Open-to-visible is the first frame where the panel region changes. First result is the time from the last key to the first of three frames where text recognition reads the expected top row. Lost keys are typed text that does not reach the field.
4. Two warm-ups, then 30 alternating runs per launcher; report median, p90 and lost keys, plus one cold run after login. Ten runs are checked against 240 fps phone video of real key presses, because a posted ⌘Space may not behave like a real one (I).
5. LINA also logs signpost intervals (hotkey, panel shown, first commit, results drawn) and physical memory over all its processes, web content helpers included.

The target: LINA's open-to-visible and first-result medians and p90 are at most Spotlight's on the same Mac (app#53, app#27), with no lost keys.

## Extensions

The top 40 Store extensions by installs (8,777,373 installs) map to LINA as follows (X: store-api, read 2026-10-23, O).

| Covered by | Count | Installs | Extensions |
| --- | --- | --- | --- |
| Launcher feature (keep) | 15 | 4,178,929 (47%) | Kill Process, Color Picker, Google Chrome, Google Translate, Visual Studio Code, Arc, Coffee, Google Search, CleanShot X, Warp, YouTube, Music, Downloads Manager, Set Audio Device, Shell |
| Plugin | 7 | 1,960,311 (22%) | Spotify Player, Linear, Slack, Notion, GitHub (Raycast), Zoom (Raycast), Todoist |
| Agent conversation | 8 | 1,353,089 (15%) | Brew, ChatGPT, Speedtest, Obsidian, Video Downloader, Lorem Ipsum, Google Gemini, Tailwind CSS |
| Mini app | 5 | 576,690 (6%) | Timers, System Monitor, Pomodoro, GIF Search, Format JSON |
| Drop | 2 | 344,659 (3%) | 1Password, Remove Paywall |
| LINA memo or to-do | 2 | 282,211 (3%) | Apple Notes (Raycast), Apple Reminders (Raycast) |
| Later | 1 | 81,484 (0%) | Clean Keyboard |

A real integration is needed for: process list and kill, the screen color sampler, one search over browser tabs, history and bookmarks (Chrome, Arc, Safari), editor recent projects, keep-awake, audio device switch and Music controls (app#76–app#82 with #1021–#1023); scripts and shell lines (app#75, #1024); and plugins where the vendor has no simple remote MCP server: Slack (the person's own Slack app, #1028) and Spotify (the person's own developer app, #1029). Linear, Notion, Todoist, Zoom and Jira only need catalog entries for their official MCP servers (#1027).

What extensions use, as a share of all Store downloads (A: store-api and raycast/extensions, O): clipboard copy or paste 80%, a List view 81%, push navigation 57%, opening URLs, files or apps 55.8%, a Form 50%, a Detail view 49%, arguments 42%, AppleScript 33.6%, scheduled background refresh 28%, a menu bar command 27.5%, reading the selection 21.1%, a Grid 15.6%. Mini app templates (lina #951, app#25) need list, navigation, form, detail, arguments, schedules and a menu bar widget; clipboard, opening things, AppleScript and selection go through a launcher action the person picks or a Node operation under a grant, never straight from a mini app. Raycast extensions are not sandboxed: they run in one Node process with the user's file and network access and all of Raycast's macOS permissions.

## Issues

Each issue is one pull request with one acceptance check and lists the rows it covers. lina-app #7, #8 and #28 and lina #954 are parents: they close when their sub-issues close. Existing issues are reused where they already cover a row: app#27 (ranking, autocomplete, first-result speed), app#25 and lina #951 (mini apps), app#16 (notifications), lina #967 and #966 (understanding the launcher line).

| Issue | Parent | Title |
| --- | --- | --- |
| [#1010](https://github.com/thisisjun786/lina/issues/1010) | app#7 | Design tokens: generate Swift for the native launcher panel from the one DTCG source |
| [#1011](https://github.com/thisisjun786/lina/issues/1011) | #954 | Launcher capability in the client protocol: a launcher line lands in the agent's main conversation with a short reply and a locator |
| [#1012](https://github.com/thisisjun786/lina/issues/1012) | #954 | macOS Node: launcher device action round trip with a receipt, refused without a grant or with Core stopped |
| [#1013](https://github.com/thisisjun786/lina/issues/1013) | #954 | macOS Node: list, open, quit, hide and restart apps for the launcher |
| [#1014](https://github.com/thisisjun786/lina/issues/1014) | #954 | macOS Node: file name, content and recent search through NSMetadataQuery inside the grant scope |
| [#1015](https://github.com/thisisjun786/lina/issues/1015) | #954 | macOS Node: open, open with, reveal, Quick Look and trash for launcher file results |
| [#1016](https://github.com/thisisjun786/lina/issues/1016) | #954 | macOS Node: place the front window at a rect, move it to another display and apply a layout |
| [#1017](https://github.com/thisisjun786/lina/issues/1017) | #954 | macOS Node: system commands for the launcher |
| [#1018](https://github.com/thisisjun786/lina/issues/1018) | #954 | macOS Node: Contacts, Calendar and Reminders reads and System Settings panes for the launcher |
| [#1019](https://github.com/thisisjun786/lina/issues/1019) | #954 | macOS Node: list and run the person's Shortcuts as launcher actions |
| [#1020](https://github.com/thisisjun786/lina/issues/1020) | #954 | macOS Node: read the selected text, the Finder selection and the front app's menu items |
| [#1021](https://github.com/thisisjun786/lina/issues/1021) | #954 | macOS Node: process list and kill, keep-awake and audio device switch |
| [#1022](https://github.com/thisisjun786/lina/issues/1022) | #954 | macOS Node: search browser tabs, history and bookmarks (Chrome, Arc, Safari) |
| [#1023](https://github.com/thisisjun786/lina/issues/1023) | #954 | macOS Node: Music playback controls and editor recent projects |
| [#1024](https://github.com/thisisjun786/lina/issues/1024) | #954 | macOS Node: run script commands and shell lines from the launcher under a grant |
| [#1025](https://github.com/thisisjun786/lina/issues/1025) | #954 | Core stores launcher items: quick links, snippets, aliases, hotkeys and favorites |
| [#1026](https://github.com/thisisjun786/lina/issues/1026) | #954 | Core lists mini apps for the launcher with their declared access |
| [#1027](https://github.com/thisisjun786/lina/issues/1027) | app#28 | Plugin catalog entries for official remote MCP servers: Linear, Notion, Todoist, Zoom and Jira |
| [#1028](https://github.com/thisisjun786/lina/issues/1028) | app#28 | Slack plugin with the person's own Slack app |
| [#1029](https://github.com/thisisjun786/lina/issues/1029) | app#28 | Spotify plugin with the person's own Spotify developer app |
| [app#31](https://github.com/thisisjun786/lina-app/issues/31) | app#7 | 런처 패널이 네이티브 NSPanel로 떠서 쓰던 앱의 포커스를 빼앗지 않는다 |
| [app#32](https://github.com/thisisjun786/lina-app/issues/32) | app#7 | 런처 패널이 시스템 Liquid Glass와 손쉬운 사용 설정을 그대로 따른다 |
| [app#33](https://github.com/thisisjun786/lina-app/issues/33) | app#7 | 단축키 직후 첫 키부터 한글이 조합되고 친 글자가 빠지지 않는다 |
| [app#34](https://github.com/thisisjun786/lina-app/issues/34) | app#7 | 런처 키가 맥 관례대로 동작한다: 방향키·↩·⌘↩·⌥↩·Tab·Esc·⌘1–9·⌘K |
| [app#35](https://github.com/thisisjun786/lina-app/issues/35) | app#7 | 런처를 켜고 ⌘Space를 Spotlight에서 넘겨받고 깨끗하게 돌려준다 |
| [app#36](https://github.com/thisisjun786/lina-app/issues/36) | app#7 | 런처에서 쓴 한 문장이 에이전트의 메인 대화로 가고 짧은 답이 패널에 보인다 |
| [app#37](https://github.com/thisisjun786/lina-app/issues/37) | app#7 | 런처에서 맡긴 긴 일은 패널을 닫아도 계속되고 결과가 원래 대화로 돌아온다 |
| [app#38](https://github.com/thisisjun786/lina-app/issues/38) | app#7 | 런처 항목마다 별칭·단축키·즐겨찾기를 두고 모든 맥에서 같게 쓴다 |
| [app#39](https://github.com/thisisjun786/lina-app/issues/39) | app#7 | 선택한 글·Finder 항목·파일을 키 하나로 에이전트 메시지에 붙인다 |
| [app#40](https://github.com/thisisjun786/lina-app/issues/40) | app#7 | 런처에서 계산하고 답을 복사한다 (Core 호출 없음) |
| [app#41](https://github.com/thisisjun786/lina-app/issues/41) | app#7 | 단위를 변환한다 (평·근·돈과 만·억·조 포함) |
| [app#42](https://github.com/thisisjun786/lina-app/issues/42) | app#7 | 환율을 변환하고 환율 날짜를 보인다 (오프라인이면 마지막 환율) |
| [app#43](https://github.com/thisisjun786/lina-app/issues/43) | app#7 | 시간대와 날짜를 계산한다 |
| [app#44](https://github.com/thisisjun786/lina-app/issues/44) | app#7 | 기기 사전으로 뜻을 보인다 |
| [app#45](https://github.com/thisisjun786/lina-app/issues/45) | app#7 | 기기 번역으로 런처 안에서 번역한다 |
| [app#46](https://github.com/thisisjun786/lina-app/issues/46) | app#7 | 이모지·기호를 한국어·영어 이름으로 찾아 복사한다 |
| [app#47](https://github.com/thisisjun786/lina-app/issues/47) | app#7 | {argument}가 있는 퀵링크를 만들고 열며 Raycast 퀵링크를 가져온다 |
| [app#48](https://github.com/thisisjun786/lina-app/issues/48) | app#7 | 맞는 결과가 없을 때 웹 검색으로 이어 간다 |
| [app#49](https://github.com/thisisjun786/lina-app/issues/49) | app#7 | 메뉴바 항목에서 런처와 대화를 열고 Core 연결 상태와 현재 에이전트를 본다 |
| [app#50](https://github.com/thisisjun786/lina-app/issues/50) | app#7 | 서비스 메뉴와 공유 시트로 고른 글·파일을 에이전트에게 보낸다 |
| [app#51](https://github.com/thisisjun786/lina-app/issues/51) | app#7 | 단축어·Spotlight와 집중 모드 필터에서 LINA 동작을 쓴다 |
| [app#52](https://github.com/thisisjun786/lina-app/issues/52) | app#7 | 데스크 맥에서 Spotlight·Raycast의 열기 시간·첫 결과 시간·메모리를 재서 기준을 정한다 |
| [app#53](https://github.com/thisisjun786/lina-app/issues/53) | app#7 | LINA 런처가 Spotlight보다 늦게 뜨지 않는다 |
| [app#54](https://github.com/thisisjun786/lina-app/issues/54) | app#7 | 런처 결과를 끌어 놓고 VoiceOver로 읽는다 |
| [app#55](https://github.com/thisisjun786/lina-app/issues/55) | app#7 | lina:// 링크로 런처를 열고 항목을 실행한다 (처음 실행 전 확인) |
| [app#56](https://github.com/thisisjun786/lina-app/issues/56) | app#8 | Core·Node·grant가 없을 때 런처에서 되는 것과 안 되는 것을 보인다 |
| [app#57](https://github.com/thisisjun786/lina-app/issues/57) | app#8 | 맥 권한을 처음 쓸 때 한 번 안내하고 없으면 그 기능만 멈춘다 |
| [app#58](https://github.com/thisisjun786/lina-app/issues/58) | app#8 | 런처에서 앱을 찾아 연다 |
| [app#59](https://github.com/thisisjun786/lina-app/issues/59) | app#8 | 앱 결과에서 종료·강제 종료·숨기기·다시 시작·Finder에서 보기·경로 복사를 한다 |
| [app#60](https://github.com/thisisjun786/lina-app/issues/60) | app#8 | 파일 이름과 최근 파일을 시스템 색인으로 찾는다 (grant 범위 안) |
| [app#61](https://github.com/thisisjun786/lina-app/issues/61) | app#8 | 파일 내용과 종류·위치 필터로 찾는다 |
| [app#62](https://github.com/thisisjun786/lina-app/issues/62) | app#8 | 파일 결과에서 열기·다른 앱으로 열기·Finder에서 보기·훑어보기·복사·휴지통을 한다 |
| [app#63](https://github.com/thisisjun786/lina-app/issues/63) | app#8 | 시스템 설정 패널을 찾아 연다 |
| [app#64](https://github.com/thisisjun786/lina-app/issues/64) | app#8 | 연락처를 찾아 전화·메시지·메일·복사를 한다 (기본 꺼짐) |
| [app#65](https://github.com/thisisjun786/lina-app/issues/65) | app#8 | 오늘 일정과 다음 회의 참가를 런처와 메뉴바에서 쓴다 |
| [app#66](https://github.com/thisisjun786/lina-app/issues/66) | app#8 | 단축어를 런처의 동작 목록으로 쓴다 |
| [app#67](https://github.com/thisisjun786/lina-app/issues/67) | app#8 | 맥에 없는 창 배치를 단축키와 런처로 쓴다: 3분할·거의 최대화·가운데 절반·되돌리기 |
| [app#68](https://github.com/thisisjun786/lina-app/issues/68) | app#8 | 창을 다음·이전 디스플레이로 보낸다 |
| [app#69](https://github.com/thisisjun786/lina-app/issues/69) | app#8 | 여러 앱의 창 배치를 저장하고 한 번에 적용한다 |
| [app#70](https://github.com/thisisjun786/lina-app/issues/70) | app#8 | 시스템 명령을 런처에서 실행한다 |
| [app#71](https://github.com/thisisjun786/lina-app/issues/71) | app#8 | 앞 앱의 메뉴 항목과 창을 런처에서 찾아 실행한다 |
| [app#72](https://github.com/thisisjun786/lina-app/issues/72) | app#8 | 클립보드 기록을 켜면 그 맥에만 암호화해 저장하고 숨김 항목은 저장하지 않는다 |
| [app#73](https://github.com/thisisjun786/lina-app/issues/73) | app#8 | 클립보드 기록을 찾고 미리 보고 복사·고정·삭제하며 보존 기간을 정한다 |
| [app#74](https://github.com/thisisjun786/lina-app/issues/74) | app#8 | 스니펫을 키워드로 찾아 자리표시자를 채워 복사한다 (자동 확장은 나중) |
| [app#75](https://github.com/thisisjun786/lina-app/issues/75) | app#8 | 스크립트 명령과 셸 한 줄을 런처에서 실행하고 결과를 본다 |
| [app#76](https://github.com/thisisjun786/lina-app/issues/76) | app#28 | 프로세스를 CPU·메모리 순으로 보고 끝낸다 |
| [app#77](https://github.com/thisisjun786/lina-app/issues/77) | app#28 | 화면에서 색을 집어 값을 복사한다 |
| [app#78](https://github.com/thisisjun786/lina-app/issues/78) | app#28 | 브라우저 탭·기록·북마크를 한 번에 찾는다 (Chrome·Arc·Safari) |
| [app#79](https://github.com/thisisjun786/lina-app/issues/79) | app#28 | 맥이 잠들지 않게 하고 메뉴바에 상태를 보인다 |
| [app#80](https://github.com/thisisjun786/lina-app/issues/80) | app#28 | 소리 출력·입력 장치를 바꾼다 |
| [app#81](https://github.com/thisisjun786/lina-app/issues/81) | app#28 | 편집기의 최근 프로젝트를 런처에서 연다 |
| [app#82](https://github.com/thisisjun786/lina-app/issues/82) | app#28 | 음악 앱 재생을 런처에서 제어한다 |
| [app#83](https://github.com/thisisjun786/lina-app/issues/83) | app#8 | Raycast와 Spotlight를 끄고 런처로 지낸 기록을 남긴다 |

## Open questions

- **Spotlight parity without Core.** Under the current rule, opening an app, finding a file or moving a window needs Core and a grant, and adds a round trip; Spotlight does these offline. Candidate: split by effect. Opening, revealing, previewing and "open with" run in the app as the person's own act; actions that change the system (windows, quitting apps, lock, Trash, scripts) stay Core → Node with a receipt. The owner decides.
- **Input the person triggers.** Paste into the front app, snippet auto-expansion and `{cursor}` need a non-competition clause for input the person starts from the launcher. Until then they are `later`.
- **App stack names.** surfaces.md says the app is React on Wails v3; lina-app #1, #2 and #6 still name Flutter. The panel decision above holds for either, and the app issues name no stack for anything outside the panel.
- **macOS 27 details to check on the Mac.** Whether the Accessibility pane is renamed "Device Control and Data Access"; whether every app now gets the paste privacy alert; whether `x-apple.systempreferences:com.apple.Keyboard-Settings.extension?Shortcuts` opens the shortcuts sheet; whether a menu shortcut can be bound to the Window > Move & Resize items.
- **Permissions per process.** Whether the Mac Node, as a separate process, needs its own Accessibility, Full Disk Access and Automation grants, and how the app shows that.

## How to read the map

- **ID**: area prefix and number. Issues cite these IDs.
- **Feature**: one thing a person can do, or one setting that changes behavior, as `area: name`.
- **Use**: how often a typical power user uses it (`high`, `mid`, `low`) and the evidence: Store installs, Homebrew installs, config counts on GitHub, Raycast's own words, or `I:` with the reason.
- **Decision**: `keep` (the launcher does it), `replace: <LINA feature>`, `drop: <reason>` or `later: <reason>`.
- **Runs**: see [Where each feature runs](#where-each-feature-runs).
- **Source**: a letter for the research area and a key from its list in [Sources](#sources), with the version, then O (observed in a current primary source) or I (inferred).
- **Issue**: the app issue (app#N) and the Core or Node issue (#N) that build a kept row, or the issue that replaces it.

Left out: Raycast for Windows window and desktop commands (10 rows), and macOS's own tiling features that no Raycast command needs (the Window menu's Move & Resize and Arrange items, drag to an edge or the menu bar, Option-drag, tile margins), which the Windows rows already name where they replace a Raycast command. Rows that two research areas both found were merged.

## Feature map

### Panel and native feel

52 rows: 51 keep, 0 replace, 0 later, 1 drop.

In `native:` rows the decision says whether each technology can meet the requirement: a is native Swift (AppKit or SwiftUI), b is Flutter for macOS, c is a web view such as the Wails v3 shell. `partial` means only with private API, an open upstream bug, or native code beside it.

| ID | Feature | Use | Decision | Runs | Permission | Acceptance check | Source | Issue |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| NAT-01 | panel: results as you type | high: I: the core behavior | keep | app+Node | none | Typing "saf" shows Safari in row 1 with no extra key | S: apple/sp-find (macOS 27) (O) | app#27 |
| NAT-02 | panel: delete search history | low: I | keep | app | none | After "Delete history", ↑ shows nothing | S: apple/sp-settings (macOS 27) (O) | app#34 |
| NAT-03 | panel: show path while ⌘ held | mid: I | keep | app | none | Hold ⌘ on a file row: its full path shows; release ⌘: it hides | S: apple/sp-keys (macOS 27); macrumors/tahoe-sp (O) | app#34 |
| NAT-04 | panel: move window | low: I | keep | app | none | Drag the panel, then reopen it: same position on the same display | S: apple/sp-find (macOS 27) (O) | app#31 |
| NAT-05 | panel: browse modes ⌘1–4 | mid: I: shown on hover and ↓ | drop: ⌘1–9 picks results; category chips cover it | app | none | ⌘2 then "pdf" shows only files | S: apple/sp-narrow (macOS 27); search/27-reviews (O) |  |
| NAT-06 | native: non-activating panel | high: every open (I: launcher core) | keep; a yes, b yes, c yes | app | none | With TextEdit in front, ⌘Space: menu bar still says TextEdit; Esc; typing goes to TextEdit with no click. | N: apple/nonactivating, pub (floating_palette 0.1.0), wails/panel (v3.0.0-beta.12) (O) | app#31 |
| NAT-07 | native: key panel while app stays inactive | high | keep; a yes, b yes, c yes | app | none | Open panel, type "abc" without clicking: text is in the field and `NSApp.isActive` logs false. | N: apple/canbecomekey, apple/nspanel, wails/panel (O) | app#31 |
| NAT-08 | native: floats above other windows | high | keep; a yes, b yes, c yes | app | none | Panel draws above a floating window and over full-screen Safari; ⌥↩ Hanja window draws above the panel. | N: apple/nspanel, apple/levels, tnp/104 (O) | app#31 |
| NAT-09 | native: closes on click outside or app switch | high (I: common dismissal) | keep; a yes, b yes, c yes | app | none | Click desktop or ⌘Tab: panel hides within one frame; previous app keeps focus. | N: apple/nspanel (I) | app#31 |
| NAT-10 | native: no Dock icon or ⌘Tab entry | high | keep; a yes, b yes, c yes | app | none | While panel is open, LINA appears neither in the Dock nor in ⌘Tab. | N: apple/policy, wails/spotlight (O) | app#31 |
| NAT-11 | native: hidden from Mission Control and window cycling | mid (I: noticed only when wrong) | keep; a yes, b yes, c yes (Wails `Transient`) | app | none | Open/close panel 20 times, then Mission Control: no LINA window; ⌘\` never selects the panel. | N: apple/collection, rc/changelog (Raycast 2.2) (O) | app#31 |
| NAT-12 | native: opens over full-screen apps | mid (I: full-screen users) | keep; a yes, b yes, c yes | app | none | Safari in full screen, ⌘Space: panel appears on that space with no space-switch animation. | N: apple/collection, wails/spotlight (O) | app#31 |
| NAT-13 | native: opens on the current Space | high | keep; a yes, b yes, c yes | app | none | Open on Space 1, close, go to Space 3, ⌘Space: panel on Space 3, no switch. | N: apple/collection, rc/settings (Raycast v2) (O) | app#31 |
| NAT-14 | native: Stage Manager untouched | low (I: few use Stage Manager) | keep; a yes, b yes, c partial (shell patch) | app | none | Stage Manager on, open/close panel: no new strip thumbnail, stage set unchanged. | N: apple/collection (O) | app#31 |
| NAT-15 | native: opens on the right display | mid (I: multi-display users) | keep; a yes, b yes, c yes | app | none | Two displays, mouse on display 2, ⌘Space: panel on display 2; unplug it: next open on display 1, same position rule. | N: apple/screen, rc/settings (O) | app#31 |
| NAT-16 | native: Liquid Glass background | high | keep; a yes, b partial, c partial | app | none | On macOS 27, LINA and Spotlight screenshots over the same wallpaper show the same glass. | N: apple/glass, apple/glass-swiftui, fl/170310, fl/59969, wails/glass, wails/private (O) | app#32 |
| NAT-17 | native: follows the Liquid Glass look setting | mid (I: set once, seen always) | keep; a yes, b no (only via native glass), c no (only via native glass) | app | none | Move the macOS 27 slider to both ends: panel opacity changes like Spotlight's without restarting LINA. | N: apple/hig-materials, 9to5/glass-slider, legend (O) | app#32 |
| NAT-18 | native: vibrancy material | high | keep; a yes, b partial, c partial | app | none | Panel material looks identical whether or not another app is active (no grey inactive look). | N: apple/vev, pub, wails/private (O) | app#32 |
| NAT-19 | native: vibrant text on material | high | keep; a yes, b no, c no | app | none | Secondary text in result rows tints with the wallpaper like Spotlight's (side-by-side screenshot). | N: apple/vev, apple/hig-materials (O) | app#32 |
| NAT-20 | native: rounded corners like Spotlight | high | keep; a yes, b yes (native host), c yes | app | none | Overlay LINA and Spotlight screenshots at 2x: corner curves match within 1 pt. | N: apple/glass, 9to5/macos27 (I) | app#32 |
| NAT-21 | native: shadow like Spotlight | high | keep; a yes, b yes, c yes | app | none | Grow from 0 to 8 results: shadow follows the new outline in the same frame; no rectangular shadow remains. | N: apple/levels (I) | app#32 |
| NAT-22 | native: light and dark | high | keep; a yes, b yes, c yes | app | none | Switch Appearance with the panel open: colors switch within one frame to LINA's light/dark tokens. | N: apple/dyncolor, fl/engine, bcd (O) | app#32 |
| NAT-23 | native: accent color | mid | keep; a yes, b partial, c partial | app | none | Set accent to Graphite with the panel open: selection and focus ring follow the decided rule at once. | N: apple/accent, fl/engine, bcd, wails/src, lina/surfaces (O) | app#32 |
| NAT-24 | native: Reduce Transparency | low (I: accessibility setting) | keep; a yes, b partial, c partial | app | none | Reduce Transparency on: panel turns opaque like Spotlight; text contrast ≥ 4.5:1. | N: apple/a11y-display, apple/hig-materials, fl/engine, bcd (O) | app#32 |
| NAT-25 | native: Reduce Motion | low | keep; a yes, b partial, c yes | app | none | Reduce Motion on, 120 fps capture of an open: no frame shows scaling or movement. | N: apple/a11y-display, apple/levels, fl/engine, bcd, wails/src (O) | app#32 |
| NAT-26 | native: Increase Contrast | low | keep; a yes, b partial, c yes | app | none | Increase Contrast on: panel uses the high-contrast token set (accent #6795FF) and shows a visible edge. | N: apple/a11y-display, apple/dyncolor, bcd (O) | app#32 |
| NAT-27 | native: hotkey toggles the panel | high | keep; a yes, b yes, c yes | app | none | ⌘Space twice: panel opens then closes; previous app still active. | N: apple/spotlight-keys (macOS 27) (O) | app#35 |
| NAT-28 | native: arrow keys move selection | high | keep; a yes, b yes, c yes | app | none | Type "sa", Down ×3: 4th row selected; field text and caret unchanged. | N: apple/textinput, apple/spotlight-keys (I) | app#34 |
| NAT-29 | native: ⌘1–9 open result N | mid (I: Alfred-style users) | keep; a yes, b yes, c yes | app | none | Type "s", ⌘3: 3rd result opens; "3" is never inserted. | N: apple/spotlight-keys (O) | app#34 |
| NAT-30 | native: Return during Korean composition | high (Korean owner) | keep; a yes, b partial, c partial | app+Core | none | Type "안녕" fast, Return while "녕" composes: Core ledger has exactly one message "안녕". | N: fl/190525, lina/surfaces (O) | app#33 |
| NAT-31 | native: ⌘↩ secondary action | mid | keep; a yes, b yes, c yes | app | none | Select a file, ⌘↩: Finder shows it selected; panel closes. | N: apple/spotlight-keys (O) | app#34 |
| NAT-32 | native: ⌥↩ alternate action | mid | keep; a yes, b partial, c partial | app | none | Korean input, type "한자", ⌥↩: Hanja candidates open, no action; ABC input: ⌥↩ runs the alternate action. | N: apple/korean-im (macOS 27), fl/190525 (O) | app#34 |
| NAT-33 | native: Tab to act | mid | keep; a yes, b yes, c yes | app | none | Type "메모" (still composing), Tab: field keeps "메모" and focus moves to the action. | N: apple/spotlight-keys (O) | app#34 |
| NAT-34 | native: Esc closes, IME first | high | keep; a yes, b partial, c partial | app | none | Hanja window open: 1st Esc closes only it, 2nd Esc closes the panel, previous app has focus. | N: apple/korean-im, rc/settings (O) | app#34 |
| NAT-35 | native: type-ahead from the first keystroke | high | keep; a yes, b partial, c partial | app | none | Harness posts ⌘Space then "safari" with 0 ms gaps, 50 runs: field reads "safari" every run (0 lost, 0 reordered). | N: rc/changelog (2.6, 2.7), rc/deepdive, se/223750, alfred/missing (O) | app#33 |
| NAT-36 | native: Korean composes from the first key | high (Korean owner) | keep; a yes, b no until #194199 fixed, c partial | app | none | Korean 2-Set, ⌘Space then keys t,k,v,k,f,l at 0 ms gaps, 50 runs: field reads "사파리" every run, composing syllable underlined. | N: zed/62661, fl/194199, lina/surfaces, lina-app#1 (O) | app#33 |
| NAT-37 | native: IME candidate window above the panel | low (Hanja use) | keep; a yes, b partial, c partial | app | none | ⌥↩ Hanja window appears at the caret, above the panel. | N: apple/textinput, tnp/104, fl/engine (O) | app#33 |
| NAT-38 | native: input source on open | mid (I: CJK users) | keep; a yes, b yes (Swift), c yes (Swift) | app | none | Option = ABC: Korean active → open → ABC; close → Korean again. | N: apple/textinput, rc/settings (O) | app#33 |
| NAT-39 | native: editing keys during composition | high | keep; a yes, b partial, c partial | app | none | Type "한글" then Backspace ×3: "한그", "한ㄱ", "한", same as TextEdit. | N: fl/120852, lina/surfaces (O) | app#33 |
| NAT-40 | native: open-to-visible time | high | keep; a yes, b unknown, c unknown (measure) | app | none | Harness (Notes) 30 ABAB runs: LINA median and p90 ≤ Spotlight's. | N: legend (M4, macOS 26.6.1), rc/deepdive, dtr (O) | app#53 |
| NAT-41 | native: first-result time | high | keep; a yes, b unknown, c unknown (measure) | app | none (Files and Folders for file results: I) | Harness: last key of "safari" → first of 3 stable frames with Safari on top: LINA median ≤ Spotlight's. | N: sixcolors, legend (O) | app#27 |
| NAT-42 | native: resident memory | high (always resident) | keep; a yes, b partial, c partial | app | none | Physical footprint of all launcher processes (incl. WebKit helpers) after 1 h idle ≤ the budget in the release manifest. | N: rc/deepdive, rc/changelog, legend (O) | app#53 |
| NAT-43 | native: no blank or stale frame | high | keep; a yes, b partial (I), c partial | app | none | 120 fps capture of 100 opens: no frame shows an empty panel or the previous query. | N: rc/deepdive, rc/changelog (2.5) (O) | app#32 |
| NAT-44 | native: menu bar extra | mid | keep; a yes, b yes, c yes | app | none | Icon opens LINA's menu; hiding it in Settings persists across restart. | N: apple/menubar, rc/settings, pub, wails/src (O) | app#49 |
| NAT-45 | native: notifications | mid | keep; a yes, b yes, c yes | app+Core | Notifications | Reply arrives with panel closed: banner's "open in conversation" opens that conversation. | N: apple/notif, pub, wails/src (O) | app#16 |
| NAT-46 | native: Focus filter | low (I: few set filters) | keep; a yes, b yes (Swift in Runner), c partial (separate Swift target) | app | none | Add LINA's filter to Work Focus; turning Work on changes launcher behavior within 2 s. | N: apple/focus (O) | app#51 |
| NAT-47 | native: Services menu | low (I: Services little used) | keep; a yes, b yes (Swift), c partial | app | none | Select text in TextEdit > Services > Send to LINA: panel opens with it as a draft. | N: apple/services, wails/src (O) | app#50 |
| NAT-48 | native: Share sheet | low | keep; a yes, b yes, c partial | app | none | Select a file result, share key: system share popover anchored to the row; AirDrop works. | N: apple/share, pub, wails/src (O) | app#50 |
| NAT-49 | native: drag out of results | low (I) | keep; a yes, b yes, c partial | app | none | Drag a file result into a Mail message: file attaches; panel stays open until the drop. | N: apple/drag, pub, rc/deepdive, wails/src (O) | app#54 |
| NAT-50 | native: VoiceOver labels | low (I) | keep; a yes, b partial, c partial | app | none | VoiceOver on, ⌘Space, type "saf", Down: VoiceOver says "Safari, application, 1 of N". | N: apple/ax, fl/191001, fl/191013, rc/deepdive (O) | app#54 |
| NAT-51 | native: launch at login | high (I: must be resident) | keep; a yes, b yes, c yes | app | none | Reboot and log in: ⌘Space opens the panel within 5 s without starting LINA by hand. | N: apple/policy (O) | app#31 |
| NAT-52 | native: tokens from one DTCG source | high | keep; a yes, b yes (Dart emitter), c yes (CSS exists) | app | none | Editing `design/tokens/light.json` without regenerating makes `node scripts/tokens.mjs --check` fail. | N: apple/dyncolor, lina/surfaces (I) | #1010 |

### Hotkey and the switch from Spotlight

8 rows: 6 keep, 1 replace, 0 later, 1 drop.

| ID | Feature | Use | Decision | Runs | Permission | Acceptance check | Source | Issue |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| HOT-01 | hotkey: ⌘Space opens launcher | high: ⌘Space is Spotlight's default (apple/sp-keys) | keep | app | none | With Spotlight's shortcut off, 20 presses of ⌘Space all show LINA, never Spotlight | S: raycast/settings (Raycast v2); apple/sp-keys (macOS 27) (O) | app#35 |
| HOT-02 | hotkey: detect Spotlight still owns ⌘Space | high: I: needed at every setup | keep | app | none (I) | Turn Spotlight's shortcut back on: LINA shows a warning on its next open; turn it off: the warning goes | S: search/symhotkeys (pre-26) (I) | app#35 |
| HOT-03 | hotkey: input-source conflict | mid: Raycast says multi-language Macs usually conflict; the owner uses Korean IME | keep | app | none | Set "Select previous input source" to ⌘Space: LINA names the conflict and opens the pane | S: raycast/settings (v2); alfred/cmd-space (O) | app#35 |
| HOT-04 | hotkey: Siri hold-⌘Space conflict | mid: I: some Macs have it by default | keep | app | none | With the Siri key on hold ⌘Space, LINA shows a hint; open time is measured both ways | S: raycast/settings (v2) (O) | app#35 |
| HOT-05 | hotkey: other Spotlight keys on ⌘Space | low: I: only after manual remaps | keep | app | none | Assign ⌘Space to Spotlight > Show Apps: LINA warns | S: macmost/show-apps (macOS 26); mjtsai/2026-02 (I) | app#35 |
| HOT-06 | hotkey: recheck after macOS update | low: I: reported after updates | keep | app | none | After an update that re-enables Spotlight's key, the first LINA open shows the switch prompt | S: search/reenable (macOS 26) (I) | app#35 |
| HOT-07 | hotkey: silent auto-disable | none | drop: private prefs file, no Apple API | — | none | LINA never writes ~/Library/Preferences/com.apple.symbolichotkeys.plist (its mtime stays the same) | S: search/symhotkeys (I) |  |
| HOT-08 | hotkey: Finder search window | low: I | replace: macOS itself | — | none | With LINA running, ⌥⌘Space still opens Finder search | S: apple/sp-keys (macOS 27) (O) |  |

### Root search, keys and settings

61 rows: 50 keep, 2 replace, 5 later, 4 drop.

| ID | Feature | Use | Decision | Runs | Permission | Acceptance check | Source | Issue |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| ROOT-01 | root: launcher hotkey | high: entry to everything (Q) | keep | app | none (I: RegisterEventHotKey); Raycast says Accessibility | From any app, press the hotkey; panel shows with caret in the field within one frame of Spotlight's time | B: M-settings (Raycast 2.9) (O) | app#35 |
| ROOT-02 | root: replace Spotlight on ⌘Space | high: manual recommends (M) | keep: guided switch and way back | app | none | Run the guided switch; ⌘Space opens LINA; "restore" turns Spotlight's shortcut back on | B: M-settings (Raycast 2.9) (O) | app#35 |
| ROOT-03 | root: hotkey conflict guide | mid: I: common for Korean typists | keep | app | none | With Input Sources on ⌘Space, LINA names the clash and opens that settings row | B: M-settings (Raycast 2.9) (O) | app#35 |
| ROOT-04 | root: Replace Spotlight command | low: one-time | keep: one-click guided switch | app | none | Run "Replace Spotlight"; System Settings opens at the Spotlight shortcut row | B: CL-v1 (1.100), M-settings (2.9) (I) | app#35 |
| ROOT-05 | root: search as you type | high: core of launcher (Q) | keep | app | none | Type "msg"; Messages is first before pressing Enter | B: M-search (Raycast 2.9) (O) | app#27 |
| ROOT-06 | root: ranking order | high: every search | keep | app | none | Alias "gc" on Chrome; typing "gc" puts Chrome first | B: M-search (Raycast 2.9) (O) | app#27 |
| ROOT-07 | root: frecency per query | high: every search | keep | app | none | Pick Calculator for "cal" 5 times; "cal" then lists Calculator above Calendar | B: M-search (Raycast 2.9) (O) | app#27 |
| ROOT-08 | root: exact-word boost | high: every search | keep | app | none | Type "Notes"; the app Notes ranks above "Search Notes" | B: CL-2.x (2.7) (O) | app#27 |
| ROOT-09 | root: reset ranking | low: I | keep | app | none | Reset ranking on an item; it returns to its default place | B: M-action (Raycast 2.9) (O) | app#27 |
| ROOT-10 | root: search sensitivity | low: I | drop: one tuned default | app | none | n/a (dropped) | B: M-settings (Raycast 2.9) (O) |  |
| ROOT-11 | root: favorites | high: manual tip | keep | app+Core | none | Add Safari to favorites; empty panel shows it first | B: M-search (Raycast 2.9) (O) | app#38 · #1025 |
| ROOT-12 | root: reorder favorites | mid: I | keep | app+Core | none | ⇧⌘↓ on first favorite moves it to second | B: M-keys (Raycast 2.9) (O) | app#38 · #1025 |
| ROOT-13 | root: favorites in compact mode | low: I | later: after compact layout | app | none | Compact bar with empty query shows favorite icons | B: M-settings (Raycast 2.9) (O) |  |
| ROOT-14 | root: alias per command | mid: M; PED examples (et, tr) | keep | app+Core | none | Set alias "et" on Empty Trash; typing "et" ranks it first | B: M-hotkeys (Raycast 2.9) (O) | app#38 · #1025 |
| ROOT-15 | root: alias then space focuses argument | mid: I | keep | app+Core | none | Type "g " for a search quick link; caret is in the query field | B: M-hotkeys (Raycast 2.9) (O) | app#38 · #1025 |
| ROOT-16 | root: hotkey per command | high: Q step "Set a Hotkey" | keep | app+Node | none (I) | Assign ⌥S to Slack; ⌥S opens Slack from another app | B: M-hotkeys (Raycast 2.9) (O) | app#38 · #1025 |
| ROOT-17 | root: set hotkey or alias from ⌘K | mid: I | keep | app+Core | none | ⇧⌘, on an item opens the recorder; saved hotkey works at once | B: M-action, M-v2 (Raycast 2.9) (O) | app#38 · #1025 |
| ROOT-18 | root: hotkey recorder | mid: I | keep | app | none | Record ⌥⌘A; it saves without pressing Enter after the countdown | B: M-hotkeys (Raycast 2.9) (O) | app#35 |
| ROOT-19 | root: hotkey conflict check | mid: I | keep | app+Core | none | Record a used combo; the owner is shown; overwrite moves it | B: M-hotkeys (Raycast 2.9) (O) | app#38 · #1025 |
| ROOT-20 | root: modifier-only hotkeys | low: I | later: needs event tap | app | Input Monitoring or Accessibility (I) | Right ⌘ tap opens LINA; Left ⌘ shortcuts still work | B: M-hotkeys (Raycast 2.9) (O) |  |
| ROOT-21 | root: double-tap modifier | low: PED example | later: needs event tap | app | Input Monitoring or Accessibility (I) | Double-tap ⌥ opens LINA; single ⌥ use is unaffected | B: M-hotkeys (Raycast 2.9) (O) |  |
| ROOT-22 | root: fn/globe and F13–F18 as single key | low: I | later | app | Input Monitoring (I) | fn tap opens LINA after the macOS globe setting is changed | B: M-hotkeys (Raycast 2.9) (O) |  |
| ROOT-23 | root: physical vs key-equivalent hotkeys | mid: I: Korean layout users | keep: physical by default | app | none | With Korean 2-Set active, ⌥S hotkey still fires | B: M-hotkeys (Raycast 2.9) (O) | app#35 |
| ROOT-24 | root: shortcuts list | mid: I | keep | app+Core | none | Filter "Hotkey set" lists only commands with hotkeys | B: M-hotkeys (Raycast 2.9) (O) | app#38 · #1025 |
| ROOT-25 | root: disable command | low: I | keep | app+Core | none | Disable Sleep; typing "sleep" no longer shows it | B: M-action (Raycast 2.9) (O) | app#38 · #1025 |
| ROOT-26 | root: copy deeplink | low: I | keep: lina:// link | app | none | Copy link of a command; opening it asks to confirm, then runs | B: M-action (Raycast 2.9) (O) | app#55 |
| ROOT-27 | root: action panel ⌘K | high: Q "unlocks much of Raycast" | keep | app | none | ⌘K on an app lists Open, Show in Finder, Quit | B: M-action (Raycast 2.9) (O) | app#34 |
| ROOT-28 | root: search in action panel | mid: I | keep | app | none | ⌘K then "qu" leaves Quit actions only | B: M-action (Raycast 2.9) (O) | app#34 |
| ROOT-29 | root: primary, secondary, tertiary action | high: every item | keep | app | none | On an app: ↵ opens, ⌘↵ reveals in Finder | B: M-keys (Raycast 2.9) (O) | app#34 |
| ROOT-30 | root: action keys shown | mid: I | keep | app | none | ⌘K shows the key next to each action | B: M-action (Raycast 2.9) (O) | app#34 |
| ROOT-31 | root: number hotkeys ⌘1–9 | mid: owner asks for ⌘1–9 (LINA-963) | keep | app | none | Hold ⌘; numbers show; ⌘2 runs the second result | B: CL-beta, CL-2.x (2.6) (I) | app#34 |
| ROOT-32 | root: search history ↑ | low: I | keep | app | none | Search "foo", close, reopen, ↑ shows "foo" | B: M-keys, M-settings (Raycast 2.9) (O) | app#34 |
| ROOT-33 | root: Esc behavior | high: every use | keep | app | none | Esc with text clears; second Esc hides panel | B: M-search, M-settings (Raycast 2.9) (O) | app#34 |
| ROOT-34 | root: Backspace goes back | mid: I | keep | app | none | In a command, empty field plus Backspace returns to root | B: M-search (Raycast 2.9) (O) | app#34 |
| ROOT-35 | root: ⌘Esc pop to root | low: PED | keep | app | none | Two levels deep, ⌘Esc shows root | B: M-keys (Raycast 2.9) (O) | app#34 |
| ROOT-36 | root: pop to root timing | mid: I | keep | app | none | Set 60 s; reopen at 30 s shows same view, at 90 s shows root | B: M-settings (Raycast 2.9) (O) | app#34 |
| ROOT-37 | root: compact mode | mid: I: Spotlight-like | keep: Spotlight-like default | app | none | Empty query shows only the bar; first key expands the list | B: M-search, M-settings (Raycast 2.9) (O) | app#32 |
| ROOT-38 | root: interface size | low: I | keep: follow macOS text size (I) | app | none | Raise macOS text size; panel text grows | B: M-settings (Raycast 2.9) (O) | app#32 |
| ROOT-39 | root: follow system appearance | high: default | keep | app | none | Toggle macOS dark mode; panel follows without restart | B: M-settings (Raycast 2.9) (O) | app#32 |
| ROOT-40 | root: themes and Theme Studio | low: Pro only (I) | drop: native look follows system | app | none | n/a (dropped) | B: M-themes (Raycast 2.9) (O) |  |
| ROOT-41 | root: Switch Theme command | low: I | drop: native look | app | none | n/a (dropped) | B: M-themes (Raycast 2.9) (O) |  |
| ROOT-42 | root: screen to show on | mid: I: multi-display | keep | app | none | Mouse on second display; panel opens there | B: M-settings (Raycast 2.9) (O) | app#31 |
| ROOT-43 | root: Spaces behavior | low: I | keep: move to current Space | app | none | From a full-screen app, panel opens over it on that Space | B: M-settings (Raycast 2.9) (O) | app#31 |
| ROOT-44 | root: auto-switch input source | mid: I: Korean typists | keep | app | none (I) | Korean active; open panel: ABC; close: Korean again | B: M-settings (Raycast 2.9) (O) | app#33 |
| ROOT-45 | root: Emacs bindings | low: I | keep | app | none | ⌃N moves selection down | B: M-keys (Raycast 2.9) (O) | app#34 |
| ROOT-46 | root: Vim bindings | low: I | later | app | none | With Vim on, ⌃J moves down | B: M-keys (Raycast 2.9) (O) |  |
| ROOT-47 | root: paging and section jumps | low: I | keep | app | none | ⌘↓ jumps to next section header | B: M-keys, CL-2.x (2.7) (O) | app#34 |
| ROOT-48 | root: fallback commands | high: I: every no-match query | replace: send line to agent conversation, plus web search link | app+Core | none | Type a sentence with no match; ↵ sends it to the last agent's conversation | B: M-settings, M1-fallback (Raycast 2.9) (O) | app#36 · #1011 |
| ROOT-49 | root: Tab to ask | mid: I | replace: Tab sends to agent conversation | Core | none | Type a question, Tab; short reply shows with "open in conversation" | B: M-quickstart, CL-2.x (2.1) (O) | app#36 · #1011 |
| ROOT-50 | root: URL detection | mid: I | keep | app | none | Type "github.com"; ↵ opens https://github.com in default browser | B: M-search (Raycast 2.9) (O) | app#27 |
| ROOT-51 | root: command arguments | mid: I | keep | app+Core | none | Quick link with 2 arguments shows 2 fields; ↵ fills both | B: M-search (Raycast 2.9) (O) | app#38 · #1025 |
| ROOT-52 | root: today's events when empty | high: WR Calendar 511/yr | keep | Node | Calendars | Meeting in 5 min shows first in the empty panel | B: M-search, M-calendar (Raycast 2.9) (O) | app#65 · #1018 |
| ROOT-53 | root: System Settings panes | mid: I: Spotlight replacement | keep | app+Node | none | Type "zoom"; ↵ opens Accessibility › Zoom | B: M-search, CL-2.x (2.6) (O) | app#63 · #1018 |
| ROOT-54 | root: alternate app names | mid: I | keep | app | none | Type "preferences"; System Settings is listed | B: M-search, CL-2.x (2.5) (O) | app#27 |
| ROOT-55 | root: localized app names | high: I: owner uses Korean | keep | app | none | macOS in Korean: "계산기" and "calculator" both find Calculator | B: CL-2.x (2.7), CL-beta (0.65) (O) | app#27 |
| ROOT-56 | root: Contacts in results | low: I | keep: off by default | Node | Contacts | Turn on; typing a contact's name lists the contact | B: M-settings (Raycast 2.9) (O) | app#64 · #1018 |
| ROOT-57 | root: show in menu bar | mid: I | keep | app | Menu Bar allow (macOS 26+) | Icon shows; its menu opens Settings | B: M-settings, M-trouble (Raycast 2.9) (O) | app#49 |
| ROOT-58 | root: open at login | high: I | keep | app | Login Items (I) | After reboot, hotkey works without opening the app | B: M-settings (Raycast 2.9) (O) | app#31 |
| ROOT-59 | root: settings search | low: I | keep | app | none | ⌘F "sensitivity" jumps to that setting | B: M-settings (Raycast 2.9) (O) | app#27 |
| ROOT-60 | root: favicon provider | low: I | keep: site itself or none (I) | app | none | With None, no request goes to a third-party icon host | B: M-settings (Raycast 2.9) (O) | app#47 |
| ROOT-61 | root: proxy and custom CA | low: I | drop: launcher needs little network (I) | app | none | n/a (dropped) | B: M-settings, CL-2.x (2.8) (O) |  |

### Applications

22 rows: 14 keep, 0 replace, 7 later, 1 drop.

| ID | Feature | Use | Decision | Runs | Permission | Acceptance check | Source | Issue |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| APP-01 | app: open | high: PED "launching apps"; WR 2,508/yr | keep | Node | none | Type "saf" ↵; Safari is frontmost; receipt recorded | B: M-apps (Raycast 2.9) (O) | app#58 · #1013 |
| APP-02 | app: app sources | mid: I | keep | app | none | Mount a DMG; its app appears without restart | B: M-search, CL-2.x (2.3) (O) | app#58 · #1013 |
| APP-03 | app: same-name apps | low: I | keep | app | none | Two Xcode copies show different paths | B: CL-2.x (2.2) (O) | app#58 · #1013 |
| APP-04 | app: show in Finder | mid: I | keep | Node | none | ⌘↵ on Safari selects it in a Finder window | B: M-apps (Raycast 2.9) (O) | app#59 · #1013 |
| APP-05 | app: show info in Finder | low: I | later | Node | Automation (Finder) (I) | ⇧⌘I opens Safari's Info window | B: M-apps (Raycast 2.9) (O) |  |
| APP-06 | app: show package contents | low: I | later | Node | none | ⌥⌘I opens Safari.app/Contents in Finder | B: M-apps (Raycast 2.9) (O) |  |
| APP-07 | app: copy path, name, bundle id | low: I | keep | app | none | ⇧⌘C on Safari puts com.apple.Safari on clipboard | B: M-apps (Raycast 2.9) (O) | app#59 · #1013 |
| APP-08 | app: hide | low: I | keep | Node | none (I) | ⇧⌘H on Mail hides Mail | B: M-apps (Raycast 2.9) (O) | app#59 · #1013 |
| APP-09 | app: quit | mid: I | keep | Node | none (I) | Quit TextEdit from panel; it exits; receipt recorded | B: M-apps (Raycast 2.9) (O) | app#59 · #1013 |
| APP-10 | app: force quit | low: I: macOS ⌥⌘Esc exists | keep: with confirm | Node | none (I) | Force quit a hung test app; it exits after confirm | B: M-apps (Raycast 2.9) (O) | app#59 · #1013 |
| APP-11 | app: restart app | low: I | keep | Node | none (I) | ⌘R on Notes; Notes relaunches | B: M-apps (Raycast 2.9) (O) | app#59 · #1013 |
| APP-12 | app: Restart Frontmost App | low: new | later | Node | none (I) | Run it from Safari; Safari relaunches | B: CL-2.x (2.9) (O) |  |
| APP-13 | app: uninstall | low: I | later: destructive | Node | admin for some apps (I) | Uninstall a test app; it is in Trash; receipt recorded | B: M-apps (Raycast 2.9) (O) |  |
| APP-14 | app: hotkey toggles visibility | mid: I | keep | Node | none (I) | Press Slack hotkey twice: Slack shows, then hides | B: M-apps (Raycast 2.9) (O) | app#59 · #1013 |
| APP-15 | app: search scopes | low: I | keep | app | Files and Folders for extra folders (I) | Add ~/Apps; an app there appears | B: M-apps (Raycast 2.9) (O) | app#58 · #1013 |
| APP-16 | app: per-app alias, hotkey, disable | mid: I | keep | app+Core | none | Untick an app; it no longer appears in results | B: M-apps (Raycast 2.9) (O) | app#38 · #1025 |
| APP-17 | app: auto quit per app | low: I | later: standing grant for background quits | Node | none (I) | Enable for Preview, 1 min; Preview quits 1 min after leaving it | B: M-autoquit (Raycast 2.9) (O) |  |
| APP-18 | app: auto quit timeout | low: I | later | Node | none (I) | Change default to 5; apps on default show 5 | B: M-autoquit (Raycast 2.9) (O) |  |
| APP-19 | app: auto quit safety | low: I | later | Node | none (I) | During a call recording, the app is not quit | B: M-autoquit (Raycast 2.9) (O) |  |
| APP-20 | app: all-apps grid or list | mid: Launchpad was removed in macOS 26 | keep | app+Node | none | ⌘1 lists every app in /Applications and /System/Applications | S: idb/tahoe-sp; macrumors/tahoe-sp (macOS 26) (I) | app#58 · #1013 |
| APP-21 | app: recent apps first | mid: I | keep | app | none | After opening Notes, Notes is first in Apps mode | S: search/27-reviews (macOS 27) (I) | app#58 · #1013 |
| APP-22 | app: iPhone apps | low: I | drop: no public API | — | none | No iPhone apps are listed | S: apple/newsroom-26 (macOS 26) (O) |  |

### Files

63 rows: 37 keep, 6 replace, 12 later, 8 drop.

| ID | Feature | Use | Decision | Runs | Permission | Acceptance check | Source | Issue |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| FILE-01 | file: name search in root | high: v2 headline feature (rc-blog); a core Spotlight use | keep: NSMetadataQuery on display name and file name | Node | none; Files and Folders for Desktop, Documents, Downloads (I) | With ~/Documents/invoice.pdf present, type `invoice`: the file appears, and the first-result time is no slower than Spotlight's on the same Mac. | F: rc-fs, rc-blog (Raycast 2.9); ap-find (macOS 27) (O) | app#60 · #1014 |
| FILE-02 | file: index source | high: affects every file query | keep: Spotlight via NSMetadataQuery first; build an own index only if the bar fails | Node | none (I); Full Disk Access if LINA builds its own index | The same 20 name queries in LINA and in Spotlight give the same top-3 files. | F: rc-tech, rc-fs (Raycast 2.9) (O) | app#60 · #1014 |
| FILE-03 | file: Search Files view with details | mid: I, root search covers most uses | keep: details pane for the selected result | Node | same as name search | Select a PDF: the pane shows kind, size, modified date and a thumbnail. | F: rc-fs (Raycast 2.9); rc-cl 2.7; rc-clb 0.71 (O) | app#60 · #1014 |
| FILE-04 | file: recent files on empty query | high: Downloads Manager 86,793 and Recents 12,799 installs | keep: sort by kMDItemLastUsedDate | Node | none (I) | Open a file in Preview, then open the launcher empty: the file is in the top 5. | F: rc-fs, rc-sb (Raycast 2.9); rc-cl 2.2 (O) | app#60 · #1014 |
| FILE-05 | file: exact-term ranking | high: I, every query | keep: rank exact > prefix > content, then recency | app | none | With `plan.md` and `old/x/plan-notes.md`, typing `plan` ranks plan.md first. | F: rc-cl 2.2, 2.3 (O) | app#27 |
| FILE-06 | file: typo-tolerant match | mid: I | later: Spotlight parity first | app | none | `invocie` finds invoice.pdf. | F: rc-cl 2.8; rc-fs (Raycast 2.9) (O) |  |
| FILE-07 | file: content search (full text) | mid: off by default in Raycast | keep: kMDItemTextContent query; content hits listed after name hits | Node | none; Files and Folders (I) | A phrase that exists only inside a .txt file finds that file within 1 s. | F: rc-fs (Raycast 2.9); rc-clb 0.67; rc-cl 2.7; ap-find (macOS 27) (O) | app#61 · #1014 |
| FILE-08 | file: kind filter | mid: I | keep: Spotlight syntax `/pdf` and `kind: | Node | none | `kind:pdf tax` lists only PDFs, and `/image beach` lists only images. | F: rc-v2, rc-clb 0.51 (Raycast 2.9); ap-filter (macOS 27) (O) | app#61 · #1014 |
| FILE-09 | file: metadata keywords | low: I | later: map to kMDItem attributes | Node | none | `tag:red` lists only files with the red tag. | F: ap-filter (macOS 27) (O) |  |
| FILE-10 | file: location filter | low: I | later: `in:<folder>` scope | Node | none | `in:Downloads pdf` returns only files under ~/Downloads. | F: ap-filter (macOS 27) (O) |  |
| FILE-11 | file: search inside an app | mid: I | replace: plugin or agent conversation (other apps' CoreSpotlight data cannot be queried) | Core | per plugin | `find mail from Kim about invoice` goes to the agent, and the reply links the mail. | F: ap-filter (macOS 27) (O) |  |
| FILE-12 | file: path typing and browse | mid: I | keep | Node | Files and Folders for protected folders (I) | Type `~/Downloads/`: its contents list, and ↵ opens it in Finder. | F: rc-fs (Raycast 2.9); rc-clb 0.62 (O) | app#60 · #1014 |
| FILE-13 | file: parent folder (⇧Tab) | low: I | keep | app | none | In `~/Downloads/a/`, ⇧Tab shows `~/Downloads/`. | F: rc-sb (Raycast 2.9) (O) | app#60 · #1014 |
| FILE-14 | file: sort folder | low: I | later: Finder covers it | app | none | While browsing a folder, ⇧⌘P set to Date Modified puts the newest file first. | F: rc-fs (Raycast 2.9) (O) |  |
| FILE-15 | file: Quick Look | mid: I, Spotlight Space habit | keep: QLPreviewPanel on Space after ↓, and on ⌘Y | Node | none; needs read access to the file (I) | ↓ to a PDF, press Space: Quick Look opens; Space closes it. While typing a query, Space types a space. | F: rc-ks, rc-fs (Raycast 2.9); ap-keys, ap-find (macOS 27) (O) | app#62 · #1015 |
| FILE-16 | file: open | high: primary action | keep: NSWorkspace.open | Node | none | ↵ on a .pdf opens it in the default PDF app. | F: rc-ap (Raycast 2.9); ap-keys (macOS 27) (O) | app#62 · #1015 |
| FILE-17 | file: open with | mid: I | keep: urlsForApplications(toOpen:) list | Node | none | ⌥⌘O on a .md lists the apps that can open it, and choosing one opens it there. | F: rc-ks, rc-ap (Raycast 2.9) (O) | app#62 · #1015 |
| FILE-18 | file: reveal in Finder | high: I, core Spotlight habit | keep: activateFileViewerSelecting; bind ⌘R and ⌘↩ | Node | none | ⌘R on a result opens Finder with the file selected. | F: rc-ks, rc-ap (Raycast 2.9); ap-keys (macOS 27) (O) | app#62 · #1015 |
| FILE-19 | file: show path | mid: I | keep | app | none | Holding ⌘ on a result shows the full path, and releasing hides it. | F: ap-find, ap-keys (macOS 27) (O) | app#62 · #1015 |
| FILE-20 | file: show info in Finder | low: I | later | Node | Automation (Finder) (I) | ⌘I on a result opens the Get Info window. | F: rc-cl 2.7 (O) |  |
| FILE-21 | file: copy path | mid: I | keep | app | none | Copy Path puts `/Users/<me>/…/file.pdf` on the clipboard. | F: rc-ap (Raycast 2.9) (O) | app#62 · #1015 |
| FILE-22 | file: copy file | mid: I | keep | app | none | Copy File, then ⌘V in a Finder window, creates a copy there. | F: rc-cffs (v1-era) (I) | app#62 · #1015 |
| FILE-23 | file: copy name | low: I | keep | app | none | Copy Name puts `file.pdf` on the clipboard. | F: rc-cffs (v1-era) (I) | app#62 · #1015 |
| FILE-24 | file: move to Trash | low: I | keep: NSWorkspace.recycle, with confirm | Node | Files and Folders for protected folders (I) | Trash a test file: it lands in the Trash, leaves the list, and a receipt exists. | F: rc-ap, rc-ks (Raycast 2.9) (O) | app#62 · #1015 |
| FILE-25 | file: share | low: I | keep: NSSharingServicePicker | app | none | Share on a file opens the share menu, and AirDrop lists the file. | F: rc-fs (Raycast 2.9) (O) | app#50 |
| FILE-26 | file: save as duplicate | low: I | drop: Finder does it | — | — | n/a (dropped) | F: rc-fs (Raycast 2.9) (O) |  |
| FILE-27 | file: open folder in Terminal | low-mid: Terminal Finder 25,480 installs | later: quick link with "open with" | Node | none | On a folder result, "Open in Terminal" opens a terminal at that path. | F: rc-fs (Raycast 2.9) (O) |  |
| FILE-28 | file: save as quick link | low: I | keep: LINA quick links | app | none | Save a file as `taxes`: typing `taxes` ↵ opens the file. | F: rc-fs (Raycast 2.9) (O) | app#60 · #1014 |
| FILE-29 | file: send to AI | mid: I | replace: agent conversation (attach to the message) | app+Core | none | "Send to agent" opens the main conversation with the file attached, and nothing is sent before Return. | F: rc-fs (Raycast 2.9) (O) |  |
| FILE-30 | file: natural-language find | mid: I | replace: agent conversation plus a Node file tool | Core+Node | Files and Folders (I) | "the PDF I downloaded last week from Apple" ↵: the agent replies with the file link, and a receipt shows the query. | F: rc-fs (Raycast 2.9) (O) |  |
| FILE-31 | file: Finder selection | low-mid: I | replace: agent conversation ("send Finder selection") | Core+Node | Automation (Finder) (I) | Select 2 files in Finder and run "Send Finder selection": the draft message has both attached. | F: rc-api-env, rc-fs (Raycast 2.9) (O) |  |
| FILE-32 | file: search scopes | mid: I | keep: scopes map to searchScopes | app+Node | Files and Folders per added folder (I) | Add /Volumes/Ext/Projects: its files appear in results. | F: rc-fs (Raycast 2.9) (O) | app#60 · #1014 |
| FILE-33 | file: ignore patterns | low-mid: I, developers | later: filter results by glob | app | none | With `*.log` set, `server` shows no .log files. | F: rc-fs (Raycast 2.9) (O) |  |
| FILE-34 | file: respect .gitignore | low-mid: I, developers | later: filter inside git repos | app | none | A file in an ignored `dist/` never appears. | F: rc-fs (Raycast 2.9) (O) |  |
| FILE-35 | file: .rayignore and "Exclude from Index" | low: I | drop: use Spotlight Search Privacy | — | — | n/a (dropped) | F: rc-fs (Raycast 2.9) (O) |  |
| FILE-36 | file: hidden files | low: I | later | app | none | With hidden files on, `.zshrc` appears. | F: rc-fs, rc-ks (Raycast 2.9) (O) |  |
| FILE-37 | file: network and removable drives | low-mid: I | later: indexed network scope | Node | Removable Volumes and Network Volumes (Files and Folders) (I) | Plug in an indexed USB drive: its files appear without a restart. | F: rc-fs (Raycast 2.9); rc-cl 2.5 (O) |  |
| FILE-38 | file: keep files from removed volumes | low: I | drop: stale results | — | — | n/a (dropped) | F: rc-fs (Raycast 2.9) (O) |  |
| FILE-39 | file: auto-exclude churn folders | low: I | drop: no own index | — | — | n/a (dropped) | F: rc-fs (Raycast 2.9); rc-cl 2.3 (O) |  |
| FILE-40 | file: low-disk-space block | low: I | drop: no own index | — | — | n/a (dropped) | F: rc-fs (Raycast 2.9) (O) |  |
| FILE-41 | file: index now and stop indexing | low: I | drop: Spotlight does the indexing | — | — | n/a (dropped) | F: rc-fs (Raycast 2.9) (O) |  |
| FILE-42 | file: index health warning | low-mid: I | keep | app | none | Add ~/Documents to Spotlight Search Privacy: LINA shows the warning and the button opens Spotlight settings. | F: rc-fs (Raycast 2.9) (O) | app#60 · #1014 |
| FILE-43 | file: reset settings | low: I | keep: cheap | app | none | Reset restores the scopes to Home plus /Applications. | F: rc-fs (Raycast 2.9) (O) | app#60 · #1014 |
| FILE-44 | file: files in root toggle | low: I | keep | app | none | Toggle off: `invoice` shows no file rows. | F: rc-fs, rc-set (Raycast 2.9) (O) | app#60 · #1014 |
| FILE-45 | file: honor .metadata_never_index | low: I | keep: Spotlight already honors it (I) | app | none | A folder with the marker never shows its files. | F: rc-cl 2.5 (O) | app#60 · #1014 |
| FILE-46 | file: do not hydrate cloud files | mid: I, iCloud Desktop and Documents are common | keep | app | none | Search an evicted iCloud file: it stays not downloaded until opened. | F: rc-cl 2.1 (O) | app#60 · #1014 |
| FILE-47 | file: Spotlight Search Privacy | low: I | replace: macOS itself (LINA results honor it) | app | none | Add a folder to Search Privacy: its files leave LINA's results. | F: ap-privacy (macOS 27) (O) |  |
| FILE-48 | file: Spotlight result categories | low: I | keep: category toggles (files, folders, settings, calculator) | app | none | Turn off Folders: no folder rows appear. | F: ap-settings, ap-cats (macOS 27) (O) | app#60 · #1014 |
| FILE-49 | file: Finder search window | low: I | drop: macOS itself | — | — | n/a (dropped) | F: ap-keys (macOS 27) (O) |  |
| FILE-50 | file: Korean file names (NFD/NFC) | high for owner: I | keep: normalize the query and names | app | none | A Finder-created `한글 메모.txt` is found by `한글`, also with the IME still composing `한`. | F: rc-clb 0.61, 0.64, 0.66 (O) | app#60 · #1014 |
| FILE-51 | file: Korean initial-consonant search | mid: I, common in Korean apps | later | app | none | `ㅎㄱ` lists `한글.txt`. | F: none (I) |  |
| FILE-52 | filter: dynamic category chips | mid: I | keep | app | none | Type "screen": a Screenshots chip appears, and clicking it limits the results | S: apple/sp-narrow (macOS 27) (O) | app#61 · #1014 |
| FILE-53 | filter: slash kind | low: I: power users | keep | app+Node | none | "/pdf invoice" shows only PDFs | S: apple/sp-keys; apple/sp-narrow (macOS 27) (O) | app#61 · #1014 |
| FILE-54 | filter: kind: keyword | low: I | keep (file kinds) | Node | none | "kind:folder src" shows only folders | S: apple/sp-narrow (macOS 27) (O) | app#61 · #1014 |
| FILE-55 | filter: metadata keywords | low: I | keep (file attributes) | Node | none | "tag:red" returns only files with the red tag | S: apple/sp-narrow (macOS 27) (O) | app#61 · #1014 |
| FILE-56 | filter: location scope | low: I | keep | Node | none | "Downloads" Tab "invoice" returns only files under ~/Downloads | S: apple/sp-narrow (macOS 27) (O) | app#61 · #1014 |
| FILE-57 | filter: inside a folder | low: I | keep | Node | Files and Folders (I) | Tab on folder "src", then type "main": only files under src | S: idb/tahoe-sp (macOS 26) (I) | app#61 · #1014 |
| FILE-58 | filter: inside an app | mid: I | replace: agent conversation (plugin); other apps' Core Spotlight data is private | Core | per plugin | "Mail" Tab offers "ask agent to search Mail" | S: apple/sp-narrow (macOS 27) (O) |  |
| FILE-59 | file: third-party cloud files | low: I: depends on the provider | later: unknown whether NSMetadataQuery sees them | Node | none (I) | Test: a cloud-only Google Drive file is found by its name | S: apple/newsroom-26 (macOS 26) (O) |  |
| FILE-60 | privacy: result source toggles | low: I | keep | app | none | Turn off Contacts: no contacts appear in results | S: apple/sp-cats (macOS 27) (O) | app#60 · #1014 |
| FILE-61 | privacy: own exclusion list | mid: I | keep | app | none | Add "node_modules": no result path contains it | S: apple/sp-privacy (macOS 27); raycast/file-search (v2) (O) | app#60 · #1014 |
| FILE-62 | privacy: queries stay on the Mac | high: I | keep: only web or agent picks leave the Mac | app | none | A network monitor sees no calls while typing 20 queries (except currency rates) | S: apple/sp-settings (macOS 27) (O) | app#60 · #1014 |
| FILE-63 | privacy: Siri Suggestions | low: I | drop: LINA has no Apple suggestions | — | none | Not in LINA | S: search summaries (macOS 26) (I) |  |

### System data from Spotlight

23 rows: 9 keep, 11 replace, 3 later, 0 drop.

| ID | Feature | Use | Decision | Runs | Permission | Acceptance check | Source | Issue |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| SD-01 | settings: find a pane | high: I: a common Spotlight use | keep (x-apple.systempreferences URLs) | Node | none | "blue" Return opens the Bluetooth pane on macOS 27 | S: apple/sp-narrow (kind:settings, macOS 27); gh/sidebar (macOS 26) (O) | app#63 · #1018 |
| SD-02 | settings: deep section | mid: I | keep | Node | none | "accessibility privacy" opens Privacy & Security > Accessibility on macOS 27 | S: gist/sysprefs (macOS 26.2) (I) | app#63 · #1018 |
| SD-03 | contact: search | mid: I | keep | Node | Contacts | "ashley" shows Ashley's card with her phone and email | S: apple/sp-narrow (kind:contact, macOS 27); apple/doc-contacts (O) | app#64 · #1018 |
| SD-04 | contact: open card | mid: I | keep | Node | Contacts | Return on a contact opens Contacts at that card | S: I (I) | app#64 · #1018 |
| SD-05 | contact: call, FaceTime, message, email | mid: I | keep | Node | Contacts | Tab on a contact, then "FaceTime", starts a call to that number | S: apple/sp-find; apple/sp-actions (macOS 27) (O) | app#64 · #1018 |
| SD-06 | contact: copy field | mid: I | keep | app+Node | Contacts | Copy the email, then ⌘V pastes the address | S: I (I) | app#64 · #1018 |
| SD-07 | calendar: find events | low: I | keep | Node | Calendars (full access) | "dentist" shows the next dentist event with its date, and Return opens it in Calendar | S: apple/sp-narrow (kind:event, macOS 27); apple/doc-eventkit (O) | app#65 · #1018 |
| SD-08 | reminders: find | low: I | keep | Node | Reminders (full access only) | "milk" shows the open reminder | S: apple/sp-narrow (kind:reminder, macOS 27) (O) | app#65 · #1018 |
| SD-09 | reminders: add | mid: I | replace: agent conversation | Core+Node | Reminders | "remind me 6pm call mom" puts a reminder in Reminders, with a receipt | S: apple/newsroom-26; macrumors/tahoe-sp (macOS 26) (O) |  |
| SD-10 | mail: search mail | mid: I | replace: agent conversation (mail plugin) | Core | per plugin; Automation (Mail) if local | Ask "find Ana's invoice email": the agent returns the message | S: apple/sp-find (macOS 27); mjtsai/2019-mail (O) |  |
| SD-11 | mail: send email | mid: Apple's example action | replace: agent conversation | Core+Node | Automation (Mail) (I) | "email Ana the report" shows a draft that is sent only after approval | S: apple/sp-actions (macOS 27) (O) |  |
| SD-12 | messages: search | mid: I | later: only chat.db with Full Disk Access | Node | Full Disk Access | With Full Disk Access, "pizza" finds yesterday's message | S: apple/sp-find (macOS 27); raycast-ext/messages (O) |  |
| SD-13 | messages: send | mid: I | replace: agent conversation | Core+Node | Automation (Messages) | "text Ana running late" is sent after approval, with a receipt | S: apple/newsroom-26 (macOS 26) (O) |  |
| SD-14 | notes: search | mid: I | replace: agent conversation | Core+Node | Automation (Notes) (I) | Ask "my note about visa": the agent returns the title and an open link | S: apple/sp-narrow (macOS 27) (O) |  |
| SD-15 | notes: create note or add file | low: I | replace: memo/to-do | Core | none | "memo buy stamps" lands in LINA memo | S: apple/sp-actions (macOS 27) (O) |  |
| SD-16 | photos: search | low: I | later: PhotoKit has no content search (I) | Node | Photos | Not built until decided | S: apple/sp-narrow (kind:image); apple/macos27-updates (O) |  |
| SD-17 | knowledge: weather, flights, sports | low: I | replace: agent conversation | Core | Location (I) | "weather tomorrow" goes to the agent conversation | S: macrumors/tahoe-sp (macOS 26) (I) |  |
| SD-18 | ask: Return asks the assistant | high: I: Apple's headline macOS 27 feature | replace: agent conversation | Core | none | A typed sentence plus Return goes to the last-used agent's main conversation, with a short reply in the panel | S: apple/sp-find; apple/sp-actions (macOS 27) (O) |  |
| SD-19 | ask: choose provider | low: I | replace: agent conversation (name the agent) | Core | none | "@ana question" goes to agent Ana | S: macrumors/macos27 (macOS 27) (I) |  |
| SD-20 | ask: about selected files | mid: I | replace: agent conversation (Services, Share, drag) | Core+Node | Files and Folders | Drag two PDFs onto the panel and ask "compare": both are attached to the message | S: 9to5/macos27 (macOS 27) (I) |  |
| SD-21 | ask: personal context | mid: I | replace: agent conversation + plugins | Core | per plugin | Ask "when is Ana's flight": the agent uses the mail plugin | S: apple/macos27-updates (macOS 27) (O) |  |
| SD-22 | visual: ask about screen | low: I | later: needs Screen Recording | Core+Node | Screen Recording | Not built until decided | S: apple/macos27-updates (macOS 27) (O) |  |
| SD-23 | system: LINA in Spotlight and Shortcuts | mid: I: reachable even while Spotlight stays on | keep | app+Core | none | In Spotlight, ⌘3 "Ask LINA" runs and the message lands in the conversation | S: apple/wwdc25-260 (macOS 26) (O) | app#51 |

### Actions and Shortcuts

14 rows: 6 keep, 5 replace, 2 later, 1 drop.

| ID | Feature | Use | Decision | Runs | Permission | Acceptance check | Source | Issue |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| ACT-01 | action: run app actions | mid: Apple says "hundreds of actions" | replace: Shortcuts catalog (no API to list or run another app's intents) | Node | none (I) | An app action wrapped in a Shortcut runs from LINA | S: apple/newsroom-26; apple/wwdc25-260 (macOS 26) (O) | app#66 · #1019 |
| ACT-02 | action: run a Shortcut | mid: I | keep | Node | none (I) | Typing "shortcut" lists the names from `shortcuts list`; Return runs one, and its exit code is in the receipt | S: apple/shortcuts-cli (macOS 27) (O) | app#66 · #1019 |
| ACT-03 | action: Shortcut with input | low: I | keep | Node | none | Run "Combine Images" on a selected file: the output file is written | S: apple/shortcuts-cli; apple/shortcuts-url (macOS 27) (O) | app#66 · #1019 |
| ACT-04 | action: parameter form | mid: I | keep (LINA's own actions) | app | none | In an action with two fields, Tab moves between them and Return runs | S: apple/sp-actions (macOS 27); macrumors/tahoe-sp (O) | app#66 · #1019 |
| ACT-05 | action: aliases on any item | mid: a reason Six Colors left Spotlight | keep: aliases on apps, files, links | app+Core | none | Alias "ss" on System Settings: "ss" plus Return opens it | S: sixcolors/2026-04 (macOS 26) (O) | app#38 · #1025 |
| ACT-06 | action: suggested quick keys | low: I | later: may surprise | app | none | Not built until decided | S: apple/sp-actions (macOS 27) (O) |  |
| ACT-07 | action: reset quick keys | low: I | keep | app+Core | none | "Reset aliases" empties the list after a confirm | S: apple/sp-settings (macOS 27) (O) | app#38 · #1025 |
| ACT-08 | action: personalized suggestions | low: I | later: needs usage history | app | none | Not built until decided | S: apple/newsroom-26 (macOS 26) (O) |  |
| ACT-09 | action: Use Model | low: I | replace: agent conversation | Core | none | The prompt goes to the agent conversation, and the reply shows in the panel | S: apple/sp-actions (macOS 27); apple/wwdc25-260 (O) |  |
| ACT-10 | action: change case | low: I | keep | app | none | Copy "Hello", run "upper case": the clipboard holds "HELLO" | S: apple/sp-actions (macOS 27) (O) | app#40 |
| ACT-11 | action: remove image background | low: I | replace: macOS itself (Finder Quick Action) | — | none | Not in LINA; Finder's Quick Action still works | S: apple/sp-actions (macOS 27) (O) |  |
| ACT-12 | action: create image | low: I | drop: Apple-only model | — | none | Not in LINA | S: apple/sp-actions (macOS 27) (O) |  |
| ACT-13 | action: recognize music | low: I | replace: macOS itself (Control Center) | — | none | Not in LINA | S: apple/sp-actions (macOS 27) (O) |  |
| ACT-14 | action: start timer | mid: I | replace: Shortcuts catalog | Node | none | "timer 10m" runs the Shortcut, and a notification fires at 10 min | S: macrumors/tahoe-sp (macOS 26) (I) | app#66 · #1019 |

### Windows

95 rows: 23 keep, 23 replace, 47 later, 2 drop.

macOS 15 to 27 already tile with Fn-⌃ keys: halves (←, →, ↑, ↓), fill (F), center (C) and return to previous size (R). Quarters, the Arrange layouts, full-screen tiling and the green-button menu are reached by menu or drag. Rows that macOS covers say `replace: macOS itself`.

| ID | Feature | Use | Decision | Runs | Permission | Acceptance check | Source | Issue |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| WIN-01 | window: left half | high (I: 45/143 config owners, the top command) | replace: macOS itself (Fn-⌃-←) | macOS | none | Fn-⌃-← in Safari fills exactly the left half of the display Safari is on, with the menu bar and Dock excluded | W: rc-manual (Raycast 2.9); ap-keys "Left" (macOS 27) (O) |  |
| WIN-02 | window: right half | high (I: 31/143 owners) | replace: macOS itself (Fn-⌃-→) | macOS | none | Fn-⌃-→ fills exactly the right half of that display's visible frame | W: rc-manual (Raycast 2.9); ap-keys "Right" (macOS 27) (O) |  |
| WIN-03 | window: top half | mid (I: 15/143 owners) | replace: macOS itself (Fn-⌃-↑) | macOS | none | Fn-⌃-↑ fills the top half of the visible frame, below the menu bar | W: rc-manual (Raycast 2.9); ap-keys "Top" (macOS 27) (O) |  |
| WIN-04 | window: bottom half | mid (I: 17/143 owners) | replace: macOS itself (Fn-⌃-↓) | macOS | none | Fn-⌃-↓ fills the bottom half of the visible frame, above the Dock | W: rc-manual (Raycast 2.9); ap-keys "Bottom" (macOS 27) (O) |  |
| WIN-05 | window: half cycling ½ ⅔ ⅓ | mid (I: on by default) | later: needs LINA-owned half keys | app+Node | Accessibility | Pressing the LINA left-half key 3 times gives widths of ½, ⅔ and ⅓ of the visible frame, pinned left | W: rc-manual (Raycast 2.9); rc-cl1 1.43 (Raycast 1.43); oma (Raycast 2.8.2) (O) |  |
| WIN-06 | window: half cycling across displays | low (I: opt-in) | later: next display covers | app+Node | Accessibility | Right half pressed twice on display 1 puts the window in the left half of display 2 | W: rc-manual (Raycast 2.9); oma "Cycle Displays" (Raycast 2.8.2) (O) |  |
| WIN-07 | window: maximize | high (I: 21/143 owners) | replace: macOS itself (Fn-⌃-F) | macOS | none | After Fn-⌃-F, Safari's frame equals the display's visible frame (less margins if that setting is on) | W: rc-manual (Raycast 2.9); ap-keys "Fill" (macOS 27) (O) |  |
| WIN-08 | window: almost maximize | mid (I: 12/143 owners) | keep: macOS has none | app+Node | Accessibility | Safari is centered, with the same small inset from the visible frame on all 4 sides | W: oma (Raycast 2.8.2); gh-dot (I) | app#67 · #1016 |
| WIN-09 | window: maximize height | low (I: 2/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari height equals the visible frame height, with x and width unchanged | W: rc-manual (Raycast 2.9) (O) |  |
| WIN-10 | window: maximize width | low (I: 0/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari width equals the visible frame width, with y and height unchanged | W: rc-manual (Raycast 2.9) (O) |  |
| WIN-11 | window: reasonable size | low (I: 6/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari size is min(60% of the visible frame, 1025×900 pt) | W: rc-manual (Raycast 2.9) (oma says 1024×900) (O) |  |
| WIN-12 | window: center | high (I: 23/143 owners) | replace: macOS itself (Fn-⌃-C) | macOS | none | Fn-⌃-C centers Safari on its display with its size unchanged | W: rc-manual (Raycast 2.9); ap-keys "Center" (macOS 27) (O) |  |
| WIN-13 | window: center half | mid (I: 11/143 owners) | keep: macOS has none | app+Node | Accessibility | Safari is ½ the visible width at full height, centered; the 2nd press gives ⅓ and the 3rd ⅔ | W: rc-cl1 1.50 (Raycast 1.50); oma (Raycast 2.8.2) (I) | app#67 · #1016 |
| WIN-14 | window: center third | low (I: 3/143 owners) | later: center half cycle covers | app+Node | Accessibility | Safari fills the middle third of the visible frame at full height | W: rc-manual "Center … Third" (Raycast 2.9) (O) |  |
| WIN-15 | window: center two thirds | low (I: 3/143 owners) | later: center half cycle covers | app+Node | Accessibility | Safari is ⅔ of the visible width at full height, centered | W: rc-cl1 1.22 (Raycast 1.22); oma (Raycast 2.8.2) (I) |  |
| WIN-16 | window: center three fourths | low (I: 1/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari is ¾ of the visible width at full height, centered | W: oma (Raycast 2.8.2) (I) |  |
| WIN-17 | window: first third | low (I: 5/143 owners; Rectangle default ⌃⌥D) | keep: macOS has no thirds | app+Node | Accessibility | Safari fills exactly the left third of the visible frame of its display | W: rc-manual (Raycast 2.9) (O) | app#67 · #1016 |
| WIN-18 | window: last third | low (I: 4/143 owners; Rectangle ⌃⌥G) | keep: macOS has no thirds | app+Node | Accessibility | Safari fills exactly the right third of the visible frame | W: rc-manual (Raycast 2.9) (O) | app#67 · #1016 |
| WIN-19 | window: first two thirds | low (I: 3/143 owners; Rectangle ⌃⌥E) | keep: macOS has no thirds | app+Node | Accessibility | Safari fills the left ⅔; together with a last third window there is no overlap and no gap (gap setting 0) | W: rc-manual (Raycast 2.9) (O) | app#67 · #1016 |
| WIN-20 | window: last two thirds | low (I: 1/143 owners; Rectangle ⌃⌥T) | keep: macOS has no thirds | app+Node | Accessibility | Safari fills the right ⅔ of the visible frame | W: rc-manual (Raycast 2.9) (O) | app#67 · #1016 |
| WIN-21 | window: top third | low (I: 0/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari fills the top third of the visible frame | W: rc-cl1 1.41 (Raycast 1.41); oma (Raycast 2.8.2) (I) |  |
| WIN-22 | window: middle third | low (I: 0/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari fills the vertical middle third | W: oma (Raycast 2.8.2) (I) |  |
| WIN-23 | window: bottom third | low (I: 0/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari fills the bottom third | W: rc-cl1 1.41 (Raycast 1.41); oma (Raycast 2.8.2) (I) |  |
| WIN-24 | window: top two thirds | low (I: 0/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari fills the top ⅔ | W: rc-cl1 1.41 (Raycast 1.41); oma (Raycast 2.8.2) (I) |  |
| WIN-25 | window: bottom two thirds | low (I: 0/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari fills the bottom ⅔ | W: rc-cl1 1.41 (Raycast 1.41); oma (Raycast 2.8.2) (I) |  |
| WIN-26 | window: top center two thirds | low (I: 0/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari is ⅔ wide, centered, touching the top of the visible frame | W: rc-cl1 1.50 (Raycast 1.50); oma (Raycast 2.8.2) (I) |  |
| WIN-27 | window: bottom center two thirds | low (I: 0/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari is ⅔ wide, centered, touching the bottom of the visible frame | W: oma (Raycast 2.8.2) (I) |  |
| WIN-28 | window: first fourth | low (I: ≤1/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari fills the leftmost quarter-width column at full height | W: rc-manual (Raycast 2.9) (O) |  |
| WIN-29 | window: second fourth | low (I: ≤1/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari fills the 2nd quarter-width column | W: rc-manual (Raycast 2.9) (O) |  |
| WIN-30 | window: third fourth | low (I: ≤1/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari fills the 3rd quarter-width column | W: rc-manual (Raycast 2.9) (O) |  |
| WIN-31 | window: last fourth | low (I: ≤1/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari fills the rightmost quarter-width column | W: rc-manual (Raycast 2.9) (O) |  |
| WIN-32 | window: first three fourths | low (I: 0/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari fills the left ¾ of the visible frame | W: rc-cl1 1.13 (Raycast 1.13); oma (Raycast 2.8.2) (I) |  |
| WIN-33 | window: last three fourths | low (I: 0/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari fills the right ¾ of the visible frame | W: rc-cl1 1.13 (Raycast 1.13); oma (Raycast 2.8.2) (I) |  |
| WIN-34 | window: top first fourth | low (I: 0/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari fills the top quarter-height band at full width | W: oma (Raycast 2.8.2) (I) |  |
| WIN-35 | window: top second fourth | low (I: 0/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari fills the 2nd quarter-height band | W: oma (Raycast 2.8.2) (I) |  |
| WIN-36 | window: top third fourth | low (I: 0/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari fills the 3rd quarter-height band | W: oma (Raycast 2.8.2) (I) |  |
| WIN-37 | window: top last fourth | low (I: 0/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari fills the bottom quarter-height band | W: oma (Raycast 2.8.2) (I) |  |
| WIN-38 | window: top three fourths | low (I: 0/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari fills the top ¾ of the visible frame | W: oma (Raycast 2.8.2) (I) |  |
| WIN-39 | window: bottom three fourths | low (I: 1/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari fills the bottom ¾ of the visible frame | W: oma (Raycast 2.8.2) (I) |  |
| WIN-40 | window: top left quarter | low (I: 6/143 owners; Rectangle ⌃⌥U) | replace: macOS itself (menu, drag to corner; no default key) | macOS | none | Window > Move & Resize > Top Left puts Safari in the top-left quarter of the visible frame | W: rc-manual (Raycast 2.9); ap-keys "Top Left" (macOS 27) (O) |  |
| WIN-41 | window: top right quarter | low (I: 5/143 owners; Rectangle ⌃⌥I) | replace: macOS itself (menu, drag; no default key) | macOS | none | Window > Move & Resize > Top Right puts Safari in the top-right quarter | W: rc-manual (Raycast 2.9); ap-keys "Top Right" (macOS 27) (O) |  |
| WIN-42 | window: bottom left quarter | low (I: 5/143 owners; Rectangle ⌃⌥J) | replace: macOS itself (menu, drag; no default key) | macOS | none | Window > Move & Resize > Bottom Left puts Safari in the bottom-left quarter, above the Dock | W: rc-manual (Raycast 2.9); ap-keys "Bottom Left" (macOS 27) (O) |  |
| WIN-43 | window: bottom right quarter | low (I: 3/143 owners; Rectangle ⌃⌥K) | replace: macOS itself (menu, drag; no default key) | macOS | none | Window > Move & Resize > Bottom Right puts Safari in the bottom-right quarter | W: rc-manual (Raycast 2.9); ap-keys "Bottom Right" (macOS 27) (O) |  |
| WIN-44 | window: top left sixth | low (I: 0/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari fills the top-left cell of a 3×2 grid | W: rc-manual (Raycast 2.9) (O) |  |
| WIN-45 | window: top center sixth | low (I: 1/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari fills the top-middle cell of a 3×2 grid | W: rc-manual (Raycast 2.9) (O) |  |
| WIN-46 | window: top right sixth | low (I: 0/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari fills the top-right cell of a 3×2 grid | W: rc-manual (Raycast 2.9) (O) |  |
| WIN-47 | window: bottom left sixth | low (I: 0/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari fills the bottom-left cell of a 3×2 grid | W: rc-manual (Raycast 2.9) (O) |  |
| WIN-48 | window: bottom center sixth | low (I: 0/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari fills the bottom-middle cell of a 3×2 grid | W: rc-manual (Raycast 2.9) (O) |  |
| WIN-49 | window: bottom right sixth | low (I: 0/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari fills the bottom-right cell of a 3×2 grid | W: rc-manual (Raycast 2.9) (O) |  |
| WIN-50 | window: move left | low (I: 0/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari's left edge meets the visible frame's left edge with its size unchanged | W: rc-manual (Raycast 2.9) (O) |  |
| WIN-51 | window: move right | low (I: 0/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari's right edge meets the visible frame's right edge with its size unchanged | W: rc-manual (Raycast 2.9) (O) |  |
| WIN-52 | window: move up | low (I: 1/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari's top edge sits just below the menu bar with its size unchanged | W: rc-manual (Raycast 2.9) (O) |  |
| WIN-53 | window: move down | low (I: 0/143 owners) | later: custom rect covers | app+Node | Accessibility | Safari's bottom edge sits just above the Dock with its size unchanged | W: rc-manual (Raycast 2.9) (O) |  |
| WIN-54 | window: move-to-edge "Center Window" | low (I) | later: with move-to-edge | app+Node | Accessibility | Move left with the setting on puts Safari vertically centered at the left edge | W: rc-cl1 1.41 (Raycast 1.41); oma (Raycast 2.8.2) (I) |  |
| WIN-55 | window: move window (exact x,y) | low (I: new in 2.2, for recordings) | replace: agent conversation | Core+Node | Accessibility | "Move Safari to 100,100" goes to the agent, the Node puts Safari's origin at (100,100) pt and writes a receipt, or refuses with a reason when there is no grant | W: rc-manual (Raycast 2.9); rc-cl2 2.2 (O) |  |
| WIN-56 | window: resize window (exact w×h) | low (I: recordings, demos) | replace: agent conversation | Core+Node | Accessibility | "Make Safari 1920×1080" leaves Safari at exactly 1920×1080 pt, with a receipt | W: rc-manual (Raycast 2.9); rc-cl2 2.2 (O) |  |
| WIN-57 | window: restore | low (I: 5/143 owners; Rectangle ⌃⌥⌫) | keep: macOS Fn-⌃-R undoes only its own tiling | app+Node | Accessibility | After a LINA first third, restore returns Safari to its exact previous frame | W: rc-manual (Raycast 2.9) (O) | app#67 · #1016 |
| WIN-58 | window: toggle fullscreen | low (I: 6/143 owners) | replace: macOS itself (⌃⌘F) | macOS | none | ⌃⌘F puts Safari in its own full-screen Space, and pressing it again exits | W: rc-manual (Raycast 2.9); ap-kbd ⌃⌘F (O) |  |
| WIN-59 | window: minimize | low (I: ⌘M is native) | replace: macOS itself (⌘M) | macOS | none | ⌘M minimizes Safari to the Dock | W: oma (Raycast 2.8.2); ap-kbd ⌘M (I) |  |
| WIN-60 | window: minimize on this display | low (I: 0/143 owners) | later: low use | app+Node | Accessibility | With 2 displays, all windows on display 2 minimize and display 1 is untouched | W: rc-manual (Raycast 2.9) (O) |  |
| WIN-61 | window: make larger | low (I: 3-4/143 owners) | later: low use | app+Node | Accessibility | Each press grows Safari on all sides until it equals the visible frame | W: rc-cl1 1.15.1 (Raycast 1.15); oma (Raycast 2.8.2) (I) |  |
| WIN-62 | window: make smaller | low (I: 4/143 owners) | later: low use | app+Node | Accessibility | Each press shrinks Safari, stopping at 25% of the visible frame | W: rc-cl1 1.15.1 (Raycast 1.15); oma (Raycast 2.8.2) (I) |  |
| WIN-63 | window: move to next display | mid (I: 17/143 owners; Rectangle ⌃⌥⌘→) | keep: macOS has no key for it | app+Node | Accessibility | With 2 displays, Safari moves to display 2 at the same relative frame | W: rc-manual (Raycast 2.9) (O) | app#67 · #1016 |
| WIN-64 | window: move to previous display | mid (I: 13/143 owners; Rectangle ⌃⌥⌘←) | keep: macOS has no key for it | app+Node | Accessibility | Safari moves back to display 1 at the same relative frame | W: rc-manual (Raycast 2.9) (O) | app#67 · #1016 |
| WIN-65 | window: display move "Wrap Around Displays" | low (I) | keep (as default) | app+Node | Accessibility | On the last display, next display moves Safari to the first display | W: oma (Raycast 2.8.2) (I) | app#68 · #1016 |
| WIN-66 | window: display move "Keep Aspect Ratio" | low (I) | keep (as default) | app+Node | Accessibility | A ½-width window on display 1 is ½-width on a display of different size | W: rc-cl1 1.41 (Raycast 1.41); oma (Raycast 2.8.2) (I) | app#68 · #1016 |
| WIN-67 | window: move to next Space | mid (I: 9/143 owners) | later: no public API, breaks with macOS updates | app+Node | Accessibility (I) | Safari disappears from Desktop 1 and shows on Desktop 2 | W: rc-manual (Raycast 2.9) (O) |  |
| WIN-68 | window: move to previous Space | mid (I: 9/143 owners) | later: no public API, breaks with macOS updates | app+Node | Accessibility (I) | Safari moves from Desktop 2 to Desktop 1 | W: rc-manual (Raycast 2.9) (O) |  |
| WIN-69 | window: switch to next Space | low (I) | replace: macOS itself (⌃→) | macOS | none | ⌃→ shows the next Space | W: rc-cl2 0.55, 2.8 (Raycast 2.0, 2.8) (O) |  |
| WIN-70 | window: switch to previous Space | low (I) | replace: macOS itself (⌃←) | macOS | none | ⌃← shows the previous Space | W: rc-cl2 0.55, 2.8 (Raycast 2.0, 2.8) (O) |  |
| WIN-71 | window: Space switch "Animate Switching" | low (I) | drop: cosmetic, private API | — | none | n/a | W: oma (Raycast 2.8.2) (I) |  |
| WIN-72 | window: System Spaces Shortcuts | low (I) | drop: macOS keys suffice | — | none | n/a | W: rc-manual (Raycast 2.9); oma (Raycast 2.8.2) (O) |  |
| WIN-73 | window: toggle grid overlay | low (I: not in the 2.9 manual) | later: undocumented in Raycast | app+Node | Accessibility | Picking a 2×2 grid region puts Safari exactly in that region | W: oma (Raycast 2.8.2) (I) |  |
| WIN-74 | window: hotkey per command | high (I: the manual tip; how window commands are mostly run) | keep | app+Node | none for the hotkey; Accessibility for the move | Assign ⌃⌥D to first third; pressing it in any app moves that app's focused window | W: rc-manual (Raycast 2.9) (O) | app#67 · #1016 |
| WIN-75 | window: run by name or alias in launcher | mid (I) | keep | app+Node | Accessibility | Typing "first third" and pressing Return moves the person's last focused window, not the launcher panel | W: rc-manual (Raycast 2.9) (O) | app#67 · #1016 |
| WIN-76 | window: hotkey presets | low (I: one-time) | keep: Rectangle keys for kept commands | app | none | After applying the Rectangle preset, ⌃⌥D puts Safari in the first third and ⌃⌥⌘→ moves it to the next display | W: rc-manual (Raycast 2.9); rc-cl1 1.19 (Raycast 1.19) (O) | app#67 · #1016 |
| WIN-77 | window: window gap | mid (I) | keep: to match macOS tile margins | app+Node | Accessibility | With a 10 pt gap, first third and last two thirds leave exactly 10 pt between them | W: rc-manual (Raycast 2.9); oma innerGap (Raycast 2.8.2); rc-manual-v1 range 0-128 px (O) | app#67 · #1016 |
| WIN-78 | window: screen gap | mid (I) | keep: to match macOS tile margins | app+Node | Accessibility | With a 10 pt screen gap, first third starts 10 pt from the visible frame's left, top and bottom | W: rc-manual (Raycast 2.9); oma outerGap (Raycast 2.8.2) (O) | app#67 · #1016 |
| WIN-79 | window: respect Stage Manager | low (I) | keep | app+Node | Accessibility | With Stage Manager on, a LINA first third starts to the right of the strip | W: rc-manual (Raycast 2.9); rc-cl2 2.0 (O) | app#67 · #1016 |
| WIN-80 | window: create custom command | low (I: Pro only) | keep: base for all presets | app+Node | Accessibility | A custom "Left 40%" puts Safari at 40% width and full height, pinned left | W: rc-manual (Raycast 2.9); rc-pricing (Pro) (O) | app#67 · #1016 |
| WIN-81 | window: duplicate command | low (I) | later: editor nicety | app | none | Duplicating "First Third" opens the editor prefilled with 33.3% width, left, full height | W: rc-manual (Raycast 2.9) (O) |  |
| WIN-82 | window: custom command deeplink | low (I) | replace: App Intents action | app+Node | Accessibility | The Shortcuts app runs LINA "Place Window" (50% width, center) and Safari matches | W: rc-manual (Raycast 2.9) (O) |  |
| WIN-83 | window: create layout | mid (I: store "Window Layouts" has 5,408 installs; apply-layout 2/143 owners) | replace: agent conversation (saves the layout) | Core+Node | Accessibility | Telling the agent "coding layout: VS Code left ⅔, Safari right ⅓ on display 2" makes a saved layout appear in the launcher | W: rc-manual (Raycast 2.9); rc-cl1 1.78, 1.80 (O) |  |
| WIN-84 | window: apply layout | mid (I) | keep | app+Node | Accessibility | The layout hotkey opens VS Code and Safari and places each at its saved frame and display within 15 s | W: rc-manual (Raycast 2.9); rc-cl1 1.81 (15 s app timeout) (O) | app#69 · #1016 |
| WIN-85 | window: save current layout | low (I: new in 2.2) | keep | app+Node | Accessibility | Capture 3 windows, move them, apply the layout: all 3 return to their captured frames within 1 pt | W: rc-manual (Raycast 2.9); rc-cl2 2.2 (O) | app#69 · #1016 |
| WIN-86 | window: layout entry argument | low (I) | keep | app+Node | Accessibility | A Safari entry with a URL opens that URL in Safari at the saved frame | W: rc-manual (Raycast 2.9); rc-cl2 2.2 (O) | app#69 · #1016 |
| WIN-87 | window: layout "Minimize Others" | low (I) | keep | app+Node | Accessibility | After applying, every unplaced window on the layout's displays is minimized | W: rc-manual (Raycast 2.9) (O) | app#69 · #1016 |
| WIN-88 | window: layout "Close Others" | low (I) | later: closes the person's windows | app+Node | Accessibility | An unplaced TextEdit window with unsaved text shows its save prompt and is not force-closed | W: rc-manual (Raycast 2.9) (O) |  |
| WIN-89 | window: layout deeplink | low (I) | replace: App Intents action | app+Node | Accessibility | The Shortcuts app runs LINA "Apply Layout: coding" and the windows match the saved layout | W: rc-manual (Raycast 2.9) (O) |  |
| WIN-90 | window: import commands and layouts | low (I) | later: Raycast migration | app | none | Importing a Raycast JSON export makes the same custom commands appear in LINA | W: oma (Raycast 2.8.2) (I) |  |
| WIN-91 | window-api: get active window | low (I: Pro-only API; window store extensions have at most 17.6k installs) | replace: Node window tool for agents | Core+Node | Accessibility | Asked "what is my front window?", the agent answers with the app and frame, and a receipt exists | W: rc-api (API 1.78+, Pro) (O) | #1016 |
| WIN-92 | window-api: list windows on active desktop | low (I) | replace: Node window tool for agents | Core+Node | Accessibility | The agent's list matches the windows visible on the current Space | W: rc-api (API 1.78+, Pro) (O) | #1016 |
| WIN-93 | window-api: list desktops | low (I) | replace: Node window tool for agents | Core+Node | Accessibility | With one full-screen app, the agent reports one FullScreen and one User desktop | W: rc-api (API 1.78+, Pro) (O) | #1016 |
| WIN-94 | window-api: set window bounds | low (I) | replace: Node window tool for agents | Core+Node | Accessibility | The agent moves Safari to x=100 and the receipt shows the before and after frames; with no grant it is refused with a reason | W: rc-api (API 1.78+, Pro) (O) | #1016 |
| WIN-95 | window: permission onboarding | high (I: every move needs it) | keep | app+Node | Accessibility | Without the grant, the first command shows a reason and a button to the Privacy & Security pane; after the grant the next command works without relaunching | W: rc-manual (Raycast 2.9); ms-27 (macOS 27); ap-ax (macOS 27) (O) | app#57 |

### Clipboard history

62 rows: 36 keep, 3 replace, 17 later, 6 drop.

| ID | Feature | Use | Decision | Runs | Permission | Acceptance check | Source | Issue |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| CLIP-01 | clipboard: history on/off | high: "one of our most popular features" (raycast/blog-pro) | keep: off until the person turns it on | app | Paste from Other Apps: Allow (I) | Fresh install: copy text; history stays empty until switched on | C: manual/clipboard-history (Raycast 2.9) (O) | app#72 |
| CLIP-02 | clipboard: open history | high: blog-pro | keep | app | none | Press the history hotkey in any app; list shows newest item first | C: manual/clipboard-history (Raycast 2.9) (O) | app#73 |
| CLIP-03 | clipboard: search entries | high: I core of the feature | keep | app | none | Copy "lina-test-42"; type "42"; that item is first | C: manual/clipboard-history (Raycast 2.9) (O) | app#73 |
| CLIP-04 | clipboard: filter by type | mid: I | keep | app | none | Copy an image and a text; Images filter shows only the image | C: manual/clipboard-history (Raycast 2.9) (O) | app#73 |
| CLIP-05 | clipboard: text items | high: I most copies are text | keep | app | Paste from Other Apps (I) | Copy bold text in TextEdit; ↵ on the entry copies it back with its bold, and ⌘V pastes it bold | C: manual/clipboard-history (Raycast 2.9) (O) | app#72 |
| CLIP-06 | clipboard: image items | mid: I | keep | app | Paste from Other Apps (I) | ⌃⇧⌘4 to clipboard; entry shows a thumbnail | C: manual/clipboard-history (Raycast 2.9) (O) | app#72 |
| CLIP-07 | clipboard: file items | mid: I | keep | app | Paste from Other Apps (I) | Copy a file in Finder; ↵ on the entry copies it, and ⌘V in another folder duplicates it | C: manual/clipboard-history (Raycast 2.9) (O) | app#72 |
| CLIP-08 | clipboard: link items | mid: I | keep | app | none | Copy a URL in Safari; entry is typed Link | C: manual/clipboard-history (Raycast 2.9) (O) | app#72 |
| CLIP-09 | clipboard: email items | low: I | keep: type tag only | app | none | Copy "a@b.co"; Emails filter shows it | C: manual/clipboard-history (Raycast 2.9) (O) | app#72 |
| CLIP-10 | clipboard: color items | low: I | later: low use | app | none | Copy "#FF0000"; entry shows a red swatch | C: manual/clipboard-history (Raycast 2.9) (O) |  |
| CLIP-11 | clipboard: grouped entries | low: I | keep | app | none | Copy 3 files at once; one entry; ↵ copies all 3, and ⌘V in Finder pastes 3 files | C: manual/new-in-v2 (Raycast 2.0) (O) | app#72 |
| CLIP-12 | clipboard: preview pane | high: I default view | keep | app | none | Select a long text entry; full text, source app and time shown | C: manual/v1-core (Raycast v1) (I) | app#73 |
| CLIP-13 | clipboard: source app | mid: I | keep: use org.nspasteboard.source, else front app | app | none | Copy in Notes; entry shows Notes as source | C: manual/new-in-v2 (Raycast 2.0); nspasteboard.org (O) | app#72 |
| CLIP-14 | clipboard: max item size | low: I | keep: set a limit (v2 limit unknown) | app | none | Copy 5 MB of text; app stays responsive; entry follows the limit | C: manual/v1-core (Raycast v1: 32,768 chars); alfred/clipboard (I) | app#72 |
| CLIP-15 | clipboard: paste to active app | high: default primary action | later: copy only for now (owner 10-23) | app | Accessibility (27: Device Control and Data Access) | In TextEdit pick an older item, ↵; text at cursor, TextEdit still focused | C: manual/clipboard-history (Raycast 2.9); maccy/readme (O) |  |
| CLIP-16 | clipboard: copy to clipboard | high: I | keep | app | none | ⌘↵ an item; ⌘V in TextEdit pastes it | C: manual/clipboard-history (Raycast 2.9) (O) | app#73 |
| CLIP-17 | clipboard: primary action setting | mid: I | keep | app | none | Set to Copy; ↵ leaves front app unchanged, ⌘V pastes item | C: manual/clipboard-history (Raycast 2.9) (O) | app#73 |
| CLIP-18 | clipboard: paste as plain text | high: store ext "Paste as Plain Text" 27,400 installs | keep: copy as plain text; paste later | app | Accessibility (27: Device Control and Data Access) | Copy bold text; "copy as plain text" on the entry, then ⌘V in Pages gives no bold | C: manual/clipboard-history (Raycast 2.9) (O) | app#73 |
| CLIP-19 | clipboard: prefer plain text setting | mid: I | keep | app | Accessibility (27: Device Control and Data Access) | Turn on; ↵ on a bold entry copies it without bold | C: manual/clipboard-history (Raycast 2.9) (O) | app#73 |
| CLIP-20 | clipboard: paste as format | low: I | later: low use | app | Accessibility (27: Device Control and Data Access) | Paste an HTML entry as RTF into TextEdit; styles kept | C: manual/clipboard-history (Raycast 2.9) (O) |  |
| CLIP-21 | clipboard: paste as PNG | low: I | later: low use | app | Accessibility (27: Device Control and Data Access) | Paste a TIFF entry into a PNG-only web upload; accepted | C: raycast/changelog (Raycast 2.6) (O) |  |
| CLIP-22 | clipboard: paste nth item ⌘1–9 | mid: I Maccy and Alfred users rely on it | keep: ⌘1–9 copies item N; paste later | app | Accessibility (27: Device Control and Data Access) | Open history; ⌘3 copies the third item, and ⌘V in TextEdit pastes it | C: maccy/readme (Maccy 2.7.1); not in manual/keyboard-shortcuts (Raycast 2.9) (O) | app#73 |
| CLIP-23 | clipboard: pin/unpin | mid: I | keep | app | none | Pin item; copy 30 new items; pinned item still on top | C: manual/clipboard-history (Raycast 2.9); manual/v1-core (O) | app#73 |
| CLIP-24 | clipboard: rename entry | low: I new in v2 | later: low use | app | none | Rename entry "invoice"; search "invoice" finds it | C: manual/clipboard-history (Raycast 2.9) (O) |  |
| CLIP-25 | clipboard: edit content | low: I | later: low use | app | none | Edit entry text; paste gives the edited text | C: manual/clipboard-history (Raycast 2.9) (O) |  |
| CLIP-26 | clipboard: delete entry | mid: I | keep | app | none | Delete item; relaunch app; search finds nothing | C: manual/clipboard-history (Raycast 2.9) (O) | app#73 |
| CLIP-27 | clipboard: delete recent entries | mid: I privacy clean-up | keep | app | none | Copy 3 items; delete last 5 min; those 3 gone, older remain | C: manual/clipboard-history (Raycast 2.9) (O) | app#73 |
| CLIP-28 | clipboard: delete all | mid: I | keep | app | none | Delete all; list empty; store file holds no item rows | C: manual/clipboard-history (Raycast 2.9) (O) | app#73 |
| CLIP-29 | clipboard: retention period | high: I every user picks one | keep: local choice, no tiers | app | none | Set 1 day; move clock 2 days; unpinned items gone | C: manual/clipboard-history (Raycast 2.9) (O) | app#73 |
| CLIP-30 | clipboard: ignored apps | mid: I | keep: default Passwords, Keychain Access, password managers | app | none | Copy a password in Passwords app; no entry appears | C: manual/clipboard-history (Raycast 2.9) (O) | app#72 |
| CLIP-31 | clipboard: skip concealed | high: every password-manager copy | keep: never stored (rule) | app | none | Copy a password in 1Password and Bitwarden desktop; no entry | C: raycast/core-clipboard; dev/clipboard-api; nspasteboard.org (O) | app#72 |
| CLIP-32 | clipboard: skip transient | high: I text expanders, Grammarly | keep: never stored (rule) | app | none | Test tool writes text plus TransientType; no entry | C: nspasteboard.org; raycast/core-clipboard (O) | app#72 |
| CLIP-33 | clipboard: skip auto-generated | mid: I | keep | app | none | Test tool writes text plus AutoGeneratedType; no entry | C: nspasteboard.org; maccy/readme (O) | app#72 |
| CLIP-34 | clipboard: skip legacy markers | low: I older apps | keep | app | none | Test tool writes text plus each legacy type; no entries | C: nspasteboard.org; maccy/readme (O) | app#72 |
| CLIP-35 | clipboard: pause recording | low: I | keep: quick privacy switch | app | none | Pause; copy; no entry; resume; copy; entry | C: maccy/readme (Maccy 2.7.1) (O) | app#72 |
| CLIP-36 | clipboard: text recognition | mid: I | later: on-device Vision after core history | app | none | Copy screenshot with "Invoice"; search "Invoice" finds it | C: manual/clipboard-history (Raycast 2.9) (O) |  |
| CLIP-37 | clipboard: OCR mode | low: I | later: with OCR | app | none | Accurate finds small grey text that Fast misses | C: manual/clipboard-history (Raycast 2.9) (O) |  |
| CLIP-38 | clipboard: copy text from image | mid: I | later: with OCR | app | none | Action on screenshot; ⌘V gives its text | C: manual/clipboard-history (Raycast 2.9) (O) |  |
| CLIP-39 | clipboard: copy text from QR code | low: I | later: low use | app | none | Copy QR image; action gives encoded URL | C: manual/clipboard-history (Raycast 2.9) (O) |  |
| CLIP-40 | clipboard: open item | low: I | keep | app | none | ⌘O on link entry opens default browser | C: manual/v1-core (Raycast v1); manual/keyboard-shortcuts (Raycast 2.9) (I) | app#73 |
| CLIP-41 | clipboard: Quick Look | mid: I native feel | keep | app | none | ⌘Y on image entry opens Quick Look | C: manual/keyboard-shortcuts (Raycast 2.9, common action) (I) | app#73 |
| CLIP-42 | clipboard: share | low: I | later: low use | app | none | Share link entry to Messages; Share sheet opens with it | C: manual/clipboard-history (Raycast 2.9); raycast/changelog (2.3) (O) |  |
| CLIP-43 | clipboard: save as snippet | low: I | keep | app | none | Save entry as snippet; it appears in snippet search | C: manual/clipboard-history (Raycast 2.9) (O) | app#73 |
| CLIP-44 | clipboard: send to AI chat | mid: I | replace: person puts item into agent message | app+Core | none | Put item in message; composer shows it; Core gets nothing until Send | C: manual/clipboard-history (Raycast 2.9) (O) | app#39 · #1020 |
| CLIP-45 | clipboard: Ask Clipboard | low: I new in 2.3 | drop: history never goes to Core | — | none | Ask agent "what did I copy"; Core logs show no clipboard data | C: manual/clipboard-history (Raycast 2.9); raycast/changelog (2.3) (O) |  |
| CLIP-46 | clipboard: link previews | low: I | drop: fetch leaks copied URLs | app | none | Copy a URL with a network monitor on; no outbound request | C: manual/clipboard-history (Raycast 2.9) (O) |  |
| CLIP-47 | clipboard: move used item to top | low: I | keep | app | none | Copy the 5th item again; it becomes first | C: manual/clipboard-history (Raycast 2.9) (O) | app#73 |
| CLIP-48 | clipboard: paste sequentially | low: store ext "Clipboard Sequential Paste" 1,365 installs | later: low use | app | Accessibility (27: Device Control and Data Access) | Copy A, B, C; run 3 times; pastes C, B, A | C: manual/clipboard-history (Raycast 2.9) (O) |  |
| CLIP-49 | clipboard: reset paste sequence | low: I | later: with sequence | app | none | Reset; next run pastes newest item | C: manual/clipboard-history (Raycast 2.9) (O) |  |
| CLIP-50 | clipboard: sequence timeout | low: I | later: with sequence | app | none | Wait 31 s; next run pastes newest item | C: manual/clipboard-history (Raycast 2.9) (O) |  |
| CLIP-51 | clipboard: newline after sequential paste | low: I | later: with sequence | app | Accessibility (27: Device Control and Data Access) | Paste 3 in sequence; 3 lines result | C: manual/clipboard-history (Raycast 2.9) (O) |  |
| CLIP-52 | clipboard: Universal Clipboard timing | low: I | later: default save on paste | app | none | Copy on iPhone; entry appears only after ⌘V on Mac | C: manual/clipboard-history (Raycast 2.9); raycast/changelog (2.7) (O) |  |
| CLIP-53 | clipboard: keep across restarts | high: I | keep | app | none | Copy item; quit and relaunch; item present | C: I: retention settings imply a store (I) | app#72 |
| CLIP-54 | clipboard: encryption at rest | high: I applies to all | keep: encrypt store, key in Keychain | app | none | `strings` on store files finds no copied marker text | C: raycast/core-clipboard; simo.sh (key in Keychain) (O) | app#72 |
| CLIP-55 | clipboard: no sync | n/a | drop: stays on that Mac (rule) | app | none | Copy on Mac A; never on Mac B; Core receives no clipboard bytes | C: manual/cloud-sync (Raycast 2.9); raycast/blog-pro (O) |  |
| CLIP-56 | clipboard: org retention lock | low: Enterprise only | drop: single owner | — | none | n/a | C: manual/enterprise-clipboard (Raycast v2) (O) |  |
| CLIP-57 | clipboard: org ignored apps | low: Enterprise only | drop: single owner | — | none | n/a | C: manual/enterprise-clipboard (Raycast v2) (O) |  |
| CLIP-58 | clipboard: merge | low: Raycast lacks it | drop: low use | app | none | n/a | C: alfred/clipboard (Alfred 5, I) (O) |  |
| CLIP-59 | spotlight-clipboard: open | mid: I built in, off by default | replace: LINA history (LINA takes ⌘Space) | macOS | none | With LINA on ⌘Space, LINA's history opens from its key; Spotlight's unused | C: apple/spotlight-clipboard (macOS 27) (O) |  |
| CLIP-60 | spotlight-clipboard: on/off | n/a | replace: guide owner to turn it off, avoid two histories | macOS | none | Turn off; ⌘Space ⌘4 shows no items | C: apple/spotlight-settings (macOS 27); apple/spotlight-clipboard (O) |  |
| CLIP-61 | spotlight-clipboard: sensitive items | n/a | keep: LINA does not rely on it | macOS | none | n/a | C: macmost/tahoe (26.0); arboard/190 (26 beta 1); apple/spotlight-clipboard warns "Sensitive information may appear" (I) | app#72 |
| CLIP-62 | clipboard: items from other devices | low: I | later: check fit with the "stays on that Mac" rule | app | none | Not built until decided | S: idb/tahoe-sp (macOS 26) (I) |  |

### Snippets

57 rows: 34 keep, 1 replace, 18 later, 4 drop.

| ID | Feature | Use | Decision | Runs | Permission | Acceptance check | Source | Issue |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| SNIP-01 | snippet: create | high: I core feature | keep | app+Core | none | Create snippet; it appears in snippet search | C: manual/snippets (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-02 | snippet: keyword | high: manual calls keywords "the real power" | keep | app+Core | none | Set "!sig"; snippet shows keyword | C: manual/snippets (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-03 | snippet: keyword characters | low: I | keep: same rules | app+Core | none | Keyword "it's" is refused with a reason | C: manual/snippets (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-04 | snippet: tags | low: I new in v2 | later: low use | app | none | Tag "work"; tag filter shows it | C: manual/snippets (Raycast 2.9) (O) |  |
| SNIP-05 | snippet: size limit | low: I | keep: a limit | app+Core | none | 70,000-char text is refused with a reason | C: manual/snippets (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-06 | snippet: rich text | low: I | later: plain first | app | none | Bold snippet pastes bold in Pages | C: manual/snippets (Raycast 2.9) (O) |  |
| SNIP-07 | snippet: search | high: I | keep | app+Core | none | Search body word; snippet found | C: manual/snippets (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-08 | snippet: filter by tag | low: I | later: with tags | app | none | Filter "work"; only tagged shown | C: manual/snippets (Raycast 2.9) (O) |  |
| SNIP-09 | snippet: paste into active app | high: I | later: copy only for now (owner 10-23) | app | Accessibility (27: Device Control and Data Access) | In TextEdit pick snippet, ↵; text at cursor | C: manual/snippets (Raycast 2.9) (O) |  |
| SNIP-10 | snippet: copy to clipboard | mid: I | keep | app+Core | none | Copy; ⌘V pastes it | C: manual/snippets (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-11 | snippet: primary action setting | low: I | later: only copy for now | app | none | Set Copy; ↵ leaves front app unchanged | C: manual/snippets (Raycast 2.9) (O) |  |
| SNIP-12 | snippet: pin | low: I | keep | app+Core | none | Pin; it is first in empty search | C: manual/snippets (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-13 | snippet: edit | mid: I | keep | app+Core | none | Change the text; the next copy of the snippet has the new text | C: manual/snippets (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-14 | snippet: duplicate | low: I | keep | app+Core | none | Duplicate; two entries, keyword not duplicated | C: manual/snippets (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-15 | snippet: delete | mid: I | keep | app+Core | none | Delete; keyword no longer expands | C: manual/snippets (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-16 | snippet: warn before delete | low: I | keep | app+Core | none | Delete asks to confirm | C: manual/snippets (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-17 | snippet: auto-expansion | high: I flagship; espanso 10,705 brew installs/yr | later: copy only for now (owner 10-23) | app | Accessibility (27: Device Control and Data Access) | In TextEdit type "!sig "; replaced by the signature | C: manual/snippets (Raycast 2.9) (O) |  |
| SNIP-18 | snippet: expansion on/off | mid: I | later: copy only for now (owner 10-23) | app | none | Off: typing "!sig" stays as typed | C: manual/snippets (Raycast 2.9) (O) |  |
| SNIP-19 | snippet: expansion mode | mid: I | later: copy only for now (owner 10-23) | app | Accessibility (27: Device Control and Data Access) | Discard mode: "!sig " gives text with no trailing space | C: manual/snippets (Raycast 2.9) (O) |  |
| SNIP-20 | snippet: expand within words | low: I | later: copy only for now (owner 10-23) | app | Accessibility (27: Device Control and Data Access) | Off: "x!sig" does not expand | C: manual/snippets (Raycast 2.9) (O) |  |
| SNIP-21 | snippet: override system snippets | low: I | later: skip conflicts first | app | Accessibility (27: Device Control and Data Access) | Same keyword in macOS and LINA inserts text once | C: manual/snippets (Raycast 2.9, experimental) (O) |  |
| SNIP-22 | snippet: injection delay | low: I | later: copy only for now (owner 10-23) | app | Accessibility (27: Device Control and Data Access) | Expansion in Chrome and Slack has no lost chars | C: manual/snippets (Raycast 2.9) (O) |  |
| SNIP-23 | snippet: response time | low: I | later: tune by measurement | app | Accessibility (27: Device Control and Data Access) | n/a | C: manual/snippets (Raycast 2.9) (O) |  |
| SNIP-24 | snippet: completion sound | low: I | later: low use | app | none | Sound plays once per expansion | C: manual/snippets (Raycast 2.9) (O) |  |
| SNIP-25 | snippet: ignored apps | mid: I | later: copy only for now (owner 10-23) | app | none | Type keyword in 1Password field; nothing happens | C: manual/snippets (Raycast 2.9) (O) |  |
| SNIP-26 | snippet: Secure Input notice | low: I | later: copy only for now (owner 10-23) | app | none | Focus a Safari password field; LINA shows Secure Input notice | C: manual/snippets (Raycast 2.9); raycast/changelog (2.5) (O) |  |
| SNIP-27 | snippet: import JSON | mid: I migration from Raycast | keep: Raycast export import | app+Core | none | Import 3-snippet JSON; all 3 appear with keywords | C: manual/snippets (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-28 | snippet: import other tools | low: textexpander 490, typinator 329 brew installs/yr | later: low use | app | none | n/a | C: manual/snippets (Raycast 2.9) (O) |  |
| SNIP-29 | snippet: export | low: I | keep | app+Core | none | Export, delete all, import; same snippets back | C: manual/snippets (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-30 | snippet: shared snippets | low: Teams only | drop: single owner | — | none | n/a | C: manual/snippets (Raycast 2.9) (O) |  |
| SNIP-31 | snippet: sync | mid: I | later: via Core if owner wants | Core | none | n/a | C: manual/cloud-sync (Raycast 2.9) (O) |  |
| SNIP-32 | snippet: gallery | low: I | drop: low use | — | none | n/a | C: ray.so/snippets (O) |  |
| SNIP-33 | snippet: macOS Text Replacements | mid: store ext "Text Replacements" 7,365 installs | replace: macOS itself for fixed text | macOS | none | Add "omw"; typing it in Notes expands | C: apple/text-replacements (macOS 27) (O) |  |
| SNIP-34 | snippet: {clipboard} | mid: I | keep | app+Core | Paste from Other Apps (I) | Copy "abc"; expand "Re: {clipboard}" gives "Re: abc" | C: manual/dynamic-placeholders (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-35 | snippet: {clipboard offset} | low: I | keep: only when history on | app+Core | none | Copy A then B; offset=1 gives A | C: manual/dynamic-placeholders (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-36 | snippet: {date} | mid: I | keep | app+Core | none | Expands to today in system format | C: manual/dynamic-placeholders (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-37 | snippet: {time} | mid: I | keep | app+Core | none | Expands to current time | C: manual/dynamic-placeholders (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-38 | snippet: {datetime} | low: I | keep | app+Core | none | Expands to date and time | C: manual/dynamic-placeholders (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-39 | snippet: {day} | low: I | keep | app+Core | none | Expands to today's weekday | C: manual/dynamic-placeholders (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-40 | snippet: date format | mid: I | keep | app+Core | none | format="yyyy-MM-dd" gives ISO date | C: manual/dynamic-placeholders (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-41 | snippet: date offset | low: I | keep | app+Core | none | offset="+1d" gives tomorrow | C: manual/dynamic-placeholders (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-42 | snippet: date locale | low: I | keep | app+Core | none | locale="ko-KR" gives Korean date | C: manual/dynamic-placeholders (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-43 | snippet: {cursor} | mid: manual examples use it | later: copy only for now (owner 10-23) | app | Accessibility (27: Device Control and Data Access) | "log({cursor})" leaves caret inside parentheses | C: manual/dynamic-placeholders (Raycast 2.9) (O) |  |
| SNIP-44 | snippet: {argument name} | low: I | keep: launcher fields | app+Core | none | Snippet with {argument name="who"} asks once, fills twice | C: manual/dynamic-placeholders (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-45 | snippet: argument default | low: I | keep | app+Core | none | Empty field gives default | C: manual/dynamic-placeholders (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-46 | snippet: argument options | low: I | keep | app+Core | none | Field shows a, b, c picker | C: manual/dynamic-placeholders (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-47 | snippet: {uuid} | low: I | keep | app+Core | none | Two expansions give two different UUIDs | C: manual/dynamic-placeholders (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-48 | snippet: {snippet name} | low: I | keep | app+Core | none | Nested snippet text appears; a 2nd level is refused | C: manual/dynamic-placeholders (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-49 | snippet: {calculator} | low: I new in v2 | keep: app calculator | app+Core | none | {calculator 2*21} gives 42 (syntax I) | C: manual/dynamic-placeholders; manual/new-in-v2 (Raycast 2.0) (O) | app#74 · #1025 |
| SNIP-50 | snippet: {selection} | low: not in snippets | drop: not a snippet placeholder | — | none | n/a | C: manual/dynamic-placeholders (Raycast 2.9) (O) |  |
| SNIP-51 | snippet: {browser-tab} | low: I | drop: agent conversation does this | — | none | n/a | C: manual/dynamic-placeholders (Raycast 2.9) (O) |  |
| SNIP-52 | snippet: modifier uppercase | low: I | keep | app+Core | none | {clipboard \| uppercase} of "ab" gives "AB" | C: manual/dynamic-placeholders (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-53 | snippet: modifier lowercase | low: I | keep | app+Core | none | "AB" gives "ab" | C: manual/dynamic-placeholders (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-54 | snippet: modifier trim | low: I | keep | app+Core | none | "  ab  " gives "ab" | C: manual/dynamic-placeholders (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-55 | snippet: modifier percent-encode | low: I | keep | app+Core | none | "a b" gives "a%20b" | C: manual/dynamic-placeholders (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-56 | snippet: modifier json-stringify | low: I | keep | app+Core | none | `a "b"` gives `"a \"b\""` | C: manual/dynamic-placeholders (Raycast 2.9) (O) | app#74 · #1025 |
| SNIP-57 | snippet: modifier raw | low: I | keep | app+Core | none | {clipboard \| trim \| uppercase} of " ab " gives "AB" | C: manual/dynamic-placeholders (Raycast 2.9) (O) | app#74 · #1025 |

### Quick links and web search

41 rows: 21 keep, 4 replace, 13 later, 3 drop.

| ID | Feature | Use | Decision | Runs | Permission | Acceptance check | Source | Issue |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| QL-01 | quicklink: create | high: Q step; reddit (I) | keep | app+Core | none | Create "gh"→https://github.com; "gh" ↵ opens it; Core not called | B: M-quicklinks (Raycast 2.9) (O) | app#47 · #1025 |
| QL-02 | quicklink: link types | high: I | keep | app+Node | none | ~/Downloads link opens Finder at Downloads | B: M-quicklinks (Raycast 2.9) (O) | app#47 · #1025 |
| QL-03 | quicklink: open with app | mid: I | keep | app+Core | none | Link set to Firefox opens in Firefox though Safari is default | B: M-quicklinks (Raycast 2.9) (O) | app#47 · #1025 |
| QL-04 | quicklink: favicon icon | mid: I | keep | app+Core | none | New web quick link shows the site's icon | B: M-quicklinks (Raycast 2.9) (O) | app#47 · #1025 |
| QL-05 | quicklink: tags | low: I | later | app | none | Filter by tag "work" shows only work links | B: M-quicklinks, M-v2 (Raycast 2.9) (O) |  |
| QL-06 | quicklink: pin in list | low: I | replace: root favorites | app | none | n/a (favorites cover it) | B: M-quicklinks, M-v2 (Raycast 2.9) (O) |  |
| QL-07 | quicklink: search command | mid: I | keep | app+Core | none | Open list; type part of name; ↵ opens | B: M-quicklinks (Raycast 2.9) (O) | app#47 · #1025 |
| QL-08 | quicklink: in root search | high: I | keep | app+Core | none | Hide one from root; it still appears in the list command | B: M-quicklinks (Raycast 2.9) (O) | app#47 · #1025 |
| QL-09 | quicklink: actions | mid: I | keep | app+Core | none | ⌘K Duplicate creates "copy" that opens the same link | B: M-quicklinks (Raycast 2.9) (O) | app#47 · #1025 |
| QL-10 | quicklink: add from library | low: I | later | app | none | Pick Wikipedia; form prefilled with query argument | B: M-quicklinks (Raycast 2.9) (O) |  |
| QL-11 | quicklink: autofill from browser | mid: I | later | app | Automation (browser) | With Safari on a page, Create shows that URL | B: M-quicklinks (Raycast 2.9) (O) |  |
| QL-12 | quicklink: autofill selection or Finder file | low: I | later | app | Accessibility (I) | Select a file in Finder; Create shows its path | B: M-quicklinks, M-v2 (Raycast 2.9) (O) |  |
| QL-13 | quicklink: {argument} | high: M search-engine examples | keep | app+Core | none | "g {argument}"; "g cats" opens search for cats | B: M-placeholders, M-quicklinks (Raycast 2.9) (O) | app#47 · #1025 |
| QL-14 | quicklink: {query} | unknown | keep: accept on import | app+Core | none | Import a link with {query}; it prompts like {argument} | B: M-placeholders (absent, Raycast 2.9) (I) | app#47 · #1025 |
| QL-15 | quicklink: {clipboard} | mid: PED | keep | app+Core | none | Copy "foo"; run link; URL contains foo | B: M-placeholders (Raycast 2.9) (O) | app#47 · #1025 |
| QL-16 | quicklink: {selection} | mid: I | later: reads front app | app | Accessibility (I) | Select "foo" in Notes; run link; URL contains foo | B: M-placeholders (Raycast 2.9) (O) |  |
| QL-17 | quicklink: {date} and time | low: I | keep | app+Core | none | {date format="yyyy-MM-dd"} inserts today in ISO form | B: M-placeholders (Raycast 2.9) (O) | app#47 · #1025 |
| QL-18 | quicklink: {uuid} | low: I | keep | app+Core | none | Two runs give two different UUIDs | B: M-placeholders (Raycast 2.9) (O) | app#47 · #1025 |
| QL-19 | quicklink: {calculator} | low: I | later | app | none | {calculator} with 2*21 inserts 42 | B: M-placeholders, M-v2 (Raycast 2.9) (O) |  |
| QL-20 | quicklink: placeholder modifiers | low: I | keep | app+Core | none | "a b" argument becomes a%20b in the URL | B: M-placeholders (Raycast 2.9) (O) | app#47 · #1025 |
| QL-21 | quicklink: selected text as argument | mid: I | later: reads front app | app | Accessibility (I) | Select "cats", press link hotkey; search for cats opens | B: M-quicklinks (Raycast 2.9) (O) |  |
| QL-22 | quicklink: prefer existing tabs | mid: I | later | app | Automation (browser) (I) | With the URL open, running the link focuses that tab | B: M-quicklinks, M-v2 (Raycast 2.9) (O) |  |
| QL-23 | quicklink: link previews | low: I | drop: low value | app | none | n/a (dropped) | B: M-quicklinks (Raycast 2.9) (O) |  |
| QL-24 | quicklink: import JSON | high: I: owner leaves Raycast | keep: import Raycast JSON | app+Core | none | Import 20 Raycast links; 20 appear, duplicate count shown | B: M-quicklinks, M-impexp (Raycast 2.9) (O) | app#47 · #1025 |
| QL-25 | quicklink: export JSON | low: I | keep | Core | none | Export, re-import into clean profile; same links | B: M-impexp (Raycast 2.9) (O) | app#47 · #1025 |
| QL-26 | quicklink: shared (Teams) | low: Teams plan | drop: single-person product | Core | none | n/a (dropped) | B: M-quicklinks, M-teams (Raycast 2.9) (O) |  |
| QL-27 | quicklink: links to Raycast deeplinks | low: I | keep: lina:// links | app+Core | none | Quick link to lina://… runs that launcher command | B: M-hotkeys, CL-beta (Raycast 2.9) (O) | app#47 · #1025 |
| QL-28 | web: fallback web search | high: Google Search 153,929 installs | keep: a row under "send to agent" | app | none | `asdf qwer` ↵ goes to the agent; ↓↵ opens Google results in the default browser. | F: rc-v1fb (Raycast v1); ap-find (macOS 26) (O) | app#48 |
| QL-29 | web: manage fallbacks | low: I | keep | app | none | Moving DuckDuckGo above Google changes the order of the fallback rows. | F: rc-set (Raycast 2.9); rc-v1fb (O) | app#48 |
| QL-30 | web: engine via quick link | high: Kagi 7,457 and DuckDuckGo 5,950 installs as extensions | keep: quick links with `{query} | app | none | `ddg swift actors` ↵ opens DuckDuckGo results for "swift actors". | F: rc-ql (Raycast 2.9) (O) | app#47 |
| QL-31 | web: quick link library | low: I | later | app | none | Adding "Wikipedia" from the library makes `wiki seoul` work. | F: rc-ql (Raycast 2.9) (O) |  |
| QL-32 | web: search suggestions | mid: Google Search 153,929 installs | later: sends keystrokes to Google; opt-in only | app | none | With it on, `raycast` shows up to 5 suggestions; it is off by default. | F: ext-google, rc-store; ap-find (macOS 26) (O) |  |
| QL-33 | web: search selected text | mid: I | later | app | Accessibility (I) | Select text in Safari and press the hotkey: the browser opens a search for it. | F: ext-google; rc-ql (Raycast 2.9) (O) |  |
| QL-34 | web: URL detection | mid: I | keep | app | none | `example.com` ↵ opens https://example.com. | F: rc-sb (Raycast 2.9) (O) | app#47 |
| QL-35 | web: open in a chosen browser | low: I | keep | app | none | A quick link set to Safari opens in Safari while Chrome is the default. | F: rc-ql (Raycast 2.9) (O) | app#47 |
| QL-36 | web: prefer existing tab | low: I | later | app | Automation (browser) (I) | With github.com open, the quick link focuses that tab. | F: rc-ql (Raycast 2.9) (O) |  |
| QL-37 | web: bang routing | low: 1,486 installs | drop: quick link keywords cover it | — | — | n/a (dropped) | F: rc-store (Search Router 1,486 installs) (O) |  |
| QL-38 | web: AI answer as fallback | high: I, v2 headline | replace: agent conversation (fixed rule) | Core | none | A typed sentence ↵ lands in the last-used agent's main conversation, and a short reply shows in the panel. | F: rc-qai (Raycast 2.9); ap-actions, ap-find (macOS 27) (O) |  |
| QL-39 | web: AI web search | mid: Perplexity 69,539 installs | replace: agent conversation with a web tool | Core | none | "who won …" goes to the agent, and the reply cites sources. | F: rc-store (O) |  |
| QL-40 | web: Siri Suggestions and related content | mid: I | replace: agent conversation | Core | none | "who is Ada Lovelace" plus Return gives an agent reply in the panel | S: apple/sp-settings (macOS 27) (O) |  |
| QL-41 | web: Safari bookmarks | mid: Six Colors relied on favorites | later: Safari data needs Full Disk Access (I) | Node | Full Disk Access (I) | Not built until decided | S: apple/sp-narrow (macOS 27); sixcolors/2026-04 (O) |  |

### Calculator, conversions and time

56 rows: 43 keep, 1 replace, 12 later, 0 drop.

| ID | Feature | Use | Decision | Runs | Permission | Acceptance check | Source | Issue |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| CALC-01 | calc: inline answer in root | high: always in Root Search; Spotlight default | keep | app | none | Typing `12*3.5` shows `42` within 100 ms of the last key. | F: rc-calc (Raycast 2.9); ap-find (macOS 27) (O) | app#40 |
| CALC-02 | calc: basic arithmetic | high: I | keep | app | none | `956*23.94` = 22,886.64. | F: rc-cfcalc; ap-calc (macOS 27) (O) | app#40 |
| CALC-03 | calc: precedence and parentheses | high: I | keep | app | none | `2+3*4` = 14 and `(2+3)*4` = 20. | F: none (I) | app#40 |
| CALC-04 | calc: × ÷ √ symbols | mid: I, pasted text | keep | app | none | `120 × 1.08` = 129.6 and `√144` = 12. | F: ap-find (macOS 27) (O) | app#40 |
| CALC-05 | calc: percent of | high: I | keep | app | none | `52% of 900` = 468. | F: rc-calc (Raycast 2.9); ap-find (macOS 27) (O) | app#40 |
| CALC-06 | calc: percent off and add | mid: I | keep | app | none | `20% off 80` = 64 and `100 + 15%` = 115. | F: rc-calc (Raycast 2.9); rc-cfcalc (O) | app#40 |
| CALC-07 | calc: powers and roots | mid: I | keep | app | none | `2 power 10` = 1,024 and `square root of 625` = 25. | F: rc-calc (Raycast 2.9) (O) | app#40 |
| CALC-08 | calc: trig and hyperbolic | low: I | keep: free with the engine | app | none | `sin(pi/2)` = 1. | F: rc-calc (Raycast 2.9) (O) | app#40 |
| CALC-09 | calc: constants | low: I | keep | app | none | `2*pi` starts 6.2831. | F: none (I) | app#40 |
| CALC-10 | calc: number bases | low: Binary Tools 2,278 installs | keep: I, developer owner | app | none | `255 in hex` = 0xFF and `0b1010` = 10. | F: rc-cl1 1.96.0 (O) | app#40 |
| CALC-11 | calc: natural-language phrasing | mid: I | keep: engine grammar, no model | app | none | `3% of $123` = $3.69. | F: rc-calc (Raycast 2.9); soulver-doc (O) | app#40 |
| CALC-12 | calc: plain number shows nothing | high: I, avoids noise | keep | app | none | `2026` shows no calculator row, and `2026+1` shows 2,027. | F: rc-cl 2.3 (O) | app#40 |
| CALC-13 | calc: syntax highlighting | low: I | later | app | none | `10 usd in krw` shows number, unit and operator in distinct colors. | F: rc-calc (Raycast 2.9); rc-clb 0.51 (O) |  |
| CALC-14 | calc: copy answer (↵) | high: primary action | keep | app | none | `12*3` ↵ leaves `36` on the clipboard and closes the panel. | F: rc-calc (Raycast 2.9) (O) | app#40 |
| CALC-15 | calc: paste answer into app (⌘↵) | mid: I | later: copy only for now (owner 10-23) | app | Accessibility (I) | In TextEdit, `12*3` ⌘↵ types `36` at the caret. | F: rc-calc (Raycast 2.9) (O) |  |
| CALC-16 | calc: copy unformatted (⌥⌘C) | mid: I | keep | app | none | `2^10` ⌥⌘C puts `1024` on the clipboard. | F: rc-calc (Raycast 2.9) (O) | app#40 |
| CALC-17 | calc: copy question and answer (⌥⇧⌘C) | low: I | later | app | none | Clipboard holds `12*3 = 36`. | F: rc-calc (Raycast 2.9) (O) |  |
| CALC-18 | calc: swap question with answer | low: I | later: an `ans` token | app | none | `12*3` ⌘⇧↵ makes the field read `36`. | F: rc-cfcalc (I) |  |
| CALC-19 | calc: calculator history | mid: I | keep: stays on the Mac, never sent to Core | app | none | Copy 3 results: History lists them newest first, also after a restart. | F: rc-cl 2.0, 2.3; rc-clb 0.54 (O) | app#40 |
| CALC-20 | calc: number format from system | high: I | keep | app | none | Under a German number format, `1234.5*2` shows `2.469`. | F: rc-calc (Raycast 2.9); rc-clb 0.68; soulver (O) | app#40 |
| CALC-21 | calc: inline translate (`in <language>`) | low: I | replace: agent conversation | Core | none | `hello in korean` shows "Ask agent" and no local result. | F: rc-clb 0.51 (O) |  |
| CALC-22 | calc: currency shorthand | low-mid: I | keep | app | none | `USD1K in krw` converts 1,000 USD. | F: rc-calc (Raycast 2.9) (O) | app#42 |
| CALC-23 | calc: currency conversion | high: Currency Exchange 20,364 installs on top of the built-in | keep | app | none | `12 usd in krw` shows a KRW value within 1 s and states the rate date and source. | F: rc-calc (Raycast 2.9); ap-calc, ap-find (macOS 27) (O) | app#42 |
| CALC-24 | calc: bare amount to home currency | mid: I | keep: target is the locale currency (KRW) | app | none | `100 usd` alone shows the KRW amount. | F: ap-calc (macOS 27) (O) | app#42 |
| CALC-25 | calc: rate source and refresh | mid: I | keep: ECB daily (30 currencies incl. KRW) plus a wider daily source | app | none | Rates older than 3 days show "stale" with their date. | F: rc-cfcalc, rc-be, flare (O) | app#42 |
| CALC-26 | calc: offline currency | mid: I | keep: cache plus bundled fallback | app | none | With Wi-Fi off, `100 eur in krw` gives a result with the cached date. | F: rc-cl 2.5 (I) | app#42 |
| CALC-27 | calc: crypto | low-mid: CoinMarketCap 5,667, CoinGecko 2,872 installs | later: needs a coin price source | app | none | `1 btc in krw` shows KRW and the price time. | F: rc-cfcalc, rc-v1calc (O) |  |
| CALC-28 | calc: length | high: I | keep | app | none | `10ft in m` = 3.048 m. | F: rc-calc (Raycast 2.9); ap-calc, ap-find (macOS 27) (O) | app#41 |
| CALC-29 | calc: mass | mid: I | keep | app | none | `25 lb in kg` = 11.34 kg. | F: ap-calc (macOS 27) (O) | app#41 |
| CALC-30 | calc: temperature | high: I | keep | app | none | `23C to F` = 73.4 °F. | F: rc-cfcalc; ap-calc (macOS 27) (O) | app#41 |
| CALC-31 | calc: volume | mid: I | keep | app | none | `3 tsp in ml` = 14.79 ml. | F: rc-cfcalc (O) | app#41 |
| CALC-32 | calc: area | low-mid: I | keep | app | none | `1 acre in m2` = 4,046.86 m². | F: none (I) | app#41 |
| CALC-33 | calc: data size | mid: I, developers | keep | app | none | `1 GiB in MB` = 1,073.74 MB. | F: none (I) | app#41 |
| CALC-34 | calc: speed | low: I | keep | app | none | `100 km/h in mph` = 62.14 mph. | F: none (I) | app#41 |
| CALC-35 | calc: duration to timespan | low: I | keep | app | none | `145 mins to timespan` = 2 h 25 min. | F: rc-calc (Raycast 2.9) (O) | app#41 |
| CALC-36 | calc: rate units | low: I | later | app | none | `20 usd/hour in krw` shows KRW per hour. | F: rc-cfcalc (O) |  |
| CALC-37 | calc: design px at ppi | low: I | later | app | none | `2 inches in px at 72 ppi` = 144 px. | F: rc-calc (Raycast 2.9) (O) |  |
| CALC-38 | calc: CSS rem/em and px | low: I | later | app | none | `2rem in px` = 32 px. | F: rc-calc (Raycast 2.9) (O) |  |
| CALC-39 | calc: Korean units (평, 근, 돈) | mid: I, real estate and prices | keep | app | none | `30평 in m2` = 99.17 m². | F: soulver (I) | app#41 |
| CALC-40 | calc: Korean number words (만, 억, 조) | mid: I | keep | app | none | `1억 + 2천만` = 120,000,000. | F: none (I) | app#41 |
| CALC-41 | calc: time in a city | mid: Timezone Converter 22,459 and World Clock 9,390 installs | keep | app | none | `time in Seoul` shows the current KST time and UTC+9. | F: rc-calc (Raycast 2.9); ap-calc (macOS 27) (O) | app#43 |
| CALC-42 | calc: convert a time between zones | mid: same as above | keep | app | none | `3pm PST in KST` = 08:00 the next day. | F: rc-calc (Raycast 2.9) (O) | app#41 |
| CALC-43 | calc: time difference | low: I | keep | app | none | `diff London` shows the hours between Seoul and London. | F: rc-calc (Raycast 2.9) (O) | app#43 |
| CALC-44 | calc: time after a delay | low: I | keep | app | none | `time in 4 hours` shows now plus 4 h. | F: rc-calc (Raycast 2.9) (O) | app#43 |
| CALC-45 | calc: city clock plus hours, DST-safe | low: I | keep | app | none | `time in New York + 3 hours` matches New York's wall clock across the 2026-11-01 DST change. | F: rc-calc (Raycast 2.9) (O) | app#43 |
| CALC-46 | calc: airports and landmarks | low-mid: I | keep: bundled city and airport list | app | none | `time in ICN` gives Seoul time. | F: rc-calc (Raycast 2.9) (O) | app#43 |
| CALC-47 | calc: ISO 8601 timestamps | low-mid: Unix Timestamp 32,211 installs | keep | app | none | `2024-03-15T14:30:00Z` shows 2024-03-15 23:30 KST. | F: rc-calc (Raycast 2.9) (O) | app#43 |
| CALC-48 | calc: unix timestamp | mid: 32,211 installs | later: needs a `to date` keyword (bare numbers show nothing) | app | none | `1700000000 to date` = 2023-11-15 07:13 KST. | F: rc-store (Unix Timestamp ext) (I) |  |
| CALC-49 | calc: now, today, tomorrow, yesterday, time | mid: I | keep | app | none | `today` shows today's date in the system format. | F: rc-calc (Raycast 2.9); rc-cl 2.3 (O) | app#43 |
| CALC-50 | calc: relative dates | mid: I | keep | app | none | On 2026-10-23, `today + 3 weeks` = 2026-11-13. | F: rc-calc (Raycast 2.9); rc-cfcalc (O) | app#43 |
| CALC-51 | calc: days until | mid: I | keep | app | none | On 2026-10-23, `days until Christmas` = 63 days. | F: rc-calc (Raycast 2.9); rc-cfcalc (O) | app#43 |
| CALC-52 | calc: add to a date or time | low: I | keep | app | none | `August 5 + 5` = August 10. | F: rc-calc (Raycast 2.9) (O) | app#43 |
| CALC-53 | calc: work hours and workdays | low: I | later | app | none | `40h in workdays` = 5 workdays. | F: rc-calc (Raycast 2.9) (O) |  |
| CALC-54 | calc: color preview | low: I, designers | later | app | none | `#ff6363` shows a red swatch. | F: rc-calc (Raycast 2.9) (O) |  |
| CALC-55 | calc: copy color as format | low: I | later | app | none | ↵ on `#ff6363` → "HSL" copies `hsl(0 100% 69%)`. | F: rc-calc (Raycast 2.9) (O) |  |
| CALC-56 | calc: random number | low: I | keep | app | none | "random 1 6" returns a value from 1 to 6 | S: apple/sp-actions (macOS 27) (O) | app#40 |

### Dictionary and translation

13 rows: 9 keep, 1 replace, 3 later, 0 drop.

| ID | Feature | Use | Decision | Runs | Permission | Acceptance check | Source | Issue |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| DICT-01 | translate: translate command | high: STORE Google Translate 505,185 | keep: on-device Apple Translation (I) | app | none (I) | Type "안녕하세요"; English shown with Core not called | B: M-translate (Raycast 2.9) (O) | app#45 |
| DICT-02 | translate: inline in root | mid: I | keep | app | none | Type "good night in french"; result row shows French | B: M-translate (Raycast 2.9) (O) | app#45 |
| DICT-03 | translate: swap languages | low: I | keep | app | none | ⌘S moves result to source and reverses | B: M-translate (Raycast 2.9) (O) | app#45 |
| DICT-04 | translate: default languages | mid: I | keep | app | none | Default ko→en applies on open | B: M-translate (Raycast 2.9) (O) | app#45 |
| DICT-05 | translate: custom pair commands | mid: I | keep | app | none | "to Japanese" command translates without picker | B: M-translate (Raycast 2.9) (O) | app#45 |
| DICT-06 | translate: selected text as source | mid: I | later: reads front app | app | Accessibility (I) | Select a sentence, press hotkey; it is translated | B: M-translate (Raycast 2.9) (O) |  |
| DICT-07 | translate: paste to active app | mid: I | later: copy only for now (LINA-963) | app | Accessibility (I) | n/a until input injection is decided | B: M-translate (Raycast 2.9) (O) |  |
| DICT-08 | translate: replace selection | low: I | later | app | Accessibility (I) | n/a until input injection is decided | B: CL-beta (O) |  |
| DICT-09 | translate: continue in chat | low: I | replace: put into agent message | Core | none | Action opens the agent conversation with both texts | B: M-translate (Raycast 2.9) (O) |  |
| DICT-10 | dict: dictionary filter | low: I | keep | app | none | Pick only English; Korean entries hidden | B: CL-beta (0.61) (I) | app#44 |
| DICT-11 | lookup: dictionary definition | mid: Easy Dictionary 40,709 installs | keep: DCSCopyTextDefinition, offline | app | none | Type `serendipity`: a definition row from the system dictionary appears, and ↵ opens Dictionary. | F: ap-find (macOS 27); rc-store (O) | app#44 |
| DICT-12 | lookup: Korean dictionaries | mid: I | keep | app | none | `사과` shows a definition when a Korean dictionary is enabled. | F: none (I) | app#44 |
| DICT-13 | define: open in Dictionary | low: I | keep | app | none | Return on a definition opens Dictionary at that word | S: I (I) | app#44 |

### Emoji and symbols

18 rows: 10 keep, 0 replace, 6 later, 2 drop.

| ID | Feature | Use | Decision | Runs | Permission | Acceptance check | Source | Issue |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| EMO-01 | emoji: search | high: Q essential; PED; WR 296/yr | keep | app | none | Type "thumbs"; 👍 is first; Core not called | B: M-emoji (Raycast 2.9) (O) | app#46 |
| EMO-02 | emoji: category filter | low: I | keep | app | none | Choose Flags; only flags show | B: M-emoji (Raycast 2.9) (O) | app#46 |
| EMO-03 | emoji: symbols and Greek | mid: I | keep | app | none | Type "alpha"; α is listed | B: M-emoji, CL-2.x (2.8) (O) | app#46 |
| EMO-04 | emoji: AI results | low: I | later: on-device model (#966) | app | none | Type "celebration"; 🎉 appears without Core | B: M-emoji (Raycast 2.9) (O) |  |
| EMO-05 | emoji: custom keywords | low: I | keep | app | none | Add "ok" to 👌; "ok" finds 👌 | B: M-emoji (Raycast 2.9) (O) | app#46 |
| EMO-06 | emoji: save AI keywords | low: I | drop: needs AI results | app | none | n/a (dropped) | B: M-emoji (Raycast 2.9) (O) |  |
| EMO-07 | emoji: pin | mid: M tip | keep | app | none | Pin 🙏; it is first with empty query | B: M-emoji (Raycast 2.9) (O) | app#46 |
| EMO-08 | emoji: recent first | high: I | keep | app | none | Use 🔥 3 times; it is near top next open | B: M-emoji (Raycast 2.9) (O) | app#46 |
| EMO-09 | emoji: default skin tone | mid: I | keep | app | none | Set medium tone; 👍 copies as 👍🏽 | B: M-emoji (Raycast 2.9) (O) | app#46 |
| EMO-10 | emoji: per-pick skin tone | low: I | keep: copy only | app | none | ⌥⌘C shows 6 tones; pick one; copied | B: M-emoji (Raycast 2.9) (O) | app#46 |
| EMO-11 | emoji: paste into front app | high: I | later: copy only for now (LINA-963) | app | Accessibility (I) | n/a until input injection is decided | B: M-emoji (Raycast 2.9) (O) |  |
| EMO-12 | emoji: copy | high: I | keep: default action | app | none | ↵ on 😀, then ⌘V in Notes pastes 😀 | B: M-emoji (Raycast 2.9) (O) | app#46 |
| EMO-13 | emoji: paste and keep open | low: I | later | app | Accessibility (I) | Two emoji inserted without reopening | B: M-emoji (Raycast 2.9) (O) |  |
| EMO-14 | emoji: copy unicode | low: I | keep | app | none | On 😀, copies U+1F600 | B: M-emoji (Raycast 2.9) (O) | app#46 |
| EMO-15 | emoji: grid columns | low: I | drop: one layout | app | none | n/a (dropped) | B: M-emoji (Raycast 2.9) (O) |  |
| EMO-16 | emoji: replace system picker | mid: I | later: fn hotkey needs event tap | app | Input Monitoring (I) | fn opens LINA emoji search | B: M-emoji (Raycast 2.9) (O) |  |
| EMO-17 | emoji: inline ":" picker | mid: I | later: LINA text fields | app | none | In LINA composer ":fire" suggests 🔥 | B: CL-2.x (2.1), M-emoji (Raycast 2.9) (O) |  |
| EMO-18 | emoji: kaomoji | low: STORE 8,657 | later: as a symbol set | app | none | Type "shrug"; ¯\_(ツ)_/¯ is listed | B: STORE (2026-10-23) (O) |  |

### Calendar

22 rows: 11 keep, 6 replace, 3 later, 2 drop.

| ID | Feature | Use | Decision | Runs | Permission | Acceptance check | Source | Issue |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| CAL-01 | calendar: My Schedule | high: WR Calendar 511/yr | keep | Node | Calendars | Open schedule; today's events show in order | B: M-calendar (Raycast 2.9) (O) | app#65 · #1018 |
| CAL-02 | calendar: enabled calendars | mid: I | keep | Node | Calendars | Untick "Birthdays"; birthdays disappear | B: M-calendar (Raycast 2.9) (O) | app#65 · #1018 |
| CAL-03 | calendar: filter by title | low: I | keep | Node | Calendars | Type "standup"; only standups show | B: M-calendar (Raycast 2.9) (O) | app#65 · #1018 |
| CAL-04 | calendar: join next meeting | high: WR 981 calls/yr (1 user) | keep | Node | Calendars | Zoom event in 2 min: ↵ in empty panel opens zoom.us app | B: M-calendar (Raycast 2.9) (O) | app#65 · #1018 |
| CAL-05 | calendar: conference providers | high: I | keep | app | none | Meet link in notes field is detected as joinable | B: M-calendar, CL-2.x (2.8) (O) | app#65 · #1018 |
| CAL-06 | calendar: root display window | mid: I | keep | app | none | At 10 min before, event shows; at 11 it does not | B: M-calendar (Raycast 2.9) (O) | app#65 · #1018 |
| CAL-07 | calendar: Google links browser | low: I | later | app | none | Meet opens in Chrome while Safari is default | B: M-calendar (Raycast 2.9) (O) |  |
| CAL-08 | calendar: default calendar app | low: I | keep | Node | none | Set Fantastical; ↵ on event opens it there | B: M-calendar (Raycast 2.9) (O) | app#65 · #1018 |
| CAL-09 | calendar: auto join | low: I | later: unattended device action | Node | Calendars | At start time, confirm prompt appears, then call opens | B: M-calendar (Raycast 2.9) (O) |  |
| CAL-10 | calendar: camera preview | low: I | drop: call apps preview | Node | Camera | n/a (dropped) | B: M-calendar (Raycast 2.9) (O) |  |
| CAL-11 | calendar: auto transcribe | low: needs Granola | drop: third-party | Node | none | n/a (dropped) | B: M-calendar (Raycast 2.9) (O) |  |
| CAL-12 | calendar: menu bar agenda | mid: I | keep: menu bar extra | app+Node | Calendars; Menu Bar allow (macOS 26+) | Set "5 min before"; title appears 5 min before start | B: M-calendar (Raycast 2.9) (O) | app#65 · #1018 |
| CAL-13 | calendar: hide current event | low: I | keep | app | none | Set 5 min; title gone 5 min after start | B: M-calendar (Raycast 2.9) (O) | app#65 · #1018 |
| CAL-14 | calendar: menu bar actions | mid: I | keep | app+Node | Calendars | Menu "Join" opens the call | B: M-calendar (Raycast 2.9) (O) | app#65 · #1018 |
| CAL-15 | calendar: create event | mid: I | replace: agent conversation | Core+Node | Calendars | "lunch with Min tomorrow 12" creates the event after approval | B: M-calendar, M-v2 (Raycast 2.9) (O) |  |
| CAL-16 | calendar: RSVP | low: I | replace: agent conversation | Core+Node | Calendars | Ask agent to accept; invite shows Accepted | B: M-calendar (Raycast 2.9) (O) |  |
| CAL-17 | calendar: block time | low: I | replace: agent conversation | Core+Node | Calendars | Ask agent "block 2–4"; block appears | B: M-calendar (Raycast 2.9) (O) |  |
| CAL-18 | calendar: email attendees | low: I | later | Node | none | Mail draft lists all attendees | B: M-calendar (Raycast 2.9) (O) |  |
| CAL-19 | calendar: copy availability | low: I | replace: agent conversation | Core | Calendars | Ask "my free times Friday"; text lists free slots | B: M-calendar (Raycast 2.9) (O) |  |
| CAL-20 | calendar: copy details | low: I | keep | app | Calendars | ⇧⌘. copies the event title | B: M-calendar (Raycast 2.9) (O) | app#65 · #1018 |
| CAL-21 | calendar: delete event | low: I | replace: agent conversation with approval | Core+Node | Calendars | Delete asks approval, then event is gone | B: M-calendar (Raycast 2.9) (O) |  |
| CAL-22 | calendar: Ask Calendar (AI) | mid: I | replace: agent conversation | Core | Calendars | "what's tomorrow?" answered in the agent conversation | B: M-calendar (Raycast 2.9) (O) |  |

### System commands and menus

33 rows: 17 keep, 1 replace, 10 later, 5 drop.

| ID | Feature | Use | Decision | Runs | Permission | Acceptance check | Source | Issue |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| SYS-01 | system: lock screen | mid: M common pattern; PED | keep | Node | none (I) | Type "lock" ↵; login window shows; receipt in Core | B: M-system (Raycast 2.9) (O) | app#70 · #1017 |
| SYS-02 | system: sleep | mid: I | keep | Node | none (I) | Run Sleep; Mac sleeps; wakes to lock screen | B: M-system (Raycast 2.9) (O) | app#70 · #1017 |
| SYS-03 | system: sleep displays | mid: PED | keep | Node | none (I) | Run it; displays go dark, Mac stays awake | B: M-system (Raycast 2.9) (O) | app#70 · #1017 |
| SYS-04 | system: restart | low: I | keep | Node | Automation (System Events) (I) | Run Restart; confirm shows; cancel keeps session | B: M-system (Raycast 2.9) (O) | app#70 · #1017 |
| SYS-05 | system: shut down | low: I | keep | Node | Automation (System Events) (I) | Run Shut Down; confirm shows first | B: M-system (Raycast 2.9) (O) | app#70 · #1017 |
| SYS-06 | system: log out | low: I: macOS ⇧⌘Q exists | keep | Node | Automation (System Events) (I) | Run Log Out; macOS logout prompt shows | B: M-system (Raycast 2.9) (O) | app#70 · #1017 |
| SYS-07 | system: reopen windows checkbox | low: new | keep | Node | none (I) | Checkbox default matches the macOS setting | B: CL-2.x (2.9) (O) | app#70 · #1017 |
| SYS-08 | system: show screen saver | low: I | drop: rare (I) | Node | none | n/a (dropped) | B: M-system (Raycast 2.9) (O) |  |
| SYS-09 | system: play/pause | low: I: keyboard media keys | drop: media keys exist | Node | Accessibility (I) | n/a (dropped) | B: M-system (Raycast 2.9) (O) |  |
| SYS-10 | system: next/previous track | low: I: keyboard media keys | drop: media keys exist | Node | Accessibility (I) | n/a (dropped) | B: M-system (Raycast 2.9) (O) |  |
| SYS-11 | system: toggle mute | mid: M common pattern (call hotkey) | keep | Node | none (I) | Run "mute"; output muted; again unmutes | B: M-system (Raycast 2.9) (O) | app#70 · #1017 |
| SYS-12 | system: volume up/down | low: I: volume keys | drop: volume keys exist | Node | none (I) | n/a (dropped) | B: M-system (Raycast 2.9) (O) |  |
| SYS-13 | system: set volume | low: I | later | Node | none (I) | "volume 30" sets output to 30% | B: M-system (Raycast 2.9) (O) |  |
| SYS-14 | system: volume presets | low: I | later | Node | none (I) | Run 50% preset; volume reads 50% | B: M-system (Raycast 2.9) (O) |  |
| SYS-15 | system: show desktop | low: I: fn-H exists (APPLE-keys) | drop: macOS fn-H | Node | Accessibility (I) | n/a (dropped) | B: M-system (Raycast 2.9) (O) |  |
| SYS-16 | system: toggle appearance | mid: M common pattern; PED | keep | Node | Automation (System Events) (I) | Run it; macOS switches dark/light | B: M-system (Raycast 2.9) (O) | app#70 · #1017 |
| SYS-17 | system: toggle Stage Manager | low: I | later | Node | none (I) | Run it; Stage Manager strip appears | B: M-system (Raycast 2.9) (O) |  |
| SYS-18 | system: open trash | low: I | keep | Node | none | Run it; Trash window opens | B: M-system (Raycast 2.9) (O) | app#70 · #1017 |
| SYS-19 | system: empty trash | mid: M common pattern (alias et); PED | keep: with confirm | Node | Automation (Finder) (I) | Run "et"; confirm; Trash is empty; receipt in Core | B: M-system, CF-system (Raycast 2.9) (O) | app#70 · #1017 |
| SYS-20 | system: eject all disks | low: I | keep | Node | none (I) | Mount 2 DMGs; run; both ejected | B: M-system, CL-2.x (2.6) (O) | app#70 · #1017 |
| SYS-21 | system: toggle hidden files | low: I: Finder ⇧⌘. exists | later | Node | none (I) | Run it; Finder shows dotfiles | B: M-system (Raycast 2.9) (O) |  |
| SYS-22 | system: hide others | low: I: macOS ⌥⌘H | replace: macOS ⌥⌘H | Node | none (I) | n/a (macOS) | B: M-system (Raycast 2.9) (O) |  |
| SYS-23 | system: unhide all | low: I | later | Node | none (I) | Hide 3 apps; run; all visible | B: M-system (Raycast 2.9) (O) |  |
| SYS-24 | system: quit all apps | mid: PED; manual tip end-of-day | keep: confirm and exclusions | Node | none (I) | With Music excluded, run; all other apps quit, Music stays | B: M-system (Raycast 2.9) (O) | app#70 · #1017 |
| SYS-25 | system: quit all except front | mid: PED "quit all other apps" | keep | Node | none (I) | Run from Safari; only Safari stays | B: M-system (Raycast 2.9) (O) | app#70 · #1017 |
| SYS-26 | system: dismiss notifications | low: I | later | Node | Accessibility (I) | 3 banners shown; run; none left | B: M-system (Raycast 2.9) (O) |  |
| SYS-27 | system: toggle Bluetooth | low: I | later | Node | Bluetooth (I) | Run; Bluetooth menu shows Off | B: M-system, CL-beta (Raycast 2.9) (O) |  |
| SYS-28 | menu: search menu items | mid: PED; I | keep | Node | Accessibility (Device Control and Data Access on macOS 27) | In Safari type "private"; New Private Window opens | B: M-nav, CL-beta (Raycast 2.9) (O) | app#71 · #1020 |
| SYS-29 | menu: item state | mid: I | keep | Node | Accessibility | A greyed menu item shows disabled and does not run | B: M-nav (Raycast 2.9) (O) | app#71 · #1020 |
| SYS-30 | menu: pinned items first | low: I | later | app | none | Pin "Export as PDF"; it is first for "ex" | B: CL-beta (0.65) (O) |  |
| SYS-31 | menu: disable per app | low: I | later | app | none | Disable for Terminal; no items listed there | B: CL-beta (0.64) (O) |  |
| SYS-32 | menu: switch windows | mid: I | keep | Node | Accessibility; Screen Recording for titles (I) | Type a document title; ↵ brings that window front | B: M-nav (Raycast 2.9) (O) | app#71 · #1020 |
| SYS-33 | menu: toggle menu bar auto-hide | unknown | later: macOS setting via Node | Node | Automation (System Events) (I) | Run it; menu bar auto-hide flips | B: M-system (absent, Raycast 2.9) (I) |  |

### Notes, focus and other built-ins

30 rows: 0 keep, 10 replace, 18 later, 2 drop.

| ID | Feature | Use | Decision | Runs | Permission | Acceptance check | Source | Issue |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| MISC-01 | notes: Raycast Notes window | mid: WR 92/yr; M hotkey tip | replace: memo quick capture | Core | none | Hotkey opens capture; text lands in Core memo inbox | B: M-notes (Raycast 2.9) (O) |  |
| MISC-02 | notes: create note | mid: I | replace: memo capture | Core | none | "Create memo" saves a new memo in Core | B: M-notes (Raycast 2.9) (O) |  |
| MISC-03 | notes: search notes | mid: I | replace: memo search | Core | none | Type a word from a memo; it is listed | B: M-notes (Raycast 2.9) (O) |  |
| MISC-04 | notes: formatting | mid: I | replace: memo block editor | Core | none | Typing "# " makes a heading | B: M-notes (Raycast 2.9) (O) |  |
| MISC-05 | notes: task lists | mid: I | replace: checklist block, promote to to-do | Core | none | "[ ] buy milk" becomes a checklist line | B: M-notes, CL-2.x (2.8) (O) |  |
| MISC-06 | notes: pin and ⌘0–9 | low: I | later | Core | none | ⌘1 opens first pinned memo | B: M-notes (Raycast 2.9) (O) |  |
| MISC-07 | notes: back and forward | low: I | later | app | none | ⌘[ returns to previous memo | B: M-notes (Raycast 2.9) (O) |  |
| MISC-08 | notes: recently deleted | low: I | replace: Core saved versions | Core | none | Delete memo, restore it from history | B: M-notes (Raycast 2.9) (O) |  |
| MISC-09 | notes: menu bar toggle | low: I | later: menu bar extra | app | none | Menu bar button opens memo capture | B: M-notes (Raycast 2.9) (O) |  |
| MISC-10 | notes: Esc loses focus | low: I | later | app | none | Esc in capture returns focus to previous app | B: M-notes (Raycast 2.9) (O) |  |
| MISC-11 | notes: send to AI Chat | mid: new | replace: put memo into agent message | Core | none | ⌘J on a memo opens conversation with it attached | B: CL-2.x (2.9) (O) | app#39 · #1020 |
| MISC-12 | notes: free limit | n/a | drop: no limit | Core | none | n/a (dropped) | B: M-notes (Raycast 2.9) (O) |  |
| MISC-13 | focus: start session | low: WR 71/yr; free | later: check macOS Focus and Screen Time first | Node | Automation and Accessibility | Block Slack for 25 min; Slack quits and cannot reopen | B: M-focus (Raycast 2.9) (O) |  |
| MISC-14 | focus: toggle session | low: M "most-used" | later | Node | Automation and Accessibility | Hotkey starts last setup; again stops it | B: M-focus (Raycast 2.9) (O) |  |
| MISC-15 | focus: pause, resume, complete, edit | low: I | later | Node | Automation and Accessibility | Pause 5 min; Slack opens; resume blocks it again | B: M-focus (Raycast 2.9) (O) |  |
| MISC-16 | focus: categories | low: I | later | app | none | Create "Social"; use it in a session | B: M-focus, CL-beta (Raycast 2.9) (O) |  |
| MISC-17 | focus: blocking behavior | low: I | later | Node | Automation (browsers), Accessibility | Visit a blocked site; block page shows | B: M-focus (Raycast 2.9) (O) |  |
| MISC-18 | focus: snooze | low: I | later | Node | none | Snooze 1 min; app usable for 1 min only | B: M-focus (Raycast 2.9) (O) |  |
| MISC-19 | focus: focus bar | low: I | later: menu bar widget | app | none | Move to menu bar; countdown shows there | B: M-focus (Raycast 2.9) (O) |  |
| MISC-20 | focus: sleep-aware | low: I | later | Node | none | Sleep 10 min mid-session; remaining time unchanged | B: M-focus (Raycast 2.9) (O) |  |
| MISC-21 | focus: restore apps | low: I | later | Node | none | With option on, Slack reopens at end | B: M-focus (Raycast 2.9) (O) |  |
| MISC-22 | focus: Shortcuts and deeplinks | low: I | later: App Intents | app | none | Shortcut "Start Focus" starts a session | B: M-focus (Raycast 2.9) (O) |  |
| MISC-23 | focus: Ask Raycast Focus | low: I | replace: agent conversation | Core | none | "focus 30 min on report" starts a session after approval | B: M-focus (Raycast 2.9) (O) |  |
| MISC-24 | shots: search screenshots | mid: WR 100/yr | later: file search plus OCR index | Node | Files and Folders (Desktop) or Full Disk Access | Take a screenshot; it is first in the grid | B: M-screenshots (Raycast 2.9) (O) |  |
| MISC-25 | shots: text search (OCR) | mid: PED | later | Node | Files and Folders (I) | "text:invoice" finds a screenshot of an invoice | B: M-screenshots (Raycast 2.9) (O) |  |
| MISC-26 | shots: paste latest screenshot | mid: I | later: copy only for now | Node | Accessibility (I) | n/a until input injection is decided | B: M-screenshots (Raycast 2.9) (O) |  |
| MISC-27 | shots: scopes and retention | low: I | later | Node | Files and Folders | Set 1 week; older ones leave the index | B: M-screenshots (Raycast 2.9) (O) |  |
| MISC-28 | camera: open camera | low: I | drop: Photo Booth or call apps | Node | Camera | n/a (dropped) | B: CL-1.63, CL-2.x (2.7, 2.8) (O) |  |
| MISC-29 | store: coin flip and dice | low: STORE 2,563 | replace: agent conversation | Core | none | Ask "flip a coin"; agent answers | B: STORE (O) |  |
| MISC-30 | store: reminders (was built-in in v1) | mid: I | replace: LINA to-dos | Core | Reminders if imported | n/a (to-do design) | B: M1-reminders (O) |  |

### AI

135 rows: 8 keep, 67 replace, 30 later, 30 drop.

| ID | Feature | Use | Decision | Runs | Permission | Acceptance check | Source | Issue |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| AI-01 | ai: Quick AI empty start | low (I) | drop: the launcher line is the prompt | — | none | n/a (dropped) | A: m/quick-ai (Raycast 2.9) (O) |  |
| AI-02 | ai: reply ↵ pastes into previous app | mid (I: default primary action) | keep: copy reply; later: paste (key injection) | app | Accessibility (paste only) | ⌘C on a panel reply copies plain text; no keystroke reaches another app | A: m/quick-ai (Raycast 2.9) (O) | app#36 · #1011 |
| AI-03 | ai: Quick AI follow-ups | mid (I) | replace: agent conversation | app+Core | none | A second launcher line lands as the next turn of the same main conversation | A: m/quick-ai (Raycast 2.9) (O) |  |
| AI-04 | ai: regenerate (⌘R, ⇧⌘R other model) | low (I) | later: action in app conversation | Core | none | Regenerate in the app replaces the last reply; the ledger keeps both requests | A: m/quick-ai, m/ai-chat (Raycast 2.9) (O) |  |
| AI-05 | ai: Quick AI new chat, history (⌘N, ⌘[ ⌘]) | low (I) | drop: one main conversation per agent | — | none | n/a (dropped) | A: m/quick-ai (Raycast 2.9) (O) |  |
| AI-06 | ai: auto-new chat after inactivity | low (I) | drop: no launcher-only conversation | — | none | n/a (dropped) | A: m/quick-ai, m/settings (Raycast 2.9) (O) |  |
| AI-07 | ai: continue in AI Chat (⌘J) | mid (I) | keep: "open in conversation" on the panel reply | app | none | ⌘J on a panel reply opens the app at that turn | A: m/quick-ai (Raycast 2.9) (O) | app#36 · #1011 |
| AI-08 | ai: hide Tab hint | low (I) | drop: minor setting | — | none | n/a (dropped) | A: m/quick-ai, cl 2.6 (O) |  |
| AI-09 | ai: dictate a prompt (⌃M) | low (I) | replace: macOS Dictation in the launcher field | app | none (system Dictation) (I) | Start system Dictation in the launcher field; Korean text appears at the caret and ↵ sends it | A: m/quick-ai, m/dictation (Raycast 2.9) (O) |  |
| AI-10 | ai: Ask User Question | low (I: always on, no setting) | replace: agent questions | app+Core | none | The agent asks a 3-option question; arrow keys and ↵ in the panel answer it and the turn continues | A: m/quick-ai, m/ai-chat (Raycast 2.9) (O) |  |
| AI-11 | ai: AI Chat window | high (I: main AI surface on Pro) | replace: LINA app conversation screen | app+Core | none | The app conversation shows the agent's history, attachments and tool calls | A: m/ai-chat (Raycast 2.9) (O) |  |
| AI-12 | ai: Send to AI reuses active chat | low (I) | drop: always the main conversation | — | none | n/a (dropped) | A: m/ai-chat (Raycast 2.9) (O) |  |
| AI-13 | ai: steer or queue follow-up | low (I) | replace: agent conversation (Core turn rules) | Core | none | A message sent mid-reply is handled by the Core rule and never lost | A: m/ai-chat (Raycast 2.9) (O) |  |
| AI-14 | ai: edit a sent message | low (I) | later: conversation edit | Core | none | Editing the last message re-runs the reply; the ledger keeps the old turn | A: m/ai-chat (Raycast 2.9) (O) |  |
| AI-15 | ai: branch chat | low (I) | replace: conversation threads | Core | none | Starting a thread from a turn leaves the main conversation unchanged | A: m/ai-chat, m/new-in-v2 (Raycast 2.9) (O) |  |
| AI-16 | ai: find in chat (⌘F) | low (I) | replace: app conversation search | app | none | ⌘F in a conversation highlights matches; ↵ steps to the next | A: m/ai-chat (Raycast 2.9) (O) |  |
| AI-17 | ai: chat history search | mid (I) | replace: app search and memory recall | app+Core | none | Searching a word from an old turn opens that turn | A: m/ai-chat (Raycast 2.9) (O) |  |
| AI-18 | ai: pin, rename, archive, auto-archive chats | low (I) | drop: one main conversation per agent | — | none | n/a (dropped) | A: m/ai-chat (Raycast 2.9) (O) |  |
| AI-19 | ai: Recently Deleted (60 days) | low (I) | drop: conversation is canon in Core | — | none | n/a (dropped) | A: m/ai-chat (Raycast 2.9) (O) |  |
| AI-20 | ai: always on top | low (I) | drop: minor window option | — | none | n/a (dropped) | A: m/ai-chat (Raycast 2.9) (O) |  |
| AI-21 | ai: background notification | mid (I) | replace: app notifications | app | Notifications | Start a long task and switch apps; a notification arrives and opens that turn | A: m/ai-chat (Raycast 2.9) (O) |  |
| AI-22 | ai: summarize long chat | mid (I: automatic) | replace: Core compaction | Core | none | A 200-turn conversation keeps answering; the ledger shows a compaction | A: m/ai-chat (Raycast 2.9) (O) |  |
| AI-23 | ai: Mermaid and charts in replies | low (I) | later: app rendering | app | none | A reply with a mermaid block renders as a diagram in the app | A: m/new-in-v2, cl 2.9 (O) |  |
| AI-24 | ai: image generation | low (I) | later: no image tool yet | Core | none | Asking for an image attaches a file to the reply that can be dragged out | A: m/ai-chat (Raycast 2.9) (O) |  |
| AI-25 | ai: chat zoom and send key | low (I) | replace: app settings | app | none | After changing the send key in the app, ↵ behaves as set | A: m/ai-chat, m/settings (Raycast 2.9) (O) |  |
| AI-26 | ai: prevent sleep during AI runs | low (I) | drop: Core is always on | — | none | n/a (dropped) | A: m/ai-chat, m/automations (Raycast 2.9) (O) |  |
| AI-27 | ai: model picker | mid (I) | replace: Core model policy (person may force) | Core | none | After telling the agent "이번엔 Claude로", the ledger shows the next turn served by Claude | A: m/ai-chat (Raycast 2.9) (O) |  |
| AI-28 | ai: Auto model preference | mid (I: default choice) | replace: Core routing (local first, #966) | Core | none | A short launcher line that fits is served locally; the ledger names the model | A: m/ai-chat, cl 2.6 (O) |  |
| AI-29 | ai: default model per surface | low (I) | replace: Core roles from connected plans | Core | none | Settings show plans in words and no model id; roles are filled automatically | A: m/ai-chat, cl 2.9 (O) |  |
| AI-30 | ai: manage model list | low (I) | drop: general settings show no model ids | — | none | n/a (dropped) | A: m/ai-chat, m/ai-credits (Raycast 2.9) (O) |  |
| AI-31 | ai: reasoning effort and creativity | low (I) | replace: Core policy chooses effort | Core | none | The ledger records the effort used for each request | A: m/ai-chat, m/ai-commands (Raycast 2.9) (O) |  |
| AI-32 | ai: Raycast model list | high (I: what every Pro user gets) | replace: the person's own accounts | Core | none | With ChatGPT and Claude connected, both serve turns and LINA bills nothing | A: dev/ai (API 2.x), m/ai-credits (O) |  |
| AI-33 | ai: top-tier models on Pro+ and Max | low (I) | drop: no LINA tiers | — | none | n/a (dropped) | A: m/ai-credits, pricing (Raycast 2.9) (O) |  |
| AI-34 | ai: BYOK keys | low (I) | replace: Core connected accounts (API keys) | Core | none | After entering a Gemini key, an agent turn is served by Gemini and the key is in the Keychain | A: m/byok, m/ai-privacy (Raycast 2.9) (O) |  |
| AI-35 | ai: BYOS Claude | mid (I: headline of 2.3) | replace: Core Claude adapter | Core | none | With the Claude plan connected, its 5-hour and weekly usage show in Connected accounts | A: m/byos, cl 2.3 (O) |  |
| AI-36 | ai: BYOS ChatGPT | mid (I) | replace: Core ChatGPT sign-in | Core | none | Sign in with ChatGPT; the default provider answers the next launcher line | A: m/byos, cl 2.3 (O) |  |
| AI-37 | ai: usage view (/usage) | mid (I) | replace: Core quota rows | app+Core | none | Connected accounts show remaining quota and reset time | A: m/ai-credits, m/byos, cl 2.6 (O) |  |
| AI-38 | ai: custom providers | low (I) | later: not in LINA's provider list | Core | none | n/a until such a provider is added | A: m/custom-providers (Raycast 2.9) (O) |  |
| AI-39 | ai: custom provider tools block | low (I) | replace: Core web tools | Core | none | A current-events question gets a reply that cites fetched sources | A: m/custom-providers, cl 2.7 (O) |  |
| AI-40 | ai: local models (Ollama) | low (O: Ollama AI extension, 42,736 installs, as a proxy) | later: Apple Foundation Models first (#966) | Node | none | With the network off, a short rewrite still completes on the Mac | A: m/local-models (Raycast 2.9) (O) |  |
| AI-41 | ai: extension-provided models | low (O: 14 store extensions, 59,498 installs) | drop: no extension model market | — | none | n/a (dropped) | A: m/extension-models, cl 2.5 (O) |  |
| AI-42 | ai: web search in chat | mid (I) | replace: Core web tools | Core | none | A question about today's news gets a reply with source links | A: m/ai-chat (Raycast 2.9) (O) |  |
| AI-43 | ai: monthly credits | low (I: billing, not a feature) | drop: uses the person's own subscriptions | — | none | n/a (dropped) | A: m/ai-credits, pricing (Raycast 2.9) (O) |  |
| AI-44 | ai: credit top-ups | low (I) | drop: no LINA billing | — | none | n/a (dropped) | A: m/ai-credits (Raycast 2.9) (O) |  |
| AI-45 | ai: plan price | low (I: billing) | drop: LINA sells no AI plan | — | none | n/a (dropped) | A: pricing (Raycast 2.9) (O) |  |
| AI-46 | ai: Profile | mid (I) | replace: Core memory and persona | Core | none | State your role once; a later reply uses it without asking | A: m/personalization (Raycast 2.9) (O) |  |
| AI-47 | ai: Memory | mid (I: v2 headline) | replace: Core memory | Core | none | Mention a project; the next day the agent recalls it and the memory view shows the source turn | A: m/personalization, m/ai-privacy (Raycast 2.9) (O) |  |
| AI-48 | ai: Add to Memory (⇧⌘L), "remember this" | low (I) | replace: Core memory | Core | none | After saying "기억해: …", the memory list shows the entry | A: m/personalization (Raycast 2.9) (O) |  |
| AI-49 | ai: @memory recall | low (I) | replace: Core memory recall | Core | none | Asking "지난주에 정한 거?" gets a reply that cites the old turn | A: m/ai-extensions (Raycast 2.9) (O) |  |
| AI-50 | ai: Agents (ex-Presets) | low (I: renamed v1 Presets) | replace: LINA agents | Core | none | An agent created as "번역가" answers with its own instructions | A: m/agents (Raycast 2.9) (O) |  |
| AI-51 | ai: agent as launcher item with hotkey or alias | low (I) | keep: name an agent in the launcher line | app+Core | none | "@번역가 hello" goes to that agent's main conversation | A: m/agents (Raycast 2.9) (O) | app#36 · #1011 |
| AI-52 | ai: built-in Deep Research agent | low (I) | replace: agent with web tools | Core | none | A research request gets a reply that lists sources | A: m/agents (Raycast 2.9) (O) |  |
| AI-53 | ai: Projects | low (I) | replace: work threads and plans | Core | none | A work thread keeps its own instructions across turns | A: m/projects (Raycast 2.9) (O) |  |
| AI-54 | ai: project working directory | low (I) | replace: Node grant on a folder | Node | Files and Folders | With a folder granted, the agent edits a file there with a receipt; outside it, the agent asks | A: m/projects, m/ai-extensions (Raycast 2.9) (O) |  |
| AI-55 | ai: Skills (SKILL.md) | low (O: 15 store extensions bundle skills) | replace: LINA skill catalog (no import of other apps' folders) | Core | none | A plugin's skill loads only while that plugin is on | A: m/skills (Raycast 2.9) (O) |  |
| AI-56 | ai: @mention a skill | low (I) | replace: Core skill catalog | Core | none | A skill named in a message is used in that turn | A: m/skills (Raycast 2.9) (O) |  |
| AI-57 | ai: Automations | low (I: new in 2.3) | replace: Core scheduled agent work | Core | none | "매일 아침 8시에 일정 요약" runs while the Mac sleeps; the result is in the conversation | A: m/automations, cl 2.3 (O) |  |
| AI-58 | ai: create automation by asking | low (I) | replace: agent conversation | Core | none | Asking the agent to schedule something gets a confirmation before it saves | A: m/automations (Raycast 2.9) (O) |  |
| AI-59 | ai: Send Focused Window to AI | mid (I: v2 headline) | later: screen read under a grant | app+Core | Accessibility, Screen Recording | Hotkey in Safari opens the panel with a window chip; the agent answers about the page | A: m/screen-awareness (Raycast 2.9) (O) |  |
| AI-60 | ai: Send Selected Text to AI | high (O: 250 store extensions read selection, 21.1% of downloads) | keep: one key sends the selection to the agent | Core+Node | Accessibility | Select text in TextEdit and press the key; a selection chip appears; the pasteboard changeCount is unchanged | A: m/screen-awareness (Raycast 2.9) (O) | app#39 · #1020 |
| AI-61 | ai: Send Screen to AI | low (I) | later: screenshot attach | app+Core | Screen Recording | The panel shows a screenshot chip; the agent describes the screen | A: m/screen-awareness (Raycast 2.9) (O) |  |
| AI-62 | ai: Send Screen Area to AI | mid (I) | later: screenshot attach | app+Core | Screen Recording | Drag an area; the chip shows that crop | A: m/screen-awareness (Raycast 2.9) (O) |  |
| AI-63 | ai: Send Element to AI | low (I) | later: element capture | app+Core | Accessibility, Screen Recording | A password field's value never appears in the message | A: m/screen-awareness, cl 2.7 (O) |  |
| AI-64 | ai: Send Window to AI | low (I) | later: window capture | app+Core | Screen Recording | Pick a background window; the chip shows that window | A: m/screen-awareness, cl 2.7 (O) |  |
| AI-65 | ai: capture goes to Chat or Quick AI | low (I) | drop: always the main conversation | — | none | n/a (dropped) | A: m/screen-awareness (Raycast 2.9) (O) |  |
| AI-66 | ai-attach: file from disk or drag | mid (I) | keep: drop a file on the launcher panel to add it to the message | app+Core | Files and Folders (I) | Drop a PDF on the panel, type "요약"; the agent summarizes that PDF | A: m/ai-chat, cl 2.1 (O) | app#39 · #1020 |
| AI-67 | ai-attach: Finder selection | mid (O: 166 store extensions read Finder selection, 10.0% of downloads) | keep: attach selected Finder items | Core+Node | Automation (Finder) (I) | Select two files in Finder and press the key; two file chips appear | A: m/screen-awareness, dev/environment (API 2.x; Raycast 2.9) (O) | app#39 · #1020 |
| AI-68 | ai-attach: clipboard history item | low (I) | keep: the person picks a history item into the message | app | none | Picking a history item puts it in the message; nothing else is sent | A: m/ai-chat (Raycast 2.9) (O) | app#39 · #1020 |
| AI-69 | ai-attach: browser tabs | mid (I) | later: browser tab read | app+Core | Automation (browser) (I) | — | A: m/ai-chat, browser-ext (Raycast 2.9) (O) |  |
| AI-70 | ai-attach: calendar events | low (I) | replace: agent calendar tool | Core+Node | Calendars | "오늘 일정 정리" gets a reply that lists today's events | A: m/ai-chat (Raycast 2.9) (O) |  |
| AI-71 | ai-attach: notes | low (I) | replace: memo reference | Core | none | A message that names a memo makes the agent quote it | A: m/ai-chat (Raycast 2.9) (O) |  |
| AI-72 | ai-cmd: Improve Writing | mid (I: built-in, selection-based) | replace: send selection to agent | app+Core | Accessibility | Select text, press the selection key, type "다듬어 줘"; the reply has the rewrite | A: m/ai-commands (Raycast 2.9) (O) | app#39 · #1020 |
| AI-73 | ai-cmd: Fix Spelling and Grammar | mid (I) | replace: send selection to agent | app+Core | Accessibility | Selection with typos plus "맞춤법" returns the corrected text | A: m/ai-commands (Raycast 2.9) (O) | app#39 · #1020 |
| AI-74 | ai-cmd: Explain This in Simple Terms | mid (I) | replace: send selection to agent | app+Core | Accessibility | Selection plus "쉽게 설명" returns a plain explanation | A: m/ai-commands (Raycast 2.9) (O) | app#39 · #1020 |
| AI-75 | ai-cmd: Change Tone to Professional | low (I) | replace: send selection to agent | app+Core | Accessibility | Selection plus "격식 있게" returns a formal version | A: m/ai-commands (Raycast 2.9) (O) | app#39 · #1020 |
| AI-76 | ai-cmd: Change Tone to Friendly | low (I) | replace: send selection to agent | app+Core | Accessibility | Selection plus "친근하게" returns a warmer version | A: m/ai-commands (Raycast 2.9) (O) | app#39 · #1020 |
| AI-77 | ai-cmd: Find Bugs in Code | low (I) | replace: send selection to agent | app+Core | Accessibility | Code selection plus "버그" returns a list of issues | A: m/ai-commands (Raycast 2.9) (O) | app#39 · #1020 |
| AI-78 | ai-cmd: Summarize Webpage | mid (I: listed on the browser extension page) | later: browser tab read | app+Core | Automation (browser) (I) | In Safari, press the key; the agent summarizes the front tab | A: m/ai-commands, browser-ext (Raycast 2.9) (O) |  |
| AI-79 | ai-cmd: Ask About Webpage | low (I) | later: browser tab read | app+Core | Automation (browser) (I) | — | A: m/ai-commands (Raycast 2.9) (O) |  |
| AI-80 | ai-cmd: Quick Fix | mid (I: default hotkey assigned) | later: in-place edit (AX write) | app+Core | Accessibility | In TextEdit, the trigger fixes the text in place and the clipboard is unchanged | A: m/ai-commands, m/new-in-v2 (Raycast 2.9) (O) |  |
| AI-81 | ai-cmd: create custom AI command | mid (I) | later: saved prompt as a launcher item | app+Core | none | A saved prompt appears in the launcher; ↵ sends it with the selection to the agent | A: m/ai-commands (Raycast 2.9) (O) |  |
| AI-82 | ai-cmd: {selection} placeholder | high (I: core of AI Commands) | keep: send selection to agent, one key | Core+Node | Accessibility | The selection arrives as a chip; the pasteboard changeCount is unchanged | A: m/placeholders (Raycast 2.9) (O) | app#39 · #1020 |
| AI-83 | ai-cmd: {clipboard} placeholder, offset | low (I) | replace: the person picks a history item into the message | app | none | A picked clipboard item appears as a chip in the message | A: m/placeholders (Raycast 2.9) (O) |  |
| AI-84 | ai-cmd: {browser-tab} placeholder | mid (I) | later: browser tab read | app+Core | Automation (browser) (I) | — | A: m/placeholders, browser-ext (Raycast 2.9) (O) |  |
| AI-85 | ai-cmd: {argument} placeholder | low (I) | drop: the launcher line carries the input | — | none | n/a (dropped) | A: m/placeholders (Raycast 2.9) (O) |  |
| AI-86 | ai-cmd: Replace Selection output | mid (I) | later: paste back only on ⌘↵ | app | Accessibility | ⌘↵ on a reply replaces the selection; without it nothing changes | A: m/ai-commands, cl 2.7 (O) |  |
| AI-87 | ai-cmd: highlight edits | low (I) | later: diff view in the reply | app | none | The reply marks inserted and removed words | A: m/ai-commands (Raycast 2.9) (O) |  |
| AI-88 | ai-cmd: per-command model, creativity, effort | low (I) | drop: Core policy | — | none | n/a (dropped) | A: m/ai-commands, cl 2.9 (O) |  |
| AI-89 | ai-cmd: share, import, duplicate, tags | low (I) | drop: no command library | — | none | n/a (dropped) | A: m/ai-commands (Raycast 2.9) (O) |  |
| AI-90 | ai-cmd: @extension inside a command | low (I) | replace: agent with plugins | Core | none | "오늘 오후 일정?" from the launcher returns calendar items | A: m/ai-commands (Raycast 2.9) (O) |  |
| AI-91 | ai-ext: @-mention extension tools | mid (O: 318 store extensions ship tools, 40.6% of downloads) | replace: plugins (Core tools with approvals) | Core | none | With the GitHub plugin on, "내 리뷰 대기 PR" lists PRs | A: m/ai-extensions (Raycast 2.9) (O) |  |
| AI-92 | ai-ext: automatic tool discovery | mid (I) | replace: Core tool selection | Core | none | A request that names no plugin still uses the right plugin | A: m/new-in-v2, cl 2.8 (O) |  |
| AI-93 | ai-ext: permission mode | high (I: applies to every tool call) | replace: Core grants and approvals | Core | none | Outside a grant, a send-type tool asks every time | A: m/ai-extensions, m/settings (Raycast 2.9) (O) |  |
| AI-94 | ai-ext: Auto approval rules | mid (I) | replace: grants and effect ledger | Core+Node | none | Reading .env asks first; git status inside a grant runs with a receipt | A: m/ai-extensions (Raycast 2.9) (O) |  |
| AI-95 | ai-ext: approval card | high (I) | replace: app approvals (the panel shows pending ones) | app+Core | none | A pending approval is visible from the launcher; ↵ allows it once | A: m/ai-extensions (Raycast 2.9) (O) |  |
| AI-96 | ai-ext: always-allowed tools list | low (I) | replace: grants list | Core | none | After a grant is revoked, the next call asks | A: m/ai-extensions (Raycast 2.9) (O) |  |
| AI-97 | ai-ext: custom instructions per extension | low (I) | replace: plugin instructions | Core | none | A plugin instruction changes the default team used in replies | A: m/ai-extensions (Raycast 2.9) (O) |  |
| AI-98 | ai-ext: @calendar | mid (I) | replace: Node calendar tool or Google plugin | Core+Node | Calendars | "내일 오전 2시간 비워 줘" creates the event with a receipt | A: m/ai-extensions (Raycast 2.9) (O) |  |
| AI-99 | ai-ext: @reminders | low (I) | replace: LINA to-do; later: Node Reminders | Core | Reminders (later) | "우유 사기 할 일로" adds an item to LINA to-do | A: m/ai-extensions (Raycast 2.9) (O) |  |
| AI-100 | ai-ext: @finder | mid (I) | replace: Node file tools under a grant | Node | Files and Folders | Moving a file by asking leaves a receipt with source and target | A: m/ai-extensions (Raycast 2.9) (O) |  |
| AI-101 | ai-ext: @file-search | mid (I) | replace: Node Spotlight query | Node | none (I) | "지난주 PDF" lists the same files that mdfind finds | A: m/ai-extensions (Raycast 2.9) (O) |  |
| AI-102 | ai-ext: @clipboard | low (I) | drop: clipboard never reaches Core | — | none | Asking the agent for clipboard history gets a refusal with the reason | A: m/ai-extensions (Raycast 2.9) (O) |  |
| AI-103 | ai-ext: @browser | mid (I) | later: browser tab read | app+Core | Automation (browser) (I) | — | A: m/ai-extensions, browser-ext (Raycast 2.9) (O) |  |
| AI-104 | ai-ext: @terminal | mid (I: agentic tasks since 2.3) | replace: Node shell under a grant | Node | per command (I) | `ls ~/Downloads` through the agent runs with a receipt; outside the grant it asks | A: m/ai-extensions, cl 2.3 (O) |  |
| AI-105 | ai-ext: @weather | low (I) | replace: Core web tools | Core | none | "오늘 서울 날씨" gets an answer with a source | A: m/ai-extensions (Raycast 2.9) (O) |  |
| AI-106 | ai-ext: @location | low (I) | later: Node location grant | Node | Location | — | A: m/ai-extensions (Raycast 2.9) (O) |  |
| AI-107 | ai-ext: @apple-health | low (I) | later: no Health source on the Mac (I) | — | none | — | A: m/ai-extensions (Raycast 2.9) (O) |  |
| AI-108 | ai-ext: @screen-awareness | mid (I) | later: screen read under a grant | app+Node | Accessibility, Screen Recording | — | A: m/screen-awareness (Raycast 2.9) (O) |  |
| AI-109 | ai-ext: @selected-text | mid (I) | replace: launcher send selection | app | Accessibility | Same check as ai-cmd {selection} | A: m/ai-extensions (Raycast 2.9) (O) | app#39 · #1020 |
| AI-110 | ai-ext: @snippets | low (I) | drop: launcher items, not agent tools | — | none | n/a (dropped) | A: m/ai-extensions (Raycast 2.9) (O) |  |
| AI-111 | ai-ext: @raycast-notes | mid (I) | replace: LINA memo | Core | none | "메모: …" creates a memo | A: m/ai-extensions (Raycast 2.9) (O) |  |
| AI-112 | ai-ext: @quicklinks | low (I) | drop: launcher items, not agent tools | — | none | n/a (dropped) | A: m/ai-extensions (Raycast 2.9) (O) |  |
| AI-113 | ai-ext: @raycast-focus | low (I) | later: focus area | Node | none (I) | — | A: m/ai-extensions, cl 2.4 (O) |  |
| AI-114 | ai-ext: @calculator | low (I) | drop: the app calculator answers directly | — | none | n/a (dropped) | A: m/ai-extensions (Raycast 2.9) (O) |  |
| AI-115 | ai-ext: @manual, @raycast-feedback | low (I) | drop: Raycast-specific | — | none | n/a (dropped) | A: m/ai-extensions (Raycast 2.9) (O) |  |
| AI-116 | mcp: add stdio server | low (I) | replace: plugin (local command; card shows the full command) | Core | none | Adding a local command shows its card with the whole command before it runs | A: m/mcp (Raycast 2.9) (O) |  |
| AI-117 | mcp: add HTTP server | low (I) | replace: plugin (remote URL) | Core | none | Adding a URL shows the card and then the tool list | A: m/mcp (Raycast 2.9) (O) |  |
| AI-118 | mcp: OAuth sign-in | low (I) | replace: plugin OAuth through the client relay | app+Core | none | Sign-in opens the system browser and the plugin turns "on" | A: m/mcp, cl 2.0 (O) |  |
| AI-119 | mcp: manage servers | low (I) | replace: plugin states | app+Core | none | A failing plugin shows its cause in words | A: m/mcp, cl 2.9 (O) |  |
| AI-120 | mcp: @mention and "Ask <server>" | low (I) | replace: plugins in agent conversation; drop: per-server command | Core | none | Naming the service in a message uses that plugin | A: m/mcp (Raycast 2.9) (O) |  |
| AI-121 | mcp: popular servers | low (I) | replace: LINA curated catalog | Core | none | A catalog plugin turns on without typing a URL or command | A: m/mcp (Raycast 2.9) (O) |  |
| AI-122 | mcp: server bundled with an extension | low (O: 2 store extensions, 41 installs) | drop: no extension store | — | none | n/a (dropped) | A: m/mcp, dev/manifest (API 2.x) (O) |  |
| AI-123 | mcp: custom instructions per server | low (I) | replace: plugin manifest instructions | Core | none | Plugin instructions show on its card and affect replies | A: m/mcp (Raycast 2.9) (O) |  |
| AI-124 | mcp: MCP on iOS | low (I) | later: LINA on iOS | Core | none | — | A: m/mcp, m/ai-extensions (Raycast 2.9) (O) |  |
| AI-125 | dictation: hotkey dictation anywhere | mid (I: Pro feature, v2 headline) | replace: macOS Dictation | macOS | Microphone, Accessibility | n/a in LINA; system Dictation works in the launcher field | A: m/dictation (Raycast 2.9) (O) |  |
| AI-126 | dictation: styles and auto styling | low (I) | drop: outside LINA scope | — | none | n/a (dropped) | A: m/dictation (Raycast 2.9) (O) |  |
| AI-127 | dictation: vocabulary | low (I) | drop: outside scope | — | none | n/a (dropped) | A: m/dictation (Raycast 2.9) (O) |  |
| AI-128 | dictation: history and last transcription | low (I) | drop: outside scope | — | none | n/a (dropped) | A: m/dictation (Raycast 2.9) (O) |  |
| AI-129 | dictation: Dictate to Note | low (I) | later: memo by voice | app+Core | Microphone | — | A: m/dictation (Raycast 2.9) (O) |  |
| AI-130 | ios: Raycast AI on iPhone and iPad | low (I) | later: LINA app on iOS | app+Core | none | — | A: m/ios, m/mcp (Raycast 2.9) (O) |  |
| AI-131 | ios: share extension AI commands | low (I) | later: iOS share sheet to agent | app+Core | none | — | A: m/ios (iOS share extension page) (O) |  |
| AI-132 | ios: Shortcuts and Action Button | low (I) | later: App Intents for "ask agent" | app+Core | none | — | A: m/ios (apple-shortcuts page) (O) |  |
| AI-133 | ios: widgets and controls | low (I) | later | app+Core | none | — | A: m/ios (widgets page) (O) |  |
| AI-134 | ai: chat sync and attachment upload | high (I: default) | drop: conversation is canon in Core | — | none | n/a (dropped) | A: m/ai-privacy (Raycast 2.9) (O) |  |
| AI-135 | ai: AI on/off switch and enterprise controls | low (I) | drop: single person | — | none | n/a (dropped) | A: m/settings, m/enterprise (Raycast 2.9) (O) |  |

### Extension API, script commands and deeplinks

111 rows: 48 keep, 40 replace, 11 later, 12 drop.

| ID | Feature | Use | Decision | Runs | Permission | Acceptance check | Source | Issue |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| API-01 | deeplink: run command URL | low: I | keep: lina:// scheme | app+Node | none | `open lina://…/empty-trash` runs it after confirm | B: DEV-deeplinks, M1-deeplinks (Raycast 2.9) (O) | app#55 |
| API-02 | deeplink: query parameters | low: I | keep: arguments and fallbackText | app | none | fallbackText=hello opens command with "hello" filled | B: DEV-deeplinks (Raycast 2.9) (O) | app#55 |
| API-03 | deeplink: confirm before run | n/a: safety | keep: confirm, remember per command | app | none | First open asks; after "always", second runs at once | B: DEV-deeplinks, M1-deeplinks (O) | app#55 |
| API-04 | deeplink: background launch | low: I | keep | app | none | `open -g` link does not steal focus from Terminal | B: M1-deeplinks, DEV-deeplinks (O) | app#55 |
| API-05 | deeplink: script and AI command links | low: I | drop: scripts and AI live elsewhere | app | none | n/a (dropped) | B: M1-deeplinks (I) |  |
| API-06 | deeplink: confetti | low: I | drop: novelty | app | none | n/a (dropped) | B: M1-deeplinks (I) |  |
| API-07 | api: view command | high (O: 3,086 of 3,458 extensions; 92.2% of downloads) | replace: mini app in the launcher panel | app+Core | none | A mini app opens in the panel and takes keys without stealing focus from the front app | A: dev/manifest, dev/lifecycle (API 2.x) (O) | app#25 |
| API-08 | api: no-view command | high (O: 1,057 extensions; 57.3% of downloads) | replace: plugin tool via agent, or Node action | Core+Node | per action | "볼륨 0" from the launcher runs as a Node action with a receipt | A: dev/manifest, dev/lifecycle (API 2.x) (O) |  |
| API-09 | api: menu-bar command | mid (O: 337 extensions; 27.5% of downloads; e.g. Spotify Player, Linear) | replace: mini app as a menu bar widget (lina-app #25) | app+Core | none | A mini app pinned to the menu bar shows its title and opens its menu | A: dev/menu-bar (API 2.x) (O) | app#25 |
| API-10 | api: List | high (O: 2,607 extensions; 81.4% of downloads) | replace: mini app list template | app+Core | none | In a mini app list, ↑↓ move, typing filters, ↵ runs the first action, no mouse needed | A: dev/ui-list (API 2.x) (O) | app#25 |
| API-11 | api: Grid | mid (O: 309 extensions; 15.6% of downloads) | replace: mini app grid template | app+Core | none | Arrow keys move in 2D in a mini app grid | A: dev/ui-grid (API 2.x) (O) | app#25 |
| API-12 | api: Detail | high (O: 1,333 extensions; 49.2% of downloads) | replace: mini app detail template or agent reply | app+Core | none | A mini app detail shows markdown and metadata at phone and desktop widths | A: dev/ui-detail (API 2.x) (O) | app#25 |
| API-13 | api: Form | high (O: 1,375 extensions; 50.1% of downloads) | replace: mini app form or agent question | app+Core | none | A mini app form submits with ⌘↵ and the write runs as an agent effect | A: dev/ui-form (API 2.x) (O) | app#25 |
| API-14 | api: Form drafts | low (O: 93 extensions; 10.6% of downloads) | replace: mini app store | Core | none | Closing the panel mid-form and reopening restores the values | A: dev/ui-form, dev/lifecycle (API 2.x) (O) | app#25 |
| API-15 | api: Action Panel (⌘K) | high (I: every List and Grid item) | keep: launcher action panel; mini app template action menu | app | none | ⌘K on a result lists its actions and each shortcut works | A: dev/ui-action-panel (API 2.x) (O) | app#34 |
| API-16 | api: Action.CopyToClipboard and Paste | high (O: 2,208 extensions; 80.0% of downloads) | keep: launcher copy; mini apps only through a person-clicked bridge copy; later: paste | app | none (Accessibility for paste) | Copy from a mini app item puts the text on the pasteboard; the mini app script cannot read it | A: dev/ui-actions, dev/clipboard (API 2.x) (O) | app#73 |
| API-17 | api: Action.Open, OpenInBrowser, OpenWith | high (O: 976 extensions call open(); 55.8% of downloads) | keep: URL to system browser; replace: Node for open-with-app | app+Node | none | A link in a mini app opens in the default browser; opening a file with an app leaves a receipt | A: dev/ui-actions, dev/utilities (API 2.x) (O) | app#47 |
| API-18 | api: Action.Push navigation | high (O: 1,581 extensions; 57.1% of downloads) | replace: mini app internal routes | app+Core | none | Esc goes back one view in a mini app, then closes the panel | A: dev/ui-actions, dev/ui-navigation (API 2.x) (O) | app#25 |
| API-19 | api: Action.ShowInFinder and showInFinder | mid (O: 321 extensions; 21.5% of downloads) | keep: launcher reveal (⌘↩) for its own results; Node for agent results | app+Node | none | ⌘↩ on a file result reveals it in Finder | A: dev/ui-actions, dev/utilities (API 2.x) (O) | app#62 · #1015 |
| API-20 | api: Action.Trash and trash() | low (O: 68 extensions; 2.9% of downloads) | replace: Node under a grant | Node | Files and Folders | Trashing by request leaves a receipt and the file is in the Trash | A: dev/ui-actions, dev/utilities (API 2.x) (O) |  |
| API-21 | api: Action.CreateSnippet and CreateQuicklink | low (I) | keep: launcher creates quick links and snippets | app+Core | none | "링크 저장" from an item opens the quick link form prefilled | A: dev/ui-actions (API 2.x) (O) | app#74 · #1025 |
| API-22 | api: Action.ToggleQuickLook | mid (I) | keep: launcher Quick Look (Space) | Node | none | Space on a file result opens Quick Look | A: dev/ui-actions (API 2.x) (O) | app#62 · #1015 |
| API-23 | api: Action.PickDate | low (I) | replace: mini app date field | app+Core | none | A date picked in a mini app is stored in its store | A: dev/ui-actions (API 2.x) (O) | app#25 |
| API-24 | api: arguments | high (O: 607 extensions; 42.1% of downloads) | keep: launcher argument fields for quick links and mini apps; rest by intent parsing (#967) | app+Core | none | Tab after a quick link name moves into its argument field and ↵ opens the filled URL | A: dev/arguments, dev/manifest (API 2.x) (O) | app#38 · #1025 |
| API-25 | api: preferences | high (O: 2,138 extensions; 86.1% of downloads) | replace: plugin or mini app manifest settings | Core | none | Mini app settings show on its card and change its behavior | A: dev/manifest, dev/preferences (API 2.x) (O) |  |
| API-26 | api: password preference | mid (O: 939 extensions; 20.1% of downloads) | replace: plugin credentials in Core secret store (mini apps get none) | Core | none | A token entered for a plugin is in the Keychain; the mini app page cannot read it | A: dev/manifest, dev/security (API 2.x) (O) |  |
| API-27 | api: help.md for required preferences | low (I) | replace: setup steps on the plugin card | app+Core | none | A plugin that needs a key shows its setup steps on the card | A: dev/changelog, dev/preferences (API 2.x) (O) |  |
| API-28 | api: LocalStorage | high (O: 1,032 extensions; 43.3% of downloads) | replace: mini app store in Core | Core | none | A value saved by a mini app is still there after the app restarts | A: dev/storage (API 2.x) (O) | app#25 |
| API-29 | api: Cache | high (O: 1,087 extensions use Cache or cached hooks; 62.0% of downloads) | replace: mini app store, or later a client cache | Core | none | A second open of a mini app list shows data before the fetch returns | A: dev/cache (API 2.x) (O) | app#25 |
| API-30 | api: Clipboard.copy, paste, clear | high (O: see CopyToClipboard row) | keep: launcher only; drop for mini apps | app | Accessibility (paste) | Mini app scripts have no clipboard API | A: dev/clipboard (API 2.x) (O) | app#73 |
| API-31 | api: Clipboard.read with offset | mid (O: 385 extensions; 18.2% of downloads) | drop: only the person moves an item into a message | — | none | No mini app or plugin can read clipboard or history | A: dev/clipboard (API 2.x) (O) |  |
| API-32 | api: copy with concealed | low (O: 33 extensions incl. 1Password, Bitwarden) | keep: launcher history honors concealed and transient markers | app | none | An item copied with the concealed marker never shows in history | A: dev/clipboard (API 2.x) (O) | app#73 |
| API-33 | api: showToast | high (O: 2,620 extensions; 88.0% of downloads) | replace: panel status line or mini app inline status | app | none | A failing mini app request shows a failure line with the reason | A: dev/feedback-toast (API 2.x) (O) |  |
| API-34 | api: showHUD | high (O: 946 extensions; 52.2% of downloads) | keep: launcher HUD after an action that closes the panel | app | none | After "볼륨 0" the panel closes and a HUD shows the result | A: dev/feedback-hud (API 2.x) (O) | app#34 |
| API-35 | api: confirmAlert | high (O: 859 extensions; 44.0% of downloads) | replace: Core approval for writes; in-page confirm for mini app UI | app+Core | none | A delete request from a mini app outside its grant asks with the mini app's name | A: dev/feedback-alert (API 2.x) (O) |  |
| API-36 | api: getSelectedText | high (O: 250 extensions; 21.1% of downloads; e.g. Google Translate, Notion) | keep: launcher feature (AX selected text, no clipboard) | Core+Node | Accessibility | With nothing selected, the key opens the panel without a chip and says so | A: dev/environment (API 2.x) (O) | app#39 · #1020 |
| API-37 | api: getSelectedFinderItems | mid (O: 166 extensions; 10.0% of downloads) | keep: launcher attaches Finder selection | Core+Node | Automation (Finder) (I) | With Finder not in front, no file chips appear | A: dev/environment (API 2.x) (O) | app#39 · #1020 |
| API-38 | api: getFrontmostApplication | mid (O: 102 extensions; 12.1% of downloads) | keep: launcher records the front app as message context | app | none | A message sent from the launcher shows "from Safari" in the turn | A: dev/utilities (API 2.x) (O) | app#39 · #1020 |
| API-39 | api: getApplications, getDefaultApplication | mid (O: 223 extensions; 22.8% of downloads) | keep: launcher app index | Node | none | Typing an app name lists it; ↵ opens it | A: dev/utilities (API 2.x) (O) | app#58 · #1013 |
| API-40 | api: open() | high (O: 976 extensions; 55.8% of downloads) | keep for URLs; replace: Node for apps and files | app+Node | none | Same check as Action.Open | A: dev/utilities (API 2.x) (O) | app#47 |
| API-41 | api: background refresh (interval) | mid (O: 321 extensions; 28.3% of downloads) | replace: mini app declared schedule (Core background run) | Core | none | A mini app with a 10-minute schedule updates its store with the panel closed | A: dev/bg-refresh, dev/manifest (API 2.x) (O) | app#25 |
| API-42 | api: updateCommandMetadata | low (O: 105 extensions; 8.9% of downloads) | later: mini app subtitle in launcher results | app+Core | none | A mini app's launcher row shows its live count | A: dev/command (API 2.x) (O) |  |
| API-43 | api: OAuth PKCE client | mid (O: 161 extensions; 19.4% of downloads; e.g. Spotify, Linear, Slack) | replace: plugin OAuth (Core, client relay) | app+Core | none | Plugin sign-in completes in the system browser; the token never reaches a mini app | A: dev/oauth, dev/changelog (API 2.x) (O) |  |
| API-44 | api: AI.ask | mid (O: 99 extensions use AI.ask or useAI; 16.8% of downloads) | replace: mini app request to Core (owner agent's model); add to #951 | Core | none | A mini app "요약" button returns text through Core; the ledger names the mini app | A: dev/ai (API 2.x) (O) | app#25 |
| API-45 | api: AI.experimental_decide | low (O: 0 extensions in repo) | replace: Core structured output (#966 guided generation) | Core | none | A schema miss falls back and never guesses | A: dev/ai, dev/changelog (API 2.5) (O) |  |
| API-46 | api: AI tools (tools entry points) | mid (O: 322 extensions; 40.6% of downloads) | replace: plugin tools (MCP) with approvals | Core | none | A plugin tool call shows in the conversation with its inputs | A: dev/tool, dev/ai-ext (API 2.x) (O) |  |
| API-47 | api: Tool.Confirmation | mid (O: 130 extensions; 12.8% of downloads) | replace: Core approval from the service profile | Core | none | A tool that sends asks every time, with the target named | A: dev/tool (API 2.x) (O) |  |
| API-48 | api: ai.instructions | low (O: 122 extensions; 12.5% of downloads) | replace: plugin instructions or skills | Core | none | Same check as the plugin instructions row | A: dev/manifest (API 2.x) (O) |  |
| API-49 | api: AI extension evals | low (O: 179 extensions; 21.7% of downloads) | later: plugin conformance tests | Core | none | — | A: dev/ai-ext (API 2.x) (O) |  |
| API-50 | api: ai.mcp in manifest | low (O: 2 extensions) | replace: plugin manifest | Core | none | — | A: dev/manifest, dev/changelog (API 2.5) (O) |  |
| API-51 | api: ai.skills in manifest | low (O: 15 extensions; 12.9% of downloads) | replace: plugin skills | Core | none | — | A: dev/manifest, dev/changelog (API 2.5) (O) |  |
| API-52 | api: ai.modelProvider | low (O: 14 extensions) | drop: no extension model market | — | none | n/a (dropped) | A: dev/provide-models (API 2.5) (O) |  |
| API-53 | api: BrowserExtension.getContent and getTabs | low (O: 64 extensions; 4.3% of downloads) | later: real native integration (browser automation or companion) | app+Core | Automation (browser) (I) | — | A: dev/browser-ext, browser-ext (API 2.x) (O) |  |
| API-54 | api: WindowManagement | low (O: 2 extensions, ~0% of downloads) | replace: Node window actions (window area) | Node | Accessibility | — | A: dev/window-mgmt (API 2.x) (O) | #1016 |
| API-55 | api: environment | high (I: read by most extensions) | replace: mini app frame passes theme, text size, reduce motion (lina-app #25) | app | none | Switching the Mac to dark mode switches an open mini app | A: dev/environment (API 2.x) (O) | app#25 |
| API-56 | api: environment.canAccess | low (I) | drop: no LINA tiers | — | none | n/a (dropped) | A: dev/environment (API 2.x) (O) |  |
| API-57 | api: launchCommand | mid (O: 410 extensions; 33.1% of downloads) | later: mini app opens another mini app by lina:// link | app | none | A link from one mini app to another asks the person once | A: dev/command (API 2.x) (O) |  |
| API-58 | api: LaunchProps (launchContext, fallbackText, draftValues) | mid (O: 188 extensions use launchContext; 25.6% of downloads) | replace: mini app URL parameters | app+Core | none | Opening a mini app with ?q= prefills its search | A: dev/lifecycle (API 2.x) (O) | app#25 |
| API-59 | api: fallback command | mid (O: 86 extensions read fallbackText; 15.3% of downloads) | keep: agent as default fallback; later: more fallbacks | app | none | Same check as "Quick AI as fallback" | A: dev/lifecycle, m/quick-ai (API 2.x; Raycast 2.9) (O) | app#36 · #1011 |
| API-60 | api: closeMainWindow, popToRoot, clearSearchBar | high (I: common after actions) | keep: launcher closes or resets after an action | app | none | After ↵ on an action, the panel closes and the line is cleared on next open | A: dev/window-search-bar (API 2.x) (O) | app#34 |
| API-61 | api: keyboard shortcuts on actions | high (I) | keep: launcher and mini app template shortcuts | app | none | ⌘1 to ⌘9 and action shortcuts work in the panel | A: dev/keyboard (API 2.x) (O) | app#34 |
| API-62 | api: runAppleScript (utils) | mid (O: 357 extensions; 33.6% of downloads; e.g. Spotify) | replace: Node action under a grant | Node | Automation (<app>) | "음악 멈춰" pauses Music with a receipt; Automation is granted to the Node | A: dev/utils (API 2.x) (O) |  |
| API-63 | api: child_process and useExec | mid (O: 758 extensions) | replace: Node action under a grant | Node | per command (I) | Same check as @terminal | A: dev/utils (API 2.x) (O) |  |
| API-64 | api: executeSQL and useSQL | low (O: 50 extensions) | later: Node read under a grant | Node | Full Disk Access (I) | — | A: dev/utils (API 2.x) (O) |  |
| API-65 | api: Swift and Rust native code | low (O: 18 Swift, 8 Rust extensions) | drop: mini apps have no native code; native needs go to Node | — | none | n/a (dropped) | A: dev/changelog, gh/swift-tools (API 2.x) (O) |  |
| API-66 | api: captureException and memory reports | low (O: 45 extensions use captureException) | replace: Core pre-live checks and app error report | Core | none | A broken mini app revision is refused before going live | A: dev/utilities, dev/changelog (API 2.x) (O) |  |
| API-67 | api: platforms | mid (O: 983 extensions list Windows) | drop: Mac launcher only | — | none | n/a (dropped) | A: dev/manifest (API 2.x) (O) |  |
| API-68 | api: Store submission | high (O: 3,451 store extensions) | replace: worker builds the mini app; Core pre-live checks; curated plugin catalog | Core | none | A mini app revision missing a checklist item is not served | A: dev/publish, m/ext-guidelines (API 2.x; Raycast 2.9) (O) |  |
| API-69 | api: Store technical rules | low (I) | replace: mini app CSP, no network, declared hosts | Core | none | A mini app call to an undeclared host is blocked | A: dev/prepare-store (API 2.x) (O) | app#25 |
| API-70 | api: runtime model | high (I: all extensions) | replace: mini app on its own loopback origin; plugin servers as separate processes | Core | none | Killing one mini app's page leaves others and the launcher working | A: dev/security, blog/v2-tech (API 2.x; Raycast 2.0) (O) | app#25 |
| API-71 | api: no sandbox | high (I: all extensions) | drop: mini apps are sandboxed; native access only via Node grants | — | none | A mini app script cannot read ~/Documents or call any host directly | A: dev/security (API 2.x) (O) |  |
| API-72 | api: automatic updates | high (I: default) | replace: mini app revisions with Keep and Undo | Core | none | Undo returns a mini app to its previous revision | A: dev/versioning, m/extensions (API 2.x; Raycast 2.9) (O) | app#25 |
| API-73 | api: private extensions (Teams) | low (O: 81 repo extensions set owner) | drop: single person | — | none | n/a (dropped) | A: dev/teams (API 2.x) (O) |  |
| API-74 | api: enterprise publish control and allow-list | low (I) | drop: single person | — | none | n/a (dropped) | A: m/enterprise (Raycast 2.9) (O) |  |
| API-75 | script: languages | mid (O: 858 community scripts; repo 6,802 stars) | replace: Node script runner listed in the launcher | app+Node | per script (I) | A bash script in the scripts folder appears in the launcher and runs with a receipt | A: m/script-commands (Raycast 2.9) (O) |  |
| API-76 | script: required metadata | mid (I) | keep: read Raycast-format headers so existing scripts work | app | none | A script with only the three required headers appears; one missing title does not | A: gh/script-commands (Raycast 2.9) (O) | app#75 · #1024 |
| API-77 | script: packageName, icon, iconDark | low (I) | keep: same headers | app | none | A script's emoji icon shows in its launcher row | A: gh/script-commands (Raycast 2.9) (O) | app#75 · #1024 |
| API-78 | script: currentDirectoryPath | low (I) | keep: same default | Node | none | A script writing ./out.txt writes it next to itself | A: gh/script-commands, m/script-commands, cl 2.4 (O) | app#75 · #1024 |
| API-79 | script: needsConfirmation | low (I) | keep: confirm before run | app+Node | none | A script with needsConfirmation true shows a confirm before running | A: gh/script-commands (Raycast 2.9) (O) | app#75 · #1024 |
| API-80 | script: mode silent | mid (I) | keep: HUD | app+Node | none | A silent script closes the panel and shows its last line | A: gh/script-commands (Raycast 2.9) (O) | app#75 · #1024 |
| API-81 | script: mode compact | mid (I) | keep: panel status line | app+Node | none | A compact script shows its last line in the panel | A: gh/script-commands (Raycast 2.9) (O) | app#75 · #1024 |
| API-82 | script: mode fullOutput | mid (I) | keep: output view in the panel | app+Node | none | Colored output keeps red and green in the panel | A: gh/script-commands (Raycast 2.9) (O) | app#75 · #1024 |
| API-83 | script: mode inline | low (I) | later: live launcher row | app+Node | none | An inline script's row text changes after its refresh | A: gh/script-commands (Raycast 2.9) (O) |  |
| API-84 | script: refreshTime and per-script toggle | low (I) | later: with inline mode | Node | none | — | A: gh/script-commands, cl 2.7 (O) |  |
| API-85 | script: arguments | mid (I) | keep: launcher argument fields | app+Node | none | Tab into argument 1, type, ↵; the script receives $1 | A: gh/script-commands (ARGUMENTS.md) (O) | app#75 · #1024 |
| API-86 | script: error handling | mid (I) | keep: failure line in the panel and a failed receipt | app+Node | none | A script that exits 1 shows its last line as the failure reason | A: gh/script-commands (Raycast 2.9) (O) | app#75 · #1024 |
| API-87 | script: script directories | mid (I) | keep: person-chosen script folders under a Node grant | app+Node | Files and Folders | A new script saved into the folder appears without a restart | A: m/script-commands (Raycast 2.9) (O) | app#75 · #1024 |
| API-88 | script: Create Script Command | low (I) | later: or the agent writes the script on request | Core+Node | none | — | A: m/script-commands (Raycast 2.9) (O) |  |
| API-89 | script: hotkey per script | mid (I) | keep: hotkey per launcher item | app+Node | none | A script's hotkey runs it from any app | A: m/script-commands (Raycast 2.9) (O) | app#75 · #1024 |
| API-90 | script: script as fallback | low (I) | later: extra fallbacks | app+Node | none | — | A: m/script-commands, cl 2.3, cl 2.6 (O) |  |
| API-91 | script: regional locale | low (I) | keep: same env | Node | none | `date` output in a script matches the system language | A: m/script-commands, cl 2.4 (O) | app#75 · #1024 |
| API-92 | script: permissions go to the host app | high (I: every script that touches the system) | replace: grants to the Mac Node, shown in its receipt | Node | Accessibility, Automation (<app>), Full Disk Access as needed | The first script needing Automation prompts for the Node, not the launcher | A: m/script-commands (Raycast 2.9) (O) |  |
| API-93 | script: login shell and PATH | low (I) | keep: same rules | Node | none | A script calling a Homebrew tool in /usr/local/bin runs | A: gh/script-commands (Raycast 2.9) (O) | app#75 · #1024 |
| API-94 | script: .template. files skipped | low (I) | keep | app | none | A foo.template.sh file is not listed | A: gh/script-commands (Raycast 2.9) (O) | app#75 · #1024 |
| API-95 | deeplink: run extension command | mid (O: 39 store extensions emit raycast:// links) | keep: lina:// link to open the launcher with text, a conversation or a mini app | app | none | `open "lina://ask?text=hi"` opens the panel with "hi" ready to send | A: dev/deeplinks, m/v1-deeplinks (API 2.x; Raycast v1) (O) | app#55 |
| API-96 | deeplink: confirm before run | mid (I: default) | keep: confirm the first run of each link target | app | none | A new lina:// target asks once; after "always", it opens directly | A: dev/deeplinks, m/v1-deeplinks (API 2.x; Raycast v1) (O) | app#55 |
| API-97 | deeplink: Copy Deeplink action | low (I) | keep: copy link on launcher items and mini apps | app | none | ⌘K on a mini app offers "Copy link" and the link opens it | A: dev/deeplinks (API 2.x) (O) | app#55 |
| API-98 | deeplink: launchType=background | low (I) | later: background action links | app+Node | none | — | A: dev/deeplinks (API 2.x) (O) |  |
| API-99 | deeplink: arguments parameter | low (I) | keep: arguments in lina:// links | app | none | A quick link link with ?arg= opens the filled URL | A: dev/deeplinks (API 2.x) (O) | app#55 |
| API-100 | deeplink: context parameter | low (I) | replace: mini app URL parameters | app | none | — | A: dev/deeplinks (API 2.x) (O) | app#25 |
| API-101 | deeplink: fallbackText parameter | mid (O: AI Chat links in every v2 manual page use it) | keep: lina:// text prefill | app | none | Same check as "run extension command" | A: dev/deeplinks, m/v1-deeplinks (API 2.x; Raycast v1) (O) | app#55 |
| API-102 | deeplink: AI Chat with text | low (I) | keep: lina:// opens an agent conversation with text | app+Core | none | `open "lina://agent/번역가?text=hello"` opens that conversation with the text in the composer | A: m/quick-ai (2.9 page links) (O) | app#55 |
| API-103 | deeplink: confetti | low (O: used in a few store extensions and scripts) | drop: novelty | — | none | n/a (dropped) | A: m/v1-deeplinks (Raycast v1) (I) |  |
| API-104 | deeplink: script command | low (I) | keep: lina:// link per script | app+Node | none | A script link runs that script after the first confirm | A: m/v1-deeplinks, gh/link-commands (Raycast v1; v2 form via community extension) (I) | app#75 · #1024 |
| API-105 | deeplink: script arguments | low (I) | keep | app+Node | none | — | A: m/v1-deeplinks (Raycast v1) (I) | app#75 · #1024 |
| API-106 | deeplink: AI command | low (I) | drop: no AI command library | — | none | n/a (dropped) | A: m/v1-deeplinks (Raycast v1) (I) |  |
| API-107 | deeplink: built-in prefill (quicklink, file search) | low (I) | keep: lina:// prefill for quick link and file search | app | none | A file-search link opens the launcher filtered to that folder | A: m/v1-deeplinks (Raycast v1) (I) | app#55 |
| API-108 | deeplink: window command without focus | low (I) | keep: background links never take focus | app+Node | Accessibility | — | A: m/v1-deeplinks (Raycast v1) (I) | app#55 |
| API-109 | deeplink: AI project and note links | low (I) | keep: links to conversations, memos, mini apps | app | none | A copied memo link opens that memo | A: m/projects, cl 2.0 (O) | app#55 |
| API-110 | deeplink: createDeeplink (utils) | low (O: 33 extensions; 2.0% of downloads) | later: link builder for mini apps | app | none | — | A: dev/createDeeplink (API 2.x) (O) |  |
| API-111 | deeplink: open the launcher | low (I) | keep: lina:// opens the launcher | app | none | `open lina://` shows the panel | A: m/aliases (Raycast 2.9) (O) | app#55 |

### Store extensions

45 rows: 15 keep, 25 replace, 2 later, 3 drop.

| ID | Feature | Use | Decision | Runs | Permission | Acceptance check | Source | Issue |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| EXT-01 | ext: Kill Process | high: 787,441 installs (#1) | keep: native process list + kill | Node | none (I: other users' processes need admin) | Start `sleep 600` in Terminal, type "kill sleep"; it is listed with CPU/mem; ↩ ends it in ≤1 s and a receipt is written | X: store-ext, gh-ext (Raycast 2.9) (O) | app#76 · #1021 |
| EXT-02 | ext: Color Picker | high: 625,764 installs (#2) | keep: pick color + copy hex; conversions via agent | app | none (I: NSColorSampler) | Run "pick color", click a pure red pixel; `#FF0000` is on the clipboard and shown with RGB/HSL | X: store-ext, gh-ext (Raycast 2.9) (O) | app#77 |
| EXT-03 | ext: Google Chrome | high: 578,453 installs (#3) | keep: one browser search (tabs, history, bookmarks) | Node | Automation (Google Chrome) | With 3 Chrome tabs open, type part of one tab title; ↩ focuses that tab; history and bookmark hits also show | X: store-ext (Raycast 2.9) (O) | app#78 · #1022 |
| EXT-04 | ext: Google Translate | high: 505,185 installs (#4) | replace: launcher translate (on-device) | app | none (Accessibility for selected text, I) | Select an English sentence, run translate to Korean; the Korean text shows in the panel with "open in conversation" and a copy action | X: store-api (Raycast 2.9) (O) | app#45 |
| EXT-05 | ext: Spotify Player | high: 497,660 installs (#5) | replace: plugin Spotify (person's own client) | Core | none | With the Spotify plugin signed in, ask "play <song> on Spotify"; it plays on the active device and the panel shows the track | X: store-api (Raycast 2.9) (O) | #1029 |
| EXT-06 | ext: Visual Studio Code | high: 418,372 installs (#6) | keep: editor recent projects | Node | none (I) | Type "code lina"; the recently opened lina folder is first; ↩ opens it in VS Code | X: store-api (Raycast 2.9) (O) | app#81 · #1023 |
| EXT-07 | ext: Linear | high: 407,648 installs (#7) | replace: plugin Linear (official MCP) | Core | none | Ask "new Linear issue: Fix login in team X"; after approval the issue exists and the panel shows its link | X: store-api, linear-mcp (Raycast 2.9) (O) | #1027 |
| EXT-08 | ext: Slack | high: 340,166 installs (#8) | replace: plugin Slack (official MCP) | Core | none | Ask "unread Slack messages?"; the reply lists the unread channels; "send hi to #test" asks for approval, then posts | X: store-api, slack-mcp (Raycast 2.9) (O) | #1028 |
| EXT-09 | ext: Brew | high: 299,969 installs (#9) | replace: agent conversation (Node shell) | Core+Node | none | Ask "which Homebrew packages are outdated?"; the reply matches `brew outdated`; "upgrade all" asks first and writes a receipt | X: store-api (Raycast 2.9) (O) |  |
| EXT-10 | ext: Notion | high: 284,639 installs (#10) | replace: plugin Notion (official MCP) | Core | none | Ask "find my Notion page about X"; the reply links it; "add 'Y' to that page" appends after approval | X: store-api, notion-mcp (Raycast 2.9) (O) | #1027 |
| EXT-11 | ext: ChatGPT | high: 264,253 installs (#11) | replace: agent conversation | Core | none | Type a question in the launcher; a short answer shows in the panel and the turn is in the agent's main conversation | X: store-api (Raycast 2.9) (O) |  |
| EXT-12 | ext: 1Password | high: 256,082 installs (#12) | drop: 1Password Quick Access does it; secrets stay off Core | — | none | With LINA bound to ⌘Space, 1Password Quick Access still opens on its own shortcut, and LINA receives no vault data | X: store-api, gh-ext (Raycast 2.9) (O) |  |
| EXT-13 | ext: Arc | high: 254,910 installs (#13) | keep: in browser search (Arc source) | Node | Automation (Arc) | With Arc open, type part of an Arc tab title; ↩ focuses that tab in its space | X: store-api (Raycast 2.9) (O) | app#78 · #1022 |
| EXT-14 | ext: GitHub (Raycast) | mid: 213,768 installs (#14), Raycast-owned | replace: plugin GitHub (first-party) | Core | none | Type "my open PRs"; the panel lists the same PRs as github.com/pulls, with links | X: store-api (Raycast 2.9) (O) |  |
| EXT-15 | ext: Speedtest | mid: 189,203 installs (#15) | replace: agent conversation (Node `networkQuality`) | Core+Node | none | Ask "how fast is my internet?"; within 30 s the reply shows download/upload Mbps and responsiveness, with a receipt | X: store-api, gh-ext (Raycast 2.9) (O) |  |
| EXT-16 | ext: Obsidian | mid: 179,038 installs (#16) | replace: agent conversation (vault folder grant) | Core+Node | Files and Folders (I) | With the vault folder granted, ask "append 'buy milk' to today's daily note"; the line is in today's note file | X: store-api (Raycast 2.9) (O) |  |
| EXT-17 | ext: Apple Notes (Raycast) | mid: 157,839 installs (#17), Raycast-owned | replace: LINA memo; Apple Notes via Node later | Core | none for memo; Full Disk Access + Automation (Notes) for Apple Notes | Type "note call mom"; a memo is saved and typing "call mom" finds it | X: store-api, gh-ext (Raycast 2.9) (O) |  |
| EXT-18 | ext: Coffee | mid: 156,074 installs (#18) | keep: keep-awake with menu bar state | Node | none (I) | Run "keep awake 1h"; `pmset -g assertions` shows LINA's sleep assertion; it ends after 1 h or on "allow sleep" | X: store-api, gh-ext (Raycast 2.9) (O) | app#79 · #1021 |
| EXT-19 | ext: Google Search | mid: 153,929 installs (#19) | keep: quick link + web suggestions | app | none | Type "g raycast"; suggestions appear while typing; ↩ opens Google results in the default browser; Core is not called | X: store-api (Raycast 2.9) (O) | app#47 |
| EXT-20 | ext: Video Downloader | mid: 138,192 installs (#20) | replace: agent conversation (Node runs yt-dlp) | Core+Node | Files and Folders (Downloads) (I) | With yt-dlp installed, ask "download <URL>"; the file is in ~/Downloads with a receipt; without yt-dlp the reply says so | X: store-api, gh-ext (Raycast 2.9) (O) |  |
| EXT-21 | ext: Timers | mid: 125,742 installs (#21) | replace: mini app (timer + menu bar widget) | app+Core | Notifications | Type "timer 5m"; a countdown shows in the menu bar; at 0 a sound and a notification fire | X: store-api (Raycast 2.9) (O) | app#25 |
| EXT-22 | ext: Apple Reminders (Raycast) | mid: 124,372 installs (#22), Raycast-owned | replace: LINA to-do; Reminders via EventKit later | Core | Reminders (for Apple Reminders) | Type "remind me to call mom at 5pm"; a to-do with a 5 pm alert is created and the alert fires | X: store-api, gh-ext (Raycast 2.9) (O) |  |
| EXT-23 | ext: System Monitor | mid: 123,452 installs (#23) | replace: mini app (menu bar widget fed by Node) | app+Core+Node | none (I) | Open the system monitor widget; CPU and memory match Activity Monitor within 5% and refresh every few seconds | X: store-api (Raycast 2.9) (O) | app#25 |
| EXT-24 | ext: CleanShot X | mid: 122,607 installs (#24) | keep: app action catalog (URL scheme) | Node | none (I) | With CleanShot X installed, type "capture area"; CleanShot's area capture starts | X: store-api (Raycast 2.9) (O) | app#47 |
| EXT-25 | ext: Zoom (Raycast) | mid: 117,953 installs (#25), Raycast-owned | replace: plugin Zoom (official MCP) | Core | none | With Zoom signed in, ask "schedule a Zoom tomorrow 10am"; after approval the meeting exists and its join link shows | X: store-api, zoom-mcp (Raycast 2.9) (O) | #1027 |
| EXT-26 | ext: Warp | mid: 117,904 installs (#26) | keep: app action catalog (URL scheme) | Node | none (I) | Type "warp lina"; Warp opens a new tab in the lina folder | X: store-api (Raycast 2.9) (O) | app#47 |
| EXT-27 | ext: Pomodoro | mid: 114,515 installs (#27) | replace: mini app (pomodoro + menu bar widget) | app+Core | Notifications | Start "pomodoro"; the menu bar shows 25:00 counting down; a notification fires at the break | X: store-api (Raycast 2.9) (O) | app#25 |
| EXT-28 | ext: Lorem Ipsum | mid: 113,402 installs (#28) | replace: agent conversation | Core | none | Ask "2 paragraphs of lorem ipsum"; the reply has 2 paragraphs and a copy action | X: store-api (Raycast 2.9) (O) |  |
| EXT-29 | ext: YouTube | mid: 112,750 installs (#29) | keep: quick link (YouTube search) | app | none | Type "yt lofi"; ↩ opens YouTube results for "lofi" in the browser; Core is not called | X: store-api (Raycast 2.9) (O) | app#47 |
| EXT-30 | ext: GIF Search | mid: 109,966 installs (#30) | replace: mini app (GIF grid) | app+Core | none | In the GIF mini app, search "thumbs up"; a grid shows; ↩ copies the GIF and it pastes into Messages | X: store-api (Raycast 2.9) (O) | app#25 |
| EXT-31 | ext: Format JSON | mid: 103,015 installs (#31) | replace: mini app (JSON formatter) | app+Core | none | Paste minified JSON into the formatter; it shows 2-space formatted output; invalid JSON shows the error position | X: store-api (Raycast 2.9) (O) | app#25 |
| EXT-32 | ext: Music | mid: 102,878 installs (#32) | keep: now-playing controls | Node | Automation (Music) | With Music playing, type "next"; the next track starts; "pause" pauses within 1 s | X: store-api, gh-ext (Raycast 2.9) (O) | app#82 · #1023 |
| EXT-33 | ext: Todoist | low: 98,477 installs (#33) | replace: plugin Todoist (official MCP, I) | Core | none | With Todoist on, ask "add task buy milk tomorrow"; the task is in Todoist, due tomorrow | X: store-api, todoist-mcp (Raycast 2.9) (O) | #1027 |
| EXT-34 | ext: Remove Paywall | low: 88,577 installs (#34) | drop: paywall bypass (terms risk) | — | none | No launcher command sends URLs to paywall-bypass services | X: store-api (Raycast 2.9) (O) |  |
| EXT-35 | ext: Google Gemini | low: 87,185 installs (#35) | replace: agent conversation | Core | none | With a Gemini model set for the agent, a launcher question is answered in that agent's main conversation | X: store-api (Raycast 2.9) (O) |  |
| EXT-36 | ext: Downloads Manager | low: 86,793 installs (#36) | keep: file search, Downloads newest first | Node | Files and Folders (Downloads) | Download a file, type "latest download"; it is first; ↩ opens it; ⌘↩ reveals it in Finder | X: store-api (Raycast 2.9) (O) | app#60 · #1014 |
| EXT-37 | ext: Tailwind CSS | low: 81,847 installs (#37) | replace: agent conversation | Core | none | Ask "Tailwind class for 1rem padding"; the reply says `p-4` and links the docs | X: store-api (Raycast 2.9) (O) |  |
| EXT-38 | ext: Clean Keyboard | low: 81,484 installs (#38); rare use (I) | later: rare use; needs event tap | Node | Accessibility (I) | Later: during lock, typed keys do not reach the front app; a mouse click unlocks | X: store-api, gh-ext (Raycast 2.9) (O) |  |
| EXT-39 | ext: Set Audio Device | low: 78,090 installs (#39) | keep: native audio device switch | Node | none (I) | With AirPods connected, type "output AirPods"; sound switches within 1 s; Sound settings shows AirPods | X: store-api (Raycast 2.9) (O) | app#80 · #1021 |
| EXT-40 | ext: Shell | low: 77,779 installs (#40) | keep: run command on Node with receipt | Node | none (I: commands may trigger prompts) | Type "> uptime"; the output shows in the panel with a receipt; it is refused with a reason when Core is down | X: store-api (Raycast 2.9) (O) | app#75 · #1024 |
| EXT-41 | ext: Jira (Raycast) | low: 69,170 installs (#45), Raycast-owned | replace: plugin Jira (Atlassian MCP, I) | Core | none | With Jira on, ask "my open Jira issues"; the list matches Jira's "Assigned to me" | X: store-api (Raycast 2.9) (O) | #1027 |
| EXT-42 | ext: Browser Bookmarks (Raycast) | low: 68,798 installs (#46), Raycast-owned | keep: in browser search | Node | Full Disk Access for Safari (I) | Type a bookmark name saved in Safari and Chrome; both show; ↩ opens it | X: store-api (Raycast 2.9) (O) | app#78 · #1022 |
| EXT-43 | ext: Google Workspace (Raycast) | low: 62,104 installs (#52), Raycast-owned | replace: plugin Google (first-party) | Core | none | Ask "find my Drive doc Budget"; the reply links it; "new Google Doc" creates one | X: store-api (Raycast 2.9) (O) |  |
| EXT-44 | ext: Raycast Explorer (Raycast) | low: 48,602 installs (#67), Raycast-owned | drop: Raycast-specific gallery | — | none | n/a | X: store-api (Raycast 2.9) (O) |  |
| EXT-45 | ext: MCP Registry (Raycast) | low: 43,928 installs (#77), Raycast-owned | later: registry search reserved, not in first release | Core | none | First release: adding a remote MCP URL in the plugin add flow shows the "Added by you" card | X: store-api, lina-integrations (Raycast 2.9) (O) |  |

### Sync, settings and platforms

23 rows: 4 keep, 8 replace, 2 later, 9 drop.

| ID | Feature | Use | Decision | Runs | Permission | Acceptance check | Source | Issue |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| META-01 | sync: cloud sync | mid: I: multi-Mac users | replace: Core is the one canon | Core | none | Add quick link on Mac A; Mac B shows it after reconnect | B: M-sync (Raycast 2.9) (O) |  |
| META-02 | sync: per-category toggles | low: I | drop: Core holds canon | Core | none | n/a (dropped) | B: M-sync (Raycast 2.9) (O) |  |
| META-03 | sync: never synced | n/a: rule | keep: clipboard stays on its Mac | app | none | Copy on Mac A; item never appears on Mac B or in Core | B: M-sync (Raycast 2.9) (O) | app#72 |
| META-04 | sync: devices list | low: I | replace: Core device and Node list | Core | none | Remove a Mac; its Node stops receiving grants | B: M-sync (Raycast 2.9) (O) |  |
| META-05 | sync: delete cloud data | low: I | replace: Core data controls | Core | none | n/a (Core design) | B: M-sync (Raycast 2.9) (O) |  |
| META-06 | export: settings and data | low: I | replace: Core backup | Core | none | n/a (Core design) | B: M-impexp (Raycast 2.9) (O) |  |
| META-07 | export: scheduled backups | low: I | replace: Core backup schedule | Core | none | n/a (Core design) | B: M-impexp (Raycast 2.9) (O) |  |
| META-08 | import: settings and data | low: I | later: Raycast import (format encrypted, I) | Core | none | Import a .rayconfig; chosen links and hotkeys appear | B: M-impexp (Raycast 2.9) (O) |  |
| META-09 | import: migrate from v1 | n/a | drop: Raycast-internal | app | none | n/a (dropped) | B: M-impexp (Raycast 2.9) (O) |  |
| META-10 | teams: shared items | low: Teams plan | drop: single-person product | Core | none | n/a (dropped) | B: M-teams (Raycast 2.9) (O) |  |
| META-11 | browser: browser extension | low: I | later: plugin giving agent the tab | Core | none (browser extension) (I) | Ask agent "summarize this tab"; it reads the active tab | B: BE, M-placeholders (Raycast 2.9) (O) |  |
| META-12 | platform: Raycast for iOS | n/a for Mac launcher | drop: LINA iOS app is separate | — | n/a | n/a (dropped) | B: M-ios, CL-ios (O) |  |
| META-13 | platform: Raycast for Windows | n/a for Mac launcher | drop: macOS first | — | n/a | n/a (dropped) | B: CL-win, M-settings (O) |  |
| META-14 | meta: settings | mid: I | keep | app | none | ⌘, in panel opens launcher settings | B: M-settings (Raycast 2.9) (O) | app#31 |
| META-15 | meta: check for updates | low: I | keep: app updater | app | none | Run it; current version reported | B: CL-2.x (O) |  |
| META-16 | meta: send feedback | low: I | replace: agent conversation | Core | none | "launcher bug: …" reaches the agent conversation | B: M-trouble (Raycast 2.9) (O) |  |
| META-17 | meta: copy or reveal logs | low: I | replace: self-diagnosis log | Core | none | Launcher errors appear in the activity log | B: M-trouble (Raycast 2.9) (O) |  |
| META-18 | meta: manual commands | low: I | replace: agent conversation | Core | none | Ask agent "how do I set a hotkey"; it answers | B: CL-beta (0.65) (O) |  |
| META-19 | meta: what's new | low: I | drop: release notes elsewhere | app | none | n/a (dropped) | B: CL-beta (0.53) (O) |  |
| META-20 | meta: confetti | low: I | drop: novelty | app | none | n/a (dropped) | B: CL-beta (0.52) (O) |  |
| META-21 | meta: typing practice | low: I | drop: novelty | app | none | n/a (dropped) | B: CL-beta (0.54, 0.63) (O) |  |
| META-22 | meta: Raycast Wrapped | low: seasonal | drop (later: usage digest) | app | none | n/a (dropped) | B: CL-v1 (1.104) (O) |  |
| META-23 | meta: quit | low: I | keep | app | none | Quit; hotkey no longer opens the panel | B: M-settings (Raycast 2.9) (O) | app#31 |

## Sources

Each research area lists its sources with the version they describe and the date of the page or release. All were read on 2026-10-23.

### B: Raycast core and built-in commands

Version key: Raycast for Mac 2.9 is current (released 2026-10-22). Manual pages have no version label; they describe the current app (2.x), so rows cite them as "(Raycast 2.9)". All read 2026-10-23.

| Key | Title | URL | Describes | Page or release date | Read |
| --- | --- | --- | --- | --- | --- |
| M-full | Raycast Manual, full text (llms-full.txt) | https://manual.raycast.com/llms-full.txt | Raycast 2.x (Mac, Windows, iOS) | per page below | 2026-10-23 |
| M-quickstart | Quickstart | https://manual.raycast.com/quickstart | Raycast 2.x | 2026-09-10 | 2026-10-23 |
| M-search | Search Bar | https://manual.raycast.com/search-bar | Raycast 2.x | 2026-09-30 | 2026-10-23 |
| M-action | Action Panel | https://manual.raycast.com/action-panel | Raycast 2.x | 2026-08-20 | 2026-10-23 |
| M-hotkeys | Command Aliases & Hotkeys | https://manual.raycast.com/command-aliases-and-hotkeys | Raycast 2.x | 2026-10-02 | 2026-10-23 |
| M-keys | Keyboard Shortcuts | https://manual.raycast.com/keyboard-shortcuts | Raycast 2.x | 2026-10-09 | 2026-10-23 |
| M-settings | Settings | https://manual.raycast.com/settings | Raycast 2.x | 2026-10-02 | 2026-10-23 |
| M-v2 | New in Raycast v2 | https://manual.raycast.com/new-in-v2 | v2 vs v1 | 2026-09-10 | 2026-10-23 |
| M-impexp | Import & Export | https://manual.raycast.com/import-export | Raycast 2.x | 2026-10-14 | 2026-10-23 |
| M-quicklinks | Quicklinks | https://manual.raycast.com/quicklinks | Raycast 2.x | 2026-08-20 | 2026-10-23 |
| M-placeholders | Dynamic Placeholders | https://manual.raycast.com/dynamic-placeholders | Raycast 2.x | 2026-08-20 | 2026-10-23 |
| M-notes | Notes | https://manual.raycast.com/notes | Raycast 2.x | 2026-10-02 | 2026-10-23 |
| M-focus | Focus | https://manual.raycast.com/focus | Raycast 2.x | 2026-10-15 | 2026-10-23 |
| M-apps | Applications | https://manual.raycast.com/applications | Raycast 2.x | 2026-10-02 | 2026-10-23 |
| M-autoquit | Auto Quit | https://manual.raycast.com/auto-quit | Raycast 2.x | 2026-10-02 | 2026-10-23 |
| M-translate | Translate | https://manual.raycast.com/translate | Raycast 2.x (Pro) | 2026-08-20 | 2026-10-23 |
| M-emoji | Emoji & Symbols | https://manual.raycast.com/emoji-symbols | Raycast 2.x | 2026-08-27 | 2026-10-23 |
| M-calendar | Calendar | https://manual.raycast.com/calendar | Raycast 2.x (Mac only) | 2026-10-02 | 2026-10-23 |
| M-screenshots | Screenshots | https://manual.raycast.com/screenshots | Raycast 2.x | 2026-09-10 | 2026-10-23 |
| M-nav | Navigation (Search Menu Bar Items, Switch Windows) | https://manual.raycast.com/navigation | Raycast 2.x | 2026-09-01 | 2026-10-23 |
| M-hyper | Hyper Key | https://manual.raycast.com/hyper-key | Raycast 2.x | 2026-09-30 | 2026-10-23 |
| M-sync | Cloud Sync | https://manual.raycast.com/cloud-sync | Raycast 2.x (Pro) | 2026-10-02 | 2026-10-23 |
| M-system | System Commands | https://manual.raycast.com/system-commands | Raycast 2.x | 2026-10-07 | 2026-10-23 |
| M-themes | Themes | https://manual.raycast.com/themes | Raycast 2.x (Pro) | 2026-07-30 | 2026-10-23 |
| M-teams | Teams, Shared Features | https://manual.raycast.com/teams/shared-features | Raycast 2.x | 2026-07-28 | 2026-10-23 |
| M-ios | iOS | https://manual.raycast.com/ios | Raycast iOS | 2026-07-06 | 2026-10-23 |
| M-trouble | Troubleshooting | https://manual.raycast.com/troubleshooting | Raycast 2.x | 2026-10-02 | 2026-10-23 |
| M1-deeplinks | v1 manual: Deeplinks | https://manual.raycast.com/v1/deeplinks | Raycast 1.x | unknown | 2026-10-23 |
| M1-fallback | v1 manual: Fallback Commands | https://manual.raycast.com/v1/fallback-commands | Raycast 1.x | unknown | 2026-10-23 |
| M1-system | v1 manual: System | https://manual.raycast.com/v1/system | Raycast 1.x | unknown | 2026-10-23 |
| M1-reminders | v1 manual: Reminders (deprecated) | https://manual.raycast.com/v1/reminders | Raycast 1.x | unknown | 2026-10-23 |
| DEV-deeplinks | Developers: Deeplinks | https://developers.raycast.com/information/lifecycle/deeplinks | Raycast API (current) | unknown | 2026-10-23 |
| CL-2.x | macOS changelog 2.0 to 2.9 | https://www.raycast.com/changelog (and /changelog/macos/2-0 ... /2-9) | Raycast 2.0 to 2.9 | 2026-08-25 to 2026-10-22 | 2026-10-23 |
| CL-beta | macOS changelog v2 beta 0.51 to 0.71 | https://www.raycast.com/changelog/macos/2, /3, /4 | Raycast v2 beta | 2026-03-27 to 2026-08-19 | 2026-10-23 |
| CL-v1 | macOS V1 changelog | https://www.raycast.com/changelog/macos-v1 | Raycast 1.95 to 1.104 | 2025-04-09 to 2025-12-16 | 2026-10-23 |
| CL-1.63 | Raycast v1.63.0, Camera Preview | https://www.raycast.com/changelog/1-63-0 | Raycast 1.63 | 2023-11-29 | 2026-10-23 |
| CL-ios | iOS changelog | https://www.raycast.com/changelog/ios | Raycast iOS 1.4.0 | 2026-10-12 | 2026-10-23 |
| CL-win | Windows changelog | https://www.raycast.com/changelog/windows | Raycast Windows 2.9 | 2026-10-22 | 2026-10-23 |
| CF-system | Core features: System | https://www.raycast.com/core-features/system | Raycast (undated marketing page) | unknown | 2026-10-23 |
| BE | Browser Extension page | https://www.raycast.com/browser-extension | current | unknown | 2026-10-23 |
| STORE | Raycast Store API, `download_count` field | https://backend.raycast.com/api/v1/store_listings/search?q=... | Store, live counts | 2026-10-23 | 2026-10-23 |
| WR | Adam Levoy, "Raycast Wrapped 2025" (one user's stats, secondary) | https://adamlevoy.com/blog/raycast-wrapped-2025 | Raycast 1.104 Wrapped 2025 | 2026-01-02 | 2026-10-23 |
| PED | "You're probably only using 3% of Raycast" (essay, author appears to work at Raycast; secondary) | https://ped.ro/writing/youre-probably-only-using-3-percent-of-raycast | Raycast v1/v2 beta | 2026-05-29 | 2026-10-23 |
| APPLE-keys | Apple: Mac keyboard shortcuts | https://support.apple.com/en-us/102650 | macOS 26 Tahoe, macOS 27 Golden Gate | 2026-09-14 | 2026-10-23 |
| LINA-963 | thisisjun786/lina issue #963, owner comments 10-23 | gh issue view 963 | LINA | 2026-10-23 | 2026-10-23 |

Use-evidence shorthand: Q = listed as an everyday essential or first step in M-quickstart; M = the manual names it as common or recommended; WR = one user's Wrapped 2025 count per year; PED = Raycast staff essay; STORE n = Store installs of a related extension; I = inferred.

### W: Window management

- `rc-manual`: Raycast Manual, Window Management. https://manual.raycast.com/window-management. Raycast v2 (current 2.9). Undated. Read 2026-10-23. Platform badges in the page source mark Mac-only and Windows-only commands.
- `rc-manual-v1`: Raycast Manual (v1), Window Management. https://manual.raycast.com/v1/window-management. Raycast v1. Undated (Wayback copy 2025-12-30 has the same text). Read 2026-10-23.
- `rc-cl2`: Raycast macOS changelog, v2. https://www.raycast.com/changelog and /changelog/macos/2 to /4. Raycast 2.0 betas 0.51 to 0.71 (2026-04 to 2026-06), 2.0 to 2.9 (2.9 on 2026-10-22). Read 2026-10-23.
- `rc-cl1`: Raycast macOS changelog, v1. https://www.raycast.com/changelog/macos-v1 (pages 1 to 16). Raycast 1.0.0 (2020-10-29) to 1.104.0 (2025-12-16). Read 2026-10-23.
- `rc-api`: Raycast API, Window Management. https://developers.raycast.com/api-reference/window-management (also the .md version). Raycast API 1.78.0 and later. Undated. Read 2026-10-23.
- `rc-apicl`: Raycast API changelog. https://developers.raycast.com/misc/changelog. API 1.78.0 (2024-07-03) introduced `WindowManagement`; 1.91.0 fixed types. Read 2026-10-23.
- `rc-pricing`: Raycast pricing. https://www.raycast.com/pricing. Current. Read 2026-10-23.
- `rc-store`: Raycast Store search API. https://www.raycast.com/api/v1/store_listings/search?q=window. Install counts as of 2026-10-23. Read 2026-10-23.
- `oma`: typecraft-dev/oma-cast, `reference/04-frontend-features.md`. https://github.com/typecraft-dev/oma-cast/blob/main/reference/04-frontend-features.md. Third-party static analysis of the Raycast 2.8.2 app bundle, so it is secondary. Committed 2026-10-19. Read 2026-10-23.
- `ap-keys`: Apple, Mac window tiling icons and keyboard shortcuts. https://support.apple.com/guide/mac-help/mac-window-tiling-icons-keyboard-shortcuts-mchl9674d0b0/27/mac/27. macOS 27 "Golden Gate". The /26/ and /15/ pages have the same tables, and 15 adds "available in macOS Sequoia 15 or later". Undated (©2026). Read 2026-10-23.
- `ap-tile`: Apple, Tile windows on Mac. https://support.apple.com/guide/mac-help/tile-app-windows-mchlef287e5d/27/mac/27. macOS 27. Undated. Read 2026-10-23.
- `ap-tileset`: Apple, Change window tiling settings on Mac. https://support.apple.com/guide/mac-help/change-window-tiling-settings-on-mac-mchl118087b0/27/mac/27, compared with /15/. macOS 27 and 15. Undated. Read 2026-10-23.
- `ap-win`: Apple, Move and arrange app windows on Mac. https://support.apple.com/guide/mac-help/work-with-app-windows-mchlp2469/27/mac/27. macOS 27. Undated. Read 2026-10-23.
- `ap-spaces`: Apple, Work in multiple spaces on Mac. https://support.apple.com/guide/mac-help/work-in-multiple-spaces-mh14112/27/mac/27. macOS 27. Undated. Read 2026-10-23.
- `ap-kbd`: Apple, Mac keyboard shortcuts. https://support.apple.com/en-us/102650. Current macOS (mentions Tahoe 26). Undated. Read 2026-10-23.
- `ap-appkeys`: Apple, Create keyboard shortcuts for apps on Mac. https://support.apple.com/guide/mac-help/create-keyboard-shortcuts-for-apps-mchlp2271/27/mac/27. macOS 27. Undated. Read 2026-10-23.
- `ap-ax`: Apple, Allow accessibility apps to access your Mac. https://support.apple.com/guide/mac-help/allow-accessibility-apps-to-access-your-mac-mh43185/27/mac/27. macOS 27, but it still names the pane "Accessibility". Undated. Read 2026-10-23.
- `ad-ax`: Apple Developer, AXUIElement.h, kAXPositionAttribute, AXUIElementSetAttributeValue and AXIsProcessTrustedWithOptions. https://developer.apple.com/documentation/applicationservices/axuielement_h. Available since macOS 10.2 (trust function since 10.9). Read 2026-10-23.
- `ad-screen`: Apple Developer, NSScreen.visibleFrame. https://developer.apple.com/documentation/appkit/nsscreen/visibleframe. Read 2026-10-23.
- `ms-27`: Microsoft Learn, Microsoft Purview Endpoint DLP on macOS 27. https://learn.microsoft.com/en-us/purview/endpoint-dlp-macos-27-changes. macOS 27. ms.date 2026-08-26, updated 2026-10-15. Read 2026-10-23.
- `rect`: Rectangle README and `Rectangle/WindowAction.swift`. https://github.com/rxhanson/Rectangle. main branch (pushed 2026-10-22). Read 2026-10-23.
- `yabai`: yabai wiki, Disabling System Integrity Protection. https://github.com/asmvik/yabai/wiki/Disabling-System-Integrity-Protection. Current. Read 2026-10-23.
- `aero`: AeroSpace README. https://github.com/nikitabobko/AeroSpace. Current. Read 2026-10-23.
- `amethyst`: Amethyst issues #1643 (macOS 14.5, 2024-05-14) and #1698 (2024-11-28). https://github.com/ianyh/Amethyst/issues/1643 and https://github.com/ianyh/Amethyst/issues/1698. Read 2026-10-23.
- `brew`: Homebrew cask install analytics, 365 days (2025-10-23 to 2026-10-23). https://formulae.brew.sh/api/analytics/cask-install/365d.json. Read 2026-10-23.
- `gh-stars`: GitHub API repository stars, read 2026-10-23.
- `gh-dot`: GitHub code search for `"raycast/window-management/"`, text matches from 151 repos and 143 owners, counted as distinct owners per command slug. Run 2026-10-23. Secondary and biased toward Karabiner, Hammerspoon and leader-key users.
- `9to5-27`: 9to5Mac, macOS 27 Golden Gate full release notes. https://9to5mac.com/2026/09/09/macos-27-golden-gate-here-are-apples-full-release-notes/. Dated 2026-09-09. Read 2026-10-23.
- `levoy`: Adam Levoy, Raycast Wrapped 2025. https://adamlevoy.com/blog/raycast-wrapped-2025. Raycast 1.104 era (2025). Read 2026-10-23.

### C: Clipboard history and snippets

| Key | Title | URL | Version described | Page/release date | Read |
| --- | --- | --- | --- | --- | --- |
| manual/clipboard-history | Raycast Manual: Clipboard History | https://manual.raycast.com/clipboard-history | Raycast v2 (current manual, app 2.9) | undated | 2026-10-23 |
| manual/snippets | Raycast Manual: Snippets | https://manual.raycast.com/snippets | Raycast v2 (2.9) | undated | 2026-10-23 |
| manual/dynamic-placeholders | Raycast Manual: Dynamic Placeholders | https://manual.raycast.com/dynamic-placeholders | Raycast v2 (2.9) | undated | 2026-10-23 |
| manual/cloud-sync | Raycast Manual: Cloud Sync | https://manual.raycast.com/cloud-sync | Raycast v2 (2.9) | undated | 2026-10-23 |
| manual/new-in-v2 | Raycast Manual: New in Raycast v2 | https://manual.raycast.com/new-in-v2 | Raycast 2.0 | undated (2.0 shipped 2026-08-25) | 2026-10-23 |
| manual/keyboard-shortcuts | Raycast Manual: Keyboard Shortcuts | https://manual.raycast.com/keyboard-shortcuts | Raycast v2 (2.9) | undated | 2026-10-23 |
| manual/enterprise-clipboard | Raycast Manual: Clipboard History Control | https://manual.raycast.com/enterprise/clipboard-history-control | Raycast v2 Enterprise | undated | 2026-10-23 |
| manual/v1-core | Raycast Manual (v1): Core | https://manual.raycast.com/v1/core | Raycast for Mac v1 only | undated | 2026-10-23 |
| raycast/changelog | Raycast changelog | https://www.raycast.com/changelog | 2.0 (2026-08-25) to 2.9 (2026-10-22) | per entry | 2026-10-23 |
| raycast/core-clipboard | Raycast: Clipboard History feature page | https://www.raycast.com/core-features/clipboard-history | unversioned marketing page (still says "until 3 months") | undated | 2026-10-23 |
| raycast/blog-pro | Introducing Raycast Pro | https://www.raycast.com/blog/introducing-raycast-pro | Raycast 1.x | 2023-05-10 | 2026-10-23 |
| dev/clipboard-api | Raycast API: Clipboard | https://developers.raycast.com/api-reference/clipboard | Raycast API (unversioned) | undated | 2026-10-23 |
| raycast/store-api | Raycast Store search API (install counts) | https://www.raycast.com/api/v1/store_listings/search?q=clipboard (and q=snippet) | live store | 2026-10-23 | 2026-10-23 |
| ray.so/snippets | Snippet Explorer | https://ray.so/snippets | live | undated | 2026-10-23 |
| simo.sh | Migrating Raycast clipboard history (blog) | https://simo.sh/notes/migrating-raycast-clipboard-history | Raycast 1.x (version not stated) | 2024-10-01 | 2026-10-23 |
| apple/spotlight-clipboard | Search your Clipboard history in Spotlight on Mac | https://support.apple.com/guide/mac-help/mchl40d5b86b/mac | macOS 27 edition (I: from version picker and links) | undated | 2026-10-23 |
| apple/spotlight-settings | Spotlight settings on Mac | https://support.apple.com/guide/mac-help/mchl54d95e8a/mac | macOS 27 edition (I) | undated | 2026-10-23 |
| macrumors/26.1 | More Spotlight clipboard settings in macOS 26.1 | https://www.macrumors.com/2025/11/04/more-spotlight-clipboard-settings-macos-26-1 | macOS 26.0 and 26.1 | 2025-11-04 | 2026-10-23 |
| macmost/tahoe | How to use the Spotlight Clipboard History in macOS Tahoe | https://macmost.com/how-to-use-the-spotlight-clipboard-history-in-macos-tahoe.html | macOS 26.0 | 2025-09-22 | 2026-10-23 |
| 9to5/27-notes | macOS 27 Golden Gate full release notes | https://9to5mac.com/2026/09/09/macos-27-golden-gate-here-are-apples-full-release-notes/ | macOS 27.0 | 2026-09-09 | 2026-10-23 |
| apple/appkit-updates | AppKit updates (April 2025: macOS pasteboard privacy) | https://developer.apple.com/documentation/updates/appkit | macOS 15.4 | April 2025 | 2026-10-23 |
| apple/accessbehavior | NSPasteboard.AccessBehavior | https://developer.apple.com/documentation/appkit/nspasteboard/accessbehavior-swift.enum | macOS 15.4+ | undated | 2026-10-23 |
| apple/nspasteboard | NSPasteboard (changeCount, detect methods) | https://developer.apple.com/documentation/appkit/nspasteboard | current SDK | undated | 2026-10-23 |
| apple/27.2-notes | macOS 27.2 Golden Gate RC Release Notes | https://developer.apple.com/documentation/macos-release-notes/macos-27_2-release-notes | macOS 27.2 RC | undated | 2026-10-23 |
| apple/global-monitor | NSEvent addGlobalMonitorForEvents(matching:handler:) | https://developer.apple.com/documentation/appkit/nsevent/addglobalmonitorforevents(matching:handler:) | macOS 10.6+ | undated | 2026-10-23 |
| apple/cgevent | CGEvent tapCreate / post; CGRequestPostEventAccess / CGRequestListenEventAccess | https://developer.apple.com/documentation/coregraphics/cgevent | macOS 10.4+ / 10.15+ | undated | 2026-10-23 |
| apple/input-monitoring | Control access to input monitoring on Mac | https://support.apple.com/guide/mac-help/mchl4cedafb6/mac | macOS 27 edition (I) | undated | 2026-10-23 |
| apple/accessibility-access | Allow accessibility apps to access your Mac | https://support.apple.com/guide/mac-help/mh43185/mac | macOS 27 edition (I) | undated | 2026-10-23 |
| apple/text-replacements | Replace text and punctuation on Mac | https://support.apple.com/guide/mac-help/mh35735/mac | macOS 27 edition (I) | undated | 2026-10-23 |
| apple-forums/836770 | Provisioning Accessibility on macOS 27 after PPPC removal | https://developer.apple.com/forums/thread/836770 | macOS 27 betas 5 and 6 | Jul to Oct 2026 | 2026-10-23 |
| apple-forums/779851 | NSPasteboard in System Extension (DTS answer) | https://developer.apple.com/forums/thread/779851 | macOS 15 | Apr 2025 | 2026-10-23 |
| mjtsai/pb-privacy | Pasteboard Privacy Preview in macOS 15.4 (with updates) | https://mjtsai.com/blog/2025/05/12/pasteboard-privacy-preview-in-macos-15-4/ | macOS 15.4 to 27 | 2025-05-12, updates to 2026-09-22 | 2026-10-23 |
| mjtsai/26.4-paste | macOS 26.4 Paste Protection | https://mjtsai.com/blog/2026/04/03/macos-26-4-paste-protection/ | macOS 26.4 | 2026-04-03 | 2026-10-23 |
| pastepal/pfoa | PastePal: Paste from Other Apps | https://docs.indiegoodies.com/pastepal/Mac/features/paste-from-other-apps | macOS 15.4+ | updated 2026-10-12 | 2026-10-23 |
| parallels/27 | Parallels forum: clipboard problem on macOS 27 | https://forum.parallels.com/threads/problem-with-clipboard-macos-27.371175/ | macOS 27 | 2026-10-01 | 2026-10-23 |
| nspasteboard.org | NSPasteboard.org markers | http://nspasteboard.org/ | spec, last history entry 2026-05-30 | 2026-05-30 | 2026-10-23 |
| maccy/readme | Maccy README | https://github.com/p0deje/Maccy | Maccy 2.7.1 (2026-08-10) | 2026-10-19 (last push) | 2026-10-23 |
| maccy/1117 | Maccy issue #1117 PasswordsMenuBarExtra | https://github.com/p0deje/Maccy/issues/1117 | Maccy 2.3.0, macOS 15.1 | 2025-06-18 to 2025-06-20 | 2026-10-23 |
| arboard/190 | 1Password arboard #190: exclude_from_history on macOS 26 | https://github.com/1Password/arboard/issues/190 | macOS 26 beta 1 | 2025-06-16 | 2026-10-23 |
| bitwarden/11025 | Bitwarden PR #11025: Set nspasteboard.ConcealedType on macOS | https://github.com/bitwarden/clients/pull/11025 | Bitwarden desktop | merged 2024-09-16 | 2026-10-23 |
| bitwarden/17404 | Bitwarden issue #17404: extension does not set pasteboard type | https://github.com/bitwarden/clients/issues/17404 | Bitwarden browser extension | closed not planned (opened 2025-11-15) | 2026-10-23 |
| 1password/19551 | 1Password community: extension copies not marked concealed | https://www.1password.community/1password-at-home-31/passwords-copied-to-the-clipboard-from-the-browser-extensions-are-not-marked-as-concealed-19551 | 1Password 8 | 2022-01-12 to 2025-05-12 | 2026-10-23 |
| alfred/clipboard | Alfred Help: Clipboard History | https://www.alfredapp.com/help/features/clipboard/ | Alfred 5 (I) | last-modified 2026-10-22 | 2026-10-23 |
| brew/cask-365d | Homebrew cask install analytics, 365 days | https://formulae.brew.sh/api/analytics/cask-install/365d.json | 2025-10-23 to 2026-10-23 | 2026-10-23 | 2026-10-23 |
| appstore/paste | App Store lookup: Paste – Limitless Clipboard (id 967805235) | https://itunes.apple.com/lookup?id=967805235&country=us | Paste 7.0.3 | 2026-10-17 | 2026-10-23 |
| github/maccy | GitHub API: p0deje/Maccy | https://api.github.com/repos/p0deje/Maccy | Maccy 2.7.1 | 2026-10-23 | 2026-10-23 |
| lina/contracts | LINA design: non-competition.md, surfaces.md (Launcher) | docs/design/non-competition.md, docs/design/surfaces.md | dev at aab108c4 | 2026-10-23 | 2026-10-23 |

### F: File search, calculator and web search

| Key | Title | URL | Version described | Page or release date | Read |
| --- | --- | --- | --- | --- | --- |
| rc-fs | Raycast Manual: File Search | https://manual.raycast.com/file-search | Raycast v2 (current 2.9) | undated | 2026-10-23 |
| rc-calc | Raycast Manual: Calculator | https://manual.raycast.com/calculator | Raycast v2 (current 2.9) | undated | 2026-10-23 |
| rc-sb | Raycast Manual: Search Bar | https://manual.raycast.com/search-bar | Raycast v2 (current 2.9) | undated | 2026-10-23 |
| rc-set | Raycast Manual: Settings (Launcher, Fallback Commands) | https://manual.raycast.com/settings | Raycast v2 (current 2.9) | undated | 2026-10-23 |
| rc-ks | Raycast Manual: Keyboard Shortcuts | https://manual.raycast.com/keyboard-shortcuts | Raycast v2 (current 2.9) | undated | 2026-10-23 |
| rc-ap | Raycast Manual: Action Panel | https://manual.raycast.com/action-panel | Raycast v2 (current 2.9) | undated | 2026-10-23 |
| rc-ql | Raycast Manual: Quicklinks | https://manual.raycast.com/quicklinks | Raycast v2 (current 2.9) | undated | 2026-10-23 |
| rc-qai | Raycast Manual: Quick AI | https://manual.raycast.com/ai/quick-ai | Raycast v2 (current 2.9) | undated | 2026-10-23 |
| rc-v2 | Raycast Manual: New in Raycast v2 | https://manual.raycast.com/new-in-v2 | Raycast v2 | undated | 2026-10-23 |
| rc-v1fb | Raycast Manual v1: Fallback Commands | https://manual.raycast.com/v1/fallback-commands | Raycast v1 | undated | 2026-10-23 |
| rc-v1calc | Raycast Manual v1: Calculator | https://manual.raycast.com/v1/calculator | Raycast v1 | undated | 2026-10-23 |
| rc-cfcalc | Raycast Core Features: Calculator (marketing page) | https://www.raycast.com/core-features/calculator | v1-era examples | undated | 2026-10-23 |
| rc-cffs | Raycast Core Features: File Search (marketing page) | https://www.raycast.com/core-features/file-search | v1-era | undated | 2026-10-23 |
| rc-cl | Raycast changelog, macOS: 2.0 to 2.9 | https://www.raycast.com/changelog | 2.0 (2026-08-25), 2.1 (09-01), 2.2 (09-08), 2.3 (09-11), 2.5 (09-28), 2.6 (10-05), 2.7 (10-09), 2.8 (10-16), 2.9 (10-22) | per release | 2026-10-23 |
| rc-clb | Raycast changelog, macOS v2 beta | https://www.raycast.com/changelog/macos/2 (also /3 and /4) | 0.51 (2026-03-27), 0.54 (04-15), 0.61 (05-20), 0.62 (06-03), 0.64 (06-12), 0.66 (06-24), 0.67 (07-03), 0.68 (07-09), 0.71 (08-19) | per release | 2026-10-23 |
| rc-cl1 | Raycast changelog, macOS v1 | https://www.raycast.com/changelog/macos-v1 | 1.96.0 (2025-04-16) to 1.104.0 (2025-12-16) | per release | 2026-10-23 |
| rc-blog | The New Raycast (blog) | https://www.raycast.com/blog/the-new-raycast | v2 public beta | 2026-05-14 | 2026-10-23 |
| rc-tech | A Technical Deep Dive Into the New Raycast | https://www.raycast.com/blog/a-technical-deep-dive-into-the-new-raycast | v2 beta | 2026-05-14 | 2026-10-23 |
| rc-store | Raycast Store search API (download_count per extension) | https://www.raycast.com/api/v1/store_listings/search?q=… | live store | live | 2026-10-23 |
| ext-google | Google Search extension README and manifest | https://github.com/raycast/extensions/tree/main/extensions/google-search | store extension | repo HEAD | 2026-10-23 |
| rc-api-env | Raycast API: Environment (getSelectedFinderItems) | https://developers.raycast.com/api-reference/environment | current API | "updated 2 months ago" | 2026-10-23 |
| rc-be | Raycast backend currency endpoints (undocumented; fetched directly) | https://backend.raycast.com/api/v1/currencies and /currencies/crypto | live | timestamp 2026-10-23 08:57 UTC | 2026-10-23 |
| flare | Flare (unofficial Raycast clone, archived): RaycastCurrencyProvider.swift | https://github.com/ByteAtATime/flare | archived repo | last push 2026-01-17 | 2026-10-23 |
| ap-calc | Get calculations and conversions in Spotlight on Mac | https://support.apple.com/guide/mac-help/mchldd6ba066/27/mac/27 | macOS 27 (same text on 26) | undated | 2026-10-23 |
| ap-filter | Narrow your search results in Spotlight on Mac | https://support.apple.com/guide/mac-help/mchl4d69efd3/27/mac/27 | macOS 27 and 26 | undated | 2026-10-23 |
| ap-keys | Spotlight keyboard shortcuts on Mac | https://support.apple.com/guide/mac-help/mh26783/27/mac/27 | macOS 27 and 26 | undated | 2026-10-23 |
| ap-find | Find what you need with Spotlight (27); Search for anything with Spotlight (26) | https://support.apple.com/guide/mac-help/mchlp1008/27/mac/27 | macOS 27 and 26 | undated | 2026-10-23 |
| ap-actions | Take actions and shortcuts in Spotlight on Mac | https://support.apple.com/guide/mac-help/mchl4953dfeb/27/mac/27 | macOS 27 and 26 | undated | 2026-10-23 |
| ap-settings | Spotlight settings on Mac | https://support.apple.com/guide/mac-help/mchl54d95e8a/27/mac/27 | macOS 27 and 26 | undated | 2026-10-23 |
| ap-privacy | Prevent Spotlight searches in specific folders or disks | https://support.apple.com/guide/mac-help/mchl1bb43b84/27/mac/27 | macOS 27 | undated | 2026-10-23 |
| ap-cats | Select result categories for Spotlight | https://support.apple.com/guide/mac-help/mchl3e00eae9/27/mac/27 | macOS 27 | undated | 2026-10-23 |
| ap-ff | Control access to files and folders on Mac | https://support.apple.com/guide/mac-help/mchld5a35146/27/mac/27 | macOS 27 | undated | 2026-10-23 |
| ad-mdq | NSMetadataQuery, searchScopes, Metadata Query Search Scopes, content relevance | https://developer.apple.com/documentation/foundation/nsmetadataquery | macOS 10.4+ (current docs) | current | 2026-10-23 |
| ad-text | kMDItemTextContent | https://developer.apple.com/documentation/coreservices/kmditemtextcontent | macOS 10.4+ | current | 2026-10-23 |
| ad-qfmt | File Metadata Query Expression Syntax (archive) | https://developer.apple.com/library/archive/documentation/Carbon/Conceptual/SpotlightQuery/Concepts/QueryFormat.html | archive, still valid syntax | archived | 2026-10-23 |
| ad-ql | QLPreviewPanel | https://developer.apple.com/documentation/quicklookui/qlpreviewpanel | macOS 10.6+ | current | 2026-10-23 |
| ad-ws | NSWorkspace (open, openApplication, recycle, activateFileViewerSelecting, urlsForApplications) | https://developer.apple.com/documentation/appkit/nsworkspace | current | current | 2026-10-23 |
| ad-css | CSSearchQuery and CSUserQuery | https://developer.apple.com/documentation/corespotlight/cssearchquery | macOS 10.12+ and 13+ | current | 2026-10-23 |
| ad-dcs | DCSCopyTextDefinition (Dictionary Services) | https://developer.apple.com/documentation/coreservices/1446842-dcscopytextdefinition | macOS 10.5+ | current | 2026-10-23 |
| dts-perm | Apple DTS forum post: On File System Permissions | https://developer.apple.com/forums/thread/678819 | macOS 10.14 to current | created 2021-04, revised 2026-09-18 | 2026-10-23 |
| so-scope | Stack Overflow 79892389: NSMetadataQuery searches stop working for sandboxed apps | https://stackoverflow.com/questions/79892389 | current macOS (unspecified) | 2026-02-19 | 2026-10-23 |
| so-docs | Stack Overflow 79923474: NSMetadataQuery not returning relevant files | https://stackoverflow.com/questions/79923474 | current macOS (unspecified) | 2026-04-10 | 2026-10-23 |
| soulver | SoulverCore README and licence | https://github.com/soulverteam/SoulverCore | 3.5.1 | 2026-08-23 | 2026-10-23 |
| soulver-doc | Soulver docs: Alfred and Raycast | https://documentation.soulver.app/documentation/integrations/alfred-and-raycast | Soulver 3 | undated | 2026-10-23 |
| ecb | ECB euro foreign exchange reference rates (page and eurofxref-daily.xml) | https://www.ecb.europa.eu/stats/eurofxref/eurofxref-daily.xml | live | rates dated 2026-10-22 | 2026-10-23 |
| frank | Frankfurter API (repo and /v1/currencies) | https://github.com/lineofflight/frankfurter | live | rates dated 2026-10-22 | 2026-10-23 |
| erapi | ExchangeRate-API open access (open.er-api.com) and terms | https://www.exchangerate-api.com/docs/free | live | last update 2026-10-23 00:02 UTC | 2026-10-23 |
| fend | fend calculator repo | https://github.com/printfn/fend | HEAD | last push 2026-10-21 | 2026-10-23 |
| mathjs | math.js repo | https://github.com/josdejong/mathjs | HEAD | last push 2026-09-13 | 2026-10-23 |
| qalc | libqalculate repo | https://github.com/Qalculate/libqalculate | HEAD | last push 2026-10-21 | 2026-10-23 |
| numi | Numi repo (issues and CLI; app closed-source) | https://github.com/nikolaeu/numi | HEAD | last push 2026-07-19 | 2026-10-23 |

Store install counts are cumulative `download_count` values from rc-store, read on 2026-10-23. Built-in Raycast features have no install counts.

### A: Raycast AI, extension API, script commands and deeplinks

All read on 2026-10-23. "Page date" is the manual's `dateModified` field where one exists.

- **cl**: Raycast changelog for macOS, https://www.raycast.com/changelog (pages /changelog/macos/2-0 to /2-9). Covers Raycast 2.0 to 2.9. Dated 2026-08-25 (2.0), 2026-09-01 (2.1), 2026-09-08 (2.2), 2026-09-11 (2.3), 2026-09-14 (2.4), 2026-09-28 (2.5), 2026-10-05 (2.6), 2026-10-09 (2.7), 2026-10-16 (2.8) and 2026-10-22 (2.9).
- **m/new-in-v2**: New in Raycast v2, https://manual.raycast.com/new-in-v2 (and .md). Raycast 2.x. Page date 2026-09-10.
- **m/quick-ai**: https://manual.raycast.com/ai/quick-ai. Raycast 2.9 (current manual). Page date 2026-10-02.
- **m/ai-chat**: https://manual.raycast.com/ai/ai-chat. Raycast 2.9. Page date 2026-10-06.
- **m/ai-commands**: https://manual.raycast.com/ai/ai-commands. Raycast 2.9. Page date 2026-10-09.
- **m/ai-extensions**: https://manual.raycast.com/ai/ai-extensions. Raycast 2.9. Page date 2026-09-10.
- **m/mcp**: https://manual.raycast.com/ai/model-context-protocol. Raycast 2.9. Page date 2026-10-02.
- **m/skills**: https://manual.raycast.com/ai/skills. Raycast 2.9. Page date 2026-08-20.
- **m/agents**: https://manual.raycast.com/ai/agents. Raycast 2.9. Page date 2026-08-20.
- **m/projects**: https://manual.raycast.com/ai/projects. Raycast 2.9. Page date 2026-09-25.
- **m/automations**: https://manual.raycast.com/ai/automations. Raycast 2.9. Page date 2026-10-01.
- **m/personalization**: https://manual.raycast.com/ai/personalization. Raycast 2.9. Page date 2026-10-08.
- **m/screen-awareness**: https://manual.raycast.com/ai/screen-awareness. Raycast 2.9. Page date 2026-10-07.
- **m/dictation**: https://manual.raycast.com/ai/dictation. Raycast 2.9. Page date 2026-10-02.
- **m/ai-credits**: https://manual.raycast.com/ai/ai-credits. Raycast 2.9. Page date 2026-10-01.
- **m/local-models**: https://manual.raycast.com/ai/local-models. Raycast 2.9. Page date 2026-10-01.
- **m/extension-models**: https://manual.raycast.com/ai/extension-models. Raycast 2.9. Page date 2026-10-20.
- **m/custom-providers**: https://manual.raycast.com/ai/custom-providers. Raycast 2.9. Page date 2026-10-07.
- **m/byok**: https://manual.raycast.com/ai/bring-your-own-key. Raycast 2.9. Page date 2026-10-01.
- **m/byos**: https://manual.raycast.com/ai/bring-your-own-subscription. Raycast 2.9. Page date 2026-10-01.
- **m/ai-privacy**: https://manual.raycast.com/ai/raycast-ai-privacy-security. Raycast 2.9. Page date 2026-10-16.
- **m/placeholders**: https://manual.raycast.com/dynamic-placeholders. Raycast 2.9. Page date 2026-08-20.
- **m/script-commands**: https://manual.raycast.com/script-commands. Raycast 2.9. Page date 2026-09-19.
- **m/extensions**: https://manual.raycast.com/extensions. Raycast 2.9. Page date 2026-09-19.
- **m/ext-guidelines**: https://manual.raycast.com/extensions-guidelines. Raycast 2.x. Page date 2026-06-24.
- **m/settings**: https://manual.raycast.com/settings. Raycast 2.9. Page date 2026-10-02.
- **m/aliases**: https://manual.raycast.com/command-aliases-and-hotkeys. Raycast 2.9. Page date 2026-10-02.
- **m/ios**: https://manual.raycast.com/ios, /ios/apple-shortcuts, /ios/share-extension and /ios/widgets-and-controls. Raycast for iOS (version unknown). Page dates 2026-07-06, 2026-04-28, 2026-07-06 and 2026-04-28.
- **m/enterprise**: https://manual.raycast.com/enterprise/private-extension-publishing and /enterprise/extensions-allow-list. Raycast 2.x. Page dates 2026-10-22 and 2026-08-31.
- **m/v1-deeplinks**: https://manual.raycast.com/v1/deeplinks. The page says it applies only to Raycast for Mac v1. Page date unknown.
- **pricing**: https://www.raycast.com/pricing. Current. Page date unknown.
- **browser-ext**: https://www.raycast.com/browser-extension. Current. Page date unknown.
- **blog/v2-tech**: A Technical Deep Dive Into the New Raycast, https://www.raycast.com/blog/a-technical-deep-dive-into-the-new-raycast. Covers Raycast 2.0. Dated 2026-05-14.
- **dev/...**: Raycast API docs, https://developers.raycast.com (pages read as `.md`). Covers `@raycast/api` 2.x; the docs changelog's latest entry is 2.5.0 (2026-09-24). The pages are undated. Pages read:
  - changelog: /misc/changelog
  - security: /information/security
  - versioning: /information/versioning
  - faq: /misc/faq
  - manifest: /information/manifest
  - lifecycle: /information/lifecycle, with /arguments, /background-refresh and /deeplinks
  - AI: /api-reference/ai and /api-reference/tool, plus /ai/getting-started, /ai/learn-core-concepts-of-ai-extensions, /ai/evals and /ai/provide-ai-models
  - environment: /api-reference/environment
  - command: /api-reference/command
  - clipboard: /api-reference/clipboard
  - storage: /api-reference/storage
  - cache: /api-reference/cache
  - feedback: /api-reference/feedback (toast, hud, alert)
  - utilities: /api-reference/utilities
  - window management: /api-reference/window-management
  - browser extension: /api-reference/browser-extension
  - menu bar: /api-reference/menu-bar-commands
  - OAuth: /api-reference/oauth
  - preferences: /api-reference/preferences
  - user interface: /api-reference/user-interface (list, grid, detail, form, actions, action-panel, navigation)
  - window and search bar: /api-reference/window-and-search-bar
  - keyboard: /api-reference/keyboard
  - teams: /teams/getting-started and /teams/publish-a-private-extension
  - publishing: /basics/publish-an-extension and /basics/prepare-an-extension-for-store
  - utils: /utilities/functions/createdeeplink and /utilities/getting-started
- **npm**: https://registry.npmjs.org/@raycast/api. `@raycast/api` 2.9.0, published 2026-10-22, requires Node >= 22.22.2. The `latest-v0` tag is 0.71.7.
- **gh/script-commands**: https://github.com/raycast/script-commands: README, documentation/OUTPUTMODES.md and documentation/ARGUMENTS.md. The repo was last pushed 2026-09-21 and has 6,802 stars. `commands/extensions.json` lists 858 scripts (updatedAt 2026-04-27).
- **store-api**: https://www.raycast.com/api/v1/store_listings (paged, sorted by downloads). Read 2026-10-23. It returned 3,451 unique public listings with 14,744,078 downloads in total. All counts and shares below are computed from this data.
- **gh/extensions**: https://github.com/raycast/extensions at commit e987fc0 (2026-10-23). I counted features across 3,458 extension `package.json` files and grepped `src/**/*.ts(x)`. Download shares come from joining those results with store-api.
- **gh/link-commands**: raycast/extensions `extensions/link-commands/CHANGELOG.md`, entry "Raycast 2 Deeplink Fix" (2026-10-01). This is a community extension, so it is a secondary source.
- **gh/swift-tools**: https://github.com/raycast/extensions-swift-tools (last pushed 2026-07-22) and https://github.com/raycast/extensions-rust-tools (last pushed 2025-12-04).
- **lina-docs**: this repository's docs/design/surfaces.md (Launcher), non-competition.md (clipboard), integrations.md (Plugins, OAuth) and runtime.md (Providers), and issues #951, #966 and lina-app #25 and #28. Used for the decisions only.

### X: Raycast Store extensions

- `store-api`: Raycast Store listing API, https://www.raycast.com/api/v1/store_listings?page=1..35&per_page=100. 3,451 entries, live data for the Raycast 2.9 store. Data date 2026-10-23, about 08:45 UTC. Read 2026-10-23.
- `store-ext`: Raycast per-extension API, https://www.raycast.com/api/v1/extensions/rolandleth/kill-process, /thomas/color-picker and /Codely/google-chrome. These three are missing from `store-api`. Data date 2026-10-23, about 08:49 UTC. Read 2026-10-23.
- `store-page`: https://www.raycast.com/store (first 10 cards, ordered Kill Process, Color Picker, Google Chrome, Translate, ...) and https://www.raycast.com/rolandleth/kill-process, which shows "787,442 Installs". Store for Raycast 2.9. Read 2026-10-23.
- `gh-ext`: https://github.com/raycast/extensions, `main` at e987fc0 (2026-10-23T05:35Z). Files read: extensions/apple-notes/package.json and src/helpers.ts, color-picker/swift/.../ColorPicker.swift, clean-keyboard/swift/.../SwiftAPIKeyboard.swift, kill-process/package.json, and the READMEs of 1password, video-downloader, speedtest, apple-reminders and music. Read 2026-10-23.
- `changelog`: https://www.raycast.com/changelog, Raycast for Mac 2.9 (2026-10-22), 2.8 (2026-10-16), 2.7 (2026-10-09). Read 2026-10-23.
- `linear-mcp`: https://linear.app/docs/mcp, Linear MCP server. Page date unknown. Read 2026-10-23.
- `notion-mcp`: https://developers.notion.com/docs/mcp, Notion MCP. Page date unknown. Read 2026-10-23.
- `slack-mcp`: https://docs.slack.dev/ai/slack-mcp-server, Slack MCP server. Page date unknown; the announcement in the changelog is dated 2026-02-17 (https://docs.slack.dev/changelog/2026/02/17/slack-mcp). Read 2026-10-23.
- `zoom-mcp`: https://developers.zoom.us/docs/mcp/zoom-meetings-mcp-server/, Zoom Meetings MCP server. The page returned 404 when fetched, so the facts come from a search snippet and are I. Date unknown. Read 2026-10-23.
- `todoist-mcp`: https://github.com/Doist/todoist-mcp, Todoist MCP (ai.todoist.net/mcp). Facts come from search results; I did not open the repo, so they are I. Date unknown. Read 2026-10-23.
- `spotify-quota`: https://developer.spotify.com/documentation/web-api/concepts/quota-modes, Spotify Web API quota modes. No page date; the page shows © 2026 and its changelog runs through July 2026. Read 2026-10-23.
- `lina-integrations`: docs/design/integrations.md in thisisjun786/lina, at dev aab108c4. Read 2026-10-23.
- `lina-963`: issue thisisjun786/lina#963, owner comments of 10-23. Read 2026-10-23.

### S: Spotlight on macOS 26 and 27

Apple, primary:
- `apple/sp-keys`: Spotlight keyboard shortcuts on Mac. https://support.apple.com/guide/mac-help/spotlight-keyboard-shortcuts-mh26783/mac. macOS 27. Page date: none shown. Read 2026-10-23.
- `apple/sp-find`: Find what you need with Spotlight on Mac. https://support.apple.com/guide/mac-help/find-what-you-need-with-spotlight-mchlp1008/mac. macOS 27. Page date: none shown. Read 2026-10-23.
- `apple/sp-actions`: Take actions and shortcuts in Spotlight on Mac. https://support.apple.com/guide/mac-help/take-actions-and-shortcuts-in-spotlight-mchl4953dfeb/27/mac/27. macOS 27; a macOS 26 version also exists at /26/mac/26. Page date: none shown. Read 2026-10-23.
- `apple/sp-narrow`: Narrow your search results in Spotlight on Mac. https://support.apple.com/guide/mac-help/narrow-search-results-in-spotlight-mchl4d69efd3/mac. macOS 27. Page date: none shown. Read 2026-10-23.
- `apple/sp-clip`: Search your Clipboard history in Spotlight on Mac. https://support.apple.com/guide/mac-help/search-your-clipboard-history-mchl40d5b86b/mac. macOS 27. Page date: none shown. Read 2026-10-23.
- `apple/sp-settings`: Spotlight settings on Mac. https://support.apple.com/guide/mac-help/spotlight-settings-on-mac-mchl54d95e8a/mac. macOS 27. Page date: none shown. Read 2026-10-23.
- `apple/sp-cats`: Select result categories for Spotlight on Mac. https://support.apple.com/guide/mac-help/select-result-categories-for-spotlight-mchl3e00eae9/mac. macOS 27. Page date: none shown. Read 2026-10-23.
- `apple/sp-privacy`: Prevent Spotlight searches in files on Mac. https://support.apple.com/guide/mac-help/prevent-spotlight-searches-specific-folders-mchl1bb43b84/mac. macOS 27. Page date: none shown. Read 2026-10-23.
- `apple/index`: About Spotlight indexing and search results on Apple devices. https://support.apple.com/en-us/102321. No OS version named. Published 2026-08-25. Read 2026-10-23.
- `apple/macos27-updates`: What's new in the updates for macOS 27 Golden Gate. https://support.apple.com/en-us/127257. macOS 27.0 and 27.0.1. Published 2026-09-28. Read 2026-10-23.
- `apple/newsroom-26`: macOS Tahoe 26 press release. https://www.apple.com/newsroom/2025/06/macos-tahoe-26-makes-the-mac-more-capable-productive-and-intelligent-than-ever/. macOS 26. 2025-06-09. Read 2026-10-23.
- `apple/shortcuts-cli`: Run shortcuts from the command line (Shortcuts User Guide). https://support.apple.com/guide/shortcuts-mac/run-shortcuts-from-the-command-line-apd455c82f02/mac. Shortcuts 10.0 on macOS 27. Page date: none shown. Read 2026-10-23.
- `apple/shortcuts-url`: Run a shortcut from a URL. https://support.apple.com/guide/shortcuts-mac/run-a-shortcut-from-a-url-apd624386f42/mac. Shortcuts 10.0 on macOS 27. Page date: none shown. Read 2026-10-23.
- `apple/wwdc25-260`: WWDC25 session 260, "Develop for Shortcuts and Spotlight with App Intents" (transcript). https://developer.apple.com/videos/play/wwdc2025/260/. macOS 26. 2025-06. Read 2026-10-23.
- `apple/wwdc26-macos`: WWDC26 macOS guide. https://developer.apple.com/wwdc26/guides/macos/. macOS 27. 2026 (no day shown). Read 2026-10-23.
- `apple/doc-nsmq`: NSMetadataQuery, NSMetadataQueryUserHomeScope, NSMetadataQueryLocalComputerScope and notificationBatchingInterval (doc JSON). https://developer.apple.com/documentation/foundation/nsmetadataquery. macOS 10.4 and later, current docs. Page date: none shown. Read 2026-10-23.
- `apple/doc-csq`: CSSearchQuery. https://developer.apple.com/documentation/corespotlight/cssearchquery. macOS 10.12 and later. Page date: none shown. Read 2026-10-23.
- `apple/doc-contacts`: CNContactStore and CNAuthorizationStatus.limited. https://developer.apple.com/documentation/contacts/cncontactstore. CNContactStore is macOS 10.11 and later; `.limited` is listed for iOS 18 and has no macOS entry. Page date: none shown. Read 2026-10-23.
- `apple/doc-eventkit`: EKEventStore requestFullAccessToEvents, requestWriteOnlyAccessToEvents, requestFullAccessToReminders. https://developer.apple.com/documentation/eventkit/ekeventstore. macOS 14 and later. Page date: none shown. Read 2026-10-23.
- `apple/doc-dcs`: DCSCopyTextDefinition. https://developer.apple.com/documentation/coreservices/1446842-dcscopytextdefinition. macOS 10.5 and later, not deprecated. Page date: none shown. Read 2026-10-23.
- `apple/doc-ql`: QLPreviewPanel. https://developer.apple.com/documentation/quicklookui/qlpreviewpanel. macOS 10.6 and later. Page date: none shown. Read 2026-10-23.
- `apple/doc-rssi`: RunSystemShortcutIntent. https://developer.apple.com/documentation/appintents/runsystemshortcutintent. Listed for iOS/iPadOS 27 only, widgets only. Page date: none shown. Read 2026-10-23.
- `apple/doc-pb`: NSPasteboard.AccessBehavior. https://developer.apple.com/documentation/appkit/nspasteboard/accessbehavior-swift.enum. macOS 15.4 and later. Page date: none shown. Read 2026-10-23.
- `apple/dts-fsperm`: "On File System Permissions" (Apple DTS, Quinn). https://developer.apple.com/forums/thread/678819. Current macOS; first posted 2021-04-26, last revised 2026-09-18. Read 2026-10-23.
- `apple/dts-postevent`: Clipboard manager and CGEvent.post (Apple DTS reply). https://developer.apple.com/forums/thread/820594. Current macOS. 2026-03. Read 2026-10-23.

Raycast and Alfred:
- `raycast/settings`: Raycast manual, Settings (Raycast Hotkey). https://manual.raycast.com/settings. Raycast v2; the minor version is not stated. Page date: none shown. Read 2026-10-23.
- `raycast/v1-hotkey`: Raycast manual v1, Hotkey. https://manual.raycast.com/v1/hotkey. Raycast v1. Page date: none shown. Read 2026-10-23.
- `raycast/file-search`: Raycast manual, File Search. https://manual.raycast.com/file-search. Raycast v2. Page date: none shown. Read 2026-10-23.
- `raycast-ext/messages`: Raycast Messages extension README. https://github.com/raycast/extensions/tree/main/extensions/messages. Current main branch. Read 2026-10-23.
- `alfred/cmd-space`: Alfred help, "Setting Cmd + Space as Alfred hotkey". https://www.alfredapp.com/help/troubleshooting/cmd-space/. Alfred 5; no macOS version named. Undated (©2026). Read 2026-10-23.

Press and community (secondary):
- `9to5/26.1-clip`: macOS Tahoe 26.1 adds new tools for Spotlight's clipboard feature. https://9to5mac.com/2025/11/04/macos-tahoe-26-1-adds-new-tools-for-spotlights-clipboard-feature/. macOS 26.1. 2025-11-04. Read 2026-10-23.
- `9to5/macos27`: macOS 27 Golden Gate now available, everything new. https://9to5mac.com/2026/09/14/macos-27-golden-gate-now-available-here-is-everything-new/. macOS 27. 2026-09-14. Read 2026-10-23.
- `macrumors/tahoe-sp`: Do more with Spotlight in macOS Tahoe. https://www.macrumors.com/how-to/do-more-with-spotlight-in-macos-tahoe/. macOS 26 and 26.1. 2025-12-03. Read 2026-10-23.
- `macrumors/macos27`: New features and changes in macOS 27. https://www.macrumors.com/2026/09/17/new-features-changes-macos-27/. macOS 27. 2026-09-17. Read 2026-10-23.
- `cultofmac/sp`: How to use Spotlight on Mac. https://www.cultofmac.com/how-to/spotlight-mac. macOS 26.1. 2025-11-12. Read 2026-10-23.
- `idb/tahoe-sp`: How to use the new Spotlight on Mac. https://www.idownloadblog.com/2025/08/06/how-to-use-new-spotlight-search-mac/. macOS 26. Updated 2026-05-03. Read 2026-10-23.
- `macmost/show-apps`: How to launch apps now that Launchpad is gone. https://macmost.com/how-to-launch-apps-now-that-launchpad-is-gone.html. macOS 26. 2025-09-25. Read 2026-10-23.
- `sixcolors/2026-04`: I'm switching back from Spotlight, at least for now (Jason Snell). https://sixcolors.com/post/2026/04/im-switching-back-from-spotlight-at-least-for-now/. macOS 26.3 to 26.5 beta. 2026-04-22. Read 2026-10-23.
- `mjtsai/2026-02`: Searching for apps with Spotlight. https://mjtsai.com/blog/2026/02/18/searching-for-apps-with-spotlight/. macOS 26.x. 2026-02-18. Read 2026-10-23.
- `mjtsai/2019-mail`: Spotlight excludes Mail folder on macOS 10.15. https://mjtsai.com/blog/2019/08/29/spotlight-excludes-mail-folder-on-macos-10-15/. macOS 10.15, and the design still holds. 2019-08-29. Read 2026-10-23.
- `eclectic/2026-02`: In the background: Spotlight indexing. https://eclecticlight.co/2026/02/10/in-the-background-spotlight-indexing/. macOS 26.2. 2026-02-10. Read 2026-10-23.
- `eclectic/2026-01`: Can you disable Spotlight and Siri in macOS Tahoe? https://eclecticlight.co/2026/01/16/can-you-disable-spotlight-and-siri-in-macos-tahoe/. macOS 26. 2026-01-16. Read 2026-10-23.
- `cassinelli/wwdc26`: WWDC26 session 345 summary. https://matthewcassinelli.com/wwdc26-sessions-new-capabilities-app-intents-framework/. macOS 27 and iOS 27. 2026-06. Read 2026-10-23.
- `gist/sysprefs`: System Settings URL schemes gist and comments: the Tahoe 26.2 string dump (2026-01-18) and the `?Shortcuts` anchor (2026-03-02). https://gist.github.com/rmcdongit/f66ff91e0dad78d4d6346a75ded4b751. macOS 26.2. Read 2026-10-23.
- `gh/sidebar`: SystemSettings-URLs-macOS, Tahoe.Sidebar.plist (pane IDs). https://github.com/jaywcjlove/SystemSettings-URLs-macOS. macOS 26. Plist added 2025-09-29. Read 2026-10-23.
- `viticci/playground`: Shortcuts Playground plugin. It reads the Shortcuts ToolKit database to list installed third-party actions. https://github.com/viticci/shortcuts-playground-plugin. macOS 26 and 27. Current. Read 2026-10-23.
- `visca/2025`: Raycast vs Alfred vs Spotlight comparison (stopwatch test). https://brandonvisca.com/raycast-vs-alfred-spotlight-comparatif-macos/. Mac mini M4; macOS version not stated. 2025-11-27. Read 2026-10-23.
- `dtr/2026`: DevToolReviews, Raycast vs Alfred vs Spotlight. https://www.devtoolreviews.com/reviews/raycast-vs-alfred-vs-spotlight. No hardware, macOS version or method given. 2026-01-29, updated 2026-10-21. Read 2026-10-23.
- `search/*`: facts that came only from search-engine summaries of pages I did not open. They are always marked I.
  - `search/symhotkeys`: zameermanji.com 2021 and superuser 1211108, on the com.apple.symbolichotkeys key 64.
  - `search/clip-concealed`: pastezo.app and sniptray.com blogs, on Spotlight clipboard skipping org.nspasteboard.ConcealedType.
  - `search/reenable`: idownloadblog 2025-12 and technobezz, on macOS updates re-enabling the Spotlight shortcut.
  - `search/27-reviews`: Macworld, CNET and MacStories macOS 27 reviews, on category navigation with ↓ and faster indexing.

### N: Native feel and panel technology

Format: key — title — URL — version described — page or release date — read.

- apple/nspanel — NSPanel, isFloatingPanel, becomesKeyOnlyIfNeeded — https://developer.apple.com/documentation/appkit/nspanel — macOS 27 SDK docs — undated — 2026-10-23
- apple/nonactivating — NSWindow.StyleMask.nonactivatingPanel — https://developer.apple.com/documentation/appkit/nswindow/stylemask-swift.struct/nonactivatingpanel — macOS 27 SDK docs — undated — 2026-10-23
- apple/canbecomekey — NSWindow.canBecomeKey — https://developer.apple.com/documentation/appkit/nswindow/canbecomekey — macOS 27 SDK docs — undated — 2026-10-23
- apple/collection — NSWindow.CollectionBehavior (canJoinAllSpaces, moveToActiveSpace, fullScreenAuxiliary, auxiliary, canJoinAllApplications, transient, ignoresCycle, stationary) — https://developer.apple.com/documentation/appkit/nswindow/collectionbehavior-swift.struct — macOS 10.5–13 APIs, macOS 27 docs — undated — 2026-10-23
- apple/levels — NSWindow.Level .floating/.statusBar, NSWindow.hasShadow, animationBehavior — https://developer.apple.com/documentation/appkit/nswindow/level-swift.struct — macOS 27 docs — undated — 2026-10-23
- apple/glass — NSGlassEffectView (contentView, cornerRadius, tintColor, style regular/clear, effectIsInteractive), NSGlassEffectContainerView — https://developer.apple.com/documentation/appkit/nsglasseffectview — macOS 26.0 (effectIsInteractive macOS 27.0) — undated — 2026-10-23
- apple/glass-swiftui — View.glassEffect(_:in:) — https://developer.apple.com/documentation/swiftui/view/glasseffect(_:in:) — macOS 26.0 — undated — 2026-10-23
- apple/vev — NSVisualEffectView, Material, State, BlendingMode — https://developer.apple.com/documentation/appkit/nsvisualeffectview — macOS 10.10+ — undated — 2026-10-23
- apple/hig-materials — Human Interface Guidelines: Materials (Liquid Glass, standard materials) — https://developer.apple.com/design/human-interface-guidelines/materials — current HIG (macOS 26/27) — undated — 2026-10-23
- apple/a11y-display — NSWorkspace accessibilityDisplayShouldReduceTransparency / ShouldReduceMotion / ShouldIncreaseContrast, accessibilityDisplayOptionsDidChangeNotification — https://developer.apple.com/documentation/appkit/nsworkspace/accessibilitydisplayshouldreducetransparency — macOS 10.10/10.12 — undated — 2026-10-23
- apple/accent — NSColor.controlAccentColor, keyboardFocusIndicatorColor — https://developer.apple.com/documentation/appkit/nscolor/controlaccentcolor — macOS 10.14 — undated — 2026-10-23
- apple/dyncolor — NSColor(name:dynamicProvider:), NSAppearance.Name.accessibilityHighContrastAqua, bestMatch(from:) — https://developer.apple.com/documentation/appkit/nscolor/init(name:dynamicprovider:) — macOS 10.14/10.15 — undated — 2026-10-23
- apple/textinput — NSTextInputClient, windowLevel(), NSTextInputContext.selectedKeyboardInputSource, allowedInputSourceLocales — https://developer.apple.com/documentation/appkit/nstextinputclient — macOS 10.6+ — undated — 2026-10-23
- apple/korean-im — Korean Input Method User Guide: convert Hangul to Hanja — https://support.apple.com/guide/korean-input-method/him001/mac — macOS 27 — undated — 2026-10-23
- apple/spotlight-keys — Spotlight keyboard shortcuts on Mac — https://support.apple.com/guide/mac-help/spotlight-keyboard-shortcuts-mh26783/mac — macOS 27 — undated — 2026-10-23
- apple/menubar — SwiftUI MenuBarExtra; NSStatusItem — https://developer.apple.com/documentation/swiftui/menubarextra — macOS 13 — undated — 2026-10-23
- apple/notif — UNUserNotificationCenter — https://developer.apple.com/documentation/usernotifications/unusernotificationcenter — macOS 10.14 — undated — 2026-10-23
- apple/focus — SetFocusFilterIntent — https://developer.apple.com/documentation/appintents/setfocusfilterintent — macOS 13.0 — undated — 2026-10-23
- apple/services — NSServices Info.plist key; NSApplication.servicesProvider — https://developer.apple.com/documentation/bundleresources/information-property-list/nsservices — macOS 10.0 — undated — 2026-10-23
- apple/share — NSSharingServicePicker — https://developer.apple.com/documentation/appkit/nssharingservicepicker — macOS 10.8 (popover since 13) — undated — 2026-10-23
- apple/drag — NSDraggingSource, NSView.beginDraggingSession(with:event:source:), NSDraggingItem, NSFilePromiseProvider — https://developer.apple.com/documentation/appkit/nsdraggingsource — macOS 10.7/10.12 — undated — 2026-10-23
- apple/ax — NSAccessibility.Notification.announcementRequested — https://developer.apple.com/documentation/appkit/nsaccessibility-swift.struct/notification/announcementrequested — macOS 10.7 — undated — 2026-10-23
- apple/screen — NSScreen.main, NSScreen.screens — https://developer.apple.com/documentation/appkit/nsscreen/screens — macOS — undated — 2026-10-23
- apple/policy — NSApplication.ActivationPolicy.accessory (LSUIElement); SMAppService — https://developer.apple.com/documentation/servicemanagement/smappservice — macOS 13 — undated — 2026-10-23
- apple/measure — SCStreamFrameInfo.displayTime, SCStreamConfiguration.minimumFrameInterval, CGEvent.post(tap:), CGEvent.timestamp, CGPreflightPostEventAccess, OSSignposter, XCTApplicationLaunchMetric (no macOS) — https://developer.apple.com/documentation/screencapturekit/scstreamframeinfo/displaytime — macOS 10.4–12.3 — undated — 2026-10-23
- apple/forum-hotkey — Developer Forums 763878: RegisterEventHotKey and Option-only modifiers — https://developer.apple.com/forums/thread/763878 — macOS 15.0–15.2 — 2024-09 (seen only via search summary) — 2026-10-23
- 9to5/macos27 — macOS 27 Golden Gate now available — https://9to5mac.com/2026/09/14/macos-27-golden-gate-now-available-here-is-everything-new/ — macOS 27 — 2026-09-14 — 2026-10-23
- 9to5/glass-slider — macOS 27 gives you more control over Liquid Glass — https://9to5mac.com/2026/09/22/macos-27-gives-you-more-control-over-liquid-glass/ — macOS 27 — 2026-09-22 — 2026-10-23
- rc/deepdive — A Technical Deep Dive Into the New Raycast — https://www.raycast.com/blog/a-technical-deep-dive-into-the-new-raycast — Raycast 2.0 (beta) — 2026-05-14 — 2026-10-23
- rc/new — The New Raycast — https://www.raycast.com/blog/the-new-raycast — Raycast 2.0 beta — 2026-05-14 — 2026-10-23
- rc/changelog — Raycast changelog — https://www.raycast.com/changelog — Raycast 2.0 (2026-08-25) to 2.9 (2026-10-22) — 2026-10-22 — 2026-10-23
- rc/settings — Raycast Manual: Settings — https://manual.raycast.com/settings — Raycast v2 — undated — 2026-10-23
- alfred/missing — Alfred: Missing typed characters — https://www.alfredapp.com/help/troubleshooting/missing-typed-characters/ — Alfred, version not stated — undated — 2026-10-23
- se/223750 — Spotlight misses first character after Cmd-Space — https://apple.stackexchange.com/questions/223750 — OS X 10.11 era — 2016-01-16 — 2026-10-23
- se/481509 — Mac sometimes changes first letter to English in macOS Tahoe — https://apple.stackexchange.com/questions/481509 — macOS 26.0 (25A353) — 2025-09-15 — 2026-10-23
- sixcolors — I'm switching back from Spotlight, at least for now — https://sixcolors.com/post/2026/04/im-switching-back-from-spotlight-at-least-for-now/ — macOS 26.3–26.5 beta — 2026-04-22 — 2026-10-23
- dtr — Raycast vs Alfred vs Spotlight (DevToolReviews) — https://www.devtoolreviews.com/reviews/raycast-vs-alfred-vs-spotlight — versions not stated, no method — 2026-01-29, updated 2026-10-21 — 2026-10-23
- legend — LegendApp framework-benchmarks: Desktop framework comparison report and Hello World harness — https://github.com/LegendApp/framework-benchmarks/blob/main/docs/DESKTOP_FRAMEWORK_REPORT.md — macOS 26.6.1, Apple M4 — 2026-09-17 — 2026-10-23
- webkit/192943 — WebKit bug 192943: WebContent process launch time — https://bugs.webkit.org/show_bug.cgi?id=192943 — macOS 10.14 (18E158) — 2018-12-20 — 2026-10-23
- fl/impeller — Flutter docs: Impeller rendering engine — https://docs.flutter.dev/perf/impeller — Flutter 3.47 — 2026-08-21 — 2026-10-23
- fl/engine — Flutter macOS embedder source: FlutterEngine.mm, FlutterTextInputPlugin.mm — https://github.com/flutter/flutter/tree/master/engine/src/flutter/shell/platform/darwin/macos/framework/Source — master — 2026-10-23 — 2026-10-23
- fl/194199 — [macOS] With semantics enabled, the first composing keystroke into an empty TextField loses its composition — https://github.com/flutter/flutter/issues/194199 — Flutter 3.47.7, macOS 27.0 — 2026-10-12 (open, P1) — 2026-10-23
- fl/190525 — [macOS] Enter that commits IME conversion also triggers Shortcuts — https://github.com/flutter/flutter/issues/190525 — Flutter 3.44.8, macOS 26.5.2 — 2026-08-04 (open) — 2026-10-23
- fl/120852 — [macOS] TextField left arrow key while composing deletes text — https://github.com/flutter/flutter/issues/120852 — Flutter 3.7 — 2023-02-16 (open) — 2026-10-23
- fl/191001 — [accessibility][macOS] VoiceOver fails to follow Tab focus — https://github.com/flutter/flutter/issues/191001 — 2026-08-12 (open) — 2026-10-23
- fl/191013 — [accessibility][macOS] VoiceOver cursor jumps to first element after a route closes — https://github.com/flutter/flutter/issues/191013 — 2026-08-12 (open) — 2026-10-23
- fl/170310 — Support for iOS 26 Liquid Glass in Cupertino widgets — https://github.com/flutter/flutter/issues/170310 — 2025-06-10 (open, P3) — 2026-10-23
- fl/59969 — [macos] Transparent FlutterViewController (backgroundColor = .clear workaround) — https://github.com/flutter/flutter/issues/59969 — Flutter 3.7.3 comment — 2023-02-17 — 2026-10-23
- pub — pub.dev API: macos_window_utils 1.9.1 (2026-01-05), window_manager 0.5.2 (2026-07-04), hotkey_manager 0.2.3 (2024-05-18), flutter_acrylic 1.1.4 (2024-06-11), tray_manager 0.8.0 (2026-10-08), floating_palette 0.1.0 (2026-02-23), super_drag_and_drop 0.9.1 (2025-06-11), share_plus 13.3.1 (2026-10-01), flutter_local_notifications 22.3.2 (2026-10-12), native_macos 0.3.0 (2026-10-21), fresnel_macos 0.1.0 (2026-07-31) — https://pub.dev — 2026-10-23
- wails/panel — Wails PR #6008 Add macOS NSPanel window support — https://github.com/wailsapp/wails/pull/6008 — v3.0.0-beta.12 — 2026-08-21 — 2026-10-23
- wails/glass — Wails PR #4534 native Liquid Glass — https://github.com/wailsapp/wails/pull/4534 — v3 alpha — 2025-08-23 — 2026-10-23
- wails/private — Wails guide: Private macOS APIs (docs source) — https://github.com/wailsapp/wails/blob/master/docs/mpress/content/guides/build/private-macos-apis.md — v3 master — 2026-10-23 — 2026-10-23
- wails/spotlight — Wails v3 examples/spotlight README — https://github.com/wailsapp/wails/tree/master/v3/examples/spotlight — v3 master — 2026-10-23 — 2026-10-23
- wails/hotkey — Wails PR #5686 global (system-wide) keyboard shortcuts — https://github.com/wailsapp/wails/pull/5686 — v3 — 2026-06-28 — 2026-10-23
- wails/src — Wails v3 macOS sources (webview_window_darwin.m, application_darwin.go, systemtray_darwin.m, services/notifications) and code search — https://github.com/wailsapp/wails/tree/master/v3/pkg/application — latest v3.0.0-beta.34 (2026-10-20) — 2026-10-23
- wails/6033 — macOS: Fn/Globe key swallowed by WKWebView before reaching IME — https://github.com/wailsapp/wails/issues/6033 — v3 — 2026-08-26 (open) — 2026-10-23
- tnp/104 — tauri-nspanel #104 high window level blocks the input method; fix PR #132 — https://github.com/ahkohd/tauri-nspanel/issues/104 — Wry WKWebView — 2025-10-20 / 2026-09-19 — 2026-10-23
- zed/62661 — macOS: IME composition fails in nonactivating panels when TIS is queried before NSApplication initialization — https://github.com/zed-industries/zed/issues/62661 — GPUI 0.2.2, macOS 26.4.1 — 2026-08-15 (open) — 2026-10-23
- bcd — MDN browser-compat-data (prefers-reduced-transparency, prefers-reduced-motion, prefers-contrast, AccentColor) — https://github.com/mdn/browser-compat-data — 8.1.7 — 2026-10-22 — 2026-10-23
- typometer — Typometer (editor latency by synthetic keys + screen read) — https://pavelfatin.com/typometer/ — n/a — 2015-12-20 — 2026-10-23
- lina/surfaces — LINA docs/design/surfaces.md (App front end, Platform adaptation, Launcher, Korean input) — this repository, dev — 2026-10-23 — 2026-10-23
- lina-app#1, #2 — Flutter Korean input and launcher checks, with owner comments of 10-23 — https://github.com/thisisjun786/lina-app/issues/2 — 2026-10-23 — 2026-10-23
