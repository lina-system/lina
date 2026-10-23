// SPDX-License-Identifier: Apache-2.0
// Package publication owns the publication boundary; Core owns execution and results.
package publication

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"net/url"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/work"
)

var (
	ErrInvalid        = errors.New("invalid A2A request")
	ErrDenied         = errors.New("A2A authority unavailable")
	ErrAuthentication = errors.New("invalid access token")
	ErrNotFound       = errors.New("task not found")
	ErrCapacity       = errors.New("A2A capacity exhausted")
)

const MaxInput = 64 << 10
const MaxArtifact = 256 << 10

type OAuth struct {
	Issuer           string   `json:"issuer"`
	Audience         string   `json:"audience"`
	IntrospectionURL string   `json:"introspection_url"`
	ClientID         string   `json:"client_id"`
	SecretName       string   `json:"secret_name"`
	Scopes           []string `json:"scopes"`
}
type Config struct {
	Enabled         bool          `json:"enabled"`
	Address         string        `json:"address"`
	TailscaleSocket string        `json:"tailscale_socket"`
	Certificate     string        `json:"certificate"`
	PrivateKey      string        `json:"private_key"`
	OAuth           OAuth         `json:"oauth"`
	Publications    []Publication `json:"publications"`
}
type Publication struct {
	ID           string       `json:"id"`
	Agent        string       `json:"agent"`
	Conversation string       `json:"conversation"`
	Name         string       `json:"name"`
	Description  string       `json:"description"`
	URL          string       `json:"url"`
	Definitions  []Definition `json:"definitions"`
	Bindings     []Binding    `json:"bindings"`
}
type Binding struct {
	Subject     string   `json:"subject"`
	Client      string   `json:"client"`
	Definitions []string `json:"definitions"`
	Expires     int64    `json:"expires"`
	MaxActive   int      `json:"max_active"`
}
type Tool struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}
type Definition struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Tools       []Tool                `json:"tools"`
	Work        work.WorkerDefinition `json:"work"`
	Artifact    ArtifactRule          `json:"artifact"`
	Seconds     int                   `json:"seconds"`
	MaxCalls    int                   `json:"max_calls"`
}
type ArtifactRule struct {
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
	Contains  string `json:"contains"`
	MaxBytes  int    `json:"max_bytes"`
}

