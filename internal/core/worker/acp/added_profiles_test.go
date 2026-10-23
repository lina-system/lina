// SPDX-License-Identifier: Apache-2.0
package acp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestAddedProfileValidation(t *testing.T) {
	valid := `{"name":"my-agent","command":"agent","args":["acp","literal; $HOME",""]}`
	for _, tc := range []struct{ name, profile, cause string }{
		{"missing-command", `{"name":"bad"}`, "command is required"},
		{"empty-command", `{"name":"bad","command":" "}`, "command is required"},
		{"relative-command", `{"name":"bad","command":"./agent"}`, "absolute path"},
		{"bad-name", `{"name":"Gemini","command":"agent"}`, "name must"},
		{"reserved", `{"name":"codex","command":"agent"}`, "name is reserved"},
		{"wrong-args", `{"name":"bad","command":"agent","args":"--acp"}`, "invalid fields"},
		{"null-args", `{"name":"bad","command":"agent","args":null}`, "invalid fields"},
		{"null-arg", `{"name":"bad","command":"agent","args":[null]}`, "invalid fields"},
		{"nul-arg", `{"name":"bad","command":"agent","args":["\u0000"]}`, "argument"},
		{"nul-command", `{"name":"bad","command":"agent\u0000"}`, "command"},
		{"verified-override", `{"name":"bad","command":"agent","verifiedVersions":["9"]}`, "invalid fields"},
		{"environment-override", `{"name":"bad","command":"agent","environment":["SECRET=private"]}`, "invalid fields"},
		{"null-entry", `null`, "invalid fields"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profiles, problems := ParseAddedProfiles([]byte(`{"version":1,"profiles":[`+tc.profile+`,`+valid+`]}`), []string{"codex"})
			if len(profiles) != 1 || profiles[0].Name != "my-agent" || len(problems) != 1 || !strings.Contains(problems[0].Error(), tc.cause) {
				t.Fatalf("profiles=%+v problems=%v", profiles, problems)
			}
			p := profiles[0]
			if !reflect.DeepEqual(p.Args, []string{"acp", "literal; $HOME", ""}) || p.VersionCommand != "" || len(p.VerifiedVersions) != 0 || len(p.Environment) != 0 {
				t.Fatal("added profile changed worker settings or verification", p)
			}
			if strings.Contains(problems[0].Error(), "SECRET") {
				t.Fatal("diagnostic echoed configuration")
			}
		})
	}
}

func TestAddedProfileDocument(t *testing.T) {
	for _, raw := range []string{``, `null`, `{}`, `{"version":2,"profiles":[]}`, `{"version":1}`, `{"version":1,"profiles":null}`, `{"version":1,"profiles":[]} {}`, `{"version":1,"profiles":[],"secret":"do-not-log"}`, strings.Repeat(" ", 64<<10+1)} {
		profiles, problems := ParseAddedProfiles([]byte(raw), nil)
		if len(profiles) != 0 || len(problems) == 0 {
			t.Fatalf("invalid document accepted: %q: %+v %v", raw[:min(len(raw), 80)], profiles, problems)
		}
		for _, problem := range problems {
			if strings.Contains(problem.Error(), "do-not-log") {
				t.Fatal("diagnostic leaked config")
			}
		}
	}
	profiles, problems := ParseAddedProfiles([]byte(`{"version":1,"profiles":[]}`), nil)
	if len(profiles) != 0 || len(problems) != 0 {
		t.Fatal("empty settings refused", profiles, problems)
	}
}

func TestAddedProfileDuplicatesAndLimits(t *testing.T) {
	profiles, problems := ParseAddedProfiles([]byte(`{"version":1,"profiles":[{"name":"same","command":"first"},{"name":"same","command":"second"}]}`), nil)
	if len(profiles) != 0 || len(problems) != 2 {
		t.Fatal("duplicate identity selected by order", profiles, problems)
	}
	for _, problem := range problems {
		if !strings.Contains(problem.Error(), "duplicate name") {
			t.Fatal(problem)
		}
	}
	for _, args := range [][]string{make([]string, 129), {strings.Repeat("x", 4097)}} {
		raw, _ := json.Marshal(map[string]any{"version": 1, "profiles": []any{map[string]any{"name": "large", "command": "agent", "args": args}}})
		if profiles, problems := ParseAddedProfiles(raw, nil); len(profiles) != 0 || len(problems) != 1 {
			t.Fatal("oversized args accepted", profiles, problems)
		}
	}
}

func TestAddedProfileOptionalArgsAndAbsoluteCommand(t *testing.T) {
	profiles, problems := ParseAddedProfiles([]byte(`{"version":1,"profiles":[{"name":"no-args","command":"/tools/my agent"}]}`), nil)
	if len(problems) != 0 || len(profiles) != 1 || profiles[0].Command != "/tools/my agent" || len(profiles[0].Args) != 0 {
		t.Fatal("command or optional args changed", profiles, problems)
	}
	var entries []any
	for range 129 {
		entries = append(entries, map[string]string{"name": "same", "command": "agent"})
	}
	raw, _ := json.Marshal(map[string]any{"version": 1, "profiles": entries})
	if profiles, problems := ParseAddedProfiles(raw, nil); len(profiles) != 0 || len(problems) != 1 || !strings.Contains(problems[0].Error(), "at most 128") {
		t.Fatal("profile count unbounded", profiles, problems)
	}
}
