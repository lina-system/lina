// SPDX-License-Identifier: Apache-2.0

// Command localmodel-eval scores the short-task route against a fixed eval set.
// It runs each case through the same routing and adapters Core uses, once on
// the on-device helper and once on the person's main model, and prints a
// measured table. It is an evidence tool, not part of a product build.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/localmodel"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/provider/applefm"
	"github.com/thisisjun786/lina/kit/provider/responses"
)

// Set is the fixed eval set. Dates are absolute so the model needs no clock.
type Set struct {
	Version int    `json:"version"`
	Cases   []Case `json:"cases"`
}

// Case is one short task and how to score its answer.
type Case struct {
	ID string `json:"id"`
	// Kind is one of the short-task kinds Core routes.
	Kind string `json:"kind"`
	// Line is what the person typed. It is recorded, not sent.
	Line string `json:"line"`
	// Prompt is the exact input the caller sends. It defaults to Line, and
	// carries the vocabulary a schema of plain strings cannot.
	Prompt string          `json:"prompt,omitempty"`
	Schema json.RawMessage `json:"schema,omitempty"`
	Expect Expect          `json:"expect"`
}

// Expect lists the checks that make an answer correct.
type Expect struct {
	Fields      map[string]string   `json:"fields,omitempty"`
	FieldAny    map[string][]string `json:"field_any,omitempty"`
	MustInclude []string            `json:"must_include,omitempty"`
	AnyOf       []string            `json:"any_of,omitempty"`
	MustExclude []string            `json:"must_exclude,omitempty"`
	MaxChars    int                 `json:"max_chars,omitempty"`
}

type sample struct {
	OK      bool
	Detail  string
	Route   string
	Reason  string
	Service string
	Model   string
	Millis  float64
}

type pathResult struct {
	// Answer is the last run's answer. Failures are recorded per run, so a
	// sample answer is never paired with another run's miss.
	Answer    string
	Correct   int
	Runs      int
	Latencies []float64
	P50       float64
	P95       float64
	Route     string
	Reason    string
	Provider  string
	Model     string
	Failures  []failure
}

type failure struct {
	Run    int    `json:"run"`
	Detail string `json:"detail"`
}

type caseResult struct {
	ID    string     `json:"id"`
	Kind  string     `json:"kind"`
	Local pathResult `json:"local"`
	Main  pathResult `json:"main"`
}

func main() {
	var (
		setPath   = flag.String("set", "testdata/localmodel-eval/set.json", "eval set")
		helper    = flag.String("helper", "", "absolute path to the Apple Foundation Models helper")
		model     = flag.String("model", applefm.OnDeviceModel, "on-device model id")
		mainBase  = flag.String("main-base", "", "main model Responses base URL, including /v1")
		mainModel = flag.String("main-model", "", "main model id")
		mainKey   = flag.String("main-key", "", "main model API key; a local proxy may need none")
		repeat    = flag.Int("repeat", 10, "measured runs per case per path")
		warmup    = flag.Int("warmup", 1, "unmeasured runs per case per path")
		timeout   = flag.Duration("timeout", 90*time.Second, "deadline for one call")
		out       = flag.String("out", "", "write per-case results as JSON here")
	)
	flag.Parse()

	set, err := loadSet(*setPath)
	if err != nil {
		fail(err)
	}
	if *repeat < 1 {
		fail(errors.New("-repeat must be at least 1"))
	}

	ctx := context.Background()
	l := lease.New(func() {})
	deps := localmodel.Deps{}

	if *helper != "" {
		adapter, err := applefm.New(applefm.Settings{Helper: *helper, Model: *model, Env: helperEnv()})
		if err != nil {
			fail(err)
		}
		profile, err := adapter.Probe(ctx, l)
		if err != nil {
			fail(fmt.Errorf("probe %s: %w", *helper, err))
		}
		deps.Profile = profile
		deps.Local = localmodel.Apple{Adapter: adapter}
		fmt.Printf("local: %s context=%d guided=%t\n", profile.Model, profile.ContextTokens, profile.GuidedGeneration)
	}
	if *mainBase != "" && *mainModel != "" {
		policy, err := responses.BuildConfig()
		if err != nil {
			fail(err)
		}
		adapter, err := responses.NewWithBaseURL(policy, nil, *mainBase)
		if err != nil {
			fail(err)
		}
		credential := provider.NewCredential("localmodel-eval", provider.APIKey, *mainKey)
		ref := provider.ModelRef(*mainModel)
		deps.Main = localmodel.AdapterModel{Adapter: adapter, Model: ref, Request: func(items []provider.Item) provider.Request {
			return provider.Request{Credential: credential, Model: ref, Items: items}
		}}
		fmt.Printf("main: %s at %s\n", *mainModel, *mainBase)
	}
	if deps.Local == nil && deps.Main == nil {
		fail(errors.New("select -helper or -main-base with -main-model"))
	}

	results := make([]caseResult, 0, len(set.Cases))
	for _, c := range set.Cases {
		row := caseResult{ID: c.ID, Kind: c.Kind}
		if deps.Local != nil {
			row.Local = runPath(ctx, l, deps, c, localmodel.ForceLocal, *warmup, *repeat, *timeout)
		}
		if deps.Main != nil {
			row.Main = runPath(ctx, l, deps, c, localmodel.ForceMain, *warmup, *repeat, *timeout)
		}
		results = append(results, row)
	}

	if *out != "" {
		body, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			fail(err)
		}
		if err := os.WriteFile(*out, append(body, '\n'), 0o644); err != nil {
			fail(err)
		}
	}
	printTable(results)
}

