// SPDX-License-Identifier: Apache-2.0
package intakereplay

func Score(c Case, bindings []Binding) Metrics {
	m := Metrics{Cases: 1, Passed: 1, Requests: len(c.Gold), Confusion: map[string]int{}}
	for i, g := range c.Gold {
		if g.Ask {
			m.Warranted++
		}
		if i >= len(bindings) {
			m.MissingBindings++
			m.Passed = 0
			continue
		}
		b := bindings[i]
		if b.Ask {
			if g.Ask {
				m.AskedWarranted++
			} else {
				m.NeedlessQuestions++
				m.Passed = 0
			}
			continue
		}
		m.Picks++
		if b.Target.Class == g.Target.Class {
			m.ClassRight++
		}
		if b.Target == g.Target {
			m.RightItem++
		} else {
			m.SilentWrong++
			m.Confusion[g.Target.Class+"->"+b.Target.Class]++
			if running(c, g.Target) || running(c, b.Target) {
				m.RunningSilentWrong++
			}
			m.Passed = 0
		}
		if g.Ask {
			m.Passed = 0
		}
	}
	for _, b := range bindings[min(len(c.Gold), len(bindings)):] {
		m.ExtraBindings++
		m.Passed = 0
		if b.Ask {
			m.NeedlessQuestions++
		} else {
			m.Picks++
			m.SilentWrong++
			m.Confusion["missing->"+b.Target.Class]++
			if running(c, b.Target) {
				m.RunningSilentWrong++
			}
		}
	}
	if len(c.Gold) == 0 {
		m.Passed = 0
	}
	return m
}
func running(c Case, t Target) bool {
	if c.State == nil {
		return false
	}
	for _, i := range c.State.Items {
		if i.ID == t.ID && (i.Kind == "task" || i.Kind == "run") && (i.State == "running" || i.State == "waiting" || i.State == "needs_input") {
			return true
		}
	}
	return false
}
func (m *Metrics) add(v Metrics) {
	m.Cases += v.Cases
	m.Passed += v.Passed
	m.Requests += v.Requests
	m.Picks += v.Picks
	m.RightItem += v.RightItem
	m.ClassRight += v.ClassRight
	m.Warranted += v.Warranted
	m.AskedWarranted += v.AskedWarranted
	m.NeedlessQuestions += v.NeedlessQuestions
	m.SilentWrong += v.SilentWrong
	m.RunningSilentWrong += v.RunningSilentWrong
	m.MissingBindings += v.MissingBindings
	m.ExtraBindings += v.ExtraBindings
	if m.Confusion == nil {
		m.Confusion = map[string]int{}
	}
	for k, n := range v.Confusion {
		m.Confusion[k] += n
	}
}
func ratio(n, d int) Ratio {
	r := Ratio{Numerator: n, Denominator: d}
	if d > 0 {
		v := float64(n) / float64(d)
		r.Rate = &v
	}
	return r
}
