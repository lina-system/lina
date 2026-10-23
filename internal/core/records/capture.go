// SPDX-License-Identifier: Apache-2.0
package records

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// Capture holds an unsaved interpretation. Core resolves belonging names and
// validates every field before offering a preview; this function never writes.
type Capture struct {
	Title    string
	Todo     Todo
	Projects []string
}

var numericCaptureDate = regexp.MustCompile(`^[0-9]{1,4}[/.-][0-9]{1,2}([/.-][0-9]{1,4})?$`)

func ParseCapture(text string) (Capture, error) {
	out := Capture{Todo: Todo{State: "open"}}
	if strings.TrimSpace(text) == "" || len(text) > 4096 || strings.ContainsAny(text, "\r\n") {
		return out, ErrInvalid
	}
	words, err := captureWords(text)
	if err != nil {
		return out, err
	}
	title := []string{}
	for _, word := range words {
		switch {
		case word == "!":
			out.Todo.Important = true
		case strings.HasPrefix(word, "%"):
			tag := strings.TrimPrefix(word, "%")
			if tag == "" {
				return out, ErrInvalid
			}
			if !slices.Contains(out.Todo.Tags, tag) {
				out.Todo.Tags = append(out.Todo.Tags, tag)
			}
		case strings.HasPrefix(word, "#"):
			name := strings.TrimPrefix(word, "#")
			if name == "" {
				return out, ErrInvalid
			}
			if !slices.Contains(out.Projects, name) {
				out.Projects = append(out.Projects, name)
			}
		case strings.HasPrefix(word, "{"):
			if !strings.HasSuffix(word, "}") || out.Todo.Deadline != "" {
				return out, ErrAmbiguousDate
			}
			out.Todo.Deadline = strings.TrimSuffix(strings.TrimPrefix(word, "{"), "}")
			if out.Todo.Deadline == "" {
				return out, ErrAmbiguousDate
			}
			out.Todo.DeadlineSource = "person:quick-add:" + text
		case numericCaptureDate.MatchString(word):
			if out.Todo.When != "" {
				return out, ErrAmbiguousDate
			}
			out.Todo.When = word
		default:
			title = append(title, word)
		}
	}
	out.Title = strings.Join(title, " ")
	if strings.TrimSpace(out.Title) == "" || len(out.Title) > 512 {
		return out, ErrInvalid
	}
	return out, out.Todo.Validate()
}
func captureWords(text string) ([]string, error) {
	words := []string{}
	var word strings.Builder
	quoted := false
	for _, c := range text {
		if c == '"' && (quoted || strings.HasPrefix(word.String(), "#") || strings.HasPrefix(word.String(), "%")) {
			quoted = !quoted
			continue
		}
		if unicode.IsSpace(c) && !quoted {
			if word.Len() > 0 {
				words = append(words, word.String())
				word.Reset()
			}
			continue
		}
		word.WriteRune(c)
	}
	if quoted {
		return nil, ErrInvalid
	}
	if word.Len() > 0 {
		words = append(words, word.String())
	}
	return words, nil
}
