// SPDX-License-Identifier: Apache-2.0
package loop

import (
	"github.com/thisisjun786/lina/kit/provider"
	"sync"
)

// Inputs is a caller-owned steering queue. The admission lane routes input after
// Run returns to the next turn. Push snapshots bytes and never blocks on a model.
type Inputs struct {
	mu    sync.Mutex
	items []provider.Item
}

func (q *Inputs) Push(item provider.Item) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append(q.items, append(provider.Item(nil), item...))
}
func (q *Inputs) Drain() []provider.Item {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	items := q.items
	q.items = nil
	return items
}
