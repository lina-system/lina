// SPDX-License-Identifier: Apache-2.0
package secrets

import (
	"os"
	"testing"
)

func TestContainsKeepsCredentialOpaque(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := OpenFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	key := Key{Person: "owner", Service: "remote", Name: "token"}
	if err = s.Put(key, []byte("fixture-secret-value")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		body []byte
		want bool
	}{{[]byte("ordinary result"), false}, {[]byte("echo fixture-secret-value"), true}, {append([]byte{0xff, 0x00}, []byte("fixture-secret-value")...), true}} {
		got, err := s.Contains(key, tc.body)
		if err != nil || got != tc.want {
			t.Fatalf("contains=%v error=%v", got, err)
		}
	}
}
