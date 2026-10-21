// SPDX-License-Identifier: Apache-2.0

// Package view contains running-list display data, without internal references.
package view

type Running struct {
	Summary, AgentName string
	State, Reason      string // display catalog codes, not storage states
}
