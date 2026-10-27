-- Advertised model-capability overrides for a group. Empty object means
-- "leave the upstream or built-in catalog value unchanged". These fields are
-- copied onto client-facing model lists only; request bodies are not rewritten.
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS model_capability_overrides JSONB NOT NULL DEFAULT '{}'::jsonb;

COMMENT ON COLUMN groups.model_capability_overrides IS
    'Optional advertised model capability overrides: context_window, max_context_window, effective_context_window_percent, max_output_tokens. Absent fields keep the catalog value. Not applied to forwarded requests.';
