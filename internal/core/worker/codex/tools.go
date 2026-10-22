// SPDX-License-Identifier: Apache-2.0
package codex

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/thisisjun786/lina/internal/core/worker"
	wire "github.com/thisisjun786/lina/third_party/codex-app-server/upstream"
)

// LINA installs only its per-thread namespace, never worker configuration.
func dynamicTools(tools []worker.Tool) ([]wire.V2DynamicToolSpec, map[string]bool, error) {
	declared := make(map[string]bool, len(tools))
	namespace := wire.V2DynamicToolSpecNamespace{Name: "lina", Description: "LINA work tools"}
	for _, tool := range tools {
		if tool.Namespace != "lina" || strings.TrimSpace(tool.Name) == "" || declared[tool.Name] {
			return nil, nil, errors.New("Codex work tools require unique names in the lina namespace")
		}
		var schema map[string]json.RawMessage
		if json.Unmarshal(tool.InputSchema, &schema) != nil || schema == nil {
			return nil, nil, errors.New("Codex work tool input schema must be an object")
		}
		declared[tool.Name] = true
		namespace.Tools = append(namespace.Tools, wire.V2DynamicToolNamespaceTool{Function: &wire.V2DynamicToolNamespaceToolFunction{Name: tool.Name, Description: tool.Description, InputSchema: append(json.RawMessage(nil), tool.InputSchema...)}})
	}
	if len(tools) == 0 {
		return nil, declared, nil
	}
	return []wire.V2DynamicToolSpec{{Namespace: &namespace}}, declared, nil
}
