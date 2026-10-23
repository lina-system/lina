// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

// A live command is reviewed by the operator before the test presses the real
// approval form. It never accepts worker-generated commands on its own.
func alphaLiveApproval(t *testing.T, h *harness, terminal *terminal, approval protocol.ApprovalRequest) {
	t.Helper()
	directory := os.Getenv("LINA_ALPHA_APPROVALS")
	if !filepath.IsAbs(directory) || approval.Card == nil {
		t.Fatal("live approval needs an operator directory")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(approval.Card.ID+"\x00"+approval.Card.Revision)))[:16]
	path := filepath.Join(directory, key)
	if err := os.WriteFile(path+".json", mustJSON(t, approval), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("operator approval pending:", key)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		answer, err := os.ReadFile(path + ".answer")
		if err == nil {
			if strings.TrimSpace(string(answer)) != "once" {
				t.Fatal("operator declined the live command")
			}
			break
		}
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		select {
		case <-ticker.C:
		case <-h.ctx.Done():
			t.Fatal("operator approval was not supplied")
		}
	}
	alphaScreen(t, h, terminal, "approval-"+key, func(s string) bool { return strings.Contains(s, approval.Card.Summary) }, "")
	h.keys(terminal, "\x1b[17~")
	confirm := design.Text(design.Korean, "card.confirm")
	cut := design.Text(design.Korean, "card.targets_cut")
	page := alphaScreen(t, h, terminal, "approval-page-"+key+"-000", func(s string) bool { return strings.Contains(s, confirm) || strings.Contains(s, cut) }, "")
	for number := 1; !strings.Contains(screenText(page), confirm); number++ {
		previous := screenText(page)
		h.keys(terminal, "\x1b[6~")
		page = alphaScreen(t, h, terminal, fmt.Sprintf("approval-page-%s-%03d", key, number), func(s string) bool {
			return s != previous && (strings.Contains(s, confirm) || strings.Contains(s, cut))
		}, "")
	}
	choice := 0
	for i, c := range approval.Choices {
		if c == protocol.ApprovalChoiceOnce {
			choice = i + 1
		}
	}
	if choice == 0 || choice > 3 {
		t.Fatal("one-time approval not offered")
	}
	h.keys(terminal, strconv.Itoa(choice))
	alphaScreen(t, h, terminal, "approval-choice-"+key, func(s string) bool { return strings.Contains(s, "["+strconv.Itoa(choice)+" ") }, "")
	h.keys(terminal, "\r")
	alphaScreen(t, h, terminal, "approval-delivered-"+key, func(s string) bool { return strings.Contains(s, design.Text(design.Korean, "resolved")) }, "")
	h.keys(terminal, "\x1b[27u")
}
