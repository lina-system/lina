# Automated terminal QA (alpha evidence for #86)

Owner decision (2026-10-24): owner acceptance is deferred to #371 until after
the alpha. This automated terminal QA is the alpha evidence for the terminal
list item of #86. It does not replace the owner-run S1–S11 measurements, the
IME composition check or the final library decision, which stay in #371.

## How it ran

- Build: `go build ./cmd/lina` and `./cmd/lina-node` from `dev` at `d7d8187d`
  (Linux, Go 1.27.3). macOS build from `dev` at `bfbc6cd0` (darwin/arm64).
- Core: a scratch installed Core (`lina install` with a synthetic bundle and a
  fixture service manager, as in `e2e/m1/tui_conversation_test.go`), one fresh
  copy per terminal, with a loopback Responses fixture
  (`LINA_RESPONSES_MODEL=fixture-model`, dummy key). No real account or model.
- Display: a private headless Sway 1.11 session (pixman renderer, Xwayland for
  X11 terminals) on Ubuntu 26.04. Screens were captured with `grim`.
- SSH/tmux: `ssh -V` and `sshd -V` both reported OpenSSH 10.2p1
  Ubuntu-2ubuntu3.8, and `tmux -V` reported tmux 3.6, on that Ubuntu 26.04 host.
  `TERM` was `xterm-256color`.
- Input: `wtype` for Wayland terminals, `xdotool type` for X11 terminals. The
  typed text reaches Core and is checked in the conversation ledger, not only on
  screen.
- Each run: start the TUI with `LANG=ko_KR.UTF-8`, type
  `안녕 리나, 폭 시험 가나다 ABC 🙂`, Enter, wait for the reply (a Korean
  paragraph, a 3-column Hangul/Latin/emoji table, a long Korean list item, bold
  and inline code), `?` shortcut sheet, Esc, Up (input recall), Ctrl+U, resize
  to 640×520 px then 1560×960 px, Ctrl+D twice.
- Font: Sarasa Term K 11 pt where the terminal takes a font option; GNOME
  Terminal uses its default profile.

## Results

Verdicts: works / partial / fails. Only function is judged.

| Terminal | Version | TERM | Korean input | Output width (T-09) | Color | Keys | Resize | Quit |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| foot (Wayland) | 1.25.0 | foot | works | partial | works (truecolor) | works | works | works |
| kitty (Wayland) | 0.45.0 | xterm-kitty | works | partial | works (truecolor) | works | works | works |
| Alacritty (Wayland) | 0.16.1 | alacritty | works | partial | works (truecolor) | works | works | works |
| WezTerm (X11) | 20240203-110809-5046fc22 | xterm-256color | works | partial | works (truecolor) | works | works | works |
| xterm (X11) | 407 | xterm | works | partial | partial (light profile) | works | works | works |
| GNOME Terminal (Wayland) | 3.58.0, VTE 0.84 | xterm-256color | works | partial | partial (light profile) | works | works | works |
| SSH (tmux PTY over `ssh -t`) | OpenSSH 10.2p1 Ubuntu-2ubuntu3.8, tmux 3.6 | xterm-256color | works | partial | n/a (text capture) | works | works | works |
| Apple Terminal (macOS) | 2.15 | — | not run | not run | not run | not run | not run | not run |
| iTerm2, Ghostty (macOS) | not installed | — | — | — | — | — | — | — |

What each verdict covers:

- Korean input: the typed line, including Hangul, Latin and a two-cell emoji,
  is stored by Core exactly as typed in every terminal. This is direct UTF-8
  input, not IME composition; IME preedit and cursor stay in #371 (S1).
- Output width: Hangul and emoji take two cells, the long Korean list item wraps
  at word boundaries with a hanging indent and never splits a syllable, at
  every width tried. **Partial** because a Markdown table in a reply is shown as
  raw pipe text and its columns do not align (#1637).
- Color: the accent bullet and prompt, muted text and bold render on every
  dark-profile terminal. **Partial** on xterm and GNOME Terminal: with a light
  default background and no `COLORFGBG` the dark theme paints only text cells,
  leaving a black-on-white patchwork (#1638).
- Keys: Enter sends, `?` opens the shortcut sheet and Esc closes it, Up recalls
  the sent input, Ctrl+D twice quits with exit 0. The sheet shows the literal
  `한도 %d` (#1636).
- Resize: after shrinking and enlarging the window the transcript re-wraps, the
  composer and footer stay on the last rows and the footer drops lower-priority
  hints at the narrow width. The work-panel heading moves with the width as
  designed.
- macOS: the desk Mac's screen was locked during the run, so no window could be
  driven or captured, and a macOS-built TUI against a Linux Core is refused by
  design (`unsupported_combination`). Apple Terminal stays for #371.

Excluded as test-tool artefacts: with a 60 ms `xdotool` key delay the first
Hangul syllable was sometimes lost in X11 terminals. A raw-byte probe in the
same xterm received every byte, the same text typed after start-up arrived
intact, and with a 180 ms delay all runs stored the full line.

## Files

`<terminal>/typed.png`, `reply.png`, `shortcuts.png`, `resize-small.png`,
`resize-large-later.png` (cropped to the window, 64-colour PNG), and
`ssh-tmux-capture.txt` for the SSH run. All text is synthetic.
