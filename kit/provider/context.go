// SPDX-License-Identifier: Apache-2.0
package provider

// RequestSizer reports the selected model's request window and a counter in
// the same units. Counters include fixed instructions, tool declarations and
// every item. An adapter without token accounting uses the conservative byte
// budget; it must never label a byte count as an observed token count.
type RequestSizer interface {
	RequestBudget(ModelRef) (window int, count func(Request) int)
}

// RequestBytes is the existing conservative input accounting for adapters
// without a tokenizer. The 128,000-byte fallback is deliberately unchanged.
func RequestBytes(request Request) int {
	n := len(request.Instructions)
	for _, items := range [][]Item{request.Items, request.Tools} {
		for _, item := range items {
			n += len(item)
		}
	}
	return n
}
