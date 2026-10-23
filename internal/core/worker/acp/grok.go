// SPDX-License-Identifier: Apache-2.0
package acp

import (
	"encoding/json"
	"strings"
	"unicode"

	wire "github.com/thisisjun786/lina/kit/acp"
)

// Handshake is an observation, not the adapter's implemented capability set.
// It cannot retain host details, authentication data or vendor metadata.
type Handshake struct {
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities"`
}

func grokHandshake(in wire.InitializeResponse) *Handshake {
	out := &Handshake{Version: "unknown", Capabilities: []string{}}
	if in.Meta != nil {
		var version string
		if json.Unmarshal((*in.Meta)["agentVersion"], &version) == nil {
			version = strings.TrimSpace(version)
			if version != "" && len(version) <= 128 && strings.IndexFunc(version, unicode.IsControl) < 0 {
				out.Version = version
			}
		}
	}
	c := in.AgentCapabilities
	if c == nil {
		return out
	}
	add := func(name string, supported bool) {
		if supported {
			out.Capabilities = append(out.Capabilities, name)
		}
	}
	enabled := func(value *bool) bool { return value != nil && *value }
	add("session/load", enabled(c.LoadSession))
	if s := c.SessionCapabilities; s != nil {
		add("session/list", s.List != nil)
		add("session/resume", s.Resume != nil)
		add("session/close", s.Close != nil)
		add("session/delete", s.Delete != nil)
		add("session/additionalDirectories", s.AdditionalDirectories != nil)
	}
	if p := c.PromptCapabilities; p != nil {
		add("prompt/image", enabled(p.Image))
		add("prompt/audio", enabled(p.Audio))
		add("prompt/embeddedContext", enabled(p.EmbeddedContext))
	}
	if m := c.McpCapabilities; m != nil {
		add("mcp/http", enabled(m.Http))
		add("mcp/sse", enabled(m.Sse))
	}
	if a := c.Auth; a != nil {
		add("auth/logout", a.Logout != nil)
	}
	return out
}
