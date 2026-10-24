// SPDX-License-Identifier: Apache-2.0
package methodreplay

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func runCase(id, role string, mustNotChange bool) Case {
	return Case{Version: 1, ID: id, Role: role, ReplyType: "status", Kind: "correction", Correction: "length",
		Checks: []Check{Length}, Budget: 40, Language: "ko", MustNotChange: mustNotChange,
		Turns: []Turn{{Role: "user", Text: "상태 알려줘"}}}
}

// The tailored variant must improve its agent and regress no other agent.
func TestRunComparesVariantsPerAgent(t *testing.T) {
	cases := []Case{
		runCase("dev-001", "dev", false),
		runCase("dev-002", "dev", true),
		runCase("life-001", "life", false),
	}
	play := func(_ context.Context, c Case, v Variant) (string, error) {
		if c.Role == "life" {
			return "확인했습니다.", nil
		}
		if c.ID == "dev-002" {
			if v.Name == "baseline" {
				return "확인했습니다.", nil
			}
			return strings.Repeat("가", 60), nil
		}
		if v.Name == "baseline" {
			return strings.Repeat("가", 60), nil
		}
		return "확인했습니다.", nil
	}
	report, err := Run(t.Context(), Config{Cases: cases, Variants: []Variant{{Name: "baseline"}, {Name: "tailored"}},
		Play: play, Repeat: 3, Changed: []string{"dev"}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Scored != 3 || report.Excluded != 0 || report.Variants[0] != "baseline" {
		t.Fatalf("report=%+v", report)
	}
	dev := report.Agents[0]
	if dev.Agent != "dev" || dev.Cases != 2 {
		t.Fatalf("agents=%+v", report.Agents)
	}
	if len(dev.Regressions) != 1 || dev.Regressions[0] != "dev-002" {
		t.Fatalf("regressions=%v", dev.Regressions)
	}
	if report.Ship.Ships || len(report.Ship.Reasons) == 0 {
		t.Fatalf("a regressed negative case must block the change: %+v", report.Ship)
	}
}

func TestRunShipsWhenTheAgentImprovesAndOthersHold(t *testing.T) {
	cases := []Case{runCase("dev-001", "dev", false), runCase("life-001", "life", false)}
	play := func(_ context.Context, c Case, v Variant) (string, error) {
		if c.Role == "dev" && v.Name == "baseline" {
			return strings.Repeat("가", 60), nil
		}
		return "확인했습니다.", nil
	}
	report, err := Run(t.Context(), Config{Cases: cases, Variants: []Variant{{Name: "baseline"}, {Name: "tailored"}},
		Play: play, Repeat: 3, Changed: []string{"dev"}})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Ship.Ships || report.Agents[0].Verdict != "improved" || report.Agents[1].Verdict != "unchanged" {
		t.Fatalf("report=%+v", report)
	}
}

func TestRunRequiresANamedChange(t *testing.T) {
	report, err := Run(t.Context(), Config{Cases: []Case{runCase("dev-001", "dev", false)},
		Variants: []Variant{{Name: "baseline"}, {Name: "tailored"}}, Repeat: 3,
		Play: func(context.Context, Case, Variant) (string, error) { return "확인했습니다.", nil }})
	if err != nil {
		t.Fatal(err)
	}
	if report.Ship.Ships || len(report.Ship.Reasons) == 0 {
		t.Fatalf("unnamed change shipped: %+v", report.Ship)
	}
}

func TestRunRecordsFailuresWithoutText(t *testing.T) {
	report, err := Run(t.Context(), Config{Cases: []Case{runCase("dev-001", "dev", false)},
		Variants: []Variant{{Name: "baseline"}, {Name: "tailored"}},
		Play: func(_ context.Context, _ Case, v Variant) (string, error) {
			if v.Name == "tailored" {
				return "", errors.New("provider unavailable")
			}
			return "확인했습니다.", nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Excluded != 1 || report.ExclusionReasons["model_error"] != 1 || report.Agents[0].Verdict != "insufficient" {
		t.Fatalf("report=%+v", report)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"상태 알려줘", "확인했습니다", "가가가"} {
		if strings.Contains(string(raw), text) {
			t.Fatalf("report leaked reply or case text: %s", raw)
		}
	}
}

func TestRunExcludesACaseThatLostARun(t *testing.T) {
	// With three repeats, a case with two failed tailored runs is excluded even
	// when its other runs completed: it is never compared as a pass or a drop.
	c := runCase("dev-001", "dev", false)
	plays := 0
	report, err := Run(t.Context(), Config{Cases: []Case{c},
		Variants: []Variant{{Name: "baseline"}, {Name: "tailored"}}, Repeat: 3, Changed: []string{"dev"},
		Play: func(_ context.Context, _ Case, v Variant) (string, error) {
			if v.Name == "tailored" {
				plays++
				if plays > 1 {
					return "", errors.New("provider unavailable")
				}
			}
			return "확인했습니다.", nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range report.Cases[0].Variants {
		if v.Name == "tailored" && (v.Reason != "model_error" || v.Runs != 1) {
			t.Fatalf("partial variant not marked: %+v", v)
		}
	}
	if report.Excluded != 1 {
		t.Fatalf("report=%+v", report)
	}
	if len(report.Agents) != 1 || report.Agents[0].Cases != 0 || report.Agents[0].Verdict != "insufficient" {
		t.Fatalf("a partial case reached the comparison: %+v", report.Agents)
	}
	if report.Ship.Ships {
		t.Fatalf("a partial case decided the ship rule: %+v", report.Ship)
	}
}

func TestRunRefusesToShipBelowThreeRepeats(t *testing.T) {
	cases := []Case{runCase("dev-001", "dev", false), runCase("life-001", "life", false)}
	play := func(_ context.Context, c Case, v Variant) (string, error) {
		if c.Role == "dev" && v.Name == "baseline" {
			return strings.Repeat("가", 60), nil
		}
		return "확인했습니다.", nil
	}
	for _, repeat := range []int{1, 2} {
		report, err := Run(t.Context(), Config{Cases: cases, Variants: []Variant{{Name: "baseline"}, {Name: "tailored"}},
			Play: play, Repeat: repeat, Changed: []string{"dev"}})
		if err != nil {
			t.Fatal(err)
		}
		if report.Agents[0].Verdict != "improved" || report.Agents[1].Verdict != "unchanged" {
			t.Fatalf("repeat %d comparison: %+v", repeat, report.Agents)
		}
		if report.Ship.Ships || !contains(report.Ship.Reasons, "fewer than three repeats") {
			t.Fatalf("repeat %d shipped: %+v", repeat, report.Ship)
		}
	}
}

func TestRunExcludesAPartialBaseline(t *testing.T) {
	// One successful baseline reply and two model errors is excluded, even
	// though the tailored side completed every run.
	plays := 0
	report, err := Run(t.Context(), Config{Cases: []Case{runCase("dev-001", "dev", false)},
		Variants: []Variant{{Name: "baseline"}, {Name: "tailored"}}, Repeat: 3, Changed: []string{"dev"},
		Play: func(_ context.Context, _ Case, v Variant) (string, error) {
			if v.Name == "baseline" {
				plays++
				if plays > 1 {
					return "", errors.New("provider unavailable")
				}
			}
			return "확인했습니다.", nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Excluded != 1 || report.Scored != 0 {
		t.Fatalf("report=%+v", report)
	}
	if len(report.Agents) != 1 || report.Agents[0].Cases != 0 || report.Agents[0].Verdict != "insufficient" {
		t.Fatalf("a partial baseline reached the comparison: %+v", report.Agents)
	}
	if report.Ship.Ships {
		t.Fatalf("a partial baseline decided the ship rule: %+v", report.Ship)
	}
}

func TestRunDropsInjectedTurnsBeforePlaying(t *testing.T) {
	c := runCase("dev-001", "dev", false)
	c.Turns = append([]Turn{{Role: "user", Text: "압축 요약", Injected: true}}, c.Turns...)
	var seen []Turn
	_, err := Run(t.Context(), Config{Cases: []Case{c}, Variants: []Variant{{Name: "baseline"}, {Name: "tailored"}},
		Play: func(_ context.Context, c Case, _ Variant) (string, error) {
			kept, _ := Dropped(c)
			seen = kept
			return "확인했습니다.", nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0].Text != "상태 알려줘" {
		t.Fatalf("injected turn reached the model: %v", seen)
	}
}
