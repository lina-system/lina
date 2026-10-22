// SPDX-License-Identifier: Apache-2.0
package takeover

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/protocol"
)

// CLI is deliberately outside cmd/ and release assembly. The manifest and
// journal belong to the operator; no source store is opened by LINA Core.
func CLI(ctx context.Context, args []string, out io.Writer) error {
	if len(args) != 5 {
		return errors.New("usage: crw-takeover approve|apply|rollback|status CORE_SOCKET MANIFEST JOURNAL TIMEOUT")
	}
	action, socket, manifestPath, journal := args[0], args[1], args[2], args[3]
	if !slices.Contains([]string{"approve", "apply", "rollback", "status"}, action) {
		return errors.New("unknown action")
	}
	timeout, err := time.ParseDuration(args[4])
	if err != nil || timeout <= 0 {
		return errors.New("positive timeout required")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var m Manifest
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&m); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("trailing manifest data")
	}
	if m.SwitchID == "" || len(m.Tasks) == 0 || strings.TrimSpace(m.Coverage) == "" || len(m.Snapshot) != 64 || !filepath.IsAbs(m.Executable) || !filepath.IsAbs(m.StateDirectory) || !filepath.IsAbs(m.Socket) || !filepath.IsAbs(journal) {
		return errors.New("complete verified manifest and absolute operator paths required")
	}
	if digest, e := hex.DecodeString(m.Snapshot); e != nil || len(digest) != 32 {
		return errors.New("invalid import snapshot digest")
	}
	slices.Sort(m.Tasks)
	if len(slices.Compact(slices.Clone(m.Tasks))) != len(m.Tasks) {
		return errors.New("duplicate tasks")
	}
	client, err := clientapi.DialWithCapabilities(ctx, socket, "operator", []protocol.Capability{{Name: "work", Version: "1.0"}})
	if err != nil {
		return err
	}
	defer client.Close()
	call := func(ctx context.Context, q protocol.WorkRequest) (protocol.WorkResult, error) {
		id, err := client.Work(ctx, q)
		if err != nil {
			return protocol.WorkResult{}, err
		}
		e, err := client.Receive(ctx)
		if err != nil {
			return protocol.WorkResult{}, err
		}
		if e.ReplyTo == nil || *e.ReplyTo != id {
			return protocol.WorkResult{}, errors.New("uncorrelated Core reply")
		}
		if e.Error != nil {
			return protocol.WorkResult{}, &clientapi.Refusal{Cause: *e.Error}
		}
		v, ok := e.Payload.(protocol.WorkResult)
		if !ok {
			return v, errors.New("unexpected Core reply")
		}
		return v, nil
	}
	if action == "approve" || action == "status" {
		q := protocol.WorkRequest{Operation: "switch_status", SwitchID: m.SwitchID}
		if action == "approve" {
			digest := m.Digest()
			q.Operation = "approve_switch"
			q.Tasks = &m.Tasks
			q.Snapshot = &digest
		}
		v, err := call(ctx, q)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(v)
	}
	owner := proc.NewOwner()
	defer owner.Close(context.Background())
	command := func(ctx context.Context, action string) ([]byte, error) {
		var stdout, stderr strings.Builder
		args := []string{"--state", m.StateDirectory, "--socket", m.Socket, "--json", "service", action}
		err := owner.Run(ctx, lease.New(func() {}), proc.Command{Path: m.Executable, Args: args, Env: []string{"PATH=/usr/bin:/bin", "HOME=" + os.Getenv("HOME")}, Stdout: &stdout, Stderr: &stderr})
		if err != nil {
			return nil, fmt.Errorf("external %s failed: %w", action, err)
		}
		if action != "status" {
			var reply struct {
				OK bool `json:"ok"`
			}
			if json.Unmarshal([]byte(stdout.String()), &reply) != nil || !reply.OK {
				return nil, fmt.Errorf("external %s refused or unknown", action)
			}
		}
		return []byte(stdout.String()), nil
	}
	p := Procedure{Manifest: m, Journal: journal, Core: call, Command: command}
	if action == "apply" {
		err = p.Apply(ctx)
	} else {
		err = p.Rollback(ctx)
	}
	if err != nil {
		return err
	}
	v, err := call(ctx, protocol.WorkRequest{Operation: "switch_status", SwitchID: m.SwitchID})
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(v)
}
