// SPDX-License-Identifier: Apache-2.0
package clientapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"

	"github.com/thisisjun786/lina/kit/rpc"
)

// WorkerToolRequest is the private, session-bound Core socket protocol. The
// server derives all execution identity; a caller cannot supply a thread.
type WorkerToolRequest struct {
	Token     string          `json:"token"`
	Method    string          `json:"method"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type WorkerTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type WorkerToolResponse struct {
	Tools  []WorkerTool    `json:"tools,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// CallWorkerTool performs one call without retrying a potentially effective
// request. Tokens are never sent to the ordinary person-facing client socket.
func CallWorkerTool(ctx context.Context, socket string, request WorkerToolRequest) (WorkerToolResponse, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return WorkerToolResponse{}, ErrUnavailable
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if err = rpc.NewWriter(conn).Write(ctx, request); err != nil {
		return WorkerToolResponse{}, ErrUnavailable
	}
	raw, err := rpc.NewReader(conn).Read()
	if err != nil {
		return WorkerToolResponse{}, ErrUnavailable
	}
	var response WorkerToolResponse
	if json.Unmarshal(raw, &response) != nil {
		return WorkerToolResponse{}, errors.New("invalid worker tool response")
	}
	return response, nil
}
