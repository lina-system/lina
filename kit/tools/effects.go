// SPDX-License-Identifier: Apache-2.0
package tools

// EffectClass describes the operation, not what the caller says it intends.
// Only a registered Core definition or a trusted service profile classifies it,
// after the schema has validated and normalized its arguments.
type EffectClass string

const (
	Unclassified     EffectClass = "unclassified"
	Research         EffectClass = "research"
	Calculation      EffectClass = "calculation"
	PrivateDraft     EffectClass = "private_draft"
	LocalWrite       EffectClass = "local_write"
	ExternalEffect   EffectClass = "external_effect"
	InvestmentOrder  EffectClass = "investment_order"
	BindingExecution EffectClass = "binding_execution"
	CommandEffect    EffectClass = "command"
	WorkerEffect     EffectClass = "worker"
	RelayEffect      EffectClass = "relay"
)

type Classifier func(Input) EffectClass

func Class(class EffectClass) Classifier { return func(Input) EffectClass { return class } }

func (s Spec) EffectClass(in Input) EffectClass {
	if s.Classify == nil {
		return Unclassified
	}
	class := s.Classify(in)
	if class == "" {
		return Unclassified
	}
	if s.Outward && (class == Research || class == Calculation || class == PrivateDraft || class == RelayEffect) {
		return ExternalEffect
	}
	if in.Write && (class == Research || class == Calculation) {
		return Unclassified
	}
	return class
}

// ServiceProfile is installed by Core, never decoded from server annotations,
// model arguments, tool descriptions or a caller-supplied effect label. A
// classifier may inspect validated arguments for multipurpose service tools.
type ServiceProfile struct {
	Operations map[string]Classifier
	Outward    map[string]bool
}

func (p ServiceProfile) Apply(spec Spec) Spec {
	spec.Classify = p.Operations[spec.Name]
	spec.Outward = p.Outward[spec.Name]
	return spec
}
