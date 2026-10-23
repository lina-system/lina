// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestDelegationReadinessReleasesActivity(t *testing.T) {
	for _, failure := range []string{"missing", "handshake", "quota", "quota_hang"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "system/core"), 0700); err != nil {
				t.Fatal(err)
			}
			binary := filepath.Join(root, "worker")
			if failure != "missing" {
				node, err := exec.LookPath("node")
				if err != nil {
					t.Fatal(err)
				}
				program := filepath.Join(root, "worker.mjs")
				source := `// SPDX-License-Identifier: Apache-2.0
import {writeFileSync} from 'node:fs';
import {createInterface} from 'node:readline';
const [root, fail, mode] = process.argv.slice(2);
if (mode === '--version') console.log('codex-cli 99.0.0');
else {
  writeFileSync(root + '/worker.pid', String(process.pid));
  createInterface({input:process.stdin}).on('line', line => {
    const {id,method} = JSON.parse(line);
    if (method === 'initialized' || fail === 'quota_hang' && method === 'account/rateLimits/read') return;
    if (method === 'account/rateLimits/read' || fail === 'handshake')
      console.log(JSON.stringify({id,error:{code:-32000,message:'synthetic failure'}}));
    else console.log(JSON.stringify({id,result:{userAgent:'fixture'}}));
  });
}
`
				if err := os.WriteFile(program, []byte(source), 0600); err != nil {
					t.Fatal(err)
				}
				quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
				wrapper := fmt.Sprintf("#!/bin/sh\n# SPDX-License-Identifier: Apache-2.0\nexec %s %s %s %s \"$@\"\n", quote(node), quote(program), quote(root), quote(failure))
				if err := os.WriteFile(binary, []byte(wrapper), 0700); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := json.Marshal(delegationConfig{Codex: binary, Git: "/unused/git", GH: "/unused/gh", Slots: 1})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "system/core/delegation.json"), cfg, 0600); err != nil {
				t.Fatal(err)
			}
			registry := lifecycle.NewRegistry(nil)
			r := &conversationRuntime{root: root, activities: registry}
			err = r.startDelegation(t.Context())
			var failureReport *delegationReadinessError
			if !errors.As(err, &failureReport) || errors.Unwrap(failureReport) == nil {
				t.Fatalf("missing readiness cause: %v", err)
			}
			want := "delegation.worker"
			if strings.HasPrefix(failure, "quota") {
				want = "delegation.quota"
			}
			if failureReport.step != want {
				t.Fatalf("readiness step=%q, want %q", failureReport.step, want)
			}
			if r.delegation != nil {
				t.Fatal("failed delegation was published")
			}
			if report := registry.Idle(t.Context()); report.Busy() || len(report.Reported) != 0 || len(report.Blocking) != 0 {
				t.Fatalf("failed initialization retained activity: %+v", report)
			}
			if failure != "missing" {
				raw, err := os.ReadFile(filepath.Join(root, "worker.pid"))
				if err != nil {
					t.Fatal(err)
				}
				pid, err := strconv.Atoi(string(raw))
				if err != nil || pid <= 0 {
					t.Fatalf("invalid worker PID: %q %v", raw, err)
				}
				if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
					t.Fatalf("failed worker still exists: %v", err)
				}
			}
		})
	}
}

func TestDelegationReadinessStream(t *testing.T) {
	topics, err := clientsrv.NewTopics("1", clientsrv.TopicOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer topics.Close()
	_, live, err := topics.Subscribe("core", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	data, err := json.Marshal(protocol.ReadinessFailure{Step: "delegation.quota"})
	if err != nil {
		t.Fatal(err)
	}
	if err := topics.Publish("core", clientsrv.TopicEvent{Kind: protocol.KindReadinessFailure, Key: "delegation", Data: data}); err != nil {
		t.Fatalf("readiness failure cannot reach subscribed clients: %v", err)
	}
	frame, err := live.Next(t.Context())
	if err != nil || frame.Event.Kind != protocol.KindReadinessFailure {
		t.Fatalf("no live readiness: %+v %v", frame, err)
	}
	start, late, err := topics.Subscribe("core", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer late.Close()
	if start.Snapshot == nil || len(start.Snapshot.Items) != 1 || start.Snapshot.Items[0].Kind != protocol.KindReadinessFailure {
		t.Fatalf("no retained readiness: %+v", start.Snapshot)
	}
}
