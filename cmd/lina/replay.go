// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/kit/protocol"
)

func runReplay(args []string) int {
	flags := flag.NewFlagSet("lina replay", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	socket := flags.String("socket", "", "Core client socket")
	output := flags.String("out", "", "new directory for reply, tool results and usage")
	candidates := flags.String("candidates", "", "candidate persona layer files")
	model := flags.String("model", "", "override recorded model")
	effort := flags.String("effort", "", "override recorded effort")
	turn := flags.String("turn", "", "select one turn from a JSON Lines episode file")
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: lina replay <episode-file|turn-id> --socket <path> --out <dir> [--candidates <dir>] [--model <id>] [--effort <value>] [--turn <id>]")
		flags.PrintDefaults()
	}
	source := ""
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		source = args[0]
		args = args[1:]
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 64
	}
	if source == "" && flags.NArg() == 1 {
		source = flags.Arg(0)
	} else if flags.NArg() != 0 {
		flags.Usage()
		return 64
	}
	if source == "" || *socket == "" || *output == "" {
		flags.Usage()
		return 64
	}
	p := protocol.ReplayRequest{TurnID: &source}
	if *model != "" {
		p.Model = model
	}
	if *effort != "" {
		p.Effort = effort
	}
	file, err := os.Open(source)
	if err == nil {
		ep, err := selectEpisode(file, *turn)
		file.Close()
		if err != nil {
			return developmentError("replay", err)
		}
		p.TurnID = nil
		p.Episode = &ep
	} else if !errors.Is(err, os.ErrNotExist) || *turn != "" {
		return developmentError("replay", err)
	}
	if *candidates != "" {
		files, err := candidateFiles(*candidates)
		if err != nil {
			return developmentError("replay", err)
		}
		p.Candidates = &files
	}
	// Refuse an existing output before incurring any model usage.
	if _, err = os.Lstat(*output); !errors.Is(err, os.ErrNotExist) {
		return developmentError("replay", errors.New("output exists"))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := developmentClient(ctx, *socket)
	if err != nil {
		return developmentError("replay", err)
	}
	defer c.Close()
	if _, err = c.Replay(ctx, p); err != nil {
		return developmentError("replay", err)
	}
	frame, err := c.Receive(ctx)
	if err != nil {
		return developmentError("replay", err)
	}
	if frame.Error != nil {
		return developmentError("replay", &clientapi.Refusal{Cause: *frame.Error})
	}
	result, ok := frame.Payload.(protocol.ReplayResult)
	if !ok {
		return developmentError("replay", errors.New("invalid response"))
	}
	if err = writeReplayOutput(*output, result); err != nil {
		return developmentError("replay", err)
	}
	if result.ExcludedReason != nil {
		fmt.Fprintln(os.Stderr, "lina replay: turn excluded; see manifest.json")
		return 0
	}
	fmt.Fprintln(os.Stdout, "Replay complete.")
	return 0
}
func selectEpisode(r io.Reader, turn string) (string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 524288)
	selected := ""
	for scanner.Scan() {
		var id struct {
			TurnID string `json:"turn_id"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &id); err != nil {
			return "", err
		}
		if turn != "" && turn != id.TurnID {
			continue
		}
		if selected != "" {
			return "", errors.New("select one turn")
		}
		selected = scanner.Text()
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if selected == "" {
		return "", errors.New("episode not found")
	}
	return selected, nil
}
func candidateFiles(path string) ([]protocol.ReplayCandidate, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	files := []protocol.ReplayCandidate{}
	for _, name := range []string{"core.md", "default-voice.md", "user-voice.md", "growth-snapshot.md"} {
		f, err := root.Open(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(f, 524289))
		f.Close()
		if err != nil {
			return nil, err
		}
		if len(data) > 524288 {
			return nil, errors.New("candidate too large")
		}
		files = append(files, protocol.ReplayCandidate{Name: name, Text: string(data)})
	}
	if len(files) == 0 {
		return nil, errors.New("no candidate layers")
	}
	return files, nil
}
func writeReplayOutput(path string, result protocol.ReplayResult) error {
	files := map[string][]byte{}
	manifest := map[string]any{"version": 1, "selected": 1, "replayed": 1, "left_out": 0, "left_out_reasons": map[string]int{}}
	if result.ExcludedReason != nil {
		manifest["replayed"] = 0
		manifest["left_out"] = 1
		manifest["left_out_reasons"] = map[string]int{*result.ExcludedReason: 1}
	} else {
		if result.Output == nil {
			return errors.New("missing replay output")
		}
		var output map[string]json.RawMessage
		if err := json.Unmarshal([]byte(*result.Output), &output); err != nil {
			return err
		}
		files["replay.json"] = []byte(*result.Output)
		for key, name := range map[string]string{"reply": "reply.json", "tool_results": "tool-results.json", "usage": "usage.json"} {
			if output[key] == nil {
				return errors.New("missing replay field")
			}
			files[name] = output[key]
		}
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	files["manifest.json"] = raw
	parent, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer parent.Close()
	name := filepath.Base(path)
	if err = parent.Mkdir(name, 0700); err != nil {
		return err
	}
	success := false
	defer func() {
		if !success {
			parent.RemoveAll(name)
		}
	}()
	for file, body := range files {
		if err = parent.WriteFile(filepath.Join(name, file), append(body, '\n'), 0600); err != nil {
			return err
		}
	}
	success = true
	return nil
}
