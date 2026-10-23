// SPDX-License-Identifier: Apache-2.0

// Package messages implements the Messages HTTP/SSE boundary without retries or
// tool execution: the request, the stream, cache marks and deferred batches.
package messages

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// Bound both individual lines and multiline events without limiting a response's
// total length. An item larger than this fails the response before tool readiness.
const maxEventBytes = 32 << 20

type wireEvent struct {
	Message      *wireMessage    `json:"message"`
	ContentBlock json.RawMessage `json:"content_block"`
	Delta        *wireDelta      `json:"delta"`
	Usage        *wireUsage      `json:"usage"`
	Error        *wireFailure    `json:"error"`
	Type         string          `json:"type"`
	Index        int64           `json:"index"`
}

type wireDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Thinking    string `json:"thinking"`
	Signature   string `json:"signature"`
	PartialJSON string `json:"partial_json"`
}

type wireBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Signature string          `json:"signature"`
	Data      string          `json:"data"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}

type wireMessage struct {
	ID      string      `json:"id"`
	Role    string      `json:"role"`
	Content []wireBlock `json:"content"`
	Usage   *wireUsage  `json:"usage"`
}

// cache_creation may be an object of lifetime buckets or a bare count, depending
// on the API version. Keeping it raw lets an unrecognized shape stay unknown
// instead of failing the whole usage record.
type wireUsage struct {
	Input         *int64          `json:"input_tokens"`
	Output        *int64          `json:"output_tokens"`
	CacheRead     *int64          `json:"cache_read_input_tokens"`
	CacheWrite    *int64          `json:"cache_creation_input_tokens"`
	CacheCreation json.RawMessage `json:"cache_creation"`
}

type eventReader struct{ scanner *bufio.Scanner }

func newEventReader(r io.Reader) *eventReader {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), maxEventBytes)
	return &eventReader{scanner: scanner}
}

// next accepts SSE data lines, comments, named events and CRLF framing. Only a
// blank line completes an event; EOF never completes a cut-off response.
func (r *eventReader) next() (wireEvent, error) {
	var data strings.Builder
	var name string
	for r.scanner.Scan() {
		line := r.scanner.Text()
		if line == "" {
			if data.Len() == 0 {
				name = ""
				continue
			}
			payload := strings.TrimSuffix(data.String(), "\n")
			var event wireEvent
			if err := json.Unmarshal([]byte(payload), &event); err != nil {
				return wireEvent{}, err
			}
			if event.Type == "" {
				event.Type = name
			}
			return event, nil
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			name = value
		case "data":
			if data.Len()+len(value)+1 > maxEventBytes {
				return wireEvent{}, errors.New("provider event too large")
			}
			data.WriteString(value)
			data.WriteByte('\n')
		}
	}
	if err := r.scanner.Err(); err != nil {
		return wireEvent{}, err
	}
	if data.Len() != 0 {
		return wireEvent{}, io.ErrUnexpectedEOF
	}
	return wireEvent{}, io.EOF
}
