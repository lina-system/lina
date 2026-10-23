// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/worker/recovery"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Exercise #135 with the installed worker and the real model. The task has no
// external effects, so an exact terminal snapshot may settle it or admit the
// single recovery. An unknown result is never counted as successful recovery.
func TestAlphaOwnerWorkerCrash(t *testing.T) {
	h, start := alphaOwnerHarness(t, true)
	workdir := t.TempDir()
	core := start()
	c, conversation := conversationClient(t, h, protocol.Capability{Name: "waiting", Version: "1.0"})
	terminal, _ := h.startConversationTUI(120, 34, "TERM=xterm-256color", "LC_ALL=ko")
	alphaScreen(t, h, terminal, "fresh", func(s string) bool { return strings.Contains(s, "Ctrl+D") }, "")
	input := fmt.Sprintf("Codex 작업자에게 문서 검토를 맡겨 줘. 작업 폴더는 %s야. 주제는 '소규모 텃밭의 물 관리'이고, 서로 다른 상황 40개를 번호 순서대로 각각 세 문장으로 분석하게 해. 파일이나 명령 실행은 필요 없고 도구 없이 답변 텍스트만 작성해. 추가 질문 없이 바로 시작해. 결과는 네가 한국어로 전달해 줘.", workdir)
	h.keys(terminal, "\x1b[200~"+input+"\x1b[201~\r")
	var before recovery.Record
	alphaWaitOwnerRecovery(t, h, func(records []recovery.Record) bool {
		if len(records) != 1 || records[0].State != "running" || records[0].Thread.TurnID == "" {
			return false
		}
		before = records[0]
		return true
	})
	alphaWorkCardScreen(t, h, terminal, design.Korean)
	alphaScreen(t, h, terminal, "worker-active", func(s string) bool { return strings.Contains(s, "연결됨") }, "")
	// Check again after the screen barrier: an already-finished task is not a
	// mid-job crash and cannot establish this scenario's acceptance.
	current := alphaOwnerRecoveryRecords(t, h)
	if len(current) != 1 || current[0].ID != before.ID || current[0].State != "running" || current[0].Thread != before.Thread {
		t.Fatal("worker stopped before the crash barrier")
	}
	core.killAndWait()
	c.Close()
	interrupted := alphaOwnerRecoveryRecords(t, h)
	alphaRecoveryEvidence(t, "before-restart", interrupted)
	if len(interrupted) != 1 || interrupted[0].Thread != before.Thread || interrupted[0].State != "running" {
		t.Fatal("crash did not preserve the acknowledged active attempt")
	}
	core = start()
	defer func() { h.stopCore(core) }()
	var after recovery.Record
	alphaWaitOwnerRecovery(t, h, func(records []recovery.Record) bool {
		if len(records) != 1 {
			t.Fatalf("recovery lost or duplicated the worker: records=%d", len(records))
		}
		after = records[0]
		if after.ID != before.ID || after.Thread.TaskID != before.Thread.TaskID || after.Input != before.Input || after.Retries > 1 {
			t.Fatal("recovery changed task identity or exceeded one continuation")
		}
		return after.ObservedAt > 0 && after.Thread.BindingEpoch > before.Thread.BindingEpoch
	})
	alphaRecoveryEvidence(t, "reconciled", []recovery.Record{after})
	if after.State == "unknown" {
		t.Fatal("real worker snapshot did not settle the interrupted task; no successful recovery observed")
	}
	alphaWaitOwnerRecovery(t, h, func(records []recovery.Record) bool {
		after = records[0]
		return after.State == "finished" || after.State == "decision" || after.State == "parked"
	})
	alphaRecoveryEvidence(t, "terminal", []recovery.Record{after})
	if after.State != "finished" {
		t.Fatalf("real worker did not recover successfully: state=%s retries=%d", after.State, after.Retries)
	}
	alphaWaitOwnerRecovery(t, h, func(_ []recovery.Record) bool { return workerRecoveryReactions(t, h) == 1 })
	reply := alphaOwnerRecoveredReply(t, h, conversation, after)
	alphaScreen(t, h, terminal, "recovered-result", func(s string) bool { return alphaRecoveredReplyVisible(input, reply.Body, s) }, "")
	if directory := os.Getenv("LINA_ALPHA_CAPTURES"); directory != "" {
		raw, err := json.MarshalIndent(reply, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, t.Name(), "recovered-reply.json"), append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	output, sessions := alphaLastWorkerOutput(t, h)
	if strings.TrimSpace(output) == "" || sessions != 1 {
		t.Fatal("recovery omitted the real result or started a second worker session")
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.stopCore(core)
	core = start()
	restored := alphaOwnerRecoveryRecords(t, h)
	alphaRecoveryEvidence(t, "second-restart", restored)
	if len(restored) != 1 || restored[0].Thread != after.Thread || restored[0].Retries != after.Retries || restored[0].State != "finished" {
		t.Fatal("second restart changed the settled attempt")
	}
	if workerRecoveryReactions(t, h) != 1 {
		t.Fatal("second restart duplicated result delivery")
	}
	if restoredReply := alphaOwnerRecoveredReply(t, h, conversation, after); restoredReply != reply {
		t.Fatal("second restart changed the delivered result")
	}
	terminal = h.startTerminalSize([]string{"TERM=xterm-256color", "LC_ALL=ko"}, false, true, 120, 34)
	alphaScreen(t, h, terminal, "recovery-restored", func(s string) bool { return alphaRecoveredReplyVisible(input, reply.Body, s) }, "")
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
}

func alphaOwnerRecoveredReply(t *testing.T, h *harness, conversation string, attempt recovery.Record) alphaRecoveryReply {
	t.Helper()
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	reply, err := alphaRecoveredReply(h.ctx, db, conversation, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if reply.ID == "" || strings.TrimSpace(reply.Body) == "" {
		t.Fatal("recovered result has no completed reply in the originating conversation")
	}
	return reply
}

func alphaOwnerRecoveryRecords(t *testing.T, h *harness) []recovery.Record {
	t.Helper()
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	records, err := (recovery.Recovery{DB: db}).Records(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	return records
}

func alphaWaitOwnerRecovery(t *testing.T, h *harness, ready func([]recovery.Record) bool) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ready(alphaOwnerRecoveryRecords(t, h)) {
			return
		}
		select {
		case <-h.ctx.Done():
			alphaRecoveryEvidence(t, "deadline", alphaOwnerRecoveryRecords(t, h))
			t.Fatal("real worker recovery deadline", h.ctx.Err())
		case <-ticker.C:
		}
	}
}

func alphaRecoveryEvidence(t *testing.T, stage string, records []recovery.Record) {
	t.Helper()
	// Store execution identities and observations, never the task packet or
	// worker's account configuration.
	observations := []map[string]any{}
	for _, r := range records {
		observations = append(observations, map[string]any{"thread": r.Thread, "state": r.State, "retries": r.Retries, "observed_at": r.ObservedAt, "finish": r.Finish})
	}
	raw, err := json.MarshalIndent(observations, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: %s", stage, raw)
	if directory := os.Getenv("LINA_ALPHA_CAPTURES"); directory != "" {
		if err := os.WriteFile(filepath.Join(directory, t.Name(), stage+".json"), append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
