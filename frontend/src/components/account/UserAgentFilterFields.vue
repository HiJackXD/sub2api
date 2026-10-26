<script setup lang="ts">
import { computed, ref, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import { presetPatterns, type UAFilterMode } from './uaFilter'

const props = withDefaults(defineProps<{
  bulk?: boolean
  invalid?: boolean
}>(), {
  bulk: false,
  invalid: false
})

const model = defineModel<{ mode: UAFilterMode; patterns: string[] }>({ required: true })

const { t } = useI18n()
const customPattern = ref('')
const modeInputId = useId()
const customInputId = useId()

const presets = [
  { key: 'claudeCode', label: 'admin.accounts.uaFilter.presetClaudeCode', patterns: presetPatterns.claudeCode },
  { key: 'codex', label: 'admin.accounts.uaFilter.presetCodex', patterns: presetPatterns.codex },
  { key: 'openCode', label: 'admin.accounts.uaFilter.presetOpenCode', patterns: presetPatterns.openCode }
] as const

const mode = computed({
  get: () => model.value.mode,
  set: (value: UAFilterMode) => {
    model.value = { mode: value, patterns: model.value.patterns }
  }
})

const modeOptions = computed(() => {
  const options: Array<{ value: UAFilterMode; label: string }> = []
  if (props.bulk) {
    options.push({ value: 'unchanged', label: t('admin.accounts.uaFilter.unchanged') })
  }
  options.push(
    { value: 'off', label: t('admin.accounts.uaFilter.off') },
    { value: 'allow', label: t('admin.accounts.uaFilter.allow') },
    { value: 'block', label: t('admin.accounts.uaFilter.block') }
  )
  return options
})

const showPatterns = computed(() => mode.value === 'allow' || mode.value === 'block')

const errorMessage = computed(() => {
  if (!props.invalid) return ''
  const meaningful = model.value.patterns.some((item) => item.trim() !== '')
  return t(meaningful ? 'admin.accounts.uaFilter.invalidPattern' : 'admin.accounts.uaFilter.emptyList')
})

function patternKey(value: string): string {
  return value.trim().toLowerCase()
}

function presetChecked(preset: readonly string[]): boolean {
  const keys = new Set(model.value.patterns.map(patternKey).filter((key) => key !== ''))
  return preset.every((item) => keys.has(item.toLowerCase()))
}

function onPresetChange(preset: readonly string[], event: Event) {
  const checked = (event.target as HTMLInputElement).checked
  const current = model.value.patterns
  let next: string[]
  if (checked) {
    next = [...current]
    const keys = new Set(next.map(patternKey))
    for (const item of preset) {
      const key = item.toLowerCase()
      if (!keys.has(key)) {
        next.push(item)
        keys.add(key)
      }
    }
  } else {
    const remove = new Set(preset.map((item) => item.toLowerCase()))
    next = current.filter((item) => !remove.has(patternKey(item)))
  }
  model.value = { mode: model.value.mode, patterns: next }
}

function addCustomPattern() {
  const text = customPattern.value.trim()
  customPattern.value = ''
  if (!text) return
  const key = text.toLowerCase()
  if (model.value.patterns.some((item) => patternKey(item) === key)) return
  model.value = { mode: model.value.mode, patterns: [...model.value.patterns, text] }
}

function removePattern(index: number) {
  model.value = {
    mode: model.value.mode,
    patterns: model.value.patterns.filter((_, itemIndex) => itemIndex !== index)
  }
}
</script>

<template>
  <div data-testid="user-agent-filter" class="border-t border-gray-200 pt-4 dark:border-dark-600">
    <p class="input-label">{{ t('admin.accounts.uaFilter.title') }}</p>
    <p class="mb-2 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.uaFilter.hint') }}</p>
    <label class="input-label" :for="modeInputId">{{ t('admin.accounts.uaFilter.mode') }}</label>
    <select
      :id="modeInputId"
      v-model="mode"
      class="input"
      data-testid="user-agent-filter-mode"
    >
      <option v-for="option in modeOptions" :key="option.value" :value="option.value">
        {{ option.label }}
      </option>
    </select>

    <div v-if="showPatterns" class="mt-3 space-y-3">
      <div class="flex flex-wrap gap-4">
        <label
          v-for="preset in presets"
          :key="preset.key"
          class="inline-flex items-center gap-2 text-sm text-gray-700 dark:text-gray-200"
        >
          <input
            type="checkbox"
            class="rounded border-gray-300 text-primary-600 focus:ring-primary-500"
            :data-testid="`user-agent-filter-preset-${preset.key}`"
            :checked="presetChecked(preset.patterns)"
            @change="onPresetChange(preset.patterns, $event)"
          />
          {{ t(preset.label) }}
        </label>
      </div>

      <div>
        <label class="input-label" :for="customInputId">{{ t('admin.accounts.uaFilter.customPattern') }}</label>
        <div class="flex gap-2">
          <input
            :id="customInputId"
            v-model="customPattern"
            type="text"
            class="input"
            data-testid="user-agent-filter-custom"
            :placeholder="t('admin.accounts.uaFilter.customPatternPlaceholder')"
            @keydown.enter.prevent="addCustomPattern"
          />
          <button
            type="button"
            class="btn btn-secondary shrink-0"
            data-testid="user-agent-filter-add"
            @click="addCustomPattern"
          >
            {{ t('admin.accounts.uaFilter.addPattern') }}
          </button>
        </div>
      </div>

      <div>
        <p class="input-label">{{ t('admin.accounts.uaFilter.patterns') }}</p>
        <ul
          v-if="model.patterns.length > 0"
          class="space-y-1"
          data-testid="user-agent-filter-patterns"
        >
          <li
            v-for="(pattern, index) in model.patterns"
            :key="`${pattern}-${index}`"
            class="flex items-center justify-between gap-2 rounded border border-gray-200 px-2 py-1 text-sm dark:border-dark-600"
          >
            <code class="truncate font-mono text-xs">{{ pattern }}</code>
            <button type="button" class="text-xs text-red-500 hover:text-red-700" @click="removePattern(index)">
              {{ t('admin.accounts.uaFilter.removePattern') }}
            </button>
          </li>
        </ul>
      </div>
    </div>

    <p
      v-if="errorMessage"
      class="mt-2 text-xs text-red-500"
      role="alert"
      data-testid="user-agent-filter-error"
    >
      {{ errorMessage }}
    </p>
  </div>
</template>
