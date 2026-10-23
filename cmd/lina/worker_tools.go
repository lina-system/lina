// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/kit/rpc"
)

func runWorkerTools(args []string) int {
	socket, token := os.Getenv("LINA_WORKER_TOOLS_SOCKET"), os.Getenv("LINA_WORKER_TOOLS_TOKEN")
	if len(args) != 0 || socket == "" || token == "" {
		return 64
	}
	if err := serveWorkerTools(context.Background(), os.Stdin, os.Stdout, socket, token); err != nil {
		return 70
	}
	return 0
}

// stdout is exclusively MCP JSON-RPC. Credentials never appear in replies.
func serveWorkerTools(ctx context.Context, in io.Reader, out io.Writer, socket, token string) error {
	reader, writer := rpc.NewReader(in), json.NewEncoder(out)
	initialized := false
	for {
		line, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		var request struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		code, message := 0, ""
		var result any
		if json.Unmarshal(line, &request) != nil {
			code, message = -32700, "Invalid JSON."
		} else if request.JSONRPC != "2.0" || request.Method == "" {
			code, message = -32600, "Invalid request."
		} else if request.ID == nil {
			// MCP notifications, including initialized, have no response.
			continue
		} else {
			switch request.Method {
			case "initialize":
				var params struct {
					ProtocolVersion string `json:"protocolVersion"`
				}
				if json.Unmarshal(request.Params, &params) != nil || params.ProtocolVersion == "" {
					code, message = -32602, "Invalid parameters."
					break
				}
				version := params.ProtocolVersion
				switch version {
				case "2024-11-05", "2025-03-26", "2025-06-18":
				default:
					version = "2025-06-18"
				}
				result = map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "lina", "version": "1"}}
				initialized = true
			case "ping":
				result = map[string]any{}
			case "tools/list", "tools/call":
				if !initialized {
					code, message = -32600, "Initialize first."
					break
				}
				call := clientapi.WorkerToolRequest{Token: token, Method: request.Method}
				if request.Method == "tools/call" {
					var params struct {
						Name      string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					}
					if json.Unmarshal(request.Params, &params) != nil || params.Name == "" {
						code, message = -32602, "Invalid parameters."
						break
					}
					call.Name, call.Arguments = params.Name, params.Arguments
					if call.Arguments == nil {
						call.Arguments = json.RawMessage(`{}`)
					}
				}
				response, err := clientapi.CallWorkerTool(ctx, socket, call)
				if err != nil || response.Error != "" {
					code, message = -32000, "Worker tool call refused."
				} else if request.Method == "tools/list" {
					result = map[string]any{"tools": response.Tools}
				} else {
					result = response.Result
				}
			default:
				code, message = -32601, "Method not found."
			}
		}
		if request.ID == nil {
			request.ID = json.RawMessage(`null`)
		}
		reply := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		if code != 0 {
			reply["error"] = map[string]any{"code": code, "message": message}
		} else {
			reply["result"] = result
		}
		if err := writer.Encode(reply); err != nil {
			return err
		}
	}
}
