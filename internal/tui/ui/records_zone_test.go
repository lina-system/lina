// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"os"
	"testing"
	"time"
)

func TestRecordZoneResolvesClientOSWithoutCoreAlias(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 23, 0, 30, 0, 0, la)
	_, offset := now.Zone()
	local := now.In(time.FixedZone("Local", offset))
	missingLink := func(string) (string, error) { return "", os.ErrNotExist }
	missingFile := func(string) ([]byte, error) { return nil, os.ErrNotExist }
	for _, tc := range []struct {
		name string
		now  time.Time
		link string
		file string
		want string
		ok   bool
	}{
		{"named", now, "", "", "America/Los_Angeles", true},
		{"linux link", local, "/usr/share/zoneinfo/America/Los_Angeles", "", "America/Los_Angeles", true},
		{"mac link", local, "/var/db/timezone/zoneinfo/America/Los_Angeles", "", "America/Los_Angeles", true},
		{"copied zone", local, "", "America/Los_Angeles\n", "America/Los_Angeles", true},
		{"unresolved", local, "", "", "", false},
		{"mismatching zone", local, "/usr/share/zoneinfo/UTC", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			link, file := missingLink, missingFile
			if tc.link != "" {
				link = func(string) (string, error) { return tc.link, nil }
			}
			if tc.file != "" {
				file = func(string) ([]byte, error) { return []byte(tc.file), nil }
			}
			name, ok := recordZone(tc.now, link, file)
			if ok != tc.ok || ok && name != tc.want {
				t.Fatal(name, ok)
			}
		})
	}
}
