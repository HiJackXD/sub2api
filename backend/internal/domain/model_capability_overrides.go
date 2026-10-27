package domain

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ModelCapabilityOverridesExtraKey is the account extra key that stores
// advertised model-capability overrides. Group overrides live on the group row.
const ModelCapabilityOverridesExtraKey = "model_capability_overrides"

// ModelCapabilityOverrides replaces selected fields on client-facing model
// catalogs. A nil field leaves the upstream or built-in value unchanged.
// These values are advertised only; they do not alter forwarded requests.
type ModelCapabilityOverrides struct {
	ContextWindow                 *int64 `json:"context_window,omitempty"`
	MaxContextWindow              *int64 `json:"max_context_window,omitempty"`
	EffectiveContextWindowPercent *int   `json:"effective_context_window_percent,omitempty"`
	MaxOutputTokens               *int64 `json:"max_output_tokens,omitempty"`
}

func (o ModelCapabilityOverrides) IsZero() bool {
	return o.ContextWindow == nil &&
		o.MaxContextWindow == nil &&
		o.EffectiveContextWindowPercent == nil &&
		o.MaxOutputTokens == nil
}

// Clone copies set values so stored overrides are not aliased to the caller.
func (o ModelCapabilityOverrides) Clone() ModelCapabilityOverrides {
	return ModelCapabilityOverrides{
		ContextWindow:                 cloneInt64(o.ContextWindow),
		MaxContextWindow:              cloneInt64(o.MaxContextWindow),
		EffectiveContextWindowPercent: cloneInt(o.EffectiveContextWindowPercent),
		MaxOutputTokens:               cloneInt64(o.MaxOutputTokens),
	}
}

// Overlay returns base with each set field of o replacing the corresponding
// base field. Group overrides use this so a group value wins over an account.
func (o ModelCapabilityOverrides) Overlay(base ModelCapabilityOverrides) ModelCapabilityOverrides {
	out := base.Clone()
	if o.ContextWindow != nil {
		out.ContextWindow = cloneInt64(o.ContextWindow)
	}
	if o.MaxContextWindow != nil {
		out.MaxContextWindow = cloneInt64(o.MaxContextWindow)
	}
	if o.EffectiveContextWindowPercent != nil {
		out.EffectiveContextWindowPercent = cloneInt(o.EffectiveContextWindowPercent)
	}
	if o.MaxOutputTokens != nil {
		out.MaxOutputTokens = cloneInt64(o.MaxOutputTokens)
	}
	return out
}

// NormalizeModelCapabilityOverrides checks admin input. Unset fields are kept.
// Zero and negative numbers are rejected so "clear" stays "omit the field".
func NormalizeModelCapabilityOverrides(in ModelCapabilityOverrides) (ModelCapabilityOverrides, error) {
	out := in.Clone()
	if err := requirePositiveInt64("context_window", out.ContextWindow); err != nil {
		return ModelCapabilityOverrides{}, err
	}
	if err := requirePositiveInt64("max_context_window", out.MaxContextWindow); err != nil {
		return ModelCapabilityOverrides{}, err
	}
	if err := requirePositiveInt64("max_output_tokens", out.MaxOutputTokens); err != nil {
		return ModelCapabilityOverrides{}, err
	}
	if out.EffectiveContextWindowPercent != nil {
		percent := *out.EffectiveContextWindowPercent
		if percent < 1 || percent > 100 {
			return ModelCapabilityOverrides{}, errors.New("effective_context_window_percent must be between 1 and 100")
		}
	}
	if out.ContextWindow != nil && out.MaxContextWindow != nil && *out.ContextWindow > *out.MaxContextWindow {
		return ModelCapabilityOverrides{}, errors.New("context_window must not exceed max_context_window")
	}
	return out, nil
}

// ParseModelCapabilityOverrides decodes an account extra value. nil and an
// empty object mean no overrides. Invalid shapes return a validation error.
func ParseModelCapabilityOverrides(raw any) (ModelCapabilityOverrides, error) {
	if raw == nil {
		return ModelCapabilityOverrides{}, nil
	}
	body, err := json.Marshal(raw)
	if err != nil {
		return ModelCapabilityOverrides{}, fmt.Errorf("model_capability_overrides must be an object: %w", err)
	}
	if string(body) == "null" {
		return ModelCapabilityOverrides{}, nil
	}
	var parsed ModelCapabilityOverrides
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ModelCapabilityOverrides{}, errors.New("model_capability_overrides must be an object of positive integers")
	}
	return NormalizeModelCapabilityOverrides(parsed)
}

func requirePositiveInt64(name string, value *int64) error {
	if value == nil {
		return nil
	}
	if *value <= 0 {
		return fmt.Errorf("%s must be a positive integer", name)
	}
	return nil
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
