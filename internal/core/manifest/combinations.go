// SPDX-License-Identifier: Apache-2.0
package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"runtime"

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

var protocolVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
var platformPattern = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9]+$`)

// ReadCombinations reads the supportedCombinations section while leaving other
// build metadata to its own consumers. An absent table is an error; an explicit
// empty table is valid and denies every combination.
func ReadCombinations(reader io.Reader) ([]Combination, error) {
	var manifest struct {
		Rows []Combination `json:"supportedCombinations"`
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxManifestBytes {
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
		if (row.Connection != "client" && row.Connection != "node") || row.Peer == "" || !platformPattern.MatchString(row.Platform) || !protocolVersionPattern.MatchString(row.ProtocolVersion) || row.Capabilities == nil || len(row.Evidence) == 0 || seen[key] {
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
			if capability.Name == "" || !protocolVersionPattern.MatchString(capability.Version) || caps[capability] {
				return fmt.Errorf("invalid or duplicate capability")
			}
			caps[capability] = true
		}
	}
	return nil
}

const maxManifestBytes = 1 << 20
const TUIPeer = "lina-tui"

// ClientCombinations is shared by the compiled listener and the build-manifest
// writer, so the published table and runtime admission cannot drift.
func ClientCombinations() []Combination {
	return ClientCombinationsFor(runtime.GOOS + "/" + runtime.GOARCH)
}

// ClientCombinationsFor describes the target selected by the build writer.
func ClientCombinationsFor(target string) []Combination {
	return []Combination{{Connection: "client", Peer: TUIPeer, Platform: target, ProtocolVersion: "1.0", Capabilities: []protocol.Capability{{Name: "work", Version: "1.0"}, {Name: "progress", Version: "1.0"}, {Name: "worker_lanes", Version: "1.0"}, {Name: "web", Version: "1.0"}, {Name: "github_merge", Version: "1.0"}, {Name: "github", Version: "1.0"}, {Name: "agents", Version: "1.0"}, {Name: "memory", Version: "1.0"}, {Name: "exchanges", Version: "1.0"}, {Name: "channels", Version: "1.0"}, {Name: "accounts", Version: "1.0"}, {Name: "sign_in_relay", Version: "1.0"}, {Name: "lifecycle", Version: "1.0"}, {Name: "conversation", Version: "1.0"}, {Name: "turn_activity", Version: "1.0"}, {Name: "replay", Version: "1.0"}, {Name: "growth", Version: "1.0"}, {Name: "waiting", Version: "1.0"}, {Name: "running", Version: "1.0"}}, Evidence: []string{"go test ./e2e/m1 -run TestM3OperationalSwitch", "go test ./e2e/m1 -run TestM2ProgressWaitingInstalledCore", "go test ./e2e/m1 -run TestM3WorkerLanes", "go test ./e2e/m1 -run TestM1Web", "go test ./e2e/m1 -run TestM2GitHubMergeInstalledCore", "go test ./e2e/m1 -run TestM2GitHubCIInstalledCore", "go test ./kit/protocol/conformance", "go test ./e2e/m1 -run TestPlannedRestart", "go test ./e2e/m1 -run TestM1TUIConversationInstalledCore", "go test ./e2e/m1 -run TestM1InstalledToolActivity", "go test ./e2e/m1 -run TestM1TUIAccountsInstalledCore", "go test ./e2e/m1 -run TestM1TUIChannel", "go test ./e2e/m1 -run TestM1TUITalkLogInstalledCore", "go test ./e2e/m1 -run TestM1TUIMemoryAgentsInstalledCore", "go test ./e2e/m1 -run TestM1ReplayExportInstalledCore", "go test ./e2e/m1 -run TestM2InstalledPersonaGrowth", "go test ./e2e/m1 -run TestM1TUIApprovalsInstalledCore", "go test ./e2e/m1 -run TestM1TurnBudgetHandoffInstalledCore"}}}
}
