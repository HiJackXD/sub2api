export type UAFilterMode = 'off' | 'allow' | 'block' | 'unchanged'

export const presetPatterns = {
  claudeCode: ['claude-cli/'],
  codex: [
    'codex_cli_rs/',
    'codex-tui/',
    'codex_vscode/',
    'codex_vscode_copilot/',
    'codex_app/',
    'codex_chatgpt_desktop/',
    'codex_atlas/',
    'codex_exec/',
    'codex_sdk_ts/',
    'codex desktop'
  ],
  openCode: ['opencode/']
} as const

export function normalizeUAFilter(mode: UAFilterMode, patterns: string[]) {
  if (mode === 'off' || mode === 'unchanged') {
    return { ok: true as const, mode, patterns: [] as string[] }
  }
  const seen = new Set<string>()
  const out: string[] = []
  for (const raw of patterns) {
    const text = raw.trim()
    if (!text) continue
    const length = [...text].length
    if (length < 3 || length > 128 || /[\u0000-\u001f\u007f]/.test(text)) {
      return { ok: false as const, field: 'ua_filter_patterns' as const }
    }
    const key = text.toLowerCase()
    if (seen.has(key)) continue
    seen.add(key)
    out.push(text)
  }
  if (out.length === 0 || out.length > 50) {
    return { ok: false as const, field: 'ua_filter_patterns' as const }
  }
  return { ok: true as const, mode, patterns: out }
}
