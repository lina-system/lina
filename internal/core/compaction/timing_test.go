// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/provider"
)

// timedPass sizes a view in bytes around a 1,000-byte window so the early (70%)
// and trigger (90%) boundaries are exact.
func timedPass(cold bool) Pass {
	return Pass{Policy: Policy{ContextWindow: 1000, TriggerPercent: 90, EarlyPercent: 70, KeepUserTokens: 100, KeepToolTokens: 1}, Cold: cold, Count: countBytes}
}
func timedView(historyBytes int) View {
	return View{Request: provider.Request{Model: "model", Purpose: provider.Turn}, Fixed: []provider.Item{message("f")}, Input: Record{ID: "input", Item: message("i"), User: true}, History: []Record{{ID: "old", Item: message(strings.Repeat("x", historyBytes)), User: true}}}
}
func summarizeOnce(t *testing.T, p Pass, v View) (Fitted, int, error) {
	t.Helper()
	published := 0
	p.Summarize = func(context.Context, PassSegment) (Text, error) {
		return Text{Requests: "open", Topics: "topic", Actions: "none", Values: "42", Omitted: "detail"}, nil
	}
	p.Publish = func(context.Context, PassSegment, Text) (Published, error) {
		published++
		return Published{ID: "root", Item: message("summary")}, nil
	}
	out, err := p.Fit(t.Context(), v, false)
	return out, published, err
}

// message("…") overhead is 28 bytes: {"role":"user","content":""}.

func TestColdBoundaryCompactsEarlyAndWarmBoundaryDoesNot(t *testing.T) {
	// 29 fixed + 29 input + (28 + 664) history = 750, between P and the trigger.
	v := timedView(664)
	out, published, err := summarizeOnce(t, timedPass(true), v)
	if err != nil || published != 1 || out.View.RootID != "root" || out.Sizes.Trigger != 700 || out.Sizes.Threshold != 900 {
		t.Fatalf("cold boundary did not compact early: published=%d sizes=%+v err=%v", published, out.Sizes, err)
	}
	out, published, err = summarizeOnce(t, timedPass(false), v)
	if err != nil || published != 0 || out.View.RootID != "" || len(out.Request.Items) == 0 || out.Sizes.Trigger != 900 {
		t.Fatalf("warm boundary compacted below the trigger: published=%d view=%+v err=%v", published, out.View, err)
	}
}

func TestColdBelowEarlyPercentDoesNotCompact(t *testing.T) {
	// 29 + 29 + (28 + 632) = 718? No: 632 + 86 = 718.
	v := timedView(602) // 688 bytes, below 700.
	out, published, err := summarizeOnce(t, timedPass(true), v)
	if err != nil || published != 0 || out.View.RootID != "" || out.Sizes.Trigger != 700 {
		t.Fatalf("cold boundary compacted below P: published=%d sizes=%+v err=%v", published, out.Sizes, err)
	}
}

func TestWarmAtTriggerCompacts(t *testing.T) {
	v := timedView(814) // 900 bytes, at the trigger.
	out, published, err := summarizeOnce(t, timedPass(false), v)
	if err != nil || published != 1 || out.View.RootID != "root" || out.Sizes.Trigger != 900 {
		t.Fatalf("trigger did not compact: published=%d sizes=%+v err=%v", published, out.Sizes, err)
	}
}

func TestEarlyPercentMustStayBelowTheTrigger(t *testing.T) {
	if _, err := (Policy{ContextWindow: 1000, TriggerPercent: 90, EarlyPercent: 95}).normalized(); err == nil {
		t.Fatal("accepted an early boundary above the trigger")
	}
	if p, err := (Policy{ContextWindow: 1000}).normalized(); err != nil || p.EarlyPercent != 70 || p.TriggerPercent != 90 {
		t.Fatalf("defaults: %+v %v", p, err)
	}
}

func TestCacheColdUsesTheAdmittedLifetime(t *testing.T) {
	now := time.Unix(10_000, 0)
	old, recent := now.Add(-2*time.Hour), now.Add(-time.Minute)
	for _, c := range []struct {
		name            string
		supported       provider.CapabilitySet
		admitted        provider.CapabilitySet
		last            time.Time
		recorded, wants bool
	}{
		{"declared and admitted", provider.CapabilitySet{provider.CacheLifetime1hCapability}, provider.CapabilitySet{provider.CacheLifetime1hCapability}, old, true, true},
		{"declared only", provider.CapabilitySet{provider.CacheLifetime1hCapability}, nil, old, true, false},
		{"admitted only", nil, provider.CapabilitySet{provider.CacheLifetime1hCapability}, old, true, false},
		{"no lifetime", nil, nil, old, true, false},
		{"five minutes expired", provider.CapabilitySet{provider.CacheLifetime5mCapability}, provider.CapabilitySet{provider.CacheLifetime5mCapability}, now.Add(-10 * time.Minute), true, true},
		{"five minutes warm", provider.CapabilitySet{provider.CacheLifetime5mCapability}, provider.CapabilitySet{provider.CacheLifetime5mCapability}, recent, true, false},
		{"one hour warm", provider.CapabilitySet{provider.CacheLifetime1hCapability}, provider.CapabilitySet{provider.CacheLifetime1hCapability}, recent, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := CacheCold(c.supported, c.admitted, c.last, c.recorded, now); got != c.wants {
				t.Fatalf("CacheCold=%v want %v", got, c.wants)
			}
		})
	}
}

func TestCacheExpiredNeedsAKnownLifetime(t *testing.T) {
	now := time.Unix(10_000, 0)
	for _, c := range []struct {
		name     string
		last     time.Time
		recorded bool
		lifetime time.Duration
		want     bool
	}{
		{"no request", time.Time{}, false, time.Hour, true},
		{"lifetime unknown", now.Add(-2 * time.Hour), true, 0, false},
		{"inside one hour", now.Add(-30 * time.Minute), true, time.Hour, false},
		{"older than one hour", now.Add(-2 * time.Hour), true, time.Hour, true},
		{"older than five minutes", now.Add(-10 * time.Minute), true, 5 * time.Minute, true},
		{"inside five minutes", now.Add(-time.Minute), true, 5 * time.Minute, false},
		{"future request", now.Add(time.Minute), true, time.Hour, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := CacheExpired(c.last, c.recorded, c.lifetime, now); got != c.want {
				t.Fatalf("CacheExpired=%v want %v", got, c.want)
			}
		})
	}
}