func loadSet(path string) (Set, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Set{}, err
	}
	var set Set
	if err := json.Unmarshal(body, &set); err != nil {
		return Set{}, err
	}
	if len(set.Cases) == 0 {
		return Set{}, errors.New("empty eval set")
	}
	seen := map[string]bool{}
	for _, c := range set.Cases {
		if c.ID == "" || seen[c.ID] {
			return Set{}, fmt.Errorf("duplicate or empty case id %q", c.ID)
		}
		seen[c.ID] = true
		if !knownKind(c.Kind) {
			return Set{}, fmt.Errorf("%s: unknown kind %q", c.ID, c.Kind)
		}
		if strings.TrimSpace(c.input()) == "" {
			return Set{}, fmt.Errorf("%s: empty input", c.ID)
		}
		if len(c.Schema) > 0 && !applefm.ValidSchema(c.Schema) {
			return Set{}, fmt.Errorf("%s: invalid schema", c.ID)
		}
	}
	return set, nil
}

func (c Case) input() string {
	if strings.TrimSpace(c.Prompt) != "" {
		return c.Prompt
	}
	return c.Line
}

func knownKind(kind string) bool {
	switch kind {
	case localmodel.Intent, localmodel.Autocomplete, localmodel.Rewrite, localmodel.Title,
		localmodel.Classify, localmodel.Extract, localmodel.Summary:
		return true
	}
	return false
}

func runPath(ctx context.Context, l lease.Lease, deps localmodel.Deps, c Case, preference string, warmup, repeat int, timeout time.Duration) pathResult {
	out := pathResult{Latencies: make([]float64, 0, repeat)}
	task := localmodel.Task{Kind: c.Kind, Text: c.input(), Schema: c.Schema, Preference: preference}
	for range warmup {
		_, _ = serve(ctx, l, deps, task, timeout)
	}
	for run := range repeat {
		start := time.Now()
		outcome, err := serve(ctx, l, deps, task, timeout)
		elapsed := time.Since(start).Seconds() * 1000
		out.Runs++
		out.Latencies = append(out.Latencies, elapsed)
		if err != nil {
			out.Failures = append(out.Failures, failure{Run: run, Detail: err.Error()})
			continue
		}
		out.Answer = answer(outcome)
		out.Route, out.Reason = outcome.Row.Route, outcome.Row.Reason
		out.Provider, out.Model = string(outcome.Row.Provider), outcome.Row.Model
		if err := score(c, outcome); err != nil {
			out.Failures = append(out.Failures, failure{Run: run, Detail: err.Error()})
			continue
		}
		out.Correct++
	}
	out.P50 = percentile(out.Latencies, 0.50)
	out.P95 = percentile(out.Latencies, 0.95)
	return out
}

