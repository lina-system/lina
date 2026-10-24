// SPDX-License-Identifier: Apache-2.0
package replay

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
)

// Compare aligns request identity, including a request missing from either run.
// Different fixture digests are refused instead of implying a valid comparison.
func Compare(w io.Writer, before, after ProviderReport) error {
	if before.Version != 1 || after.Version != 1 || before.Source == "" || before.Source != after.Source {
		return ErrInvalid
	}
	type key struct {
		turn  string
		round int
	}
	left, right := map[key]RequestReport{}, map[key]RequestReport{}
	var order []key
	for i, report := range []ProviderReport{before, after} {
		dst := left
		if i == 1 {
			dst = right
		}
		for _, r := range report.Requests {
			k := key{r.Turn, r.Round}
			if k.turn == "" || k.round < 1 {
				return ErrInvalid
			}
			if _, ok := dst[k]; ok {
				return ErrInvalid
			}
			dst[k] = r
			if _, ok := left[k]; i == 0 || !ok {
				order = append(order, k)
			}
		}
	}
	if _, err := fmt.Fprintf(w, "Before: %s / %s; after: %s / %s\n\n| Request | Input before / after | Read before / after | Cache share before / after | Written before / after | Lifetime writes before / after | Plain before / after | First output ms before / after | Failure before / after |\n| --- | --- | --- | --- | --- | --- | --- | --- | --- |\n", before.Provider, before.Model, after.Provider, after.Model); err != nil {
		return err
	}
	count := func(n *int64) string {
		if n == nil {
			return "unknown"
		}
		return strconv.FormatInt(*n, 10)
	}
	elapsed := func(n *float64) string {
		if n == nil {
			return "unknown"
		}
		return strconv.FormatFloat(*n, 'f', 3, 64)
	}
	share := func(n *float64) string {
		if n == nil {
			return "unknown"
		}
		return strconv.FormatFloat(*n*100, 'f', 1, 64) + "%"
	}
	writes := func(r RequestReport) string {
		if r.WrittenByLifetime == nil {
			return "unknown"
		}
		b, _ := json.Marshal(r.WrittenByLifetime)
		return string(b)
	}
	for _, k := range order {
		a, b := left[k], right[k]
		if _, ok := left[k]; !ok {
			a.Failure = "missing"
		}
		if _, ok := right[k]; !ok {
			b.Failure = "missing"
		}
		if _, err := fmt.Fprintf(w, "| %s:%d | %s / %s | %s / %s | %s / %s | %s / %s | %s / %s | %s / %s | %s / %s | %s / %s |\n", k.turn, k.round, count(a.Input), count(b.Input), count(a.Read), count(b.Read), share(a.CacheShare), share(b.CacheShare), count(a.Written), count(b.Written), writes(a), writes(b), count(a.Plain), count(b.Plain), elapsed(a.FirstOutputMS), elapsed(b.FirstOutputMS), a.Failure, b.Failure); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "\nRun failure before / after: %s / %s\n", before.Failure, after.Failure)
	return err
}
