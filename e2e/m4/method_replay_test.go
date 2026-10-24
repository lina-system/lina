// SPDX-License-Identifier: Apache-2.0
package m4_test

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/thisisjun786/lina/persona"
)

// buildLina compiles the command under test once per scenario run. The harness
// runs the installed entry point, not an in-process copy, so the measured path
// is the one the owner uses.
func buildLina(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "lina")
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	build := exec.CommandContext(t.Context(), "go", "build", "-mod=readonly", "-o", binary, "./cmd/lina")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build lina: %v\n%s", err, out)
	}
	return binary
}

// fixtureReply is what the fixture model answers. The tailored dev record
// carries a workers block, so the fixture can answer differently for the two
// variants without reading case text.
const (
	baselineReply = "상태를 확인하는 중입니다. 진행 상황을 정리하면 배포 단계와 검증 단계가 남아 있고, 각 단계의 세부 내용을 하나씩 설명드리겠습니다. 먼저 배포 단계에서는 이전 커밋이 반영되었는지 확인해야 합니다. 다음으로 검증 단계에서는 실제 실행 화면을 열어 결과를 비교해야 합니다. 이 작업은 되돌릴 수 있으니 제가 진행하겠습니다."
	tailoredReply = "배포를 마쳤습니다. 실행 중인 화면에서 값을 다시 읽어 확인했습니다."
	// privateMarker appears only in the private case text and replies; a report
	// that repeats it would leak replay data.
	privateMarker = "PRIVATE-REPLAY-MARKER"
	// baselineMarker marks the snapshot's dev core layer, so the fixture model
	// can answer differently for the two variants without reading case text.
	baselineMarker = "UNTAILORED-LAYER-MARKER"
)

func methodFixture(t *testing.T) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Instructions string `json:"instructions"`
			Input        []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("fixture request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if strings.Contains(body.Instructions, baselineMarker) {
			mu.Lock()
			requests++
			mu.Unlock()
			answerText(w, baselineReply)
			return
		}
		answerText(w, tailoredReply)
	}))
	t.Cleanup(server.Close)
	return server
}