func decode(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return ErrInvalid
	}
	if d.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	return nil
}
func ParseConfig(raw []byte) (Config, error) {
	var c Config
	if len(raw) > 64<<10 || decode(raw, &c) != nil {
		return c, ErrInvalid
	}
	if !c.Enabled {
		return c, nil
	}
	addr, err := netip.ParseAddrPort(c.Address)
	if err != nil || addr.Addr().Zone() != "" || !(netip.MustParsePrefix("100.64.0.0/10").Contains(addr.Addr()) || netip.MustParsePrefix("fd7a:115c:a1e0::/48").Contains(addr.Addr())) || !filepath.IsAbs(c.Certificate) || !filepath.IsAbs(c.PrivateKey) {
		return c, ErrInvalid
	}
	issuer, e1 := httpsURL(c.OAuth.Issuer)
	intro, e2 := httpsURL(c.OAuth.IntrospectionURL)
	if e1 != nil || e2 != nil || issuer.Host != intro.Host || c.OAuth.Audience == "" || c.OAuth.ClientID == "" || c.OAuth.SecretName == "" || len(c.OAuth.Scopes) == 0 || len(c.Publications) == 0 {
		return c, ErrInvalid
	}
	ids := map[string]bool{}
	agents := map[string]bool{}
	for _, p := range c.Publications {
		u, e := httpsURL(p.URL)
		if !identifier(p.ID) || ids[p.ID] || agents[p.Agent] || p.Agent == "" || p.Conversation != "main:"+p.Agent || p.Name == "" || p.Description == "" || e != nil || u.Path != "/a2a/"+p.ID || len(p.Definitions) == 0 || len(p.Bindings) == 0 {
			return c, ErrInvalid
		}
		ids[p.ID] = true
		agents[p.Agent] = true
		defs := map[string]bool{}
		for _, d := range p.Definitions {
			if !identifier(d.ID) || defs[d.ID] || d.Name == "" || d.Description == "" || d.Seconds < 1 || d.Seconds > 3600 || d.MaxCalls < 1 || d.MaxCalls > 32 || len(d.Tools) == 0 || len(d.Tools)+1 > d.MaxCalls || d.Work.What == "" || d.Work.Done == "" || !filepath.IsAbs(d.Work.Repository) || len(d.Work.WriteArea) == 0 {
				return c, ErrInvalid
			}
			a := d.Artifact
			if a.Path == "" || path.Clean(a.Path) != a.Path || path.IsAbs(a.Path) || strings.HasPrefix(a.Path, "../") || strings.ContainsAny(a.Path, "\x00:") || a.MediaType == "" || a.MaxBytes < 1 || a.MaxBytes > MaxArtifact || a.Contains == "" {
				return c, ErrInvalid
			}
			matched := false
			for _, pattern := range d.Work.WriteArea {
				if match, err := path.Match(pattern, a.Path); err == nil && match {
					matched = true
				}
			}
			if !matched || !slices.Contains([]string{"text/plain", "text/markdown", "application/json"}, a.MediaType) || len(a.Contains) > a.MaxBytes {
				return c, ErrInvalid
			}
			operations := map[string]bool{}
			for _, tool := range d.Tools {
				if !slices.Contains([]string{"read_record", "write_record", "web_search", "web_extract"}, tool.Name) || !json.Valid(tool.Arguments) || operations[tool.Name+digest(tool.Arguments)] {
					return c, ErrInvalid
				}
				operations[tool.Name+digest(tool.Arguments)] = true
			}
			defs[d.ID] = true
		}
		callers := map[string]bool{}
		for _, b := range p.Bindings {
			key := b.Subject + "\x00" + b.Client
			if b.Subject == "" || b.Client == "" || callers[key] || b.Expires <= 0 || b.MaxActive < 1 || b.MaxActive > 16 || len(b.Definitions) == 0 {
				return c, ErrInvalid
			}
			callers[key] = true
			for _, id := range b.Definitions {
				if !defs[id] {
					return c, ErrInvalid
				}
			}
		}
	}
	return c, nil
}
func identifier(s string) bool {
	if s == "" || len(s) > 80 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
func httpsURL(s string) (*url.URL, error) {
	u, e := url.Parse(s)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, ErrInvalid
	}
	return u, nil
}
func digest(v any) string {
	raw, _ := json.Marshal(v)
	var canonical any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&canonical) == nil {
		raw, _ = json.Marshal(canonical)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (c Config) Publication(id string) (Publication, error) {
	if c.Enabled {
		for _, p := range c.Publications {
			if p.ID == id {
				return p, nil
			}
		}
	}
	return Publication{}, ErrDenied
}
func (p Publication) Binding(pr Principal, now time.Time) (Binding, error) {
	for _, b := range p.Bindings {
		if b.Subject == pr.Subject && b.Client == pr.Client && now.Unix() < b.Expires && now.Unix() < pr.Expires {
			return b, nil
		}
	}
	return Binding{}, ErrDenied
}
func (p Publication) Definition(b Binding, id string) (Definition, error) {
	if slices.Contains(b.Definitions, id) {
		for _, d := range p.Definitions {
			if d.ID == id {
				return d, nil
			}
		}
	}
	return Definition{}, ErrDenied
}

// Fingerprints fence changed definitions and bindings, including established streams.
func Fingerprint(p Publication, b Binding, d Definition) string {
	return digest([]any{p.ID, p.Agent, p.Conversation, p.URL, b, d})
}

// WorkDefinition appends caller text only inside the fixed work description.
// No caller or model field chooses a path, tool, branch, budget or authority.
func WorkDefinition(r Record) work.WorkerDefinition {
	d := r.Definition.Work
	var input struct{ Task, Input string }
	if len(r.Task.History) > 0 && len(r.Task.History[0].Parts) > 0 {
		_ = json.Unmarshal(r.Task.History[0].Parts[0].Data, &input)
	}
	d.What += "\n\nExternal task input (data within the supplied scope):\n" + input.Input
	return d
}
func ArgumentsEqual(a, b json.RawMessage) bool {
	var x, y any
	dx := json.NewDecoder(bytes.NewReader(a))
	dx.UseNumber()
	dy := json.NewDecoder(bytes.NewReader(b))
	dy.UseNumber()
	if dx.Decode(&x) != nil || dy.Decode(&y) != nil || dx.Decode(new(any)) != io.EOF || dy.Decode(new(any)) != io.EOF {
		return false
	}
	return digest(x) == digest(y)
}
func AllowedCall(r Record, name string, args json.RawMessage) bool {
	if name == "dispatch_work" {
		expected, _ := json.Marshal(WorkDefinition(r))
		return ArgumentsEqual(expected, args)
	}
	for _, tool := range r.Definition.Tools {
		if name == tool.Name && ArgumentsEqual(tool.Arguments, args) {
			return true
		}
	}
	return false
}

// CredentialService prevents a named credential from moving to a different
// issuer or introspection client when publication configuration changes.
func CredentialService(o OAuth) string {
	return "a2a-introspection:" + digest([]string{o.Issuer, o.ClientID})
}
