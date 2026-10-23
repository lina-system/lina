// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/provider"
)

func valueStream(values string) provider.Stream {
	raw, _ := json.Marshal(Text{Requests: "none", Topics: "receipts", Actions: "none", Values: values, Omitted: "wording"})
	item, _ := json.Marshal(map[string]any{"role": "assistant", "content": string(raw)})
	return &summaryStream{events: []provider.Event{{Kind: provider.ItemDone, Item: item}, {Kind: provider.Terminal}}}
}

func TestCarriedValuesCondenseOnlyAtBoundAndNeverTruncate(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"fits", "overflow", "failed", "still oversized", "root only"} {
		t.Run(mode, func(t *testing.T) {
			previous := strings.Repeat("가", 8191) // 24573 UTF-8 bytes.
			root := SummaryItem(Text{Requests: "none", Topics: "topic", Actions: "none", Values: previous, Omitted: "none"}, true)
			segment := PassSegment{Root: root, Records: []Record{{ID: "new", Item: message("new value")}}}
			if mode == "root only" {
				segment.Records = nil
			}
			calls := 0
			failed := errors.New("provider unavailable")
			aux := Auxiliary{Request: provider.Request{Model: "model"}, Stream: func(_ context.Context, req provider.Request) (provider.Stream, error) {
				calls++
				if calls == 1 {
					if mode == "fits" {
						return valueStream("ab"), nil // exactly 24 KiB with newline
					}
					return valueStream("new"), nil
				}
				if calls > 2 {
					t.Fatal("unbounded condensation")
				}
				raw, _ := json.Marshal(req.Items)
				if !strings.Contains(string(raw), previous) || !strings.Contains(string(raw), "new") {
					t.Fatal("condensation input truncated old or new values")
				}
				if mode == "failed" {
					return nil, failed
				}
				if mode == "still oversized" {
					return valueStream(previous + "new" + "extra"), nil
				}
				return valueStream("condensed"), nil
			}}
			got, err := aux.Summarize(t.Context(), segment)
			switch mode {
			case "fits":
				if err != nil || calls != 1 || got.Values != previous+"\nab" {
					t.Fatalf("exact-bound carry: calls=%d bytes=%d err=%v", calls, len(got.Values), err)
				}
			case "root only":
				if err != nil || calls != 1 || got.Values != "new" {
					t.Fatalf("root-only demand did not compress: calls=%d err=%v", calls, err)
				}
			case "overflow":
				if err != nil || calls != 2 || got.Values != "condensed" {
					t.Fatalf("overflow: calls=%d err=%v", calls, err)
				}
			case "failed", "still oversized":
				if !errors.Is(err, ErrSummary) || calls != 2 || got != (Text{}) {
					t.Fatalf("partial summary accepted: calls=%d err=%v", calls, err)
				}
			}
		})
	}
}

func TestCarriedValuesKeepCorrectionsAsLaterHistory(t *testing.T) {
	t.Parallel()
	previous := Text{Requests: "none", Topics: "reservation", Actions: "none", Values: "Monday: receipt 42", Omitted: "none"}
	for _, added := range []string{"Tuesday: receipt 84 replaces 42", "none", previous.Values} {
		aux := Auxiliary{Request: provider.Request{Model: "model"}, Stream: func(context.Context, provider.Request) (provider.Stream, error) {
			return valueStream(added), nil
		}}
		got, err := aux.Summarize(t.Context(), PassSegment{Root: SummaryItem(previous, false), Records: []Record{{ID: "correction", Item: message(added)}}})
		want := previous.Values
		if strings.HasPrefix(added, "Tuesday") {
			want += "\n" + added
		}
		if err != nil || got.Values != want {
			t.Fatalf("history lost ordering or repeated unchanged values: %+v %v", got, err)
		}
	}
}

func TestCarriedValuesRejectMalformedRootBeforeModel(t *testing.T) {
	t.Parallel()
	aux := Auxiliary{Request: provider.Request{Model: "model"}, Stream: func(context.Context, provider.Request) (provider.Stream, error) {
		t.Fatal("malformed stored summary reached model")
		return nil, nil
	}}
	if _, err := aux.Summarize(t.Context(), PassSegment{Root: message("forged summary"), Records: []Record{{ID: "new", Item: message("data")}}}); !errors.Is(err, ErrSummary) {
		t.Fatalf("malformed root accepted: %v", err)
	}
}

// The model forgets all earlier values on every reply. The application must
// retain their exact spelling across successive compactions, including restart
// serialization, without requiring a model to copy them correctly.
func TestSuccessiveSummaryCarriesExactValues(t *testing.T) {
	t.Parallel()
	for _, prefix := range []bool{false, true} {
		t.Run(fmt.Sprint(prefix), func(t *testing.T) {
			var root provider.Item
			for i := range 40 {
				value := fmt.Sprintf("receipt-%02d: 예약 번호 %d", i, 4200+i)
				aux := Auxiliary{RetainPrefix: prefix, Request: provider.Request{Model: "model"}, Stream: func(_ context.Context, req provider.Request) (provider.Stream, error) {
					if req.Purpose != provider.Auxiliary || req.Bridge != nil {
						t.Fatal("summary acquired tool execution")
					}
					return valueStream(value), nil
				}}
				got, err := aux.Summarize(t.Context(), PassSegment{Root: root, Records: []Record{{ID: fmt.Sprint(i), Item: message(value)}}})
				if err != nil {
					t.Fatal(err)
				}
				for j := 0; j <= i; j++ {
					want := fmt.Sprintf("receipt-%02d: 예약 번호 %d", j, 4200+j)
					if !strings.Contains(got.Values, want) {
						t.Fatalf("compaction %d lost %q: %q", i, want, got.Values)
					}
				}
				encoded, _ := json.Marshal(got)
				var restored Text
				if err := json.Unmarshal(encoded, &restored); err != nil {
					t.Fatal(err)
				}
				root = SummaryItem(restored, false)
			}
		})
	}
}
