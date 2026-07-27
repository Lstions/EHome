<template>
  <el-dialog :model-value="visible" :title="definition?.name || '受控操作'" width="440px" :close-on-click-modal="false" @update:model-value="emit('update:visible', $event)">
    <el-alert v-if="unsupported" type="warning" :closable="false" title="此动作的参数类型尚未获得客户端支持，已安全禁用。" />
    <el-form v-else label-position="top">
      <el-form-item v-for="field in fields" :key="field.name" :label="field.name" :required="required.has(field.name)">
        <el-switch v-if="field.parameter.type === 'boolean'" v-model="values[field.name]" />
        <el-select v-else-if="field.parameter.enum?.length" v-model="values[field.name]" style="width:100%">
          <el-option v-for="option in field.parameter.enum" :key="option" :label="option" :value="option" />
        </el-select>
        <el-input-number v-else-if="field.parameter.type === 'integer' || field.parameter.type === 'number'" v-model="values[field.name]" :min="field.parameter.minimum" :max="field.parameter.maximum" :precision="field.parameter.type === 'integer' ? 0 : undefined" style="width:100%" />
        <el-input v-else v-model="values[field.name]" :maxlength="field.parameter.max_length" />
      </el-form-item>
    </el-form>
    <template #footer><el-button @click="emit('update:visible', false)">取消</el-button><el-button type="primary" :disabled="unsupported || !complete" @click="submit">继续</el-button></template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, reactive, watch } from 'vue'
import type { ActionDefinition, ActionParameter } from '@/api/deviceOperation'

const props = defineProps<{ visible: boolean; definition: ActionDefinition | null }>()
const emit = defineEmits<{ 'update:visible': [value: boolean]; submit: [params: Record<string, unknown>] }>()
const values = reactive<Record<string, unknown>>({})
const properties = computed(() => props.definition?.input_schema?.properties ?? {})
const fields = computed(() => Object.entries(properties.value).sort(([a], [b]) => a.localeCompare(b)).map(([name, parameter]) => ({ name, parameter })))
const required = computed(() => new Set(props.definition?.input_schema?.required ?? []))
const unsupported = computed(() => fields.value.some(({ parameter }) => !isSupported(parameter)))
const complete = computed(() => [...required.value].every(name => values[name] !== undefined && values[name] !== ''))

function isSupported(parameter: ActionParameter) {
  return ['string', 'boolean', 'integer', 'number'].includes(parameter.type) && (!parameter.enum || parameter.type === 'string')
}
function reset() {
  for (const key of Object.keys(values)) delete values[key]
  for (const { name, parameter } of fields.value) {
    if (required.value.has(name) && parameter.type === 'boolean') values[name] = false
  }
}
function submit() {
  if (!unsupported.value && complete.value) emit('submit', { ...values })
}
watch(() => [props.visible, props.definition?.id], reset, { immediate: true })
</script>
