// SPDX-License-Identifier: Apache-2.0
package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"

	"github.com/thisisjun786/lina/kit/protocol"
)

// Combination is one evidenced row of a build's supported-combination table.
// Product versions never participate in this key.
type Combination struct {
	Connection      string                `json:"connection"`
	Peer            string                `json:"peer"`
	Platform        string                `json:"platform"`
	ProtocolVersion string                `json:"protocolVersion"`
	Capabilities    []protocol.Capability `json:"capabilities"`
	Evidence        []string              `json:"evidence"`
}

var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
var platformPattern = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9]+$`)

// ReadCombinations reads the supportedCombinations section while leaving other
// build metadata to its own consumers. An absent table is an error; an explicit
// empty table is valid and denies every combination.
func ReadCombinations(reader io.Reader) ([]Combination, error) {
	var manifest struct {
		Rows []Combination `json:"supportedCombinations"`
	}
	data, err := io.ReadAll(io.LimitReader(reader, MaxFrameBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxFrameBytes {
		return nil, fmt.Errorf("manifest exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&manifest); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("trailing manifest data")
	}
	if manifest.Rows == nil {
		return nil, fmt.Errorf("missing supportedCombinations")
	}
	if err := ValidateCombinations(manifest.Rows); err != nil {
		return nil, err
	}
	return manifest.Rows, nil
}

func ValidateCombinations(rows []Combination) error {
	seen := make(map[[4]string]bool)
	for _, row := range rows {
		key := [4]string{row.Connection, row.Peer, row.Platform, row.ProtocolVersion}
		if (row.Connection != "client" && row.Connection != "node") || row.Peer == "" || !platformPattern.MatchString(row.Platform) || !versionPattern.MatchString(row.ProtocolVersion) || row.Capabilities == nil || len(row.Evidence) == 0 || seen[key] {
			return fmt.Errorf("invalid or duplicate supported combination")
		}
		seen[key] = true
		for _, evidence := range row.Evidence {
			if evidence == "" {
				return fmt.Errorf("empty combination evidence")
			}
		}
		caps := make(map[protocol.Capability]bool)
		for _, capability := range row.Capabilities {
			if capability.Name == "" || !versionPattern.MatchString(capability.Version) || caps[capability] {
				return fmt.Errorf("invalid or duplicate capability")
			}
			caps[capability] = true
		}
	}
	return nil
}
