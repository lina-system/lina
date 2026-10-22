// SPDX-License-Identifier: Apache-2.0
package update

// Cell is an observation, not an instruction or permission to activate.
type Cell string

const (
	Unknown Cell = "unknown"
	Pass    Cell = "pass"
	Fail    Cell = "fail"
)

// Gate requires all six C-38 observations. Preparation can establish only four;
// the activation owner must supply a fenced idle observation and a recent
// verified backup generation. Zero, missing and unknown values fail closed.
type Gate struct {
	Idle       Cell `json:"idle"`
	Candidate  Cell `json:"candidate_verified"`
	Schema     Cell `json:"schema_relation"`
	Peers      Cell `json:"peer_rows"`
	Space      Cell `json:"free_space"`
	Generation Cell `json:"verified_generation"`
}

func (g Gate) Ready() bool {
	return g.Idle == Pass && g.Candidate == Pass && g.Schema == Pass && g.Peers == Pass && g.Space == Pass && g.Generation == Pass
}
