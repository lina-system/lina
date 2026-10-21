// SPDX-License-Identifier: Apache-2.0

// Package view contains waiting-list display data, without internal references.
package view

import "time"

type Kind string

const (
	Decide Kind = "decide"
	Do     Kind = "do"
	Reply  Kind = "reply"
)

type Effort int

const (
	FiveMinutes Effort = iota + 1
	ThirtyMinutes
	Longer
)

type Waiting struct {
	FreeText                                     bool
	Kind                                         Kind
	Sentence, Context, Recommendation, AgentName string
	Answers                                      []string
	Effort                                       Effort
	Due                                          time.Time
}
