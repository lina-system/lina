// SPDX-License-Identifier: Apache-2.0
package result

import "testing"

func TestCompletionRequiresEveryKindAndResolvedReference(t *testing.T) {
	for _, tc := range []struct {
		name string
		refs []EvidenceReference
		want string
	}{
		{"missing", nil, "claimed"},
		{"wrong-kind", []EvidenceReference{{Kind: "receipt", TurnID: "turn", CallID: "read"}}, "claimed"},
		{"unresolved", []EvidenceReference{{Kind: "source_reopened", TurnID: "turn", CallID: "missing"}}, "claimed"},
		{"partial", []EvidenceReference{{Kind: "source_reopened", TurnID: "turn", CallID: "read"}}, "claimed"},
		{"complete", []EvidenceReference{{Kind: "source_reopened", TurnID: "turn", CallID: "read"}, {Kind: "receipt", TurnID: "turn", CallID: "read"}}, "done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := AssessCompletion(CompletionRequest{WorkKind: "document", Evidence: tc.refs}, []string{"source_reopened", "receipt"}, func(ref EvidenceReference) bool { return ref.TurnID == "turn" && ref.CallID == "read" })
			if got.State != tc.want || (got.Label == "not verified") != (tc.want == "claimed") {
				t.Fatalf("assessment: %+v", got)
			}
		})
	}
}