// methodBaseline writes an untailored triple for every starter and marks the dev
// core layer. The shipped triple is the other side of the comparison, so the
// measured difference is real preset data rather than a fixture shortcut.
func methodBaseline(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, starter := range persona.Starters() {
		if err := os.MkdirAll(filepath.Join(dir, starter.Key), 0700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"core-v1.md", "default-voice-v1.md", "work-method-v1.json"} {
			data, err := fs.ReadFile(persona.Shipped(), starter.Key+"/"+name)
			if err != nil {
				t.Fatal(err)
			}
			if starter.Key == "dev" && name == "core-v1.md" {
				data = append([]byte(baselineMarker+"\n"), data...)
			}
			if err := os.WriteFile(filepath.Join(dir, starter.Key, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return dir
}

func answerText(w http.ResponseWriter, text string) {
	item, _ := json.Marshal(map[string]any{"type": "message", "role": "assistant",
		"content": []map[string]string{{"type": "output_text", "text": text}}})
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":%q}\n\n", text)
	fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\n", item)
	fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
}

func methodCases(t *testing.T) string {
	t.Helper()
	lines := []string{
		`{"version":1,"id":"dev-001","role":"dev","reply_type":"status","kind":"correction","correction":"length","origin":"native","checks":["length","done_evidence"],"budget":60,"language":"ko","reversible":true,"turns":[{"role":"user","text":"` + privateMarker + ` 배포 상태만 알려줘"}]}`,
		`{"version":1,"id":"dev-002","role":"dev","reply_type":"status","kind":"accepted","origin":"native","checks":["no_internal"],"language":"ko","must_not_change":true,"turns":[{"role":"user","text":"` + privateMarker + ` 고친 부분 실행해서 보여줘"}]}`,
		`{"version":1,"id":"life-001","role":"life","reply_type":"comparison","kind":"accepted","origin":"native","checks":["no_internal"],"language":"ko","must_not_change":true,"turns":[{"role":"user","text":"` + privateMarker + ` 두 제품 중에 뭐가 나아?"}]}`,
	}
	path := filepath.Join(t.TempDir(), "cases.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestMethodReplayThroughTheCommand measures one preset change through the
// installed command: with a snapshot baseline the dev agent improves, the life
// agent holds, and the report carries scores and ids only.
func TestMethodReplayThroughTheCommand(t *testing.T) {
	server := methodFixture(t)
	cases := methodCases(t)
	binary := buildLina(t)
	baseline := methodBaseline(t)
	run := func(changed string, wantExit int) map[string]any {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), binary, "method-replay", cases, "--changed", changed, "--baseline", baseline, "--repeat", "3")
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir(),
			"LINA_RESPONSES_BASE_URL=" + server.URL + "/v1", "LINA_RESPONSES_MODEL=fixture-model",
			"LINA_RESPONSES_API_KEY=synthetic",
		}
		out, err := cmd.Output()
		if wantExit == 0 && err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				t.Logf("stderr: %s", exit.Stderr)
			}
			t.Fatalf("method-replay: %v %s", err, out)
		}
		var report map[string]any
		if err := json.Unmarshal(out, &report); err != nil {
			t.Fatalf("output: %s: %v", out, err)
		}
		if strings.Contains(string(out), privateMarker) {
			t.Fatalf("report leaked replay text: %s", out)
		}
		return report
	}
	report := run("dev", 0)
	if report["scored"] != float64(3) || report["excluded"] != float64(0) {
		t.Fatalf("report: %v", report)
	}
	if report["baseline"] != "snapshot" {
		t.Fatalf("baseline: %v", report["baseline"])
	}
	agents := report["agents"].([]any)
	verdicts := map[string]string{}
	for _, raw := range agents {
		a := raw.(map[string]any)
		verdicts[a["agent"].(string)] = a["verdict"].(string)
	}
	if verdicts["dev"] != "improved" || verdicts["life"] != "unchanged" {
		t.Fatalf("verdicts: %v", verdicts)
	}
	ship := report["ship"].(map[string]any)
	if ship["ships"] != true {
		t.Fatalf("ship: %v", ship)
	}
	// Measuring an agent that did not change must not report a shipping change.
	held := run("life", 0)
	if held["ship"].(map[string]any)["ships"] != false {
		t.Fatalf("unchanged agent shipped: %v", held["ship"])
	}
}

// A case line the reader refuses is excluded, never scored, and the command
// reports it by line and reason.
func TestMethodReplayRefusesAnInvalidCase(t *testing.T) {
	server := methodFixture(t)
	path := filepath.Join(t.TempDir(), "cases.jsonl")
	invalid := `{"version":1,"id":"dev-001","role":"dev","reply_type":"status","kind":"correction","checks":["tone"],"turns":[{"role":"user","text":"상태"}]}`
	if err := os.WriteFile(path, []byte(invalid+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := buildLina(t)
	cmd := exec.CommandContext(t.Context(), binary, "method-replay", path, "--changed", "dev")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir(),
		"LINA_RESPONSES_BASE_URL=" + server.URL + "/v1", "LINA_RESPONSES_MODEL=fixture-model",
		"LINA_RESPONSES_API_KEY=synthetic",
	}
	out, err := cmd.Output()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 1 {
		t.Fatalf("invalid cases scored: %v %s", err, out)
	}
	var report struct {
		Scored   int `json:"scored"`
		Rejected []struct {
			Line   int    `json:"line"`
			Reason string `json:"reason"`
		} `json:"rejected"`
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatal(err)
	}
	if report.Scored != 0 || len(report.Rejected) != 1 || report.Rejected[0].Reason != "invalid_case" {
		t.Fatalf("report: %s", out)
	}
}
