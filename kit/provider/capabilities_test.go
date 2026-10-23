// SPDX-License-Identifier: Apache-2.0
package provider_test

import (
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/provider"
)

func TestCapabilityAdmissionAndValues(t *testing.T) {
	if (provider.CapabilitySet{"unknown"}).Valid() || (provider.CapabilitySet{provider.CacheMarksCapability, provider.CacheMarksCapability}).Valid() {
		t.Fatal("invalid set accepted")
	}
	if len(provider.Admitted("unknown")) != 0 {
		t.Fatal("unknown route admitted")
	}
	caps := provider.Admitted("chatgpt")
	caps[0] = provider.ServerStateCapability
	if provider.Admitted("chatgpt").Has(provider.ServerStateCapability) {
		t.Fatal("admission aliases caller")
	}
	p := provider.Position{Items: 1}
	req := provider.Request{Items: []provider.Item{[]byte(`{}`), []byte(`{}`)}, CacheIdentity: strings.Repeat("a", 64), Layout: &provider.RequestLayout{Fixed: provider.Position{}, CurrentInput: p}}
	supported := provider.CapabilitySet{provider.CacheIdentityCapability, provider.CacheMarksCapability, provider.CacheLifetime1hCapability}
	for _, use := range []provider.CapabilityUse{
		{CacheIdentity: "name"},
		{CacheIdentity: strings.Repeat("b", 64)},
		{CacheMarks: []provider.CacheMark{{Position: p, Lifetime: "5m"}}},
		{CacheMarks: []provider.CacheMark{{Position: p}, {Position: p}}},
		{CacheMarks: []provider.CacheMark{{Position: provider.Position{Items: 2}}}},
	} {
		req.Capabilities = use
		if provider.ValidateUse(req, supported, supported) == nil {
			t.Fatalf("accepted %+v", use)
		}
	}
	req.Capabilities = provider.CapabilityUse{CacheIdentity: req.CacheIdentity, CacheMarks: []provider.CacheMark{{Position: p, Lifetime: provider.Cache1Hour}}}
	if err := provider.ValidateUse(req, supported, supported); err != nil {
		t.Fatal(err)
	}
	if err := provider.ValidateUse(req, supported, nil); err == nil {
		t.Fatal("unadmitted use accepted")
	}
}

func TestUsableCacheLifetimeNeedsDeclarationAndAdmission(t *testing.T) {
	for _, c := range []struct {
		name      string
		supported provider.CapabilitySet
		admitted  provider.CapabilitySet
		want      time.Duration
		known     bool
	}{
		{"one hour", provider.CapabilitySet{provider.CacheLifetime1hCapability}, provider.CapabilitySet{provider.CacheLifetime1hCapability}, time.Hour, true},
		{"five minutes", provider.CapabilitySet{provider.CacheLifetime5mCapability}, provider.CapabilitySet{provider.CacheLifetime5mCapability}, 5 * time.Minute, true},
		{"both prefers one hour", provider.CapabilitySet{provider.CacheLifetime1hCapability, provider.CacheLifetime5mCapability}, provider.CapabilitySet{provider.CacheLifetime1hCapability, provider.CacheLifetime5mCapability}, time.Hour, true},
		{"declared only", provider.CapabilitySet{provider.CacheLifetime1hCapability}, nil, 0, false},
		{"admitted only", nil, provider.CapabilitySet{provider.CacheLifetime1hCapability}, 0, false},
		{"none", nil, nil, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, known := provider.UsableCacheLifetime(c.supported, c.admitted)
			if got != c.want || known != c.known {
				t.Fatalf("UsableCacheLifetime=%v,%v want %v,%v", got, known, c.want, c.known)
			}
		})
	}
}
