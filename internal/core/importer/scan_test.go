// SPDX-License-Identifier: Apache-2.0
package importer

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestSecretScanPreservesOriginalBytesAndOffsets(t *testing.T) {
	body := []byte("한글 prefix token=\"synthetic-value\" end\n-----BEGIN PRIVATE KEY-----\nsynthetic bytes\n-----END PRIVATE KEY-----\nokay")
	original := bytes.Clone(body)
	spans := ScanSecrets(body)
	if len(spans) != 2 {
		t.Fatalf("spans=%v", spans)
	}
	for _, span := range spans {
		if span.Kind != "secret" || span.Start < 0 || span.End > len(body) || span.End <= span.Start {
			t.Fatal(span)
		}
	}
	if got := string(body[spans[0].Start:spans[0].End]); got != `"synthetic-value"` {
		t.Fatal("wrong byte offsets", got)
	}
	if !bytes.Equal(body, original) {
		t.Fatal("scan altered evidence")
	}
	if !reflect.DeepEqual(spans, ScanSecrets(body)) {
		t.Fatal("nondeterministic scan")
	}
	masked, err := Project(body, spans, false)
	if err != nil {
		t.Fatal(err)
	}
	derived, err := Project(body, spans, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range [][]byte{masked, derived} {
		if bytes.Contains(b, []byte("synthetic")) || bytes.Contains(b, []byte("PRIVATE KEY")) {
			t.Fatal("projection leaked flagged bytes")
		}
	}
	if !bytes.Contains(masked, []byte("[REDACTED]")) || bytes.Contains(derived, []byte("[REDACTED]")) {
		t.Fatal("wrong projections")
	}
	if !bytes.Equal(body, original) {
		t.Fatal("projection altered original")
	}
}

func TestSecretShapesBinaryAndUnclosedKey(t *testing.T) {
	for _, body := range []string{
		"sk-" + strings.Repeat("x", 24), "ghp_" + strings.Repeat("x", 24),
		"Bearer synthetic-auth", "password: 'synthetic-pass'", "api_key=synthetic",
		"eyJmb28.eyJiYXI.c2ln", "-----BEGIN RSA PRIVATE KEY-----\nunfinished",
		"\x00\xfftoken=synthetic\x00",
	} {
		if spans := ScanSecrets([]byte(body)); len(spans) == 0 {
			t.Fatalf("unflagged synthetic shape %q", body)
		}
	}
	if spans := ScanSecrets([]byte("normal text and token counts")); len(spans) != 0 {
		t.Fatal(spans)
	}
}

func TestProjectionRejectsInvalidFlags(t *testing.T) {
	for _, spans := range [][]Span{
		{{Start: -1, End: 2, Kind: "secret"}}, {{Start: 0, End: 4, Kind: "secret"}},
		{{Start: 2, End: 1, Kind: "secret"}}, {{Start: 0, End: 2, Kind: "secret"}, {Start: 1, End: 3, Kind: "secret"}},
		{{Start: 0, End: 1, Kind: "unknown"}},
	} {
		if _, err := Project([]byte("abc"), spans, false); err == nil {
			t.Fatal("invalid flags accepted", spans)
		}
	}
}
