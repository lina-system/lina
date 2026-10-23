// SPDX-License-Identifier: Apache-2.0

// Package update prepares verified candidate compositions without activating them.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/manifest"
	"github.com/thisisjun786/lina/kit/protocol"
)

type PreflightResult struct {
	UpdateProtocol int    `json:"update_protocol"`
	StoreSchema    int    `json:"store_schema"`
	BinarySchema   int    `json:"binary_schema"`
	SchemaRelation string `json:"schema_relation"`
	PeerRows       int    `json:"peer_rows"`
}

// Preflight has no dependency on boot, providers, process locks or writers.
// Required rows describe the installed peers; omitted rows use this build's
// local TUI requirements. A candidate must retain their protocol/capabilities.
func Preflight(ctx context.Context, root, manifestPath, requiredPath string) (PreflightResult, error) {
	result := PreflightResult{UpdateProtocol: 1, BinarySchema: len(canon.Migrations())}
	for _, name := range []string{".", "system", "system/core"} {
		info, err := os.Lstat(filepath.Join(root, name))
		if err != nil {
			return result, err
		}
		if err := ctlfiles.ValidatePrivateDirectory(info); err != nil {
			return result, err
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "system/core/main.json"))
	if err != nil {
		return result, err
	}
	var main struct {
		ID string `json:"main_id"`
	}
	if json.Unmarshal(data, &main) != nil || main.ID == "" {
		return result, errors.New("invalid installed identity")
	}
	path := filepath.Join(root, "canon/canon.sqlite")
	if info, err := os.Lstat(filepath.Dir(path)); err == nil {
		if err := ctlfiles.ValidatePrivateDirectory(info); err != nil {
			return result, err
		}
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil {
				return result, err
			}
			if len(entries) != 0 {
				return result, canon.ErrRecoveryRequired
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	result.StoreSchema, err = canon.CheckSchema(ctx, path, canon.Migrations())
	if err != nil {
		return result, err
	}
	result.SchemaRelation = "equal"
	if result.StoreSchema < result.BinarySchema {
		result.SchemaRelation = "forward"
	}
	rows, err := readRows(manifestPath)
	if err != nil {
		return result, err
	}
	// Manifest declarations cannot add protocol implementations to a binary.
	if err := CheckPeers(manifest.ClientCombinations(), rows); err != nil {
		return result, err
	}
	required, err := readRows(requiredPath)
	if err != nil {
		return result, err
	}
	if len(required) == 0 {
		return result, errors.New("peer requirements are empty")
	}
	if err := CheckPeers(rows, required); err != nil {
		return result, err
	}
	result.PeerRows = len(required)
	return result, nil
}

func readRows(path string) ([]manifest.Combination, error) {
	if path == "" {
		return manifest.ClientCombinations(), nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return manifest.ReadCombinations(file)
}

// CheckPeers requires an evidenced matching protocol row and every required
// capability. Product version strings are deliberately absent from the match.
func CheckPeers(candidate, required []manifest.Combination) error {
	if err := manifest.ValidateCombinations(candidate); err != nil {
		return err
	}
	if err := manifest.ValidateCombinations(required); err != nil {
		return err
	}
	for _, peer := range required {
		found := false
		for _, row := range candidate {
			if row.Connection != peer.Connection || row.Peer != peer.Peer || row.Platform != peer.Platform || row.ProtocolVersion != peer.ProtocolVersion {
				continue
			}
			caps := true
			for _, requiredCap := range peer.Capabilities {
				matched := false
				for _, cap := range row.Capabilities {
					if protocol.CapabilitySubset(requiredCap, cap) {
						matched = true
						break
					}
				}
				if !matched {
					caps = false
					break
				}
			}
			if caps {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unsupported_combination: %s %s %s %s", peer.Connection, peer.Peer, peer.Platform, peer.ProtocolVersion)
		}
	}
	return nil
}
