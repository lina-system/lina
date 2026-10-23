// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"strings"
	"time"
)

// "Local" is a process alias, not a portable timezone. Resolve the client's OS
// zone when Go did not retain an IANA name, and never delegate it to Core's TZ.
func recordZone(now time.Time, readlink func(string) (string, error), readfile func(string) ([]byte, error)) (string, bool) {
	accept := func(name string) (string, bool) {
		name = strings.TrimSpace(strings.TrimPrefix(name, ":"))
		if name == "" || name == "Local" || strings.HasPrefix(name, "/") {
			return "", false
		}
		zone, err := time.LoadLocation(name)
		if err != nil {
			return "", false
		}
		_, want := now.Zone()
		_, got := now.In(zone).Zone()
		return name, want == got
	}
	if name, ok := accept(now.Location().String()); ok {
		return name, true
	}
	if path, err := readlink("/etc/localtime"); err == nil {
		if _, name, ok := strings.Cut(path, "zoneinfo/"); ok {
			name = strings.TrimPrefix(strings.TrimPrefix(name, "posix/"), "right/")
			if zone, ok := accept(name); ok {
				return zone, true
			}
		}
	}
	if data, err := readfile("/etc/timezone"); err == nil && len(data) < 256 {
		return accept(string(data))
	}
	return "", false
}
