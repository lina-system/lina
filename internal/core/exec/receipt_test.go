// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/thisisjun786/lina/kit/tools"
)

func TestUnknownFileReplyRetainsRecoveryReceipts(t *testing.T) {
	reply := tools.FileReply{State: tools.Unknown, Code: "effect_unknown", Result: tools.FileResult{Changes: []tools.FileReceipt{{Path: "changed", Before: "old", After: "new", Version: "saved-version"}}}}
	result, err := fileResult(reply)
	if !errors.Is(err, tools.ErrUnknown) || result.State != tools.Unknown {
		t.Fatalf("unknown state: %+v %v", result, err)
	}
	var saved tools.FileReply
	if err := json.Unmarshal(result.Body, &saved); err != nil || len(saved.Result.Changes) != 1 || saved.Result.Changes[0].Version != "saved-version" {
		t.Fatalf("lost recovery: %+v %v", saved, err)
	}
}
