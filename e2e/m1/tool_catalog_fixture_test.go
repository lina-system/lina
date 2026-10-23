// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"slices"
)

type fixtureToolDeclaration struct {
	Name       string
	Parameters struct {
		Properties struct{ Name struct{ Enum []string } }
	}
}

// Scripts already know their operation schemas. Use Core's advertised bridge
// for a deferred operation, without adding unrelated discovery rounds to the
// workflow being tested. M4 exercises actual load_tools calls separately.
func toolCatalogModel(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var req struct{ Tools []fixtureToolDeclaration }
		_ = json.Unmarshal(raw, &req)
		offered, deferred := map[string]bool{}, map[string]bool{}
		for _, d := range req.Tools {
			offered[d.Name] = true
			if d.Name == "call_tool" {
				for _, name := range d.Parameters.Properties.Name.Enum {
					deferred[name] = true
				}
			}
		}
		if !offered["call_tool"] {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(catalogResponseWriter{w, offered, deferred}, r)
	})
}

type catalogResponseWriter struct {
	http.ResponseWriter
	offered, deferred map[string]bool
}

func (w catalogResponseWriter) toolInvocation(name string, args any) (string, any) {
	if !w.offered[name] && w.deferred[name] {
		return "call_tool", map[string]any{"name": name, "arguments": args}
	}
	return name, args
}
func (w catalogResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func toolReachable(declarations []json.RawMessage, name string) bool {
	for _, raw := range declarations {
		var d fixtureToolDeclaration
		if json.Unmarshal(raw, &d) == nil && (d.Name == name || d.Name == "call_tool" && slices.Contains(d.Parameters.Properties.Name.Enum, name)) {
			return true
		}
	}
	return false
}
