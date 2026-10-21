// SPDX-License-Identifier: Apache-2.0

// Package grant implements the shared Core and Node authorization check.
package grant

import "time"

// Code is a closed denial reason. Callers must never infer authority from prose.
type Code string

const (
	ErrGrant         Code = "invalid_grant"
	ErrMain          Code = "wrong_main"
	ErrNode          Code = "wrong_node"
	ErrNodeBoot      Code = "wrong_node_boot"
	ErrEpoch         Code = "stale_epoch"
	ErrTask          Code = "wrong_task"
	ErrCommand       Code = "wrong_command"
	ErrHost          Code = "wrong_host"
	ErrOSUser        Code = "wrong_os_user"
	ErrSession       Code = "wrong_session"
	ErrProfile       Code = "wrong_profile"
	ErrPerson        Code = "wrong_person"
	ErrAgent         Code = "wrong_agent"
	ErrScope         Code = "outside_scope"
	ErrStaleRevision Code = "stale_revision"
	ErrRevoked       Code = "revoked_grant"
	ErrExpired       Code = "expired_grant"
	ErrOSPermission  Code = "os_permission_denied"
)

func (c Code) Error() string { return string(c) }

// Empty optional identities mean explicitly none, never a wildcard. Target and
// tool names are canonical identifiers supplied by the execution adapter.
type Request struct {
	GrantID, MainID, NodeID, NodeBootID                                 string
	Epoch                                                               uint64
	TaskID, CommandID, Host, OSUser, GUISession, Profile, Person, Agent string
	Tool, Target, Revision                                              string
	Write                                                               bool
}
type Permission struct {
	Tool, Target string
	Write        bool
}
type Grant struct {
	Identity  Request
	Expires   time.Time
	Revoked   bool
	Scope     []Permission
	Revisions map[string]string
}

// Environment comes from the executor, never the request or model arguments.
type Environment struct {
	Epoch                 uint64
	Now                   time.Time
	OSPermitted           bool
	LiveSession, Revision string
}

func Check(g Grant, r Request, e Environment) error {
	i := g.Identity
	for _, c := range []struct {
		bad  bool
		code Code
	}{
		{i.GrantID == "" || r.GrantID != i.GrantID, ErrGrant},
		{i.MainID == "" || r.MainID != i.MainID, ErrMain},
		{r.NodeID != i.NodeID, ErrNode},
		{r.NodeBootID != i.NodeBootID || (i.NodeID == "") != (i.NodeBootID == ""), ErrNodeBoot},
		{i.Epoch == 0 || r.Epoch != i.Epoch || e.Epoch != i.Epoch, ErrEpoch},
		{i.TaskID == "" || r.TaskID != i.TaskID, ErrTask},
		{r.CommandID != i.CommandID, ErrCommand},
		{i.Host == "" || r.Host != i.Host, ErrHost},
		{i.OSUser == "" || r.OSUser != i.OSUser, ErrOSUser},
		{r.GUISession != i.GUISession || e.LiveSession != i.GUISession, ErrSession},
		{r.Profile != i.Profile, ErrProfile},
		{i.Person == "" || r.Person != i.Person, ErrPerson},
		{r.Agent != i.Agent, ErrAgent},
		{g.Revoked, ErrRevoked},
		{e.Now.IsZero() || g.Expires.IsZero() || !e.Now.Before(g.Expires), ErrExpired},
		{!e.OSPermitted, ErrOSPermission},
	} {
		if c.bad {
			return c.code
		}
	}
	allowed := false
	for _, p := range g.Scope {
		if p.Tool != "" && p.Target != "" && p.Tool == r.Tool && p.Target == r.Target && (!r.Write || p.Write) {
			allowed = true
			break
		}
	}
	if !allowed {
		return ErrScope
	}
	revision, tracked := g.Revisions[r.Target]
	if (r.Write || tracked) && (revision == "" || r.Revision != revision || e.Revision != revision) {
		return ErrStaleRevision
	}
	return nil
}
