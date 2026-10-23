// SPDX-License-Identifier: Apache-2.0
package codex

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/thisisjun786/lina/internal/core/worker"
	wire "github.com/thisisjun786/lina/third_party/codex-app-server/upstream"
)

func verifyApprovalChoice(request worker.Request, raw json.RawMessage, allow bool) error {
	var expected any
	if request.Method == "item/permissions/requestApproval" {
		var p wire.PermissionsRequestApprovalParams
		if err := json.Unmarshal(request.Params, &p); err != nil {
			return err
		}
		scope := wire.PermissionGrantScopeTurn
		r := wire.PermissionsRequestApprovalResponse{Scope: &scope}
		if allow {
			r.Permissions = wire.GrantedPermissionProfile{FileSystem: p.Permissions.FileSystem, Network: p.Permissions.Network}
		}
		expected = r
	} else {
		decision := "decline"
		if allow {
			decision = "accept"
		}
		expected = map[string]string{"decision": decision}
	}
	data, err := json.Marshal(expected)
	if err != nil {
		return err
	}
	var compact bytes.Buffer
	if json.Compact(&compact, raw) != nil || !bytes.Equal(compact.Bytes(), data) {
		return errors.New("Codex approval does not match signed choice")
	}
	return nil
}

// Validate known response shapes, then forward the original JSON unchanged.
// Raw unions in the generator require explicit checks at this boundary.
func validateAnswer(r worker.Request, raw json.RawMessage) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return errors.New("Codex answer must be an object")
	}
	valid := false
	switch r.Method {
	case string(wire.ServerRequestItemCommandExecutionRequestApprovalMethodItemCommandExecutionRequestApproval):
		var v wire.CommandExecutionRequestApprovalResponse
		valid = json.Unmarshal(raw, &v) == nil && approvalDecision(v.Decision, true)
	case string(wire.ServerRequestItemFileChangeRequestApprovalMethodItemFileChangeRequestApproval):
		var v wire.FileChangeRequestApprovalResponse
		valid = json.Unmarshal(raw, &v) == nil && approvalDecision(v.Decision, false)
	case string(wire.ServerRequestItemPermissionsRequestApprovalMethodItemPermissionsRequestApproval):
		var v wire.PermissionsRequestApprovalResponse
		var permissions map[string]json.RawMessage
		valid = json.Unmarshal(raw, &v) == nil && json.Unmarshal(fields["permissions"], &permissions) == nil && permissions != nil
		if v.Scope != nil {
			switch *v.Scope {
			case wire.PermissionGrantScopeTurn, wire.PermissionGrantScopeSession:
			default:
				valid = false
			}
		}
	case string(wire.ServerRequestItemToolRequestUserInputMethodItemToolRequestUserInput):
		var v wire.ToolRequestUserInputResponse
		var p wire.ToolRequestUserInputParams
		valid = json.Unmarshal(raw, &v) == nil && v.Answers != nil && json.Unmarshal(r.Params, &p) == nil && len(v.Answers) == len(p.Questions)
		for _, q := range p.Questions {
			answer, ok := v.Answers[q.ID]
			valid = valid && ok && answer.Answers != nil
		}
	case string(wire.ServerRequestMcpServerElicitationRequestMethodMcpServerElicitationRequest):
		var v wire.McpServerElicitationRequestResponse
		if json.Unmarshal(raw, &v) == nil {
			switch v.Action {
			case wire.McpServerElicitationActionAccept, wire.McpServerElicitationActionDecline, wire.McpServerElicitationActionCancel:
				valid = true
			}
		}
	case string(wire.ServerRequestItemToolCallMethodItemToolCall):
		var v wire.DynamicToolCallResponse
		var success *bool
		valid = json.Unmarshal(raw, &v) == nil && json.Unmarshal(fields["success"], &success) == nil && success != nil && v.ContentItems != nil
		for _, item := range v.ContentItems {
			valid = valid && len(item.Unrecognized) == 0 && (item.InputText != nil || item.InputImage != nil || item.InputAudio != nil)
		}
	}
	if !valid {
		return errors.New("invalid Codex method response")
	}
	return nil
}

func approvalDecision(raw json.RawMessage, command bool) bool {
	var scalar string
	if json.Unmarshal(raw, &scalar) == nil {
		// These open, untagged unions are RawMessage in the generated schema.
		switch scalar {
		case "accept", "acceptForSession", "decline", "cancel":
			return true
		}
		return false
	}
	if !command {
		return false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || len(object) != 1 {
		return false
	}
	if value, ok := object["acceptWithExecpolicyAmendment"]; ok {
		var v wire.CommandExecutionApprovalDecisionVariant3AcceptWithExecpolicyAmendment
		return json.Unmarshal(value, &v) == nil && v.ExecpolicyAmendment != nil
	}
	if value, ok := object["applyNetworkPolicyAmendment"]; ok {
		var v wire.CommandExecutionApprovalDecisionVariant4ApplyNetworkPolicyAmendment
		if json.Unmarshal(value, &v) != nil {
			return false
		}
		switch v.NetworkPolicyAmendment.Action {
		case wire.NetworkPolicyRuleActionAllow, wire.NetworkPolicyRuleActionDeny:
			return v.NetworkPolicyAmendment.Host != ""
		}
	}
	return false
}
