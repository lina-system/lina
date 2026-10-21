// SPDX-License-Identifier: Apache-2.0
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
)

type FileReply struct {
	State  State      `json:"state"`
	Result FileResult `json:"result"`
	Code   string     `json:"code,omitempty"`
}

// ServeFiles handles exactly one bounded request in the file worker. Locations
// come from Core's process invocation, not the request. It must run inside the
// verified OS sandbox. Errors on the wire are codes, never host filesystem paths.
func ServeFiles(ctx context.Context, root, versions string, input io.Reader, output io.Writer) error {
	reply := FileReply{State: Failed}
	data, err := io.ReadAll(io.LimitReader(input, 3*MaxFileBytes+1))
	var req FileRequest
	if err == nil {
		err = decodeStrict(data, &req)
	}
	if err == nil {
		err = ValidateFileRequest(req)
	}
	if err == nil {
		var files *Files
		files, err = OpenFiles(root, versions)
		if err == nil {
			reply.Result, err = files.Execute(ctx, req)
			files.Close()
		}
	}
	if err == nil {
		reply.State = Applied
	} else {
		switch {
		case errors.Is(err, ErrUnknown):
			reply.State = Unknown
			reply.Code = "effect_unknown"
		case errors.Is(err, ErrFileRevision):
			reply.Code = "stale_revision"
		case errors.Is(err, ErrPatchContext):
			reply.Code = "stale_patch_context"
		case errors.Is(err, ErrFileScope):
			reply.Code = "file_outside_scope"
		case errors.Is(err, ErrFileSize):
			reply.Code = "file_too_large"
		case errors.Is(err, ErrInvalid):
			reply.Code = "invalid_tool_call"
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			reply.State = Cancelled
			reply.Code = "cancelled"
		default:
			reply.Code = "file_io_failed"
		}
	}
	return json.NewEncoder(output).Encode(reply)
}
