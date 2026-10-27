import { describe, expect, it } from 'vitest'
import {
  emptyModelCapabilityForm,
  modelCapabilityFormFromAPI,
  modelCapabilityFormToAPI,
  modelCapabilityPreview,
  validateModelCapabilityForm,
} from '../modelCapabilityOverrides'

describe('modelCapabilityOverrides', () => {
  it('leaves every field empty when the API object is absent', () => {
    expect(modelCapabilityFormToAPI(emptyModelCapabilityForm())).toEqual({})
    expect(modelCapabilityFormFromAPI(undefined)).toEqual(emptyModelCapabilityForm())
  })

  it('rejects a context window larger than the max and a percent outside 1-100', () => {
    const form = {
      ...emptyModelCapabilityForm(),
      context_window: '200',
      max_context_window: '100',
    }
    expect(validateModelCapabilityForm(form)).toBe('admin.groups.form.modelCapability.contextExceedsMax')

    form.max_context_window = ''
    form.effective_context_window_percent = '0'
    expect(validateModelCapabilityForm(form)).toBe(
      'admin.groups.form.modelCapability.effective_context_window_percentInvalid',
    )
  })

  it('shows the Codex compact line and usable window from the raw context window', () => {
    const form = { ...emptyModelCapabilityForm(), context_window: '128000' }
    expect(modelCapabilityPreview(form)).toEqual({ compact: 115200, usable: 121600 })
    expect(modelCapabilityFormToAPI(form)).toEqual({ context_window: 128000 })
  })
})
