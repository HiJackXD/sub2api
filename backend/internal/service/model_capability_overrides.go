package service

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ValidateModelCapabilityOverridesExtra rejects a malformed account extra value
// before any upstream call. A missing key is valid.
func ValidateModelCapabilityOverridesExtra(extra map[string]any) error {
	_, err := normalizeModelCapabilityOverridesExtra(extra)
	return err
}

func normalizeModelCapabilityOverrides(in ModelCapabilityOverrides) (ModelCapabilityOverrides, error) {
	out, err := domain.NormalizeModelCapabilityOverrides(in)
	if err != nil {
		return ModelCapabilityOverrides{}, infraerrors.BadRequest("INVALID_MODEL_CAPABILITY_OVERRIDES", err.Error())
	}
	return out, nil
}

// normalizeModelCapabilityOverridesExtra validates and canonicalizes the account
// extra key. A missing key is left untouched. An empty object clears overrides.
func normalizeModelCapabilityOverridesExtra(extra map[string]any) (map[string]any, error) {
	if extra == nil {
		return extra, nil
	}
	raw, exists := extra[domain.ModelCapabilityOverridesExtraKey]
	if !exists {
		return extra, nil
	}
	parsed, err := domain.ParseModelCapabilityOverrides(raw)
	if err != nil {
		return nil, infraerrors.BadRequest("INVALID_MODEL_CAPABILITY_OVERRIDES", err.Error())
	}
	cloned := maps.Clone(extra)
	if cloned == nil {
		cloned = map[string]any{}
	}
	if parsed.IsZero() {
		cloned[domain.ModelCapabilityOverridesExtraKey] = map[string]any{}
		return cloned, nil
	}
	body, err := json.Marshal(parsed)
	if err != nil {
		return nil, fmt.Errorf("encode model capability overrides: %w", err)
	}
	var encoded map[string]any
	if err := json.Unmarshal(body, &encoded); err != nil {
		return nil, fmt.Errorf("encode model capability overrides: %w", err)
	}
	cloned[domain.ModelCapabilityOverridesExtraKey] = encoded
	return cloned, nil
}

// ModelCapabilityOverrides reads advertised catalog overrides from account extra.
// Malformed stored values are ignored so a model list can still be served.
func (a *Account) ModelCapabilityOverrides() ModelCapabilityOverrides {
	if a == nil || a.Extra == nil {
		return ModelCapabilityOverrides{}
	}
	parsed, err := domain.ParseModelCapabilityOverrides(a.Extra[domain.ModelCapabilityOverridesExtraKey])
	if err != nil {
		return ModelCapabilityOverrides{}
	}
	return parsed
}

func accountsHaveCapabilityOverrides(accounts []Account) bool {
	for i := range accounts {
		if !accounts[i].ModelCapabilityOverrides().IsZero() {
			return true
		}
	}
	return false
}

// applyCatalogModelCapabilityOverrides rewrites context fields on a Codex or
// OpenAI model list. Group fields replace account fields. Account fields apply
// only when every account that can serve the model advertises the same value.
// Missing keys are not added, and an empty policy returns the original bytes.
func applyCatalogModelCapabilityOverrides(
	body []byte,
	group *Group,
	accounts []Account,
	platform string,
	routes []CompositeModelRoute,
	routesAvailable bool,
) ([]byte, error) {
	groupOverrides := ModelCapabilityOverrides{}
	if group != nil {
		groupOverrides = group.ModelCapabilityOverrides
	}
	if groupOverrides.IsZero() && !accountsHaveCapabilityOverrides(accounts) {
		return body, nil
	}
	perModel := map[string]ModelCapabilityOverrides{}
	if accountsHaveCapabilityOverrides(accounts) {
		for _, modelID := range catalogModelIDs(body) {
			agreed := agreeAccountCapabilityOverrides(codexServingAccountsForModel(
				platform, modelID, accounts, group, routes, routesAvailable,
			))
			if !agreed.IsZero() {
				perModel[modelID] = agreed
			}
		}
	}
	return applyModelCapabilityOverridesBody(body, groupOverrides, perModel)
}

func applyModelCapabilityOverridesBody(body []byte, groupOverrides ModelCapabilityOverrides, accountOverridesByModel map[string]ModelCapabilityOverrides) ([]byte, error) {
	if len(body) == 0 || (groupOverrides.IsZero() && len(accountOverridesByModel) == 0) {
		return body, nil
	}
	parsed := gjson.ParseBytes(body)
	models := parsed.Get("models")
	path := "models"
	if !models.IsArray() {
		models = parsed.Get("data")
		path = "data"
	}
	if !models.IsArray() {
		return body, nil
	}

	updated := body
	changed := false
	for index, model := range models.Array() {
		modelID := strings.TrimSpace(model.Get("slug").String())
		if modelID == "" {
			modelID = strings.TrimSpace(model.Get("id").String())
		}
		resolved := groupOverrides.Overlay(accountOverridesByModel[modelID])
		if resolved.IsZero() {
			continue
		}
		for _, field := range capabilityOverrideNumbers(resolved) {
			current := model.Get(field.name)
			if !current.Exists() {
				continue
			}
			if current.Type == gjson.Number && current.Int() == field.value {
				continue
			}
			var err error
			updated, err = sjson.SetBytes(updated, fmt.Sprintf("%s.%d.%s", path, index, field.name), field.value)
			if err != nil {
				return nil, fmt.Errorf("set %s on model %q: %w", field.name, modelID, err)
			}
			changed = true
		}
	}
	if !changed {
		return body, nil
	}
	return updated, nil
}

