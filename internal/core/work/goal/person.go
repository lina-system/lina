// SPDX-License-Identifier: Apache-2.0
package goal

import "errors"

// ErrPersonLine is the person-facing text check.
var ErrPersonLine = errors.New("person_line_rejected")

// StartLine will name what will be done, what done means, the assumptions and the asks.
func StartLine(Goal) (string, error) { return "", ErrPersonLine }

// CloseLine will begin with the outcome word and one evidence clause per criterion.
func CloseLine(Goal) (string, error) { return "", ErrPersonLine }

// RunningRow is the running-list projection of one goal task.
func RunningRow(Task) (summary, state string) { return "", "" }
