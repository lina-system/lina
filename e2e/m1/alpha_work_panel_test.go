// SPDX-License-Identifier: Apache-2.0
package e2e_test

import "testing"

func TestAlphaWorkCardElapsed(t *testing.T) {
	for _, elapsed := range []string{"0m", "1m", "59m", "1h 00m", "1h 05m", "23h 59m", "1d 00h", "2d 04h", "100d 23h"} {
		t.Run(elapsed, func(t *testing.T) {
			panel := "일\n테스트 작업    " + elapsed + "\n  답변 반영\n"
			if !alphaHasWorkCard(panel) {
				t.Fatalf("valid work card rejected at elapsed %s", elapsed)
			}
		})
	}
	for name, panel := range map[string]string{
		"empty":             "일\n",
		"no name":           "0m\n  답변 반영\n",
		"no activity":       "테스트 작업 0m\n  \n",
		"elapsed in prose":  "테스트 작업\n  0m mentioned in activity\n",
		"unknown elapsed":   "테스트 작업 ?\n  답변 반영\n",
		"invalid minutes":   "테스트 작업 60m\n  답변 반영\n",
		"invalid remainder": "테스트 작업 1h 60m\n  답변 반영\n",
		"invalid hours":     "테스트 작업 1d 24h\n  답변 반영\n",
	} {
		t.Run(name, func(t *testing.T) {
			if alphaHasWorkCard(panel) {
				t.Fatal("accepted panel without a named work card, elapsed time and activity")
			}
		})
	}
}
