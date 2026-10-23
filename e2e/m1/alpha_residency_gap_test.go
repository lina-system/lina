// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"strconv"
	"strings"
	"testing"
)

// residencyGapRecord keeps the collector stall that preceded the widest
// observation gap. The stall is the previous sample's read, write and fsync,
// which the next observation cannot start until they return.
type residencyGapRecord struct {
	Maximum            float64
	ReadBeforeMaximum  float64
	WriteBeforeMaximum float64
	SyncBeforeMaximum  float64
	read               float64
	write              float64
	sync               float64
	seen               bool
}

func (r *residencyGapRecord) note(gap, read, write, sync float64) {
	if !r.seen || gap > r.Maximum {
		r.Maximum = gap
		r.ReadBeforeMaximum = r.read
		r.WriteBeforeMaximum = r.write
		r.SyncBeforeMaximum = r.sync
	}
	r.read, r.write, r.sync = read, write, sync
	r.seen = true
}

func parsePressureAvg10(text string) (some, full float64, ok bool) {
	someOK, fullOK := false, false
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		var dest *float64
		var flag *bool
		switch fields[0] {
		case "some":
			dest, flag = &some, &someOK
		case "full":
			dest, flag = &full, &fullOK
		default:
			continue
		}
		for _, field := range fields[1:] {
			value, found := strings.CutPrefix(field, "avg10=")
			if !found {
				continue
			}
			n, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return 0, 0, false
			}
			*dest = n
			*flag = true
		}
	}
	if !someOK || !fullOK {
		return 0, 0, false
	}
	return some, full, true
}

func TestResidencyGapKeepsTheStallBeforeTheMaximum(t *testing.T) {
	var stalled residencyGapRecord
	stalled.note(1, 0.25, 0.5, 0.125)
	stalled.note(2.5, 0.125, 0.125, 0.125)
	if stalled.Maximum != 2.5 || stalled.ReadBeforeMaximum != 0.25 || stalled.WriteBeforeMaximum != 0.5 || stalled.SyncBeforeMaximum != 0.125 {
		t.Fatalf("maximum gap %.3f kept read %.3f write %.3f sync %.3f", stalled.Maximum, stalled.ReadBeforeMaximum, stalled.WriteBeforeMaximum, stalled.SyncBeforeMaximum)
	}

	var scheduled residencyGapRecord
	scheduled.note(1, 0.125, 0.125, 0.125)
	scheduled.note(7.75, 0.125, 0.125, 0.125)
	if scheduled.Maximum != 7.75 || scheduled.SyncBeforeMaximum != 0.125 {
		t.Fatalf("maximum gap %.3f kept sync %.3f", scheduled.Maximum, scheduled.SyncBeforeMaximum)
	}
	if scheduled.ReadBeforeMaximum+scheduled.WriteBeforeMaximum+scheduled.SyncBeforeMaximum >= scheduled.Maximum {
		t.Fatal("scheduling delay is not covered by the previous collector stall")
	}
}

func TestParsePressureAvg10(t *testing.T) {
	text := "some avg10=1.50 avg60=0.20 avg300=0.00 total=10\nfull avg10=0.25 avg60=0.00 avg300=0.00 total=2\n"
	some, full, ok := parsePressureAvg10(text)
	if !ok || some != 1.5 || full != 0.25 {
		t.Fatalf("pressure some %.2f full %.2f ok %v", some, full, ok)
	}
	if _, _, ok := parsePressureAvg10("some avg10=1\n"); ok {
		t.Fatal("partial pressure snapshot was accepted")
	}
}
