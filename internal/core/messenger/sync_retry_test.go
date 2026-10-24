// SPDX-License-Identifier: Apache-2.0
package messenger_test

import (
	"errors"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
)

// A transient failure must not park a source: the next scheduled read retries
// it, and a source that recovers comes back to connected.
func TestFailingSourceIsReadAgainOnSchedule(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	adapter := &fakeAdapter{chats: []messenger.Chat{{ID: "dm-1", Kind: "direct"}}, fail: errors.New("platform refused")}
	service := messenger.NewService(f.store, map[string]messenger.Adapter{"discord": adapter}, time.Hour)
}