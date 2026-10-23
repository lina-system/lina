// SPDX-License-Identifier: Apache-2.0
package codex

import "github.com/thisisjun786/lina/internal/core/worker"

func (s *Session) DeliveryProfile() worker.DeliveryProfile {
	return worker.DeliveryProfile{Tier: "push", Mechanism: "turn/steer; turn/start when idle", Available: true}
}
