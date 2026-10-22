// SPDX-License-Identifier: Apache-2.0
package work

import "time"

// These are work-ledger values. The waiting flow owns admission and timers;
// the work owner records their dependency and revision transitions.
const DoNotAct = "do_not_act"

type WaitReason string
type QuestionRound struct {
	ID, Goal, Person, Agent, Conversation string
	Deadline                              time.Time
	Questions                             []PersonQuestion
}
type PersonQuestion struct {
	ID, Prompt, Recommendation string
	Reason                     WaitReason
	Irreversible               bool
	Choices                    []string
	Default                    string
	Dependents                 []string
}
type QuestionAnswer struct {
	MessageID, Message string
	Values             map[string]string
}
type RoundAnswer = QuestionAnswer
type QuestionDecision struct {
	RoundAnswer
	Kind string
	At   time.Time
}
