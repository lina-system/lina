// SPDX-License-Identifier: Apache-2.0
package runs

import "github.com/thisisjun786/lina/kit/provider"

// Steering remains in the durable queue until Transform includes the drained
// prefix in a committed checkpoint. A crash between drain and request loses no
// note. The kit loop owns insertion into its history, including its final drain.
type inputQueue struct{ drain func() []provider.Item }

func (q inputQueue) Drain() []provider.Item { return q.drain() }
func (inputQueue) Close() []provider.Item   { return nil }
