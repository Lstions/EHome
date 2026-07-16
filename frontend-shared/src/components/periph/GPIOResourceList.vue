<template>
  <section class="gpio-resource-panel" aria-label="GPIO 硬件资源">
    <el-alert v-if="offline" type="warning" :closable="false" title="节点离线，显示最后已知配置" />
    <el-skeleton v-if="loading" :rows="4" animated />
    <template v-else>
      <el-empty v-if="resources.length === 0" description="等待节点硬件资源上报" />
      <ul v-else class="resource-list">
        <li
          v-for="row in rows"
          :key="row.resource.id"
          data-testid="gpio-resource-row"
          class="resource-row"
          :data-state="row.state"
          :aria-busy="row.busy"
        >
          <div class="identity">
            <strong>GPIO {{ row.pin }}</strong>
            <span v-if="row.config?.label" class="label">{{ row.config.label }}</span>
            <el-tag size="small" :type="row.state === 'occupied' ? 'warning' : row.config ? 'primary' : 'info'">
              {{ row.state === 'occupied' ? row.occupiedBy : row.config ? 'GPIO' : '可用' }}
            </el-tag>
          </div>
          <div class="configuration">
            <template v-if="row.config">
              <span>{{ directionLabel(row.config.direction) }}</span>
              <span v-if="row.config.direction === 1">初始 {{ row.config.initial_level === 1 ? 'HIGH' : 'LOW' }}</span>
            </template>
            <span v-else-if="row.state === 'occupied'">{{ row.occupiedBy }}</span>
            <span v-else>ESP32 已上报</span>
          </div>
          <div class="runtime">
            <template v-if="row.config?.direction === 1">
              <span>{{ levelLabel(row.level) }}</span>
              <el-switch
                :model-value="row.level === 1"
                :disabled="offline || row.busy"
                :loading="row.busy"
                :aria-label="`GPIO ${row.pin} 输出电平`"
                active-text="HIGH"
                inactive-text="LOW"
                @change="(value: boolean) => setLevel(row, value ? 1 : 0)"
              />
            </template>
            <template v-else-if="row.config">
              <span>{{ levelLabel(row.level) }}</span>
              <el-button size="small" type="primary" :disabled="offline || row.busy" :loading="row.busy" @click="readLevel(row)">读取</el-button>
            </template>
          </div>
          <div class="actions">
            <el-button
              v-if="row.state === 'available'"
              :data-testid="`configure-gpio-${row.pin}`"
              size="small"
              type="primary"
              :disabled="offline"
              @click="emit('configure', row.pin)"
            >配置 GPIO</el-button>
            <template v-else-if="row.config">
              <el-button size="small" text :disabled="offline" @click="emit('edit', row.pin)">编辑</el-button>
              <el-button size="small" text :disabled="offline" @click="emit('remove', row.pin)">移除配置</el-button>
            </template>
          </div>
          <div v-if="row.feedback" class="feedback" role="status">{{ row.feedback }}</div>
        </li>
      </ul>
      <div v-if="staleConfigs.length" class="stale-configs" role="status">
        <strong>无效配置</strong>
        <span v-for="config in staleConfigs" :key="config.pin">GPIO{{ config.pin }} 未在节点报告中</span>
      </div>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, onUnmounted, reactive, watch } from 'vue'
import { ElMessage } from 'element-plus'
import type { GPIOBusResource } from '@/api/node'
import { gpioApi, type GPIOConfig } from '@/api/periph'

interface GPIORow {
  resource: GPIOBusResource
  pin: number
  config?: GPIOConfig
  occupiedBy?: string
  state: 'available' | 'configured' | 'occupied'
  level: number | null
  busy: boolean
  feedback: string
}

const props = withDefaults(defineProps<{
  resources: GPIOBusResource[]
  configs: GPIOConfig[]
  nodeId: string
  offline?: boolean
  loading?: boolean
  occupiedPins?: Map<number, string>
  registerPending?: (payload: { requestId: number; pin: number; action: number }) => boolean
}>(), {
  offline: false,
  loading: false,
  occupiedPins: () => new Map(),
})
const emit = defineEmits<{
  (event: 'configure' | 'edit' | 'remove', pin: number): void
}>()
let operationGeneration = 0
let disposed = false

function resourcePin(resource: GPIOBusResource): number {
  if (typeof resource.pin === 'number') return resource.pin
  const match = resource.id.match(/\d+/)
  return match ? Number(match[0]) : Number.NaN
}

