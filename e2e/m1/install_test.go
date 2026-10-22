// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/install"
	"github.com/thisisjun786/lina/internal/core/manifest"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Only systemd and the optional installed tool are fixtures. Installation,
// artifact verification, Core, the client protocol and the TUI are real.
func TestM1Install(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("Linux Core installation")
	}
	data, err := os.ReadFile("install.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		ConnectionState string                `json:"connection_state"`
		RefusalExit     int                   `json:"refusal_exit"`
		ToolName        string                `json:"tool_name"`
		Tools           []installToolScenario `json:"tools"`
	}
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	bundle := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "node", "build-install-bundle.mjs", bundle)
	for _, key := range []string{"PATH", "HOME", "GOCACHE", "GOMODCACHE", "TMPDIR"} {
		if value, ok := os.LookupEnv(key); ok {
			build.Env = append(build.Env, key+"="+value)
		}
	}
	if data, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build install bundle: %v\n%s", err, data)
	}
	m, err := manifest.Read(filepath.Join(bundle, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(m.VerifiedWorkerVersions[scenario.ToolName], "0.163.0") {
		t.Fatal("built manifest must verify Codex 0.163.0")
	}
	for _, tool := range scenario.Tools {
		t.Run(tool.Version, func(t *testing.T) {
			runInstallScenario(t, bundle, scenario.ConnectionState, scenario.RefusalExit, scenario.ToolName, tool, m.VerifiedWorkerVersions[scenario.ToolName])
		})
	}
}

type installToolScenario struct {
	Version string `json:"version"`
	Status  string `json:"status"`
}

func runInstallScenario(t *testing.T, bundle, connectionState string, refusalExit int, toolName string, toolScenario installToolScenario, verified []string) {
	t.Helper()
	h := newHarness(t, filepath.Join(bundle, "lina"))
	base := h.root
	var data []byte
	var err error
	bin := filepath.Join(base, "tools")
	if err = os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	systemctl := `#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
case "$2" in
  show) printf 'not-found\n';;
  daemon-reload|enable) printf '%s\n' "$*" >> "$HOME/service-calls";;
  *) exit 97;;
esac
`
	if err = os.WriteFile(filepath.Join(bin, "systemctl"), []byte(systemctl), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(bin, toolName), []byte("#!/bin/sh\n# SPDX-License-Identifier: Apache-2.0\nprintf '%s\\n' "+strconv.Quote(toolScenario.Version)+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(base, "state")
	units := filepath.Join(base, "units")
	args := []string{"install", "--state-root", state, "--unit-dir", units, "--bundle", bundle}
	invoke := func(code int) string {
		p := h.command(args...)
		for i, entry := range p.command.Env {
			if strings.HasPrefix(entry, "PATH=") {
				p.command.Env[i] = "PATH=" + bin
			}
		}
		p.start(t)
		p.wait(t, code)
		return p.stdout.String()
	}
	output := invoke(0)
	if !strings.Contains(output, toolName+": "+toolScenario.Version+" ("+toolScenario.Status) {
		t.Fatalf("install did not report tool version/status: %s", output)
	}
	var installed install.Result
	data, err = os.ReadFile(filepath.Join(state, "system/install.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &installed); err != nil {
		t.Fatal(err)
	}
	if installed.MainID == "" {
		t.Fatal("no installed identity")
	}
	found := false
	for _, tool := range installed.Tools {
		if tool.Name == toolName {
			found = tool.Version == toolScenario.Version && tool.Status == toolScenario.Status && slices.Equal(tool.VerifiedVersions, verified)
		}
	}
	if !found {
		t.Fatalf("tool inventory: %+v", installed.Tools)
	}
	data, err = os.ReadFile(filepath.Join(state, "system/tools.json"))
	if err != nil {
		t.Fatal(err)
	}
	var inventory []install.Tool
	if err := json.Unmarshal(data, &inventory); err != nil {
		t.Fatal(err)
	}
	if !slices.EqualFunc(inventory, installed.Tools, func(a, b install.Tool) bool {
		return a.Name == b.Name && a.Path == b.Path && a.Version == b.Version && a.Status == b.Status && slices.Equal(a.VerifiedVersions, b.VerifiedVersions)
	}) {
		t.Fatalf("persisted tools differ from install result: %+v", inventory)
	}
	credentials, err := os.ReadDir(filepath.Join(state, "secrets"))
	if err != nil || len(credentials) != 0 {
		t.Fatal("install required or created an account")
	}
	before, err := os.ReadFile(filepath.Join(state, "system/core/main.json"))
	if err != nil {
		t.Fatal(err)
	}
	invoke(refusalExit)
	after, err := os.ReadFile(filepath.Join(state, "system/core/main.json"))
	if err != nil || string(before) != string(after) {
		t.Fatal("repeat installation changed identity")
	}
	// Launch the actual installed unit's argv, with the harness owning process
	// cleanup. A stub service manager never stands in for Core readiness.
	unit, err := os.ReadFile(filepath.Join(units, "lina-core.service"))
	if err != nil {
		t.Fatal(err)
	}
	var argv []string
	for _, line := range strings.Split(string(unit), "\n") {
		if command, ok := strings.CutPrefix(line, "ExecStart="); ok {
			for _, quoted := range regexp.MustCompile(`"(?:[^"\\]|\\.)*"`).FindAllString(command, -1) {
				arg, err := strconv.Unquote(quoted)
				if err != nil {
					t.Fatal(err)
				}
				argv = append(argv, strings.NewReplacer("%%", "%", "$$", "$").Replace(arg))
			}
		}
	}
	if len(argv) != 6 {
		t.Fatalf("invalid installed service argv: %v", argv)
	}
	h.binary, h.root, h.socket = argv[0], state, installed.Socket
	core := h.command(argv[1:]...)
	core.start(t)
	conn := h.connect(core)
	client := &client{conn: conn, reader: bufio.NewReaderSize(conn, 1<<20)}
	answer := client.call(t, protocol.KindHello, clientHello([]string{"1.0"}))
	welcome, ok := answer.Payload.(protocol.Welcome)
	if !ok || welcome.MainID != installed.MainID || welcome.Epoch == "0" || answer.Error != nil || answer.Epoch != welcome.Epoch {
		t.Fatalf("installed Core handshake: %+v", answer)
	}
	client.welcome = welcome
	h.inputRefused(client)
	term := h.startTUI("TERM=xterm-256color", "NO_COLOR=1", "LC_ALL=C")
	expectCatalogState(t, h, term, design.English, connectionState)
	h.keys(term, "\x04")
	term.process.wait(t, 0)
	h.inputRefused(client)
	h.stopCore(core)
}

func makeInstallBundle(t *testing.T, base, lina, node string) string {
	t.Helper()
	bundle := filepath.Join(base, "bundle")
	if err := os.Mkdir(bundle, 0700); err != nil {
		t.Fatal(err)
	}
	binaries := []map[string]any{}
	for name, source := range map[string]string{"lina": lina, "lina-node": node} {
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(bundle, name), data, 0700); err != nil {
			t.Fatal(err)
		}
		binaries = append(binaries, map[string]any{"name": name, "size": len(data), "sha256": fmt.Sprintf("%x", sha256.Sum256(data))})
	}
	// Synthetic provenance, exact hashes of the artifacts built by this scenario.
	m := map[string]any{"version": "0.0.1-alpha.dev", "commit": strings.Repeat("a", 40), "date": "2026-10-20", "goToolchain": "go1.27.3", "kitVersion": 1,
		"supportedCombinations": manifest.ClientCombinations(),
		"target":                runtime.GOOS + "/" + runtime.GOARCH,
		"providers":             map[string]any{"chatgpt": map[string]any{"adapter": "responses", "retryCount": 0, "requestFields": []string{"model", "input", "store", "stream"}}},
		"sqlite":                map[string]any{"module": "github.com/ncruces/go-sqlite3", "version": "v0.30.5", "moduleSum": "h1:6usmTQ6khriL8oWilkAZSJM/AIpAlVL2zFrlcpDldCE=", "sqliteVersion": "3.51.2", "wasmSHA256": strings.Repeat("d", 64), "execution": "wazero", "sqliteVec": nil}, "binaries": binaries}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(bundle, "manifest.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return bundle
}
