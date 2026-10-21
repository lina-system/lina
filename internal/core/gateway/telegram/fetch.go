// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

var filePathPattern = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)

func (c *client) fetch(ctx context.Context, id string) (io.ReadCloser, error) {
	if id == "" {
		return nil, errResponse
	}
	data, err := c.call(ctx, "getFile", map[string]any{"file_id": id})
	if err != nil {
		return nil, err
	}
	var file gotgbot.File
	if json.Unmarshal(data, &file) != nil || file.FilePath == "" || strings.HasPrefix(file.FilePath, "/") || !filePathPattern.MatchString(file.FilePath) {
		return nil, errResponse
	}
	for _, part := range strings.Split(file.FilePath, "/") {
		if part == "" || part == "." || part == ".." {
			return nil, errResponse
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiOrigin+"/file/bot"+opaqueToken+"/"+file.FilePath, nil)
	if err != nil {
		return nil, errResponse
	}
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errTransport
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, errResponse
	}
	return res.Body, nil
}
