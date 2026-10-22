// SPDX-License-Identifier: Apache-2.0

package proc

import (
	"encoding/binary"
	"testing"
)

func TestProcessTokenOnlyMatchesEnvironment(t *testing.T) {
	const token = "LINA_PROC_OWNER=synthetic-owner"
	for _, tc := range []struct {
		name string
		argc uint32
		body string
		want bool
	}{
		{"inherited", 2, "/bin/tool\x00\x00tool\x00arg\x00PATH=/bin\x00" + token + "\x00\x00", true},
		{"argv impersonation", 2, "/bin/tool\x00\x00tool\x00" + token + "\x00PATH=/bin\x00\x00", false},
		{"token prefix", 1, "/bin/tool\x00tool\x00" + token + "-other\x00\x00", false},
		{"another owner", 1, "/bin/tool\x00tool\x00LINA_PROC_OWNER=other\x00\x00", false},
		{"cleared environment", 1, "/bin/tool\x00tool\x00\x00", false},
		{"truncated arguments", 2, "/bin/tool\x00tool\x00", false},
		{"unterminated environment", 1, "/bin/tool\x00tool\x00" + token, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := binary.LittleEndian.AppendUint32(nil, tc.argc)
			data = append(data, tc.body...)
			if got := hasProcessToken(data, token); got != tc.want {
				t.Fatalf("match=%v, want %v", got, tc.want)
			}
		})
	}
}
