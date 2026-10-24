// SPDX-License-Identifier: Apache-2.0
package mocks

import (
	"context"
	"encoding/json"
	generatedui "github.com/thisisjun786/lina/design"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/result"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "proc-supervise" {
		os.Exit(proc.SuperviseMain(os.Args[2:], os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "proc-job" {
		os.Exit(proc.JobMain(os.Args[2:], os.Stderr))
	}
	os.Exit(m.Run())
}

type testLeaser struct{}

func (testLeaser) Acquire(context.Context, lease.Activity) (lease.Lease, error) {
	return lease.New(func() {}), nil
}
func TestChromiumSelectsCheckedRadio(t *testing.T) {
	c := Chromium{Leaser: testLeaser{}}
	fragment := `<main class="stack"><label class="row"><input type="radio" checked="checked" data-check="morning" data-action="select" aria-label="Morning"/>Morning</label><label class="row"><input type="radio" data-check="afternoon" data-action="select" aria-label="Afternoon"/>Afternoon</label><label class="row"><input type="checkbox" checked="checked" data-check="done" data-action="select" aria-label="Done"/>Done</label></main>`
	items := []result.MockItem{
		{ID: "morning", Description: "Morning", Action: "select"},
		{ID: "afternoon", Description: "Afternoon", Action: "select"},
		{ID: "done", Description: "Done", Action: "select"},
	}
	out, err := c.Draw(t.Context(), fragment, items)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range out.Checks {
		if !check.Passed {
			t.Errorf("check failed: %s", check.ID)
		}
	}
}
func TestChromiumDrawsCheckedMock(t *testing.T) {
	c := Chromium{Leaser: testLeaser{}}
	out, err := c.Draw(t.Context(), `<main class="stack"><h1 data-check="title">Tasks</h1><button data-check="filter" data-action="toggle" data-target="tasks">Today</button><section id="tasks" class="card">One task</section></main>`, []result.MockItem{{ID: "title", Description: "Tasks", Action: "none"}, {ID: "filter", Description: "Today", Action: "toggle", Target: "tasks"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range out.Checks {
		if !check.Passed {
			t.Errorf("check failed: %s", check.ID)
		}
	}
	if len(out.PNG) < 8 || string(out.PNG[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatal("no PNG")
	}
}

func TestMockFrameIsolationWithNegativeControls(t *testing.T) {
	var hits atomic.Int32
	sentinel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(204)
	}))
	defer sentinel.Close()
	c := Chromium{Leaser: testLeaser{}}
	page := generatedui.MockPage("Mock", `<main>Static content</main>`)
	err := c.withBrowser(t.Context(), func(b *browser) error {
		probe := func(frame string) (bool, bool, error) {
			world, err := b.loadFrame(t.Context(), frame, 390)
			if err != nil {
				return false, false, err
			}
			raw, _ := json.Marshal(sentinel.URL + "/core")
			var result struct{ Network, Parent bool }
			err = b.evaluate(t.Context(), world, `(async()=>{let network=false,parent=false;try{await fetch(`+string(raw)+`,{mode:'no-cors'});network=true}catch{}try{void window.parent.document.body;parent=true}catch{}return{Network:network,Parent:parent}})()`, &result)
			return result.Network, result.Parent, err
		}
		network, parent, err := probe(generatedui.MockFrame(page, 390))
		if err != nil {
			return err
		}
		if network || parent || hits.Load() != 0 {
			t.Fatalf("protected frame escaped: network=%t parent=%t hits=%d", network, parent, hits.Load())
		}
		unprotected := regexp.MustCompile(`<meta http-equiv="Content-Security-Policy"[^>]*>`).ReplaceAllString(page, "")
		frame := strings.Replace(generatedui.MockFrame(unprotected, 390), `sandbox="allow-scripts"`, `sandbox="allow-scripts allow-same-origin"`, 1)
		network, parent, err = probe(frame)
		if err != nil {
			return err
		}
		if !network || !parent || hits.Load() != 1 {
			t.Fatalf("negative control did not reach target: network=%t parent=%t hits=%d", network, parent, hits.Load())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMockEvaluationFirstVersions(t *testing.T) {
	var cases []struct {
		ID, Request, Fragment string
		Checklist             []result.MockItem
		OwnerLabelled         bool            `json:"owner_labelled"`
		Expected              map[string]bool `json:"expected_dimensions"`
	}
	raw, err := os.ReadFile("testdata/evaluation.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	c := Chromium{Leaser: testLeaser{}}
	for _, tc := range cases {
		t.Run(tc.ID, func(t *testing.T) {
			if tc.OwnerLabelled {
				t.Fatal("synthetic fixture cannot claim an owner label")
			}
			out, err := c.Draw(t.Context(), tc.Fragment, tc.Checklist)
			if err != nil {
				t.Fatal(err)
			}
			if dir := os.Getenv("LINA_MOCK_EVIDENCE_DIR"); dir != "" {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "mock-"+tc.ID+".png"), out.PNG, 0644); err != nil {
					t.Fatal(err)
				}
				report, _ := json.MarshalIndent(map[string]any{"case_id": tc.ID, "request": tc.Request, "version": 1, "checks": out.Checks, "text": true, "owner_labels": 0, "judge": "unqualified"}, "", "  ")
				if err := os.WriteFile(filepath.Join(dir, "mock-"+tc.ID+".json"), append(report, '\n'), 0644); err != nil {
					t.Fatal(err)
				}
			}
			for _, check := range out.Checks {
				t.Logf("first_version dimension=%s pass=%t", check.ID, check.Passed)
				if check.Passed != tc.Expected[check.ID] {
					t.Errorf("dimension %s", check.ID)
				}
			}
			body := &result.Mock{Request: tc.Request, Checklist: tc.Checklist, Page: result.AssetRef{ID: "page", Revision: 1}, PNG: result.AssetRef{ID: "png", Revision: 1}}
			text := result.PlainText(body, nil)
			for _, item := range tc.Checklist {
				if !strings.Contains(text, item.Description) {
					t.Fatal("text dimension omitted a checklist item")
				}
			}
			if !strings.Contains(text, "Export PNG:") {
				t.Fatal("text export absent")
			}
			t.Log("first_version dimension=text pass=true; judge=unqualified owner_labels=0")
		})
	}
}
