<template>
  <div class="space-y-3 border-t border-gray-200 pt-4 dark:border-dark-600">
    <div>
      <h3 class="text-sm font-medium text-gray-900 dark:text-gray-100">
        {{ t('admin.groups.form.modelCapability.title') }}
      </h3>
      <p class="input-hint">{{ t('admin.groups.form.modelCapability.hint') }}</p>
    </div>
    <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
      <div v-for="field in fields" :key="field">
        <label class="input-label" :for="`${idPrefix}-${field}`">
          {{ t(`admin.groups.form.modelCapability.${field}`) }}
        </label>
        <input
          :id="`${idPrefix}-${field}`"
          :data-testid="`${idPrefix}-${field}`"
          :value="modelValue[field]"
          type="text"
          inputmode="numeric"
          class="input"
          :placeholder="t('admin.groups.form.modelCapability.placeholder')"
          @input="updateField(field, ($event.target as HTMLInputElement).value)"
        />
      </div>
    </div>
    <p v-if="preview" class="input-hint" data-testid="model-capability-preview">
      {{ t('admin.groups.form.modelCapability.preview', preview) }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  modelCapabilityPreview,
  type ModelCapabilityForm,
} from '@/views/admin/modelCapabilityOverrides'

const props = defineProps<{
  idPrefix: string
  modelValue: ModelCapabilityForm
}>()

const emit = defineEmits<{
  'update:modelValue': [value: ModelCapabilityForm]
}>()

const { t } = useI18n()

const fields = [
  'context_window',
  'max_context_window',
  'effective_context_window_percent',
  'max_output_tokens',
] as const

const preview = computed(() => modelCapabilityPreview(props.modelValue))

const updateField = (field: (typeof fields)[number], value: string) => {
  emit('update:modelValue', { ...props.modelValue, [field]: value })
}
</script>
