<template>
  <el-dialog :model-value="visible" :title="definition?.name || '受控操作'" :width="manyFields ? '640px' : '440px'" :close-on-click-modal="false" @update:model-value="emit('update:visible', $event)">
    <el-alert v-if="unsupported" type="warning" :closable="false" title="此动作的参数类型尚未获得客户端支持，已安全禁用。" />
    <el-form v-else label-position="top" :class="{ 'multi-column': manyFields }">
      <el-form-item v-for="field in fields" :key="field.name" :label="field.name" :required="required.has(field.name)">
        <el-switch v-if="field.parameter.type === 'boolean'" v-model="values[field.name]" />
        <el-select v-else-if="field.parameter.enum?.length" v-model="values[field.name]" style="width:100%">
          <el-option v-for="option in field.parameter.enum" :key="option" :label="option" :value="option" />
        </el-select>
        <el-input-number v-else-if="field.parameter.type === 'integer' || field.parameter.type === 'number'" :model-value="numberValue(field.name)" @update:model-value="(v) => setNumberValue(field.name, v)" :min="field.parameter.minimum" :max="field.parameter.maximum" :precision="field.parameter.type === 'integer' ? 0 : undefined" style="width:100%" />
        <el-input v-else :model-value="textValue(field.name)" @update:model-value="(v) => setTextValue(field.name, v)" :maxlength="field.parameter.max_length" />
      </el-form-item>
    </el-form>
    <!-- 「读取」放在**取消左边**（用户指定位置）：先读设备现有值回填表单，再改、再提交。
         读取本身不改设备，但它可能仍需后端确认令牌（read_*_parameters 是 elevated），
         由父组件经 useControlAction 处理，这里只负责发起与展示 loading。 -->
    <template #footer>
      <el-button v-if="canRead" :loading="reading" @click="emit('read')">读取</el-button>
      <el-button @click="emit('update:visible', false)">取消</el-button>
      <el-button type="primary" :disabled="unsupported || !complete" @click="submit">继续</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, reactive, watch } from 'vue'
import type { ActionDefinition, ActionParameter } from '@/api/deviceOperation'

const props = defineProps<{ visible: boolean; definition: ActionDefinition | null; canRead?: boolean; reading?: boolean; prefill?: Record<string, unknown> | null }>()
const emit = defineEmits<{ 'update:visible': [value: boolean]; submit: [params: Record<string, unknown>]; read: [] }>()
const values = reactive<Record<string, string | number | boolean | undefined>>({})
const properties = computed(() => props.definition?.input_schema?.properties ?? {})
// 数字感知自然排序：resistance_1, resistance_2, …, resistance_10（字母序会排成 1,10,11,…,2）
const fields = computed(() => Object.entries(properties.value).sort(([a], [b]) => a.localeCompare(b, undefined, { numeric: true })).map(([name, parameter]) => ({ name, parameter })))
// 多参数动作（如 write_internal_resistance 30 串内阻）用双列宽对话框，避免单列过长
const manyFields = computed(() => fields.value.length > 6)
const required = computed(() => new Set(props.definition?.input_schema?.required ?? []))
const unsupported = computed(() => fields.value.some(({ parameter }) => !isSupported(parameter)))
const complete = computed(() => [...required.value].every(name => values[name] !== undefined && values[name] !== ''))

/** el-input-number / el-input 的 v-model 目标类型比渲染值更窄，此处做带守卫的读写适配（非 any）。 */
function numberValue(name: string): number | undefined {
  const v = values[name]
  return typeof v === 'number' ? v : undefined
}
function setNumberValue(name: string, v: number | null | undefined): void {
  values[name] = v ?? undefined
}
function textValue(name: string): string | number | undefined {
  const v = values[name]
  return typeof v === 'string' || typeof v === 'number' ? v : undefined
}
function setTextValue(name: string, v: string | number | null | undefined): void {
  values[name] = v ?? undefined
}

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

/**
 * 回填「读取」回来的现有值（2026-10-16 用户要求："参数应该先读取现有值"）。
 *
 * 为什么必要：写入类动作的参数表动辄 15/30/33 项（保护参数、系统参数、内阻表），
 * 空白手填既慢又极易填错——而这些值设备本来就有，读一次就能拿到。
 *
 * 只覆盖快照里真实出现的字段：读操作返回的是"它能读到的全部"，
 * 可能与写 schema 不完全一一对应（实测 read_protection_parameters 回 23 项、
 * 写入 schema 是 33 项），未读到的字段保持用户已填/默认值，绝不清空成全 undefined。
 * 布尔与数字保持原类型（el-switch / el-input-number 需要），其余转字符串。
 */
watch(() => props.prefill, (snapshot) => {
  if (!snapshot) return
  for (const { name } of fields.value) {
    if (!Object.prototype.hasOwnProperty.call(snapshot, name)) continue
    const value = snapshot[name]
    if (value === null || value === undefined) continue
    if (typeof value === 'boolean' || typeof value === 'number') values[name] = value
    else values[name] = String(value)
  }
})
</script>

<style scoped>
.multi-column {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  column-gap: 16px;
}
@media (max-width: 768px) {
  .multi-column {
    grid-template-columns: 1fr;
  }
}
</style>