func answer(outcome localmodel.Outcome) string {
	if len(outcome.Object) > 0 {
		return string(outcome.Object)
	}
	return strings.Join(strings.Fields(outcome.Text), " ")
}

func serve(ctx context.Context, l lease.Lease, deps localmodel.Deps, task localmodel.Task, timeout time.Duration) (localmodel.Outcome, error) {
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return localmodel.Serve(callCtx, l, task, deps)
}

func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	rank := int(float64(len(sorted)) * p)
	if float64(rank) < float64(len(sorted))*p {
		rank++
	}
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

func score(c Case, out localmodel.Outcome) error {
	if len(c.Schema) > 0 {
		if len(out.Object) == 0 {
			return errors.New("no object")
		}
		var object map[string]any
		if err := json.Unmarshal(out.Object, &object); err != nil {
			return fmt.Errorf("object: %v", err)
		}
		for name, want := range c.Expect.Fields {
			got, ok := object[name]
			if !ok {
				return fmt.Errorf("%s missing", name)
			}
			if !sameField(name, fmt.Sprint(got), want) {
				return fmt.Errorf("%s=%q want %q", name, fmt.Sprint(got), want)
			}
		}
		for name, alternatives := range c.Expect.FieldAny {
			got, ok := object[name]
			if !ok {
				return fmt.Errorf("%s missing", name)
			}
			if !anyContained(fmt.Sprint(got), alternatives) {
				return fmt.Errorf("%s=%q want any of %v", name, fmt.Sprint(got), alternatives)
			}
		}
		return nil
	}
	text := strings.TrimSpace(out.Text)
	if text == "" {
		return errors.New("empty text")
	}
	normalized := normalizeText(text)
	for _, want := range c.Expect.MustInclude {
		if !strings.Contains(normalized, normalizeText(want)) {
			return fmt.Errorf("missing %q", want)
		}
	}
	if len(c.Expect.AnyOf) > 0 && !anyContained(text, c.Expect.AnyOf) {
		return fmt.Errorf("missing any of %v", c.Expect.AnyOf)
	}
	for _, bad := range c.Expect.MustExclude {
		if strings.Contains(normalized, normalizeText(bad)) {
			return fmt.Errorf("contains %q", bad)
		}
	}
	if c.Expect.MaxChars > 0 {
		if n := utf8.RuneCountInString(text); n > c.Expect.MaxChars {
			return fmt.Errorf("%d chars over %d", n, c.Expect.MaxChars)
		}
	}
	return nil
}

func sameField(name, got, want string) bool {
	switch name {
	case "date":
		return normalizeDate(got) == normalizeDate(want)
	case "time":
		return normalizeTime(got) == normalizeTime(want)
	}
	return normalizeText(got) == normalizeText(want)
}

func anyContained(text string, alternatives []string) bool {
	normalized := normalizeText(text)
	for _, alternative := range alternatives {
		if strings.Contains(normalized, normalizeText(alternative)) {
			return true
		}
	}
	return false
}