const reportedPins = computed(() => new Set(props.resources.map(resourcePin).filter(Number.isFinite)))
const staleConfigs = computed(() => props.configs.filter(config => !reportedPins.value.has(config.pin)))
const rows = computed<GPIORow[]>(() => props.resources
  .map(resource => {
    const pin = resourcePin(resource)
    const config = props.configs.find(item => item.pin === pin)
    const occupiedBy = props.occupiedPins.get(pin)
    return reactive({
      resource,
      pin,
      config,
      occupiedBy,
      state: config ? 'configured' : occupiedBy ? 'occupied' : 'available',
      level: null,
      busy: false,
      feedback: '',
    }) as GPIORow
  })
  .filter(row => Number.isFinite(row.pin))
  .sort((left, right) => left.pin - right.pin))

function directionLabel(direction: number): string {
  return ['INPUT', 'OUTPUT', 'INPUT_PULLUP', 'INPUT_PULLDOWN'][direction] || 'UNKNOWN'
}
function levelLabel(level: number | null): string {
  return level === 1 ? 'HIGH' : level === 0 ? 'LOW' : '未知'
}
async function setLevel(row: GPIORow, level: 0 | 1) {
  if (row.busy || props.offline) return
  const nodeId = props.nodeId
  const generation = operationGeneration
  const previous = row.level
  row.busy = true
  row.feedback = `正在写入 ${level ? 'HIGH' : 'LOW'}…`
  try {
    const ack = await gpioApi.set(nodeId, row.pin, level)
    if (disposed || generation !== operationGeneration || props.nodeId !== nodeId) return
    if (!props.registerPending?.({ requestId: ack.request_id, pin: row.pin, action: level ? 1 : 0 })) row.feedback = '写入命令已发送，等待设备响应'
  } catch (error: unknown) {
    if (disposed || generation !== operationGeneration || props.nodeId !== nodeId) return
    row.level = previous
    row.feedback = '写入失败 · 重试'
    ElMessage.error(`GPIO 操作失败: ${error instanceof Error ? error.message : '未知错误'}`)
  } finally {
    if (!disposed && generation === operationGeneration && props.nodeId === nodeId) row.busy = false
  }
}
async function readLevel(row: GPIORow) {
  if (row.busy || props.offline) return
  const nodeId = props.nodeId
  const generation = operationGeneration
  row.busy = true
  row.feedback = '正在读取…'
  try {
    const ack = await gpioApi.read(nodeId, row.pin)
    if (disposed || generation !== operationGeneration || props.nodeId !== nodeId) return
    if (!props.registerPending?.({ requestId: ack.request_id, pin: row.pin, action: 2 })) row.feedback = '读取命令已发送，等待设备响应'
  } catch (error: unknown) {
    if (disposed || generation !== operationGeneration || props.nodeId !== nodeId) return
    row.feedback = '读取失败 · 重试'
    ElMessage.error(`GPIO 读取失败: ${error instanceof Error ? error.message : '未知错误'}`)
  } finally {
    if (!disposed && generation === operationGeneration && props.nodeId === nodeId) row.busy = false
  }
}
function applyRuntimeLevel(pin: number, level: number | null) {
  const row = rows.value.find(item => item.pin === pin)
  if (!row) return
  if (level === 0 || level === 1) row.level = level
  row.feedback = level === null ? '设备操作失败 · 重试' : ''
}
watch(() => props.nodeId, () => { operationGeneration++ })
onUnmounted(() => { disposed = true; operationGeneration++ })
defineExpose({ applyRuntimeLevel })
</script>

<style scoped>
.gpio-resource-panel { display: flex; flex-direction: column; gap: 12px; min-width: 0; }
.resource-list { list-style: none; margin: 0; padding: 0; }
.resource-row { display: grid; grid-template-columns: minmax(130px, 1fr) minmax(170px, 1.2fr) minmax(180px, 1.4fr) auto; align-items: center; gap: 12px; min-height: 64px; padding: 12px 16px; border-bottom: 1px solid var(--el-border-color-lighter); position: relative; }
.resource-row[data-state="configured"]::before { content: ''; position: absolute; inset: 0 auto 0 0; width: 3px; background: var(--el-color-primary); }
.identity, .configuration, .runtime, .actions { display: flex; align-items: center; gap: 8px; min-width: 0; flex-wrap: wrap; }
.actions { justify-content: flex-end; }
.label, .stale-configs { color: var(--el-text-color-secondary); font-size: 12px; }
.feedback { grid-column: 1 / -1; color: var(--el-color-danger); }
.stale-configs { display: flex; flex-direction: column; gap: 4px; padding: 8px 16px; border: 1px solid var(--el-color-warning-light-5); }
@media (max-width: 768px) { .resource-row { grid-template-columns: 1fr auto; } .configuration, .runtime, .feedback { grid-column: 1 / -1; } }
</style>
