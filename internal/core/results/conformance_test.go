// SPDX-License-Identifier: Apache-2.0

package results

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/thisisjun786/lina/kit/protocol/conformance"
	"github.com/thisisjun786/lina/kit/result"
)

func TestCoreResultConformance(t *testing.T) {
	t.Parallel()
	cases, err := conformance.LoadApplications(os.DirFS("../../../protocol/fixtures/client/1.0/application/results"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			t.Parallel()
			if c.Service != "results" {
				t.Fatalf("unhandled service %q", c.Service)
			}
			var scope result.Scope
			if err := json.Unmarshal(c.Setup, &scope); err != nil {
				t.Fatal(err)
			}
			store, db, _, _ := setup(t)
			var id string
			var request result.PresentRequest
			err := conformance.RunApplication(c, conformance.Application{
				Execute: func(raw json.RawMessage) (json.RawMessage, error) {
					if err := json.Unmarshal(raw, &request); err != nil {
						return nil, err
					}
					card, err := store.Present(t.Context(), scope, request)
					if errors.Is(err, result.ErrMalformed) {
						return json.Marshal(map[string]any{"error": "malformed", "result": nil})
					}
					if err != nil {
						return nil, err
					}
					id = card.ID
					r, err := store.Read(t.Context(), scope, result.ReadRequest{ID: id})
					if err != nil {
						return nil, err
					}
					return json.Marshal(map[string]any{"error": "", "result": map[string]any{"type": r.Type, "body": r.Body}})
				},
				Observe: func() (json.RawMessage, error) {
					state := map[string]any{"events": count(t, db, "conv_event"), "heads": count(t, db, "result_heads"), "saves": count(t, db, "result_saves")}
					if id != "" {
						r, err := store.Read(t.Context(), scope, result.ReadRequest{ID: id})
						if err != nil {
							return nil, err
						}
						body, err := result.Validate(request.Type, request.Body, result.Limits{})
						if err != nil {
							return nil, err
						}
						state["revision"], state["status"] = r.Revision, r.Status
						state["title"], state["summary"] = r.Title, r.Summary
						state["saved_as"], state["revised_by"], state["provenance"] = r.SavedAs, r.RevisedBy, r.Provenance
						state["text_complete"] = r.Text != "" && r.Text == result.PlainText(body, nil)
					}
					return json.Marshal(state)
				},
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
