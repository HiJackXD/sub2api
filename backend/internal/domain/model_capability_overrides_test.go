package domain

import "testing"

func TestNormalizeModelCapabilityOverrides(t *testing.T) {
	t.Parallel()

	window := int64(128000)
	maxWindow := int64(256000)
	percent := 80
	output := int64(8192)
	got, err := NormalizeModelCapabilityOverrides(ModelCapabilityOverrides{
		ContextWindow:                 &window,
		MaxContextWindow:              &maxWindow,
		EffectiveContextWindowPercent: &percent,
		MaxOutputTokens:               &output,
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got.ContextWindow == nil || *got.ContextWindow != window || got.ContextWindow == &window {
		t.Fatalf("context_window = %v, want cloned %d", got.ContextWindow, window)
	}

	zero := int64(0)
	if _, err := NormalizeModelCapabilityOverrides(ModelCapabilityOverrides{ContextWindow: &zero}); err == nil {
		t.Fatal("expected zero context_window to be rejected")
	}
	low := int64(200000)
	high := int64(100000)
	if _, err := NormalizeModelCapabilityOverrides(ModelCapabilityOverrides{ContextWindow: &low, MaxContextWindow: &high}); err == nil {
		t.Fatal("expected context_window above max_context_window to be rejected")
	}
	badPercent := 0
	if _, err := NormalizeModelCapabilityOverrides(ModelCapabilityOverrides{EffectiveContextWindowPercent: &badPercent}); err == nil {
		t.Fatal("expected percent 0 to be rejected")
	}
	overPercent := 101
	if _, err := NormalizeModelCapabilityOverrides(ModelCapabilityOverrides{EffectiveContextWindowPercent: &overPercent}); err == nil {
		t.Fatal("expected percent 101 to be rejected")
	}
}

func TestParseModelCapabilityOverridesFromExtra(t *testing.T) {
	t.Parallel()

	got, err := ParseModelCapabilityOverrides(map[string]any{"context_window": float64(64000)})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.ContextWindow == nil || *got.ContextWindow != 64000 {
		t.Fatalf("context_window = %v", got.ContextWindow)
	}
	if _, err := ParseModelCapabilityOverrides("64000"); err == nil {
		t.Fatal("expected a non-object to be rejected")
	}
	empty, err := ParseModelCapabilityOverrides(map[string]any{})
	if err != nil || !empty.IsZero() {
		t.Fatalf("empty object = %#v, %v", empty, err)
	}
}
