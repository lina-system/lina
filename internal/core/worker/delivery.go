// SPDX-License-Identifier: Apache-2.0
package worker

// DeliveryProfile describes this connection, not a vendor's theoretical ability.
// An unavailable transport must never be advertised as push.
type DeliveryProfile struct {
	Tier      string `json:"tier"`
	Mechanism string `json:"mechanism"`
	Available bool   `json:"available"`
}

func Delivery(a Adapter) DeliveryProfile {
	if p, ok := a.(interface{ DeliveryProfile() DeliveryProfile }); ok {
		return p.DeliveryProfile()
	}
	return DeliveryProfile{Tier: "pull", Mechanism: "no live inbox transport", Available: false}
}
