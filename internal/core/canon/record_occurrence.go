// SPDX-License-Identifier: Apache-2.0
package canon

// RecordOccurrence is the work-ledger payload for a single live record's
// recurrence. It records history without creating a delegated work card.
type RecordOccurrence struct {
	Record    string `json:"record"`
	At        string `json:"at"`
	From      string `json:"from"`
	Next      string `json:"next"`
	Reason    string `json:"reason"`
	Exhausted bool   `json:"exhausted,omitempty"`
}
