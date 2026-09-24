<template>
  <section class="gpio-resource-panel" aria-label="GPIO 硬件资源">
    <!-- D6：离线提示同时说明**写操作会被拒绝**。改前只说"显示最后已知配置"，
         用户仍会去点「配置 GPIO」（按钮此时是 disabled，但没有任何文字解释为什么）。 -->
    <el-alert
      v-if="offline"
      type="warning"
      :closable="false"
      title="节点离线：显示最后已知配置，且无法写入（配置/写入/读取均已禁用）"
    />
    <el-skeleton v-if="loading" :rows="4" animated />
    <template v-else>
      <!-- 「无效配置」必须**在 resources 为空时也显示**：设备一个 GPIO 都没上报，
           恰恰是配置全部变成孤儿的时候（此为改前行为，我一度因嵌套重构把它藏了，
           被 `HardwareResourceLists.spec` 的 "does not invent rows" 用例当场拦下）。 -->
      <div v-if="staleConfigs.length" class="stale-configs" role="status">
        <strong>无效配置</strong>
        <span v-for="config in staleConfigs" :key="config.pin">GPIO{{ config.pin }} 未在节点报告中</span>
      </div>
      <el-empty v-if="resources.length === 0" description="等待节点硬件资源上报" />
      <template v-else>
        <!-- D6：过滤条。实测典型节点上报 8 个 GPIO，而通常只有 0-2 个真被配置：
             改前必须逐行看「可用」标签才能找出已配置项。 -->
        <div v-if="resources.length > 1" class="row-filter" role="group" aria-label="GPIO 筛选">
          <el-radio-group v-model="filter" size="small" data-testid="gpio-filter">
            <el-radio-button value="all">全部 {{ rows.length }}</el-radio-button>
            <el-radio-button value="configured">已配置 {{ counts.configured }}</el-radio-button>
            <el-radio-button value="unconfigured">未配置 {{ counts.unconfigured }}</el-radio-button>
          </el-radio-group>
        </div>
        <p v-if="visibleRows.length === 0" class="filter-empty" role="status">
          {{ filter === 'configured' ? '该节点当前没有已配置的 GPIO 引脚。' : '所有引脚都已配置。' }}
        </p>
        <ul v-else class="resource-list">
          <li
            v-for="row in visibleRows"
            :key="row.resource.id"
            data-testid="gpio-resource-row"
            class="resource-row"
            :data-state="row.state"
            :aria-busy="row.busy"
          >
          <!-- D6：identity 与 configuration **合并为一列**。
               改前是两列，未配置行的第二列显示「ESP32 已上报」（每行逐字重复，
               实测 8 行相同）⇒ 一列在说同一句话、另一列空着，中间留下大片空白。
               合并后：引脚标识与它的配置摘要相邻，未配置时该列自然收缩（不再占位）。
               注意**不能**用"设备上报的方向/上下拉"填充——实测后端
               `reportedGPIOResource` 只有 `struct{ Pin int }`（handler_periph.go:28-30），
               前端类型里的 direction/pull/enabled 是从未到达的预留字段，
               照那个思路写会显示一片「未知」。 -->
          <div class="info">
            <span class="pin-id">GPIO {{ row.pin }}</span>
            <span v-if="row.config?.label" class="label">{{ row.config.label }}</span>
            <el-tag size="small" :type="row.state === 'occupied' ? 'warning' : row.config ? 'primary' : 'info'">
              {{ row.state === 'occupied' ? row.occupiedBy : row.config ? 'GPIO' : '可用' }}
            </el-tag>
            <template v-if="row.config">
              <span class="sep" aria-hidden="true">·</span>
              <span class="summary">{{ directionLabel(row.config.direction) }}</span>
              <span v-if="row.config.direction === 1" class="summary">
                初始 {{ row.config.initial_level === 1 ? 'HIGH' : 'LOW' }}
              </span>
            </template>
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
                @change="(value: string | number | boolean) => setLevel(row, value === true ? 1 : 0)"
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
      </template>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import type { GPIOBusResource } from '@/api/node'
import { gpioApi, type GPIOConfig } from '@/api/periph'
import { useGuardedOperation } from '@/composables/useGuardedOperation'

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

const { run: guardedRun } = useGuardedOperation({
  nodeId: () => props.nodeId,
  offline: () => props.offline,
  errorPrefix: 'GPIO 操作失败',
})

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

/**
 * D6 过滤：典型节点上报 8 个 GPIO，通常只有 0-2 个真被配置。
 * 改前要逐行读「可用」标签才能找出已配置项 ⇒ 加三态过滤。
 * 口径：`occupied`（被总线/其它外设占用）**既不算已配置也不算未配置**——
 * 它同样是"还没配"，归入未配置，避免用户切到"已配置"时看不到它而以为丢了。
 */
