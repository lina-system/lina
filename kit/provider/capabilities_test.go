// SPDX-License-Identifier: Apache-2.0
package provider_test

import (
	"github.com/thisisjun786/lina/kit/provider"
	"strings"
	"testing"
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
