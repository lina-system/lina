// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/install"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
)

// The CLI and handshake deliberately report different versions, so a passing
// dispatch cannot hide a missing CLI probe behind initialize metadata.
func TestGrokWorkerThroughRunningCore(t *testing.T) {
	for _, mode := range []string{"success", "needs-sign-in"} {
		t.Run(mode, func(t *testing.T) {
			runACPWorkersThroughRunningCore(t, []acpScenario{{
				name: "grok", worker: "grok", binary: "grok", mode: mode,
				script: grokAgentScript(), afterFinish: verifyGrokObservation,
			}})
		})
	}
}

func verifyGrokObservation(t *testing.T, h *harness, fixture string, record dispatch.Record) {
	t.Helper()
	trace, err := os.ReadFile(filepath.Join(fixture, "trace"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"grok --version\n", "grok --no-auto-update agent stdio\n", "session/new\n"} {
		if !strings.Contains(string(trace), want) {
			t.Fatalf("missing Grok invocation %q: %s", want, trace)
		}
	}
	if strings.Contains(string(trace), "--always-approve") {
		t.Fatal("Grok bypassed approvals")
	}
	var inventory install.Result
	readACPFixture(t, filepath.Join(h.root, "system"), "install.json", &inventory)
	found := false
	for _, tool := range inventory.Tools {
		if tool.Name == "grok" {
			found = tool.Version == "fixture-unverified-9" && tool.Status == "unverified"
		}
	}
	if !found || record.Worker != "grok" || record.Version != "fixture-unverified-9" {
		t.Fatal("Grok CLI version missing from installation or readiness", record.Version)
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-grok-") || strings.Contains(string(raw), "_meta") {
		t.Fatal("private handshake or account metadata reached the dispatch record")
	}
	if record.Code == "needs_sign_in" {
		want := []dispatch.ReadinessFailure{{Worker: "grok", Version: "fixture-unverified-9", Code: "needs_sign_in"}}
		if !reflect.DeepEqual(record.ReadinessFailures, want) || strings.Contains(string(trace), "session/prompt\n") {
			t.Fatal("Grok sign-in refusal was not preserved before prompting", record.ReadinessFailures)
		}
		return
	}
	var result struct {
		Handshake map[string]json.RawMessage `json:"handshake"`
	}
	if err := json.Unmarshal(record.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Handshake) != 2 || string(result.Handshake["version"]) != `"handshake-fixture-759"` {
		t.Fatalf("Grok handshake must record only version and capabilities: %s", record.Result)
	}
	var capabilities []string
	if err := json.Unmarshal(result.Handshake["capabilities"], &capabilities); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(capabilities, []string{"session/load", "session/list", "session/resume", "session/close"}) {
		t.Fatal("advertised Grok capabilities lost", capabilities)
	}
}

func grokAgentScript() string {
	// Reuse the committed-file/PR fixture and its exact argument check. Only
	// Grok's measured initialize and unauthenticated session responses differ.
	script := strings.Replace(acpAgentScript, "if (process.argv[2]==='--version')", "fs.appendFileSync(path.join(root,'trace'),'grok '+process.argv.slice(2).join(' ')+'\\n');\nif (process.argv[2]==='--version')", 1)
	script = strings.Replace(script, "const m=JSON.parse(line);", "const m=JSON.parse(line);fs.appendFileSync(path.join(root,'trace'),m.method+'\\n');", 1)
	script = strings.Replace(script, "reply(m.id,{protocolVersion:1,...(worker==='cursor'||worker==='grok'?{}:{agentInfo:{name:'fixture',version:worker==='claude-acp'?'adapter-fixture-1':'fixture-unverified-9'}})});", `reply(m.id,{protocolVersion:1,_meta:{agentVersion:'handshake-fixture-759',host:'private-grok-host',cwd:'private-grok-path',extensions:{hooks:'private-grok-hook'}},authMethods:[{id:'private-grok-auth',name:'private-grok-account'}],agentCapabilities:{loadSession:true,_meta:{host:'private-grok-capability'},sessionCapabilities:{list:{_meta:{host:'private-grok-list'}},resume:{},close:{},_meta:{host:'private-grok-session'}}}});`, 1)
	script = strings.Replace(script, "case 'session/new':", `case 'session/new':
  if(process.env.LINA_ACP_CASE==='needs-sign-in') {
   send({id:m.id,error:{code:-32000,message:'private-grok-account',data:{host:'private-grok-error'}}});break;
  }`, 1)
	return script
}
