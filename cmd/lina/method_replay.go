// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/methodreplay"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/persona"
)

// runMethodReplay scores the private M4-1 replay set before and after the
// per-agent tailoring. It is a development command: it reads an explicit case
// file, plays each case once per variant through the configured model route and
// writes only scores, check outcomes and case ids.
func runMethodReplay(args []string) int {
	flags := flag.NewFlagSet("lina method-replay", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	baseline := flags.String("baseline", "", "directory of untailored <preset>/{core-v1.md,default-voice-v1.md,work-method-v1.json}")
	changed := flags.String("changed", "", "comma-separated agents whose tailoring this run measures")
	repeat := flags.Int("repeat", 1, "observed runs per case and variant")
	model := flags.String("model", "", "model id; defaults to LINA_RESPONSES_MODEL")
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: lina method-replay <cases.jsonl> --changed <agent[,agent]> [--baseline <dir>] [--repeat <n>] [--model <id>]")
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
	if source == "" || *repeat < 1 || *repeat > 20 {
		flags.Usage()
		return 64
	}
	var names []string
	for _, name := range strings.Split(*changed, ",") {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		flags.Usage()
		return 64
	}
	variants, err := replayVariants(*baseline)
	if err != nil {
		return developmentError("method-replay", err)
	}
	file, err := os.Open(source)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lina method-replay: cannot read the case file")
		return 66
	}
	cases, rejected := methodreplay.Read(file)
	file.Close()
	route, err := modelpath.ResponsesFromEnv(os.Getenv)
	if err != nil || route == nil {
		fmt.Fprintln(os.Stderr, "lina method-replay: no model route configured")
		return 78
	}
	if *model != "" {
		route.Model = provider.ModelRef(*model)
	}
	player := methodreplay.Player{Adapter: route.Adapter, Model: route.Model, Credential: route.Request(nil).Credential}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	report, err := methodreplay.Run(ctx, methodreplay.Config{
		Cases: cases, Variants: variants, Repeat: *repeat, Changed: names, Baseline: baselineKind(*baseline),
		Play: func(ctx context.Context, c methodreplay.Case, v methodreplay.Variant) (string, error) {
			deadline, stop := context.WithTimeout(ctx, 2*time.Minute)
			defer stop()
			return player.Play(deadline, c, v)
		},
	})
	if err != nil {
		return developmentError("method-replay", err)
	}
	report.Rejected = rejected
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return 74
	}
	if report.Excluded > 0 || len(rejected) > 0 {
		return 1
	}
	return 0
}

// baselineKind names the before side so a report says what it compared.
func baselineKind(snapshot string) string {
	if snapshot == "" {
		return "shipped-layers-without-record"
	}
	return "snapshot"
}

// replayVariants builds the tailored variant from the shipped triple and the
// untailored one from a snapshot or, by default, the shipped layers without a
// work-method record.
func replayVariants(baseline string) ([]methodreplay.Variant, error) {
	tailored := methodreplay.Variant{Name: "tailored", Presets: map[string]methodreplay.Preset{}}
	before := methodreplay.Variant{Name: "baseline", Presets: map[string]methodreplay.Preset{}}
	for _, starter := range persona.Starters() {
		core, voice, err := persona.Load(persona.Shipped(), starter.Key)
		if err != nil {
			return nil, err
		}
		record, missing, err := persona.LoadWorkMethod(persona.Shipped(), starter.Key)
		if err != nil || missing {
			return nil, errors.New("shipped work method unavailable")
		}
		tailored.Presets[starter.Key] = methodreplay.Preset{Layers: agents.Layers{Core: core, DefaultVoice: voice}, WorkMethod: record}
		before.Presets[starter.Key] = methodreplay.Preset{Layers: agents.Layers{Core: core, DefaultVoice: voice}, WorkMethod: persona.EmptyWorkMethod(starter.Key)}
	}
	if baseline == "" {
		return []methodreplay.Variant{before, tailored}, nil
	}
	snapshot, err := snapshotPresets(baseline)
	if err != nil {
		return nil, err
	}
	before.Presets = snapshot
	return []methodreplay.Variant{before, tailored}, nil
}

// snapshotPresets reads the untailored triple of each starter from a directory
// laid out like the shipped persona tree.
func snapshotPresets(dir string) (map[string]methodreplay.Preset, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	presets := map[string]methodreplay.Preset{}
	for _, starter := range persona.Starters() {
		core, voice, err := persona.Load(root.FS(), starter.Key)
		if err != nil {
			return nil, err
		}
		record, missing, err := persona.LoadWorkMethod(root.FS(), starter.Key)
		if err != nil {
			return nil, err
		}
		if missing {
			record = persona.EmptyWorkMethod(starter.Key)
		}
		presets[starter.Key] = methodreplay.Preset{Layers: agents.Layers{Core: core, DefaultVoice: voice}, WorkMethod: record}
	}
	return presets, nil
}
