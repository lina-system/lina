# Connection surface renders

These PNGs come from the built `lina tui` binary through a Linux pseudo-terminal.
The fixture peer answers its real client handshake with the repository's welcome
or refusal envelope. The unavailable case uses an absent socket; the failed case
sends malformed JSON. The capture observes the complete frame before responding
or sending `q` followed by Enter. There are no sleeps or synthetic screen strings.

[Capture metadata](capture.json) records the binary digest, renderer version,
terminal dimensions, display environment and observed exit codes. The images
cover connecting, connected, quit, refusal, unavailable Core and failure in both
light and dark. Additional captures cover 16 colours, 256 colours, `NO_COLOR`,
ASCII glyphs, high contrast and a narrow 32-column terminal.

| Dark | Light |
| --- | --- |
| ![Connected, dark](dark-connected.png) | ![Connected, light](light-connected.png) |
| ![Connecting, dark](dark-connecting.png) | ![Connecting, light](light-connecting.png) |
| ![Quit, dark](dark-quit.png) | ![Quit, light](light-quit.png) |
| ![Refused, dark](dark-refused.png) | ![Refused, light](light-refused.png) |
| ![Unavailable, dark](dark-stopped.png) | ![Unavailable, light](light-stopped.png) |
| ![Failed, dark](dark-failed.png) | ![Failed, light](light-failed.png) |

[16-colour ASCII](dark-16-72-connected.png) ·
[256-colour ASCII](dark-256-72-connected.png) ·
[NO_COLOR ASCII](dark-none-72-connected.png) ·
[High contrast](high-contrast-connected.png) ·
[Narrow light ASCII](light-truecolor-32-connected.png)

## Reproduce

Run from the repository root on Linux, with Python 3 and Go. Freeze is a temporary
tool; it is not a module dependency. The script closes its sockets and PTYs, joins
its child processes and removes intermediate ANSI captures.

```sh
brand_tools=$(mktemp -d)
GOBIN="$brand_tools" go install github.com/charmbracelet/freeze@v0.2.2
go build -mod=readonly -buildvcs=false -o "$brand_tools/lina" ./cmd/lina
python3 internal/tui/ui/testdata/capture_brand.py \
  --binary "$brand_tools/lina" --freeze "$brand_tools/freeze" \
  --output evidence/brand
rm -rf "$brand_tools"
```

Freeze rasterizes SGR styling but does not emulate cursor-up/erase. The capture
therefore selects the last complete draw after the binary's erase marker, keeping
every content, spacing, glyph and colour byte in that draw. Monochrome output has
no escapes and is rendered as its actual compact transcript. Failure captures
include the command's existing diagnostic on standard error. The light renderer
uses the GitHub light default palette so unstyled diagnostics have a light-terminal
foreground; dark uses Freeze's default. Freeze does not reproduce bold font weight.

Visual iteration corrected trailing cells that leaked the terminal background,
aligned wrapped supporting text with the state label, reduced the gap below the
wordmark and made the canonical-mode quit hint explicit. Cobalt stays confined to
the wordmark dot and the active action; state marks stay neutral. The resulting
screens have one reading edge and a single hairline, with no box around content.

This is deterministic connection-surface evidence. It does not establish live
conversation rendering, the Bubble Tea spike, physical terminal palette colours,
IME behaviour or Omarchy preference discovery.