type capabilityOverrideNumber struct {
	name  string
	value int64
}

func capabilityOverrideNumbers(overrides ModelCapabilityOverrides) []capabilityOverrideNumber {
	fields := make([]capabilityOverrideNumber, 0, 4)
	if overrides.ContextWindow != nil {
		fields = append(fields, capabilityOverrideNumber{name: "context_window", value: *overrides.ContextWindow})
	}
	if overrides.MaxContextWindow != nil {
		fields = append(fields, capabilityOverrideNumber{name: "max_context_window", value: *overrides.MaxContextWindow})
	}
	if overrides.EffectiveContextWindowPercent != nil {
		fields = append(fields, capabilityOverrideNumber{name: "effective_context_window_percent", value: int64(*overrides.EffectiveContextWindowPercent)})
	}
	if overrides.MaxOutputTokens != nil {
		fields = append(fields, capabilityOverrideNumber{name: "max_output_tokens", value: *overrides.MaxOutputTokens})
	}
	return fields
}

func catalogModelIDs(body []byte) []string {
	parsed := gjson.ParseBytes(body)
	models := parsed.Get("models")
	if !models.IsArray() {
		models = parsed.Get("data")
	}
	if !models.IsArray() {
		return nil
	}
	ids := make([]string, 0, len(models.Array()))
	seen := map[string]struct{}{}
	for _, model := range models.Array() {
		modelID := strings.TrimSpace(model.Get("slug").String())
		if modelID == "" {
			modelID = strings.TrimSpace(model.Get("id").String())
		}
		if modelID == "" {
			continue
		}
		if _, ok := seen[modelID]; ok {
			continue
		}
		seen[modelID] = struct{}{}
		ids = append(ids, modelID)
	}
	return ids
}

func agreeAccountCapabilityOverrides(accounts []Account) ModelCapabilityOverrides {
	if len(accounts) == 0 {
		return ModelCapabilityOverrides{}
	}
	agreed := accounts[0].ModelCapabilityOverrides()
	for i := 1; i < len(accounts); i++ {
		agreed = intersectCapabilityOverrides(agreed, accounts[i].ModelCapabilityOverrides())
	}
	return agreed
}

func intersectCapabilityOverrides(left, right ModelCapabilityOverrides) ModelCapabilityOverrides {
	return ModelCapabilityOverrides{
		ContextWindow:                 sameInt64Override(left.ContextWindow, right.ContextWindow),
		MaxContextWindow:              sameInt64Override(left.MaxContextWindow, right.MaxContextWindow),
		EffectiveContextWindowPercent: sameIntOverride(left.EffectiveContextWindowPercent, right.EffectiveContextWindowPercent),
		MaxOutputTokens:               sameInt64Override(left.MaxOutputTokens, right.MaxOutputTokens),
	}
}

func sameInt64Override(left, right *int64) *int64 {
	if left == nil || right == nil || *left != *right {
		return nil
	}
	return left
}

func sameIntOverride(left, right *int) *int {
	if left == nil || right == nil || *left != *right {
		return nil
	}
	return left
}

// codexServingAccountsForModel returns catalog accounts that can serve modelID.
// The filter matches the account set used when intersecting Codex metadata.
func codexServingAccountsForModel(
	platform string,
	modelID string,
	accounts []Account,
	group *Group,
	compositeRoutes []CompositeModelRoute,
	compositeRoutesAvailable bool,
) []Account {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return nil
	}
	routedAccountIDs := codexModelRoutingAccountIDs(group, modelID)
	routed := len(routedAccountIDs) > 0
	upstreamModel := modelID
	if platform == PlatformComposite {
		var resolved bool
		platform, upstreamModel, resolved = resolveCodexCompositeModelTarget(
			modelID,
			accounts,
			compositeRoutes,
			compositeRoutesAvailable,
		)
		if !resolved {
			return nil
		}
	}
	if !isConcreteRequestPlatform(platform) {
		return nil
	}

	explicitClaims := false
	if upstreamModel == modelID {
		for i := range accounts {
			account := &accounts[i]
			if routed && !containsInt64(routedAccountIDs, account.ID) {
				continue
			}
			if codexAccountServesPlatformModel(account, platform, modelID) && codexExplicitModelMappingClaims(*account, modelID) {
				explicitClaims = true
				break
			}
		}
	}

	serving := make([]Account, 0)
	for i := range accounts {
		account := &accounts[i]
		if !codexAccountServesPlatformModel(account, platform, modelID) {
			continue
		}
		if routed && !containsInt64(routedAccountIDs, account.ID) {
			continue
		}
		if explicitClaims {
			if !codexExplicitModelMappingClaims(*account, modelID) {
				continue
			}
		} else if !account.IsModelSupported(upstreamModel) {
			continue
		}
		serving = append(serving, *account)
	}
	return serving
}
