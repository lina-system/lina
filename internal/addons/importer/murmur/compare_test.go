// SPDX-License-Identifier: Apache-2.0
package murmur_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/addons/importer/murmur"
)

func TestComparisonRecords(t *testing.T) {
	h := synthetic(t)
	h.events[0].messages[0].at = h.events[0].messages[0].at.Add(-time.Hour)
	h.events[0].messages[1].native = "9223372036854775807"
	h.write(t, "")
	raw, err := os.ReadFile(filepath.Join(h.root, "spool/events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := murmur.ComparisonRecords(t.Context(), bytes.NewReader(raw), "kakaotalk", "kakao-1")
	if err != nil || len(got) != 2 {
		t.Fatalf("keys: %+v, %v", got, err)
	}
	if !got[0].At.Equal(h.events[1].messages[0].at) || got[1].Message != "9223372036854775807" {
		t.Fatalf("archive precedence or ID precision: %+v", got)
	}
	again, err := murmur.ComparisonRecords(t.Context(), bytes.NewReader(append(append([]byte{}, raw...), raw...)), "kakaotalk", "kakao-1")
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatalf("duplicates: %+v, %v", again, err)
	}
	other, err := murmur.ComparisonRecords(t.Context(), bytes.NewReader(raw), "kakaotalk", "other")
	if err != nil || len(other) != 0 {
		t.Fatalf("account: %+v, %v", other, err)
	}
	for _, tail := range []string{"{", "{\"private-body\":true}\n", "{}\n"} {
		_, err := murmur.ComparisonRecords(t.Context(), bytes.NewReader(append(append([]byte{}, raw...), tail...)), "kakaotalk", "kakao-1")
		if err == nil || strings.Contains(err.Error(), "private-body") {
			t.Fatalf("tail error: %v", err)
		}
	}
	// Two JSON documents on one line must never hide a second event.
	first := bytes.SplitN(raw, []byte("\n"), 2)[0]
	_, err = murmur.ComparisonRecords(t.Context(), bytes.NewReader(append(append(append([]byte{}, first...), first...), '\n')), "kakaotalk", "kakao-1")
	if err == nil {
		t.Fatal("accepted concatenated events")
	}
	oversized := bytes.Replace(raw, []byte("live text"), bytes.Repeat([]byte("x"), 16<<20), 1)
	if _, err := murmur.ComparisonRecords(t.Context(), bytes.NewReader(oversized), "kakaotalk", "kakao-1"); err == nil {
		t.Fatal("accepted oversized event")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := murmur.ComparisonRecords(ctx, bytes.NewReader(raw), "kakaotalk", "kakao-1"); err == nil {
		t.Fatal("ignored cancellation")
	}
}
