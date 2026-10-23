// SPDX-License-Identifier: Apache-2.0
package worker

import "encoding/json"

// WorkToolDeclarations is the shared Codex and ACP work-tool contract.
func WorkToolDeclarations() []Tool {
	schema := func(properties, required string) json.RawMessage {
		return json.RawMessage(`{"type":"object","properties":{` + properties + `},"required":[` + required + `],"additionalProperties":false}`)
	}
	return []Tool{
		{Namespace: "lina", Name: "read_projection", Description: "Read this assignment's task packet and revision. No other conversation, material or memory is authorized by this session.", InputSchema: schema("", "")},
		{Namespace: "lina", Name: "ask", Description: "Ask the originating person a question and wait for their answer.", InputSchema: schema(`"question":{"type":"string"}`, `"question"`)},
		{Namespace: "lina", Name: "coordinate", Description: "Send a coordination request to LINA Core in the originating conversation. Attach only visible UTF-8 files from this worktree (10 files, 256 KiB each, 512 KiB total). Results are XML-escaped lina-message frames containing JSON. This acknowledges receipt, not completion of the requested action.", InputSchema: schema(`"request":{"type":"string","maxLength":65536},"files":{"type":"array","maxItems":10,"items":{"type":"string"}}`, `"request"`)},
		{Namespace: "lina", Name: "submit_receipt", Description: "Submit a version 1 completion claim using the existing work receipt shape. Read the projection for assignment identity. The receipt is retained as worker evidence; it cannot verify or deliver a task.", InputSchema: schema(`"receipt":`+workerReceiptSchema, `"receipt"`)},
	}
}

// The worker gets the existing ledger receipt fields rather than an opaque
// object it would have to guess. Core still validates every field on ingress.
const workerReceiptSchema = `{
 "type":"object","additionalProperties":false,
 "required":["schema_version","eventId","relationshipId","executionGeneration","attempt","revisionHash","outcome","producer","turnRef","emittedAt","manifest","provenance"],
 "properties":{
  "schema_version":{"type":"integer","enum":[1]},
  "eventId":{"type":"string","description":"First 32 hex characters of SHA256 of relationshipId|executionGeneration|revisionHash|outcome for ready_for_review; otherwise relationshipId|executionGeneration|outcome|turnRef.turnId|attempt."},
  "relationshipId":{"type":"string","description":"assignment_id from read_projection"},
  "executionGeneration":{"type":"integer","minimum":1,"description":"execution_generation from read_projection"},
  "attempt":{"type":"integer","minimum":1},
  "revisionHash":{"type":"string","description":"SHA256 of sorted path:sha256 manifest entries joined with newline; 64 zeros when empty."},
  "outcome":{"type":"string","enum":["ready_for_review","failed","interrupted","blocked_needs_input"]},
  "producer":{"type":"string","enum":["child"]},
  "turnRef":{"type":"object","additionalProperties":false,"required":["threadId","turnId","turnStatus"],"properties":{"threadId":{"type":"string"},"turnId":{"type":"string"},"turnStatus":{"type":"string","enum":["completed","failed","interrupted"]}}},
  "emittedAt":{"type":"string","description":"RFC3339 timestamp with explicit zone"},
  "manifest":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["path","sha256"],"properties":{"path":{"type":"string"},"sha256":{"type":"string"},"bytes":{"type":"integer","minimum":0}}}},
  "provenance":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["artifact_id","task_id","node_id","path","sha256","observed_at"],"properties":{"artifact_id":{"type":"string"},"task_id":{"type":"string"},"node_id":{"type":"string"},"path":{"type":"string"},"sha256":{"type":"string"},"observed_at":{"type":"string"}}}}
 }
}`
