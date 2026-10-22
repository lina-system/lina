// SPDX-License-Identifier: Apache-2.0

module github.com/thisisjun786/lina

go 1.27

toolchain go1.27.3

replace github.com/thisisjun786/lina/kit => ./kit

require (
	charm.land/bubbles/v2 v2.2.1
	charm.land/bubbletea/v2 v2.1.0
	charm.land/glamour/v2 v2.0.1
	charm.land/lipgloss/v2 v2.0.6
	github.com/PaulSonOfLars/gotgbot/v2 v2.0.0-rc.37
	github.com/alecthomas/chroma/v2 v2.14.0
	github.com/charmbracelet/colorprofile v0.4.3
	github.com/charmbracelet/ultraviolet v0.0.0-20261008173134-6b8d4baf91b4
	github.com/charmbracelet/x/ansi v0.11.9
	github.com/charmbracelet/x/term v0.2.2
	github.com/charmbracelet/x/vt v0.1.0
	github.com/ncruces/go-sqlite3 v0.30.5
	github.com/thisisjun786/lina/kit v0.0.0
	github.com/yuin/goldmark v1.7.17
	golang.org/x/sys v0.49.0
)

require (
	github.com/atotto/clipboard v0.1.4 // indirect
	github.com/aymerick/douceur v0.2.0 // indirect
	github.com/charmbracelet/x/exp/ordered v0.1.0 // indirect
	github.com/charmbracelet/x/exp/slice v0.0.0-20250327172914-2fdc97757edf // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/dlclark/regexp2 v1.11.0 // indirect
	github.com/go-logr/logr v1.2.0 // indirect
	github.com/golang/groupcache v0.0.0-20210331224755-41bb18bfe9da // indirect
	github.com/google/go-licenses/v2 v2.0.1 // indirect
	github.com/google/licenseclassifier/v2 v2.0.0 // indirect
	github.com/gorilla/css v1.0.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/mattn/go-runewidth v0.0.30 // indirect
	github.com/microcosm-cc/bluemonday v1.0.27 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/ncruces/julianday v1.0.0 // indirect
	github.com/nishanths/exhaustive v0.15.1 // indirect
	github.com/otiai10/copy v1.10.0 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/sergi/go-diff v1.2.0 // indirect
	github.com/spf13/cobra v1.7.0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	github.com/tetratelabs/wazero v1.11.0 // indirect
	github.com/xo/terminfo v1.0.0 // indirect
	github.com/yuin/goldmark-emoji v1.0.5 // indirect
	go.opencensus.io v0.24.0 // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/telemetry v0.0.0-20261010193011-f71dc92da45e // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/tools v0.52.0 // indirect
	golang.org/x/vuln v1.9.0 // indirect
	k8s.io/klog/v2 v2.90.1 // indirect
)

tool (
	github.com/google/go-licenses/v2
	github.com/nishanths/exhaustive/cmd/exhaustive
	golang.org/x/vuln/cmd/govulncheck
)
