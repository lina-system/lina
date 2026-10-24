// SPDX-License-Identifier: Apache-2.0
package murmur

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sort"
	"strings"

	"github.com/thisisjun786/lina/internal/core/messenger"
)

// ComparisonRecords reads a handed-over version-1 spool without config or any
// writes. It retains only keys, timestamps and the merge rule's provenance.
// Errors deliberately omit source values, field names and message text.
func ComparisonRecords(ctx context.Context, input io.Reader, platform, account string) ([]messenger.ComparisonRecord, error) {
	known := false
	for _, value := range Platforms {
		known = known || value == platform
	}
	if !known || account == "" || strings.ContainsRune(account, 0) {
		return nil, ErrRefused
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	scanner.Split(func(data []byte, eof bool) (int, []byte, error) {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			return i + 1, data[:i], nil
		}
		if eof && len(data) > 0 {
			return 0, nil, ErrIncompleteTail
		}
		return 0, nil, nil
	})
	keys := map[string]*row{}
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := scanner.Bytes()
		if !json.Valid(line) {
			return nil, ErrRefused
		}
		event := &store{rows: map[string]*row{}, rooms: map[string]*room{}}
		if err := event.apply(line, 0, 0); err != nil {
			return nil, ErrRefused
		}
		for key, value := range event.rows {
			if Platforms[value.Source] != platform || value.Account != account {
				continue
			}
			if strings.ContainsRune(value.RoomID, 0) || strings.ContainsRune(value.NativeID, 0) {
				return nil, ErrRefused
			}
			if old := keys[key]; old != nil && !wins(value, old) {
				continue
			}
			// Do not retain message bodies, names, authors, attachments or raw data.
			keys[key] = &row{RoomID: value.RoomID, NativeID: value.NativeID, At: value.At, Observed: value.Observed, Ingest: value.Ingest}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if scanner.Err() != nil {
		return nil, ErrRefused
	}
	result := make([]messenger.ComparisonRecord, 0, len(keys))
	for _, value := range keys {
		result = append(result, messenger.ComparisonRecord{Chat: value.RoomID, Message: value.NativeID, At: value.At})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Chat != result[j].Chat {
			return result[i].Chat < result[j].Chat
		}
		return result[i].Message < result[j].Message
	})
	return result, nil
}
