// SPDX-License-Identifier: Apache-2.0
package assemble_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestCapabilityLayoutAndIdentity(t *testing.T) {
	in := input()
	in.MainID, in.ConversationID = "main", "conversation"
	in.History = append([]provider.Item{json.RawMessage(`{"role":"user","content":"previous"}`)}, in.History...)
	previous := 1
	in.PreviousInput = &previous
	r, report := assemble.Assemble(in, count)
	if report.Failure != "" {
		t.Fatal(report)
	}
	if r.CacheIdentity == "" || len(r.CacheIdentity) != 64 || strings.Contains(r.CacheIdentity, "conversation") {
		t.Fatal(r.CacheIdentity)
	}
	if r.Layout == nil || !reflect.DeepEqual(r.Layout, report.Layout) {
		t.Fatal("missing layout")
	}
	if r.Layout.Fixed.Items != 6 || r.Layout.Fixed.Tools != 1 || r.Layout.PreviousInput == nil || r.Layout.PreviousInput.Items != 8 || r.Layout.CurrentInput.Items != r.Layout.Fixed.Items+1+len(in.History)+1 || r.Layout.Newest != nil {
		t.Fatalf("layout: %+v", r.Layout)
	}
	if !reflect.DeepEqual(r.Items[r.Layout.CurrentInput.Items-1], in.Current) || r.Layout.CurrentInput.Items >= len(r.Items) {
		t.Fatal("current-input boundary must precede the context packet")
	}
	original := assemble.Digest(r)
	changed := *r.Layout
	changed.Fixed.Tools++
	r.Layout = &changed
	if assemble.Digest(r) == original {
		t.Fatal("layout not hashed")
	}
	in.History = nil
	in.PreviousInput = nil
	in.Summary = json.RawMessage(`{"role":"user","content":"compacted"}`)
	compacted, _ := assemble.Assemble(in, count)
	if compacted.CacheIdentity != r.CacheIdentity {
		t.Fatal("compaction changed identity")
	}
	in.ConversationID = "other"
	other, _ := assemble.Assemble(in, count)
	if other.CacheIdentity == r.CacheIdentity {
		t.Fatal("conversations share identity")
	}
}

func TestCapabilitySelectionUsesOnlyDeclaredAndAdmitted(t *testing.T) {
	in := input()
	in.MainID, in.ConversationID = "main", "conversation"
	r, _ := assemble.Assemble(in, count)
	caps := provider.CapabilitySet{provider.CacheIdentityCapability, provider.CacheMarksCapability, provider.CacheLifetime1hCapability, provider.ReasoningStateCapability}
	used, err := assemble.SelectCapabilities(r, caps, caps)
	if err != nil || used.CacheIdentity != r.CacheIdentity || len(used.CacheMarks) != 2 || used.CacheMarks[0].Lifetime != provider.Cache1Hour || !used.ReasoningState {
		t.Fatalf("use=%+v err=%v", used, err)
	}
	empty, err := assemble.SelectCapabilities(r, nil, caps)
	if err != nil || !reflect.DeepEqual(empty, provider.CapabilityUse{}) {
		t.Fatalf("assumed capabilities %+v %v", empty, err)
	}
	empty, err = assemble.SelectCapabilities(r, caps, nil)
	if err != nil || !reflect.DeepEqual(empty, provider.CapabilityUse{}) {
		t.Fatalf("unadmitted capabilities %+v %v", empty, err)
	}
}