func normalizeText(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

var datePattern = regexp.MustCompile(`(\d{4})\s*[-./년]\s*(\d{1,2})\s*[-./월]\s*(\d{1,2})`)

// normalizeDate reads 2026-10-25, 2026.10.25 and 2026년 10월 25일 alike.
func normalizeDate(s string) string {
	match := datePattern.FindStringSubmatch(s)
	if match == nil {
		return normalizeText(s)
	}
	year, _ := strconv.Atoi(match[1])
	month, _ := strconv.Atoi(match[2])
	day, _ := strconv.Atoi(match[3])
	return fmt.Sprintf("%04d-%02d-%02d", year, month, day)
}

var timePattern = regexp.MustCompile(`(\d{1,2})\s*[:시]\s*(\d{1,2})?`)

// normalizeTime reads 15:00, 3:00 PM and 오후 3시 alike, as 24-hour HH:MM.
func normalizeTime(s string) string {
	text := normalizeText(s)
	pm := strings.Contains(text, "오후") || strings.Contains(text, "pm") || strings.Contains(text, "p.m")
	am := strings.Contains(text, "오전") || strings.Contains(text, "am") || strings.Contains(text, "a.m")
	match := timePattern.FindStringSubmatch(text)
	if match == nil {
		return text
	}
	hour, _ := strconv.Atoi(match[1])
	minute := 0
	if match[2] != "" {
		minute, _ = strconv.Atoi(match[2])
	}
	if pm && hour < 12 {
		hour += 12
	}
	if am && hour == 12 {
		hour = 0
	}
	return fmt.Sprintf("%02d:%02d", hour, minute)
}

func printTable(results []caseResult) {
	type aggregate struct {
		cases, localOK, mainOK, runs int
		local, main                  []float64
	}
	kinds := []string{
		localmodel.Intent, localmodel.Autocomplete, localmodel.Rewrite, localmodel.Title,
		localmodel.Classify, localmodel.Extract, localmodel.Summary,
	}
	byKind := map[string]*aggregate{}
	for _, kind := range kinds {
		byKind[kind] = &aggregate{}
	}
	for _, row := range results {
		a := byKind[row.Kind]
		if a == nil {
			continue
		}
		a.cases++
		a.localOK += row.Local.Correct
		a.mainOK += row.Main.Correct
		a.local = append(a.local, row.Local.Latencies...)
		a.main = append(a.main, row.Main.Latencies...)
	}
	fmt.Println()
	fmt.Println("| Task kind | Cases | Local correct | Main correct | Local p50 | Local p95 | Main p50 | Main p95 |")
	fmt.Println("| --- | --- | --- | --- | --- | --- | --- | --- |")
	var totalCases, totalLocalOK, totalMainOK int
	var allLocal, allMain []float64
	for _, kind := range kinds {
		a := byKind[kind]
		if a.cases == 0 {
			continue
		}
		totalCases += a.cases
		totalLocalOK += a.localOK
		totalMainOK += a.mainOK
		allLocal = append(allLocal, a.local...)
		allMain = append(allMain, a.main...)
		fmt.Printf("| %s | %d | %s | %s | %s | %s | %s | %s |\n", kind, a.cases,
			fraction(a.localOK, len(a.local)), fraction(a.mainOK, len(a.main)),
			millis(percentile(a.local, 0.50)), millis(percentile(a.local, 0.95)),
			millis(percentile(a.main, 0.50)), millis(percentile(a.main, 0.95)))
	}
	fmt.Printf("| all | %d | %s | %s | %s | %s | %s | %s |\n", totalCases,
		fraction(totalLocalOK, len(allLocal)), fraction(totalMainOK, len(allMain)),
		millis(percentile(allLocal, 0.50)), millis(percentile(allLocal, 0.95)),
		millis(percentile(allMain, 0.50)), millis(percentile(allMain, 0.95)))
	fmt.Println()
	for _, row := range results {
		for _, path := range []struct {
			name   string
			result pathResult
		}{{"local", row.Local}, {"main", row.Main}} {
			if path.result.Runs == 0 {
				continue
			}
			status := "ok"
			if path.result.Correct < path.result.Runs {
				status = fmt.Sprintf("FAIL %d/%d", path.result.Runs-path.result.Correct, path.result.Runs)
			}
			fmt.Printf("%s %s %s route=%s reason=%s served=%s/%s\n", row.ID, path.name, status,
				path.result.Route, path.result.Reason, path.result.Provider, path.result.Model)
			for _, miss := range path.result.Failures {
				fmt.Printf("  miss run %d: %s\n", miss.Run, miss.Detail)
			}
			fmt.Printf("  last answer: %q\n", truncate(path.result.Answer, 200))
		}
	}
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func fraction(part, total int) string {
	if total == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d/%d", part, total)
}

func millis(v float64) string {
	return fmt.Sprintf("%.0f ms", v)
}

func helperEnv() []string {
	var env []string
	for _, key := range []string{"HOME", "TMPDIR", "LANG", "LC_ALL"} {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "localmodel-eval:", err)
	os.Exit(1)
}
