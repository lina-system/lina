// SPDX-License-Identifier: Apache-2.0

package render

import (
	"sort"
	"unicode/utf8"
)

// Unicode 17 UAX #29 extended grapheme clusters (GB3 through GB13).
const (
	gbOther uint8 = iota
	gbCR
	gbLF
	gbControl
	gbExtend
	gbZWJ
	gbRegionalIndicator
	gbPrepend
	gbSpacingMark
	gbL
	gbV
	gbT
	gbLV
	gbLVT
)
const (
	incbNone uint8 = iota
	incbConsonant
	incbExtend
	incbLinker
)

type runeRange struct {
	lo, hi rune
	value  uint8
}

func property(r rune, ranges []runeRange) uint8 {
	i := sort.Search(len(ranges), func(i int) bool { return ranges[i].hi >= r })
	if i < len(ranges) && ranges[i].lo <= r {
		return ranges[i].value
	}
	return 0
}
func graphemeClass(r rune) uint8 {
	if r >= 0xac00 && r <= 0xd7a3 {
		if (r-0xac00)%28 == 0 {
			return gbLV
		}
		return gbLVT
	}
	return property(r, graphemeRanges)
}
func control(c uint8) bool { return c == gbCR || c == gbLF || c == gbControl }

func nextCluster(text string) (string, string) {
	if text == "" {
		return "", ""
	}
	previous := gbOther
	regional := 0
	pictographic, zwjPictographic := false, false
	consonant, linker := false, false
	end := 0
	for pos, r := range text {
		current := graphemeClass(r)
		ep := property(r, pictographicRanges) != 0
		indic := property(r, indicRanges)
		if pos > 0 {
			join := false
			switch {
			case previous == gbCR && current == gbLF:
				join = true
			case control(previous) || control(current):
			case previous == gbL && (current == gbL || current == gbV || current == gbLV || current == gbLVT):
				join = true
			case (previous == gbLV || previous == gbV) && (current == gbV || current == gbT):
				join = true
			case (previous == gbLVT || previous == gbT) && current == gbT:
				join = true
			case current == gbExtend || current == gbZWJ || current == gbSpacingMark || previous == gbPrepend:
				join = true
			case consonant && linker && indic == incbConsonant:
				join = true
			case previous == gbZWJ && zwjPictographic && ep:
				join = true
			case previous == gbRegionalIndicator && current == gbRegionalIndicator && regional%2 == 1:
				join = true
			}
			if !join {
				return text[:pos], text[pos:]
			}
		}
		zwjPictographic = current == gbZWJ && pictographic
		if ep {
			pictographic = true
		} else if current != gbExtend {
			pictographic = false
		}
		if indic == incbConsonant {
			consonant = true
			linker = false
		} else if indic == incbLinker {
			linker = consonant
		} else if indic != incbExtend {
			consonant = false
			linker = false
		}
		if current == gbRegionalIndicator {
			regional++
		} else {
			regional = 0
		}
		previous = current
		_, n := utf8.DecodeRuneInString(text[pos:])
		end = pos + n
	}
	return text[:end], text[end:]
}
