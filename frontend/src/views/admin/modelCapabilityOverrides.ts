export interface ModelCapabilityOverrides {
  context_window?: number | null
  max_context_window?: number | null
  effective_context_window_percent?: number | null
  max_output_tokens?: number | null
}

export interface ModelCapabilityForm {
  context_window: string
  max_context_window: string
  effective_context_window_percent: string
  max_output_tokens: string
}

const fields = [
  'context_window',
  'max_context_window',
  'effective_context_window_percent',
  'max_output_tokens',
] as const

type CapabilityField = (typeof fields)[number]

export function emptyModelCapabilityForm(): ModelCapabilityForm {
  return {
    context_window: '',
    max_context_window: '',
    effective_context_window_percent: '',
    max_output_tokens: '',
  }
}

export function modelCapabilityFormFromAPI(raw: unknown): ModelCapabilityForm {
  const form = emptyModelCapabilityForm()
  if (!raw || typeof raw !== 'object') {
    return form
  }
  const source = raw as Record<string, unknown>
  for (const field of fields) {
    const value = source[field]
    if (typeof value === 'number' && Number.isFinite(value)) {
      form[field] = String(value)
    }
  }
  return form
}

function parseField(value: string): number | null {
  const trimmed = value.trim()
  if (!trimmed) {
    return null
  }
  if (!/^[1-9]\d*$/.test(trimmed)) {
    return Number.NaN
  }
  const parsed = Number(trimmed)
  if (!Number.isSafeInteger(parsed)) {
    return Number.NaN
  }
  return parsed
}

export function validateModelCapabilityForm(form: ModelCapabilityForm): string | null {
  const parsed: Partial<Record<CapabilityField, number>> = {}
  for (const field of fields) {
    const value = parseField(form[field])
    if (Number.isNaN(value)) {
      return `admin.groups.form.modelCapability.${field}Invalid`
    }
    if (value != null) {
      parsed[field] = value
    }
  }
  const percent = parsed.effective_context_window_percent
  if (percent != null && percent > 100) {
    return 'admin.groups.form.modelCapability.effective_context_window_percentInvalid'
  }
  const contextWindow = parsed.context_window
  const maxContextWindow = parsed.max_context_window
  if (contextWindow != null && maxContextWindow != null && contextWindow > maxContextWindow) {
    return 'admin.groups.form.modelCapability.contextExceedsMax'
  }
  return null
}

export function modelCapabilityFormToAPI(form: ModelCapabilityForm): ModelCapabilityOverrides {
  const overrides: ModelCapabilityOverrides = {}
  for (const field of fields) {
    const value = parseField(form[field])
    if (value != null && !Number.isNaN(value)) {
      overrides[field] = value
    }
  }
  return overrides
}

export function modelCapabilityPreview(form: ModelCapabilityForm): { compact: number; usable: number } | null {
  const contextWindow = parseField(form.context_window)
  if (contextWindow == null || Number.isNaN(contextWindow)) {
    return null
  }
  const percent = parseField(form.effective_context_window_percent)
  const usablePercent = percent == null || Number.isNaN(percent) ? 95 : percent
  return {
    compact: Math.floor((contextWindow * 9) / 10),
    usable: Math.floor((contextWindow * usablePercent) / 100),
  }
}
