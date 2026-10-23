// SPDX-License-Identifier: Apache-2.0
package assemble

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/thisisjun786/lina/kit/provider"
)

func identity(parts ...string) string {
	b, _ := json.Marshal(parts)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func ConversationIdentity(main, agent, conversation string) string {
	return identity("lina-cache-conversation-v1", main, agent, conversation)
}
func RunIdentity(run string) string { return provider.RunCacheIdentity(run) }

// SelectCapabilities is pure Core policy: no model output, clock or network
// discovery selects optimizations. Unsupported or unadmitted choices stay absent.
func SelectCapabilities(r provider.Request, supported, admitted provider.CapabilitySet) (provider.CapabilityUse, error) {
	var u provider.CapabilityUse
	has := func(c provider.Capability) bool { return supported.Has(c) && admitted.Has(c) }
	u.PrefixCache = has(provider.PrefixCacheCapability)
	u.ReasoningState = has(provider.ReasoningStateCapability)
	if has(provider.CacheIdentityCapability) && r.CacheIdentity != "" {
		u.CacheIdentity = r.CacheIdentity
	}
	if has(provider.CacheMarksCapability) && r.Layout != nil {
		lifetime := provider.CacheLifetime("")
		if has(provider.CacheLifetime1hCapability) {
			lifetime = provider.Cache1Hour
		}
		for _, p := range []*provider.Position{&r.Layout.Fixed, r.Layout.PreviousInput, &r.Layout.CurrentInput, r.Layout.Newest} {
			if p == nil {
				continue
			}
			duplicate := false
			for _, mark := range u.CacheMarks {
				if mark.Position == *p {
					duplicate = true
				}
			}
			if !duplicate {
				u.CacheMarks = append(u.CacheMarks, provider.CacheMark{Position: *p, Lifetime: lifetime})
			}
		}
	}
	r.Capabilities = u
	return u, provider.ValidateUse(r, supported, admitted)
}
