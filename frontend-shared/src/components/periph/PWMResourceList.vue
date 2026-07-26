<template>
  <section class="pwm-resource-panel" aria-label="PWM 硬件资源">
    <el-alert v-if="offline" type="warning" :closable="false" title="节点离线，显示最后已知配置" />
    <el-skeleton v-if="loading" :rows="4" animated />
    <template v-else>
      <el-empty v-if="resources.length === 0" description="等待节点硬件资源上报" />
      <ul v-else class="resource-list">
        <li
          v-for="row in rows"
          :key="row.resource.id"
          data-testid="pwm-resource-row"
          class="resource-row"
          :data-state="row.config ? 'configured' : 'available'"
          :aria-busy="row.busy"
        >
          <div class="identity">
            <strong>{{ row.resource.id }}<template v-if="row.config"> → GPIO{{ row.config.pin }}</template></strong>
            <span v-if="row.config?.label" class="label">{{ row.config.label }}</span>
            <el-tag size="small" :type="row.config ? 'success' : 'info'">{{ row.config ? 'PWM' : '可用' }}</el-tag>
          </div>
          <div class="configuration">
            <template v-if="row.config">
              <span>{{ row.config.frequency }} Hz</span>
              <span>{{ row.config.resolution }} bit</span>
              <span v-if="row.config.auto_start">自动启动</span>
            </template>
            <template v-else>
              <span>通道 {{ row.resource.channel }}</span>
              <span>最高 {{ row.resource.max_resolution_bits }} bit</span>
              <span>{{ row.resource.timer_count }} timers</span>
            </template>
          </div>
          <div class="runtime">
            <template v-if="row.config">
              <el-tag size="small" :type="row.running === true ? 'success' : 'info'">{{ row.running === true ? '运行中' : row.running === false ? '已停止' : '状态未知' }}</el-tag>
              <span class="duty">{{ (row.duty / 100).toFixed(2) }}%</span>
              <el-slider
                :model-value="row.duty"
                :min="0"
                :max="10000"
                :step="10"
                :show-tooltip="false"
                :disabled="offline || row.running !== true || row.busy"
                :aria-label="`${row.resource.id} GPIO ${row.config.pin} PWM 占空比`"
                @input="(value: number) => row.duty = value"
                @change="(value: number) => scheduleDuty(row, value)"
              />
            </template>
          </div>
          <div class="actions">
            <el-button
              v-if="!row.config"
              :data-testid="`configure-pwm-${row.resource.id}`"
              size="small"
              type="primary"
              :disabled="offline || availablePins.length === 0"
              @click="emit('configure', row.resource.id)"
            >配置 PWM</el-button>
            <template v-else>
              <el-button
                v-if="row.running === false"
                :data-testid="`start-pwm-${row.resource.id}`"
                size="small"
                type="primary"
                :disabled="offline || row.busy"
                :loading="row.busy"
                @click="start(row)"
              >启动</el-button>
			  <el-button v-else-if="row.running === true" size="small" :disabled="offline || row.busy" :loading="row.busy" @click="stop(row)">停止</el-button>
			  <el-button v-else size="small" disabled>等待状态</el-button>
              <el-button size="small" text :disabled="offline" @click="emit('edit', row.resource.id)">编辑</el-button>
              <el-button size="small" text :disabled="offline" @click="emit('remove', row.resource.id)">移除配置</el-button>
            </template>
          </div>
          <div v-if="row.feedback" class="feedback" role="status">{{ row.feedback }}</div>
        </li>
      </ul>
      <div v-if="staleConfigs.length" class="stale-configs" role="status">
        <strong>无效配置</strong>
        <span v-for="config in staleConfigs" :key="config.hardware_id">{{ config.hardware_id }} → GPIO{{ config.pin }} 未在节点报告中</span>
      </div>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, onUnmounted, reactive, watch } from 'vue'
import { ElMessage } from 'element-plus'
import type { PWMBusResource } from '@/api/node'
import { pwmApi, type PWMConfig } from '@/api/periph'
import { useGuardedOperation } from '@/composables/useGuardedOperation'

interface PWMRow {
  resource: PWMBusResource
  config?: PWMConfig
  running: boolean | null
  duty: number
  confirmedDuty: number
  busy: boolean
  feedback: string
}

const props = withDefaults(defineProps<{
  resources: PWMBusResource[]
  configs: PWMConfig[]
  nodeId: string
  availablePins: number[]
  offline?: boolean
  loading?: boolean
  registerPending?: (payload: { requestId: number; hardwareId: string; action: number }) => boolean
}>(), { offline: false, loading: false })
const emit = defineEmits<{
  (event: 'configure' | 'edit' | 'remove', hardwareId: string): void
}>()

const reportedIds = computed(() => new Set(props.resources.map(resource => resource.id)))
const staleConfigs = computed(() => props.configs.filter(config => !reportedIds.value.has(config.hardware_id)))
const rows = computed<PWMRow[]>(() => props.resources
  .map(resource => {
    const config = props.configs.find(item => item.hardware_id === resource.id)
    return reactive({
      resource,
      config,
      running: null,
      duty: config?.duty ?? 0,
      confirmedDuty: config?.duty ?? 0,
      busy: false,
      feedback: '',
    }) as PWMRow
  })
  .sort((left, right) => left.resource.channel - right.resource.channel || left.resource.id.localeCompare(right.resource.id)))

