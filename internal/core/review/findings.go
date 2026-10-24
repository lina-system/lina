// SPDX-License-Identifier: Apache-2.0
// Package review owns Core's design-conformance findings.
package review

import (
	"bytes"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Finding is evidence, never an instruction or a delivery verdict.
type Finding struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	File     string `json:"file"`
	Claim    string `json:"claim"`
}

const FindingsSchema = `{
 "$schema":"https://json-schema.org/draft/2020-12/schema",
 "type":"object","required":["findings"],"additionalProperties":false,
 "properties":{"findings":{"type":"array","maxItems":256,"items":{
  "type":"object","required":["id","severity","file","claim"],"additionalProperties":false,
  "properties":{
   "id":{"type":"string","minLength":1,"maxLength":128,"pattern":"^[A-Za-z0-9_-]+$"},
   "severity":{"enum":["P0","P1","P2","P3"]},
   "file":{"type":"string","minLength":1,"maxLength":4096},
   "claim":{"type":"string","minLength":1,"maxLength":4096}
  }
 }}}
}`

var ErrReply = errors.New("invalid_review_reply")
var findingSchema = sync.OnceValue(func() *jsonschema.Schema {
	compiler := jsonschema.NewCompiler()
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(FindingsSchema))
	if err != nil {
		panic(err)
	}
	if err = compiler.AddResource("findings.json", doc); err != nil {
		panic(err)
	}
	schema, err := compiler.Compile("findings.json")
	if err != nil {
		panic(err)
	}
	return schema
})

// Parse accepts exactly one complete schema-valid object. Prose, partial JSON
// and mixed valid/invalid records produce no findings.
func Parse(raw []byte) ([]Finding, error) {
	if len(raw) > 1<<20 {
		return nil, ErrReply
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil || findingSchema().Validate(doc) != nil {
		return nil, ErrReply
	}
	var reply struct {
		Findings []Finding `json:"findings"`
	}
	if json.Unmarshal(raw, &reply) != nil {
		return nil, ErrReply
	}
	seen := map[string]bool{}
	for _, f := range reply.Findings {
		if seen[f.ID] || strings.TrimSpace(f.Claim) == "" || f.File == "." || path.IsAbs(f.File) || path.Clean(f.File) != f.File || f.File == ".." || strings.HasPrefix(f.File, "../") || strings.ContainsAny(f.File, "\x00\r\n\\") {
			return nil, ErrReply
		}
		seen[f.ID] = true
	}
	return reply.Findings, nil
}

// maxFindings is FindingsSchema's maxItems. Core-computed lists stay within it
// so FinishIn can record them.
const maxFindings = 256

// findingAccepted reports whether one finding satisfies FindingsSchema and Parse.
func findingAccepted(f Finding) bool {
	raw, err := json.Marshal(map[string]any{"findings": []Finding{f}})
	if err != nil {
		return false
	}
	_, err = Parse(raw)
	return err == nil
}
