// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
)

// Existing scripted workflows expect their schemas before choosing operations.
// Have the fixture model load the remaining catalog once, then run that script.
// Core executes and journals the discovery call; no declarations are injected
// into a request. M4 inspects the untouched initial requests separately.
func toolCatalogModel(next http.Handler) http.Handler {
	var sequence atomic.Uint64
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var req struct {
			Tools []struct {
				Name       string
				Parameters struct {
					Properties struct {
						Tools struct{ Items struct{ Enum []string } }
					}
				}
			}
		}
		_ = json.Unmarshal(raw, &req)
		var available []string
		offered := map[string]bool{}
		for _, d := range req.Tools {
			offered[d.Name] = true
			if d.Name == "load_tools" {
				available = d.Parameters.Properties.Tools.Items.Enum
			}
		}
		var missing []string
		for _, name := range available {
			if !offered[name] {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			emitPersonaCall(w, fmt.Sprintf("fixture-load-%d", sequence.Add(1)), "load_tools", map[string]any{"tools": missing})
			return
		}
		next.ServeHTTP(w, r)
	})
}
