// SPDX-License-Identifier: Apache-2.0
package a2a

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/thisisjun786/lina/kit/tools"
)

func readBounded(r io.Reader, n int) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, int64(n)+1))
	if err != nil || len(b) > n {
		return nil, ErrInvalid
	}
	return b, nil
}
func (s *Service) fetchCard(ctx context.Context, c Connection) (Card, Interface, string, error) {
	var card Card
	if origin(c.CardURL) != c.Origin || s.Network.Check(ctx, c.CardURL, c.PrivateHost) != nil {
		return card, Interface{}, "", ErrDestination
	}
	req, err := http.NewRequestWithContext(ctx, "GET", c.CardURL, nil)
	if err != nil {
		return card, Interface{}, "", ErrInvalid
	}
	req.Header.Set("A2A-Version", "1.0")
	response, err := s.Network.client(c.Origin, c.PrivateHost).Do(req)
	if err != nil {
		return card, Interface{}, "", ErrDestination
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return card, Interface{}, "", ErrInvalid
	}
	raw, err := readBounded(response.Body, 256<<10)
	if err != nil || json.Unmarshal(raw, &card) != nil {
		return card, Interface{}, "", ErrInvalid
	}
	if card.Name == "" || !slices.Contains(card.InputModes, "text/plain") || len(card.OutputModes) == 0 {
		return card, Interface{}, "", ErrInvalid
	}
	for _, e := range card.Capabilities.Extensions {
		if e.Required {
			return card, Interface{}, "", ErrInvalid
		}
	}
	var chosen Interface
	for _, i := range card.Interfaces {
		if i.Binding == "JSONRPC" && i.Version == "1.0" && origin(i.URL) == c.Origin {
			chosen = i
			break
		}
	}
	if chosen.URL == "" || s.Network.Check(ctx, chosen.URL, c.PrivateHost) != nil {
		return card, chosen, "", ErrDestination
	}
	// Canonical JSON digest catches semantic changes without depending on whitespace.
	var canonical any
	if json.Unmarshal(raw, &canonical) != nil {
		return card, chosen, "", ErrInvalid
	}
	normalized, _ := json.Marshal(canonical)
	return card, chosen, hash(normalized), nil
}
func authentication(c Connection) (header, prefix string, err error) {
	if c.Scheme == "none" && len(c.Card.SecurityRequirements) == 0 {
		return "", "", nil
	}
	raw, ok := c.Card.SecuritySchemes[c.Scheme]
	if !ok {
		return "", "", ErrInvalid
	}
	var scheme struct {
		API   *struct{ Location, Name string } `json:"apiKeySecurityScheme"`
		HTTP  *struct{ Scheme string }         `json:"httpAuthSecurityScheme"`
		OAuth *json.RawMessage                 `json:"oauth2SecurityScheme"`
	}
	if json.Unmarshal(raw, &scheme) != nil {
		return "", "", ErrInvalid
	}
	admitted := false
	for _, req := range c.Card.SecurityRequirements {
		if scopes, ok := req.Schemes[c.Scheme]; ok && len(req.Schemes) == 1 {
			admitted = true
			for _, scope := range scopes.List {
				if c.OAuth == nil || !slices.Contains(c.OAuth.Scopes, scope) {
					return "", "", ErrInvalid
				}
			}
		}
	}
	if !admitted {
		return "", "", ErrInvalid
	}
	switch {
	case scheme.HTTP != nil && scheme.HTTP.Scheme == "Bearer":
		return "Authorization", "Bearer ", nil
	case scheme.API != nil && scheme.API.Location == "header":
		name := http.CanonicalHeaderKey(scheme.API.Name)
		if name == "" || strings.ContainsAny(name, " \t\r\n:") || slices.Contains([]string{"Host", "Cookie", "Content-Type", "Content-Length", "A2a-Version", "Connection", "Proxy-Authorization"}, name) {
			return "", "", ErrInvalid
		}
		return name, "", nil
	case scheme.OAuth != nil && c.OAuth != nil:
		return "Authorization", "Bearer ", nil
	default:
		return "", "", ErrInvalid
	}
}
func (s *Service) Configure(ctx context.Context, c Connection, credential []byte) (Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.Person == "" || c.ID == "" || len(c.ID) > 100 || strings.ContainsAny(c.ID, `:/\ `) || origin(c.Origin) != c.Origin || len(c.Profiles) == 0 || len(c.Profiles) > 32 {
		return Connection{}, ErrInvalid
	}
	if old, err := s.Connection(ctx, c.Person, c.ID); err == nil && old.Person != c.Person {
		return Connection{}, ErrScope
	}
	names := map[string]bool{}
	for _, p := range c.Profiles {
		digest, err := hex.DecodeString(p.SHA256)
		if p.Name == "" || names[p.Name] || p.MaxInput < 1 || p.MaxInput > 64<<10 || p.MaxOutput < 1 || p.MaxOutput > 1<<20 || err != nil || len(digest) != 32 || p.MediaType == "" {
			return Connection{}, ErrInvalid
		}
		switch p.Class {
		case tools.Research, tools.Calculation, tools.PrivateDraft, tools.ExternalEffect, tools.InvestmentOrder, tools.BindingExecution, tools.Unclassified:
		default:
			return Connection{}, ErrInvalid
		}
		names[p.Name] = true
	}
	var err error
	c.Card, c.Interface, c.CardHash, err = s.fetchCard(ctx, c)
	if err != nil {
		return Connection{}, err
	}
	for _, p := range c.Profiles {
		if !slices.Contains(c.Card.OutputModes, p.MediaType) {
			return Connection{}, ErrInvalid
		}
	}
	if _, _, err = authentication(c); err != nil {
		return Connection{}, err
	}
	if c.OAuth != nil {
		if err = s.validateOAuth(ctx, c); err != nil {
			return Connection{}, err
		}
	}
	if len(credential) > 0 {
		if c.OAuth != nil || c.Scheme == "none" || len(credential) > 8192 || strings.ContainsAny(string(credential), "\r\n") {
			return Connection{}, ErrInvalid
		}
		if err = s.Vault.Put(key(c), credential); err != nil {
			return Connection{}, ErrSignIn
		}
	}
	c.Enabled = true
	c.Revision = randomID()
	if err = s.saveConnection(ctx, c); err != nil {
		return Connection{}, err
	}
	return c, nil
}
func (s *Service) unchanged(ctx context.Context, o Origin, c Connection, p Profile) error {
	if err := s.allowed(ctx, o, c, p); err != nil {
		return err
	}
	_, i, h, err := s.fetchCard(ctx, c)
	if err != nil {
		return err
	}
	if h != c.CardHash || i != c.Interface {
		return ErrChanged
	}
	return s.allowed(ctx, o, c, p)
}
