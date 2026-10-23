// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"time"

	"github.com/thisisjun786/lina/kit/provider"
)

// CacheCold reports whether a turn boundary may rewrite history before the
// trigger because the conversation's cache has expired anyway. The expiry is
// the lifetime the route's capability set gives; a route that declares none, or
// whose lifetime the route admission does not accept, has an unknown expiry and
// never counts as expired.
func CacheCold(supported, admitted provider.CapabilitySet, last time.Time, recorded bool, now time.Time) bool {
	lifetime, known := provider.UsableCacheLifetime(supported, admitted)
	return known && CacheExpired(last, recorded, lifetime, now)
}

// CacheExpired decides whether a conversation's cache can no longer serve its
// history. recorded is false when the conversation has no earlier request:
// there is no cache to lose. lifetime is the cache lifetime the route's
// capability set gives, and zero when the route declares none. A request time
// ahead of now (clock skew) is not treated as expired.
func CacheExpired(last time.Time, recorded bool, lifetime time.Duration, now time.Time) bool {
	if !recorded {
		return true
	}
	if lifetime <= 0 || last.After(now) {
		return false
	}
	return now.Sub(last) >= lifetime
}