function applyRuntimeState(hardwareId: string, running: boolean | null, duty?: number) {
  const row = rows.value.find(item => item.resource.id === hardwareId)
  if (!row) return
  if (running !== null) row.running = running
  if (typeof duty === 'number') {
    row.duty = duty
    row.confirmedDuty = duty
  }
  row.feedback = running === null ? '设备操作失败 · 重试' : ''
}

let stateGeneration = 0

const { run: guardedRun, invalidate } = useGuardedOperation({
  nodeId: () => props.nodeId,
  offline: () => props.offline,
  errorPrefix: 'PWM 操作失败',
})

async function refreshStates() {
  const generation = ++stateGeneration
  if (props.offline) return
  await Promise.all(rows.value.filter(row => row.config?.enabled).map(async row => {
    try {
      const state = await pwmApi.getState(props.nodeId, row.resource.id)
      if (generation !== stateGeneration) return
	  props.registerPending?.({ requestId: state.request_id, hardwareId: row.resource.id, action: 4 })
	  /* Register ownership only. The synchronous early-ACK replay may have
	   * already applied authoritative duty/running; never overwrite it here. */
    } catch {
      if (generation === stateGeneration) row.running = null
    }
  }))
}
watch(() => [props.nodeId, props.configs, props.offline] as const, () => { void refreshStates() }, { immediate: true, deep: true })

async function start(row: PWMRow) {
  await guardedRun(row, '正在启动…', async () => {
    const ack = await pwmApi.start(props.nodeId, row.resource.id)
    if (!props.registerPending?.({ requestId: ack.request_id, hardwareId: row.resource.id, action: 2 })) row.feedback = '启动命令已发送，等待设备响应'
  })
}
async function stop(row: PWMRow) {
  await guardedRun(row, '正在停止…', async () => {
    const ack = await pwmApi.stop(props.nodeId, row.resource.id)
    if (!props.registerPending?.({ requestId: ack.request_id, hardwareId: row.resource.id, action: 3 })) row.feedback = '停止命令已发送，等待设备响应'
  })
}

const dutyTimers = new Map<string, ReturnType<typeof setTimeout>>()
function cancelDutyTimers() {
  dutyTimers.forEach(timer => clearTimeout(timer))
  dutyTimers.clear()
}
function scheduleDuty(row: PWMRow, duty: number) {
  const previousTimer = dutyTimers.get(row.resource.id)
  if (previousTimer) clearTimeout(previousTimer)
  row.busy = true
  row.feedback = '待应用'
  dutyTimers.set(row.resource.id, setTimeout(async () => {
    await guardedRun(row, '待应用', async () => {
      const ack = await pwmApi.setDuty(props.nodeId, row.resource.id, duty)
      if (!props.registerPending?.({ requestId: ack.request_id, hardwareId: row.resource.id, action: 0 })) row.feedback = '占空比命令已发送，等待设备响应'
    }, { skipEntryGuard: true, rollback: () => { row.duty = row.confirmedDuty } })
    dutyTimers.delete(row.resource.id)
  }, 300))
}
onUnmounted(() => {
  invalidate()
  stateGeneration++
  cancelDutyTimers()
})
watch(() => props.nodeId, () => { invalidate(); cancelDutyTimers() })
defineExpose({ applyRuntimeState })
</script>

<style scoped>
.pwm-resource-panel { display: flex; flex-direction: column; gap: 12px; min-width: 0; }
.resource-list { list-style: none; margin: 0; padding: 0; }
.resource-row { display: grid; grid-template-columns: minmax(170px, 1fr) minmax(190px, 1.2fr) minmax(260px, 1.6fr) auto; align-items: center; gap: 12px; min-height: 64px; padding: 12px 16px; border-bottom: 1px solid var(--el-border-color-lighter); position: relative; }
.resource-row[data-state="configured"]::before { content: ''; position: absolute; inset: 0 auto 0 0; width: 3px; background: var(--el-color-success); }
.identity, .configuration, .runtime, .actions { display: flex; align-items: center; gap: 8px; min-width: 0; flex-wrap: wrap; }
.runtime :deep(.el-slider) { min-width: 100px; flex: 1; }
.actions { justify-content: flex-end; }
.label, .stale-configs { color: var(--el-text-color-secondary); font-size: 12px; }
.duty { font-family: monospace; font-weight: 600; }
.feedback { grid-column: 1 / -1; color: var(--el-color-danger); }
.stale-configs { display: flex; flex-direction: column; gap: 4px; padding: 8px 16px; border: 1px solid var(--el-color-warning-light-5); }
@media (max-width: 768px) { .resource-row { grid-template-columns: 1fr auto; } .configuration, .runtime, .feedback { grid-column: 1 / -1; } }
</style>
