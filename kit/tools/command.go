// SPDX-License-Identifier: Apache-2.0
package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"time"
)

type CommandRequest struct {
	Argv    []string `json:"argv"`
	Network bool     `json:"network,omitempty"`
}

// CommandPolicy is installed by Core, never decoded from model arguments. A
// reusable prefix names an executable and any fixed subcommands. For shells,
// interpreters and command wrappers it must include the entire command; callers
// must not grant a standing shell/interpreter prefix with arbitrary code args.
// NetworkDefault and Outward come only from an installed service profile.
type CommandPolicy struct {
	Name, Target                   string
	Prefix                         []string
	NetworkDefault, Outward, Write bool
}

func decodeStrict(raw []byte, value any) error {
	if len(raw) > 3*MaxFileBytes || !json.Valid(raw) {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return ErrInvalid
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return ErrInvalid
	}
	return nil
}

func CommandSpec(policy CommandPolicy) (Spec, error) {
	if policy.Name == "" || policy.Target == "" || len(policy.Prefix) == 0 {
		return Spec{}, ErrInvalid
	}
	policy.Prefix = slices.Clone(policy.Prefix)
	for _, arg := range policy.Prefix {
		if arg == "" || strings.ContainsRune(arg, 0) {
			return Spec{}, ErrInvalid
		}
	}
	prefix, _ := json.Marshal(policy.Prefix)
	hash := sha256.Sum256(prefix)
	return Spec{Classify: Class(CommandEffect), Name: policy.Name, Outward: policy.Outward, Conc: 1, Deadline: 15 * time.Second, Placement: Core, Schema: func(raw json.RawMessage) (Input, error) {
		var req CommandRequest
		if err := decodeStrict(raw, &req); err != nil {
			return Input{}, err
		}
		if len(req.Argv) < len(policy.Prefix) || len(req.Argv) > 1024 || !slices.Equal(req.Argv[:len(policy.Prefix)], policy.Prefix) {
			return Input{}, ErrInvalid
		}
		for _, arg := range req.Argv {
			if len(arg) > 1<<20 || strings.ContainsRune(arg, 0) {
				return Input{}, ErrInvalid
			}
		}
		req.Network = req.Network || policy.NetworkDefault
		args, _ := json.Marshal(req)
		what, _ := json.Marshal(req.Argv)
		return Input{Arguments: args, Target: policy.Target, What: "Run " + string(what), Where: policy.Target, ScopeKey: "network-prefix:" + hex.EncodeToString(hash[:]), Network: req.Network && !policy.NetworkDefault, Write: policy.Write, Undoable: !policy.Outward}, nil
	}}, nil
}
