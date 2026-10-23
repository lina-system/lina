// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/thisisjun786/lina/internal/core/work"
)

// ReadRules reads the owner's provisional choice table. No provider credentials
// or worker settings enter the table. An absent table preserves install order.
func ReadRules(root string) ([]work.ChoiceRule, error) {
	f, err := os.Open(filepath.Join(root, "system", "core", "worker-choice.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return nil, work.ErrInvalid
	}
	var table struct {
		Provisional bool              `json:"provisional"`
		Rules       []work.ChoiceRule `json:"rules"`
	}
	decoder := json.NewDecoder(io.LimitReader(f, 64<<10))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&table); err != nil {
		return nil, err
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return nil, work.ErrInvalid
	}
	if !table.Provisional || len(table.Rules) > 128 {
		return nil, work.ErrInvalid
	}
	for _, rule := range table.Rules {
		if !ruleWorker(rule.Worker) || rule.Priority < -1000 || rule.Priority > 1000 || len(rule.Tags) > 16 {
			return nil, work.ErrInvalid
		}
		for _, tag := range rule.Tags {
			if strings.TrimSpace(tag) == "" || len(tag) > 4096 {
				return nil, work.ErrInvalid
			}
		}
	}
	return table.Rules, nil
}
