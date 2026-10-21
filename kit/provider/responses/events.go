// SPDX-License-Identifier: Apache-2.0
package responses

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// Bound both individual lines and multiline events without limiting a response's
// total length. Items larger than this fail the response before tool readiness.
const maxEventBytes = 32 << 20

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
			if payload == "[DONE]" {
				return wireEvent{}, io.EOF
			}
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
