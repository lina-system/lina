// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/worker"
)

const progressInterval = 250 * time.Millisecond
const progressTextLimit = 64 << 10

type progressKey struct{ phase, item string }
type progressText struct {
	raw                strings.Builder
	published          string
	complete, overflow bool
}

// progressBuffer keeps raw fragments only in memory. It projects the latest
// safe observation at a bounded rate, rather than replaying the durable work
// ledger for each token. Finish flushes the complete unit before the receipt.
type progressBuffer struct {
	streams       map[progressKey]*progressText
	latest        *progressText
	dirty, capped bool
	lastAttempt   time.Time
	lastSummary   string
}

func (b *progressBuffer) Observe(p worker.Progress, at time.Time) (string, error) {
	if p.Text == "" {
		return "", nil
	}
	if !utf8.ValidString(p.Text) {
		return "", secrets.ErrSpan
	}
	switch p.Phase {
	case "message", "thought", "delta":
		if b.streams == nil {
			b.streams = map[progressKey]*progressText{}
		}
		key := progressKey{p.Phase, p.ItemID}
		stream := b.streams[key]
		if stream == nil && len(b.streams) >= 32 {
			b.capped = true
			b.streams = nil
		}
		if b.capped {
			b.latest = &progressText{overflow: true}
		} else {
			if stream == nil {
				stream = &progressText{}
				b.streams[key] = stream
			}
			if stream.raw.Len()+len(p.Text) > progressTextLimit {
				stream.raw.Reset()
				stream.overflow = true
			}
			if !stream.overflow {
				stream.raw.WriteString(p.Text)
			}
			b.latest = stream
		}
	default:
		b.latest = &progressText{complete: true}
		if len(p.Text) > progressTextLimit {
			b.latest.overflow = true
		} else {
			b.latest.raw.WriteString(p.Text)
		}
	}
	b.dirty = true
	return b.Flush(at, false)
}

func (b *progressBuffer) Flush(at time.Time, final bool) (string, error) {
	if b.latest == nil || !b.dirty && !final {
		return "", nil
	}
	if !final && !b.lastAttempt.IsZero() && at.Sub(b.lastAttempt) < progressInterval {
		return "", nil
	}
	b.lastAttempt = at
	b.dirty = false
	text := "Worker is responding."
	if !b.latest.overflow {
		raw := b.latest.raw.String()
		if !final && !b.latest.complete {
			raw = safeProgressPrefix(raw)
		}
		masked, err := secrets.Mask(raw)
		if err != nil {
			return "", err
		}
		text = masked.Text()
		if !strings.HasPrefix(text, b.latest.published) {
			return "", errors.New("progress masking changed a published prefix")
		}
		b.latest.published = text
	}
	text = strings.TrimSpace(text)
	// Mask before shortening; a truncated credential must never evade detection.
	if runes := []rune(text); len(runes) > 512 {
		text = "…" + string(runes[len(runes)-512:])
	}
	if text == "" || text == b.lastSummary {
		return "", nil
	}
	b.lastSummary = text
	return text, nil
}

// Match the established conversation-stream boundary: structured and quoted
// values wait for a complete unit, and possible credential introducers in the
// trailing token stay private. secrets.Mask remains the detector authority.
func safeProgressPrefix(text string) string {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return ""
	}
	if start := strings.Index(text, "-----"); start >= 0 {
		text = text[:start]
	}
	if start := strings.IndexAny(text, "\"'"); start >= 0 {
		text = text[:start]
	}
	end := len(text)
	tokenStart := strings.LastIndexAny(text, " \t\r\n") + 1
	for i := tokenStart; i < end; i++ {
		for _, prefix := range []string{"sk-", "ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_", "AIza", "xoxb-", "xoxa-", "xoxp-", "xoxr-", "xoxs-", "AKIA", "ASIA", "eyJ", "-----"} {
			if strings.HasPrefix(text[i:], prefix) || strings.HasPrefix(prefix, text[i:]) {
				end = i
				break
			}
		}
	}
	return text[:end]
}
