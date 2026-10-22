// SPDX-License-Identifier: Apache-2.0
package tools

import (
	"encoding/json"
	"strings"
	"time"
)

// FileSpec binds a tool to one Core-selected write/read root. The root is the
// admission target for the whole set, including multi-file patches. Each file's
// own content revision is checked inside the sandbox immediately before write.
func FileSpec(name, scope string) (Spec, error) {
	operation := map[string]string{"read_file": "read", "write_file": "write", "delete_file": "delete", "restore_file": "restore", "apply_patch": "patch"}[name]
	if operation == "" || scope == "" {
		return Spec{}, ErrInvalid
	}
	return Spec{Classify: func(in Input) EffectClass {
		if in.Write {
			return LocalWrite
		}
		return Research
	}, Name: name, Conc: 1, Deadline: 15 * time.Second, Placement: Core, Schema: func(raw json.RawMessage) (Input, error) {
		var req FileRequest
		if operation == "patch" && strings.HasPrefix(string(raw), "*** Begin Patch\n") {
			req.Patch = string(raw)
		} else if err := decodeStrict(raw, &req); err != nil {
			return Input{}, err
		}
		if req.Operation != "" && req.Operation != operation {
			return Input{}, ErrInvalid
		}
		req.Operation = operation
		if err := ValidateFileRequest(req); err != nil {
			return Input{}, err
		}
		args, _ := json.Marshal(req)
		where := scope
		if req.Path != "" {
			where += "/" + req.Path
		} else {
			paths, _ := PatchPaths(req.Patch)
			encoded, _ := json.Marshal(paths)
			where += " " + string(encoded)
		}
		return Input{Arguments: args, Target: scope, What: name, Where: where, ScopeKey: "files:" + scope, Write: operation != "read", Undoable: true}, nil
	}}, nil
}

func ValidateFileRequest(req FileRequest) error {
	if req.Operation == "manifest" {
		if req.Path != "" || req.Revision != "" || len(req.Content) > 0 || req.Patch != "" || req.Version != "" {
			return ErrInvalid
		}
		return nil
	}
	if len(req.Content) > MaxFileBytes {
		return ErrFileSize
	}
	if req.Operation == "patch" {
		if req.Path != "" || req.Revision != "" || len(req.Content) > 0 || req.Version != "" {
			return ErrInvalid
		}
		_, err := PatchPaths(req.Patch)
		return err
	}
	if !FilePath(req.Path) || req.Patch != "" {
		return ErrInvalid
	}
	if req.Operation != "write" && len(req.Content) > 0 {
		return ErrInvalid
	}
	if req.Operation != "restore" && req.Version != "" {
		return ErrInvalid
	}
	if req.Operation == "read" {
		if req.Revision != "" {
			return ErrInvalid
		}
		return nil
	}
	if req.Revision == "" {
		return ErrInvalid
	}
	switch req.Operation {
	case "write", "delete":
		return nil
	case "restore":
		if req.Version != "" {
			return nil
		}
	}
	return ErrInvalid
}
