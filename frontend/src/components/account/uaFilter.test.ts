import { describe, expect, it } from 'vitest'
import { normalizeUAFilter, presetPatterns } from './uaFilter'

describe('normalizeUAFilter', () => {
  it('clears patterns when off', () => {
    expect(normalizeUAFilter('off', ['claude-cli/'])).toEqual({ ok: true, mode: 'off', patterns: [] })
  })

  it('rejects an empty allow list', () => {
    expect(normalizeUAFilter('allow', ['  ']).ok).toBe(false)
  })

  it('dedupes case-insensitively and keeps the first casing', () => {
    const result = normalizeUAFilter('allow', [' Claude-CLI/ ', 'claude-cli/', 'opencode/'])
    expect(result).toEqual({ ok: true, mode: 'allow', patterns: ['Claude-CLI/', 'opencode/'] })
  })

  it('stores codex desktop rather than bare codex', () => {
    expect(presetPatterns.codex).toContain('codex desktop')
    expect(presetPatterns.codex).not.toContain('codex')
    expect(presetPatterns.claudeCode).toEqual(['claude-cli/'])
    expect(presetPatterns.openCode).toEqual(['opencode/'])
  })
})