const filter = ref<'all' | 'configured' | 'unconfigured'>('all')
const counts = computed(() => ({
  configured: rows.value.filter(row => row.state === 'configured').length,
  unconfigured: rows.value.filter(row => row.state !== 'configured').length,
}))
const visibleRows = computed(() => {
  if (filter.value === 'all') return rows.value
  if (filter.value === 'configured') return rows.value.filter(row => row.state === 'configured')
  return rows.value.filter(row => row.state !== 'configured')
})

function directionLabel(direction: number): string {
  return ['INPUT', 'OUTPUT', 'INPUT_PULLUP', 'INPUT_PULLDOWN'][direction] || 'UNKNOWN'
}
function levelLabel(level: number | null): string {
  return level === 1 ? 'HIGH' : level === 0 ? 'LOW' : '未知'
}
async function setLevel(row: GPIORow, level: 0 | 1) {
  const previous = row.level
  await guardedRun(row, `正在写入 ${level ? 'HIGH' : 'LOW'}…`, async () => {
    const ack = await gpioApi.set(props.nodeId, row.pin, level)
    if (!props.registerPending?.({ requestId: ack.request_id, pin: row.pin, action: level ? 1 : 0 })) row.feedback = '写入命令已发送，等待设备响应'
  }, { rollback: () => { row.level = previous }, errorFeedback: '写入失败 · 重试', errorLabel: 'GPIO 写入失败' })
}
async function readLevel(row: GPIORow) {
  await guardedRun(row, '正在读取…', async () => {
    const ack = await gpioApi.read(props.nodeId, row.pin)
    if (!props.registerPending?.({ requestId: ack.request_id, pin: row.pin, action: 2 })) row.feedback = '读取命令已发送，等待设备响应'
  }, { errorFeedback: '读取失败 · 重试', errorLabel: 'GPIO 读取失败' })
}
function applyRuntimeLevel(pin: number, level: number | null) {
  const row = rows.value.find(item => item.pin === pin)
  if (!row) return
  if (level === 0 || level === 1) row.level = level
  row.feedback = level === null ? '设备操作失败 · 重试' : ''
}
defineExpose({ applyRuntimeLevel })
</script>

<style scoped>
.gpio-resource-panel { display: flex; flex-direction: column; gap: 12px; min-width: 0; }
/* D6：过滤条与"筛选后为空"的说明。 */
.row-filter { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.filter-empty { margin: 0; padding: 12px 16px; color: var(--el-text-color-secondary); font-size: 13px; border: 1px dashed var(--el-border-color); border-radius: 8px; }
.resource-list { list-style: none; margin: 0; padding: 0; }
/* D6：列宽收紧 + identity/configuration 合并为一列。
   改前四列 `minmax(130px,1fr) minmax(170px,1.2fr) minmax(180px,1.4fr) auto`，
   其中第二列专为「ESP32 已上报」那句**每行相同**的占位文案预留 170px+。
   删除该文案并合并列后，用 `1fr` 让信息列吃满剩余空间，
   避免"一个按钮 + 一大片空白"（实测改前未配置行的空列宽达 497px）。 */
.resource-row { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, auto) auto; align-items: center; gap: 12px; min-height: 56px; padding: 10px 16px; border-bottom: 1px solid var(--el-border-color-lighter); position: relative; }
.resource-row[data-state="configured"]::before { content: ''; position: absolute; inset: 0 auto 0 0; width: 3px; background: var(--el-color-primary); }
.info, .runtime, .actions { display: flex; align-items: center; gap: 8px; min-width: 0; flex-wrap: wrap; }
.info { gap: 6px 8px; }
.pin-id { font-weight: 600; white-space: nowrap; }
.summary { color: var(--el-text-color-regular); font-size: 13px; white-space: nowrap; }
.sep { color: var(--el-text-color-placeholder); }
.actions { justify-content: flex-end; white-space: nowrap; }
.label, .stale-configs { color: var(--el-text-color-secondary); font-size: 12px; }
.feedback { grid-column: 1 / -1; color: var(--el-color-danger); }
.stale-configs { display: flex; flex-direction: column; gap: 4px; padding: 8px 16px; border: 1px solid var(--el-color-warning-light-5); }
@media (max-width: 768px) {
  /* 移动端：信息列占满一行，操作区独立成行（改前两列并排会把按钮挤到很窄）。 */
  .resource-row { grid-template-columns: 1fr; }
  .runtime, .actions, .feedback { grid-column: 1 / -1; }
  /* D6 / 规范 §4.4.5 MUST：移动端可点击区域 ≥44px。
     实测本行按钮在桌面是 24px 高（padding 5px 11px, font 12px），
     移动端沿用同尺寸 ⇒ 不达标。这里只抬移动端的实际盒子，不动桌面密度。 */
  .actions { justify-content: flex-start; }
  .actions :deep(.el-button) { min-height: 44px; padding-left: 16px; padding-right: 16px; }
}
</style>
