// SPDX-License-Identifier: Apache-2.0
package grant

import (
	"errors"
	"testing"
	"time"
)

func fixture() (Grant, Request, Environment) {
	r := Request{GrantID: "grant", MainID: "main", NodeID: "node", NodeBootID: "boot", Epoch: 7, TaskID: "task", CommandID: "command", Host: "host", OSUser: "user", GUISession: "session", Profile: "profile", Person: "person", Agent: "agent", Tool: "edit", Target: "document", Revision: "r1", Write: true}
	g := Grant{Identity: r, Expires: time.Unix(200, 0), Scope: []Permission{{Tool: "edit", Target: "document", Write: true}}, Revisions: map[string]string{"document": "r1"}}
	return g, r, Environment{Epoch: 7, Now: time.Unix(100, 0), OSPermitted: true, LiveSession: "session", Revision: "r1"}
}

func TestCheckFailsClosedPerField(t *testing.T) {
	g, r, e := fixture()
	if err := Check(g, r, e); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		code   Code
		mutate func(*Grant, *Request, *Environment)
	}{
		{"grant", ErrGrant, func(g *Grant, r *Request, e *Environment) { r.GrantID = "other" }},
		{"main", ErrMain, func(g *Grant, r *Request, e *Environment) { r.MainID = "other" }},
		{"node", ErrNode, func(g *Grant, r *Request, e *Environment) { r.NodeID = "other" }},
		{"boot", ErrNodeBoot, func(g *Grant, r *Request, e *Environment) { r.NodeBootID = "other" }},
		{"epoch", ErrEpoch, func(g *Grant, r *Request, e *Environment) { e.Epoch++ }},
		{"task", ErrTask, func(g *Grant, r *Request, e *Environment) { r.TaskID = "other" }},
		{"command", ErrCommand, func(g *Grant, r *Request, e *Environment) { r.CommandID = "other" }},
		{"host", ErrHost, func(g *Grant, r *Request, e *Environment) { r.Host = "other" }},
		{"user", ErrOSUser, func(g *Grant, r *Request, e *Environment) { r.OSUser = "other" }},
		{"session", ErrSession, func(g *Grant, r *Request, e *Environment) { e.LiveSession = "other" }},
		{"profile", ErrProfile, func(g *Grant, r *Request, e *Environment) { r.Profile = "other" }},
		{"person", ErrPerson, func(g *Grant, r *Request, e *Environment) { r.Person = "other" }},
		{"agent", ErrAgent, func(g *Grant, r *Request, e *Environment) { r.Agent = "other" }},
		{"scope", ErrScope, func(g *Grant, r *Request, e *Environment) { r.Target = "other" }},
		{"revision", ErrStaleRevision, func(g *Grant, r *Request, e *Environment) { e.Revision = "r2" }},
		{"revoked", ErrRevoked, func(g *Grant, r *Request, e *Environment) { g.Revoked = true }},
		{"expiry", ErrExpired, func(g *Grant, r *Request, e *Environment) { e.Now = g.Expires }},
		{"os", ErrOSPermission, func(g *Grant, r *Request, e *Environment) { e.OSPermitted = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, r, e := fixture()
			tc.mutate(&g, &r, &e)
			if err := Check(g, r, e); !errors.Is(err, tc.code) {
				t.Fatalf("got %v want %v", err, tc.code)
			}
		})
	}
	if err := Check(Grant{}, Request{}, Environment{}); err == nil {
		t.Fatal("zero authority accepted")
	}
}

func TestLocalWorkerAndReadScope(t *testing.T) {
	g, r, e := fixture()
	r.NodeID = ""
	r.NodeBootID = ""
	r.GUISession = ""
	r.Profile = ""
	g.Identity = r
	e.LiveSession = ""
	if err := Check(g, r, e); err != nil {
		t.Fatal(err)
	}
	g.Scope[0].Write = false
	if err := Check(g, r, e); !errors.Is(err, ErrScope) {
		t.Fatal(err)
	}
	r.Write = false
	if err := Check(g, r, e); err != nil {
		t.Fatal(err)
	}
}
