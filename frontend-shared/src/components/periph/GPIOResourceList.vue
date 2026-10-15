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
        <div v-if="visibleRows.length" class="resource-head" aria-hidden="true">
          <span>引脚</span><span>运行状态</span><span>操作</span>
        </div>
        <ul class="resource-list">
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
/* UI-3（2026-10-15）：改前第三列前的信息列写 `1fr` ⇒ 该列吃满整行剩余宽度，
   而内容（"GPIO 1 [可用]"）只有 ~120px，于是每行出现跨屏空白
   （实测用户截图："GPIO 1 可用 …………………………………… [配置 GPIO]"）。
   现改为 `auto` 让信息列**按内容收缩**，空白收敛到操作区左侧的弹性间隔：
   信息聚拢在左、操作贴右，中间留白由 `justify-content: space-between` 的语义承担，
   而不是被一列无内容的 1fr 撑开。 */
/* UI-3（2026-10-15）紧凑行（规范 §4.3.3：含开关/状态/多操作的资源管理用纵向行列表）。
   改前 `minmax(0,1fr) …` 让第一列吃满整行，而内容（"GPIO 1 可用"）只有 ~120px，
   于是每行出现跨屏空白（用户截图："GPIO 1 可用 ……………… [配置 GPIO]"）。
   第一次改为三列并把剩余宽度给"运行状态"后，**未配置行**该列无内容，
   仍留 855px 空洞（实测 gapBetweenInfoAndActions=879）—— 问题只是换了位置。
   现在：列全部按内容收缩，操作区用 `margin-left:auto` 贴右，
   行高压到 44px（内容一行放得下时不再占 56px）。空行不再有"被撑开的中间列"。 */
.resource-row { display: flex; align-items: center; column-gap: 12px; row-gap: 8px; flex-wrap: wrap; min-height: 44px; padding: 6px 16px; border-bottom: 1px solid var(--el-border-color-lighter); position: relative; transition: background-color 0.15s; }
.resource-row:hover { background: var(--el-fill-color-lighter); }
/* 操作区贴右：flex 行里用 auto 外边距把空白**收在操作区左侧**，
   而不是让它变成一列无内容的弹性轨道。 */
.resource-row .actions { margin-left: auto; }
/* 列头：桌面端让紧凑行读成一张表（窄屏隐藏）。列宽与行内三段对齐。 */
.resource-head { display: flex; column-gap: 12px; padding: 6px 16px; border-bottom: 1px solid var(--el-border-color-lighter); font-size: 12px; color: var(--el-text-color-secondary); }
.resource-head > span:first-child { min-width: 120px; }
.resource-head > span:nth-child(2) { flex: 0 0 auto; }
.resource-head > span:last-child { margin-left: auto; }
.resource-row[data-state="configured"]::before { content: ''; position: absolute; inset: 0 auto 0 0; width: 3px; background: var(--el-color-primary); }
.info, .runtime, .actions { display: flex; align-items: center; gap: 8px; min-width: 0; flex-wrap: wrap; }
/* 引脚标识列定宽下限：各行左缘对齐（与列头 "引脚" 对齐）。 */
.info { gap: 6px 8px; min-width: 120px; }
.pin-id { font-weight: 600; white-space: nowrap; }
.summary { color: var(--el-text-color-regular); font-size: 13px; white-space: nowrap; }
.sep { color: var(--el-text-color-placeholder); }
/* `align-self: center` 让操作区**按内容宽度**排列，不被 grid 拉伸。
   桌面下 `.resource-row` 的第三列是 `auto` 本来就不会拉伸；但移动端单列
   （`grid-template-columns: 1fr`）会把 `.actions` 撑满整行，进而把里面的按钮
   也拉成整行宽 —— 8 个引脚就是 8 个全宽蓝按钮，视觉过重（实测宽 310px）。
   固定为内容宽度后按钮回到 ~106px。 */
.actions { justify-content: flex-end; align-self: center; white-space: nowrap; }
.label, .stale-configs { color: var(--el-text-color-secondary); font-size: 12px; }
.feedback { grid-column: 1 / -1; color: var(--el-color-danger); }
.stale-configs { display: flex; flex-direction: column; gap: 4px; padding: 8px 16px; border: 1px solid var(--el-color-warning-light-5); }
@media (max-width: 768px) {
  /* 移动端：信息列占满一行，操作区独立成行（改前两列并排会把按钮挤到很窄）。 */
  /* 窄屏：列头无意义（单列堆叠），隐藏；行改为单列。 */
  .resource-head { display: none; }
  .resource-row { grid-template-columns: 1fr; }
  .runtime, .actions, .feedback { grid-column: 1 / -1; }
  /* D6 修订 / 规范 §4.4.5 MUST：移动端可点击区域 ≥44px。
   *
   * ⚠️ 选择器**刻意写得比看起来需要的更长**，原因是一次真实失败：
   * 初版写 `.actions :deep(.el-button)`（编译为 `.actions[data-v-x] .el-button`，特异性 (0,3,0)），
   * 被 theme.css:722 的窄屏规则
   *   `.el-button--small:not(.is-circle):not(.is-link):not(.is-text) { min-height: 36px }`
   * 压过 —— 那条的 `:not()` 链把特异性堆到 **(0,4,0)**，**优先级高于书写顺序**。
   * 结果按钮实测仍是 **36px**；而我当时只断言"源码里有 min-height:44px"，
   * 完全没发现它没生效（源码层断言测不出级联结果）。
   *
   * 现选择器复刻全局规则的 `:not()` 链并**再加一层 `.resource-row` 后代**：
   * 特异性 (0,5,0) > (0,4,0)，实测生效。若将来 theme.css 那条被改动或删除，
   * 本规则依然成立（不依赖它消失）。 */
  .resource-row .actions { justify-content: flex-start; }
  .resource-row .actions :deep(.el-button.el-button--small:not(.is-circle):not(.is-link):not(.is-text)) {
    min-height: 44px;
    padding-left: 16px;
    padding-right: 16px;
  }
}
</style>
