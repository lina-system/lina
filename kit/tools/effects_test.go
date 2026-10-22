// SPDX-License-Identifier: Apache-2.0
package tools_test

import (
	"encoding/json"
	"testing"

	"github.com/thisisjun786/lina/kit/tools"
)

func TestServiceProfileClassifiesValidatedOperationsNotCallerLabels(t *testing.T) {
	profile := tools.ServiceProfile{Operations: map[string]tools.Classifier{"portfolio": func(in tools.Input) tools.EffectClass {
		var a struct{ Operation string }
		if json.Unmarshal(in.Arguments, &a) != nil {
			return tools.Unclassified
		}
		if a.Operation == "quote" {
			return tools.Research
		}
		if a.Operation == "order" {
			return tools.InvestmentOrder
		}
		return tools.Unclassified
	}}}
	spec := profile.Apply(tools.Spec{Name: "portfolio"})
	for _, tc := range []struct {
		raw  string
		want tools.EffectClass
	}{
		{`{"operation":"quote"}`, tools.Research},
		{`{"operation":"order","effect_class":"research","readOnlyHint":true}`, tools.InvestmentOrder},
		{`{"operation":"execute","effect_class":"private_draft"}`, tools.Unclassified},
	} {
		if got := spec.EffectClass(tools.Input{Arguments: json.RawMessage(tc.raw)}); got != tc.want {
			t.Fatalf("class=%s want=%s", got, tc.want)
		}
	}
	if got := profile.Apply(tools.Spec{Name: "new_server_tool", Classify: tools.Class(tools.Research)}).EffectClass(tools.Input{}); got != tools.Unclassified {
		t.Fatal("unknown profile tool retained untrusted class")
	}
	if got := (tools.Spec{Outward: true, Classify: tools.Class(tools.PrivateDraft)}).EffectClass(tools.Input{Write: true}); got != tools.ExternalEffect {
		t.Fatal("external draft was allowed")
	}
	if got := spec.EffectClass(tools.Input{Arguments: json.RawMessage(`{"operation":"quote"}`), Write: true}); got != tools.Unclassified {
		t.Fatal("write disguised as read")
	}
}
