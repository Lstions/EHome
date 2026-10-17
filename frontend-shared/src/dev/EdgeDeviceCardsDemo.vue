<template>
  <!--
    EdgeDeviceCardsDemo.vue — 边缘设备「卡片列表」重设计评审页（2026-10-16）

    · 数据来自真实后端：GET /api/v1/edge-devices（与生产列表页同一接口，带分页/筛选）。
    · 放在 MainLayout 之下（/dev/edge-device-cards），评审时与生产页同一外壳（侧栏/顶栏）。
    · 只做卡片外观重设计，不改生产页 EdgeDeviceList.vue，不引入新依赖。
  -->
  <div class="edc-page">
    <div class="edc-banner">DEV · 边缘设备卡片重设计评审（数据来自真实后端 GET /edge-devices）</div>

    <div class="edc-head">
      <div class="edc-head-main">
        <h2 class="edc-title">边缘设备</h2>
        <p class="edc-sub">共 <b>{{ total }}</b> 台 · 卡片式重设计（最新读数改为中文语义 + 关键指标块）</p>
      </div>
      <div class="edc-toolbar">
        <el-input
          v-model="keyword"
          class="edc-search"
          size="small"
          clearable
          placeholder="搜索名称 / 类型"
          :prefix-icon="Search"
        />
        <el-select v-model="typeFilter" class="edc-select" size="small" clearable placeholder="设备类型">
          <el-option v-for="o in deviceTypeOptions" :key="o.value" :label="o.label" :value="o.value" />
        </el-select>
        <el-select v-model="statusFilter" class="edc-select" size="small" clearable placeholder="状态">
          <el-option v-for="o in statusOptions" :key="o.value" :label="o.label" :value="o.value" />
        </el-select>
        <el-button size="small" :icon="Refresh" :loading="loading" @click="fetchDevices">刷新</el-button>
      </div>
    </div>

    <el-alert v-if="loadError" type="error" :closable="false" show-icon class="edc-error">
      <template #title>边缘设备列表加载失败</template>
      <div class="edc-error-detail">{{ loadError }}</div>
    </el-alert>

    <!-- 加载骨架：固定网格，不因数据到达改变高度 -->
    <div v-if="loading && !cards.length" class="edc-grid">
      <div v-for="n in 6" :key="n" class="edc-card"><el-skeleton :rows="4" animated /></div>
    </div>

    <el-empty v-else-if="!cards.length && !loadError" description="暂无边缘设备" />

    <div v-else class="edc-grid">
      <article
        v-for="c in cards"
        :key="c.device.id"
        class="edc-card"
        :class="{ 'is-offline': c.offline }"
      >
        <div class="edc-top">
          <span class="device-type-icon edc-icon" :class="c.typeTone">
            <el-icon :size="20"><component :is="c.icon" /></el-icon>
          </span>
          <div class="edc-ident">
            <div class="edc-name-row">
              <h3 class="edc-name" :title="c.device.name">{{ c.device.name }}</h3>
              <span class="edc-status" :class="c.statusTone"><i class="dot"></i>{{ c.statusText }}</span>
            </div>
            <p class="edc-type">{{ c.typeLabel }}<span v-if="c.device.protocol"> · {{ c.device.protocol.toUpperCase() }}</span></p>
          </div>
        </div>

        <div class="edc-meta">
          <button
            type="button"
            class="edc-chip is-copy"
            :title="'点击复制节点：' + c.node"
            :aria-label="'复制所属节点 ' + c.node"
            @click="copyText(c.node, '节点')"
          >
            <el-icon :size="14"><Connection /></el-icon>
            <span class="chip-label">节点</span>
            <b class="chip-value mono">{{ c.node }}</b>
          </button>
          <span class="edc-chip">
            <el-icon :size="14"><DataLine /></el-icon>
            <span class="chip-label">通道</span>
            <b class="chip-value">{{ c.bus }}</b>
          </span>
          <span v-if="c.address" class="edc-chip">
            <el-icon :size="14"><Location /></el-icon>
            <span class="chip-label">从机地址</span>
            <b class="chip-value mono">{{ c.address }}</b>
          </span>
        </div>

        <div v-if="c.readings.length" class="edc-readings">
          <div v-for="r in c.readings" :key="r.key" class="edc-reading">
            <span class="r-label" :title="r.label">{{ r.label }}</span>
            <span class="r-value">{{ r.value }}<span v-if="r.unit" class="r-unit">{{ r.unit }}</span></span>
          </div>
        </div>
        <div v-else class="edc-readings is-empty">
          <el-icon :size="15"><Warning /></el-icon>
          <span>等待首条采集数据</span>
        </div>
        <p v-if="c.hasData" class="edc-time">更新于 {{ c.updated }}</p>

        <div class="edc-actions">
          <el-button size="small" type="primary" :icon="View" @click="openDetail(c.device)">详情</el-button>
          <el-button size="small" :icon="Edit" @click="demoOnly('编辑', c.device)">编辑</el-button>
          <el-button
            size="small"
            type="danger"
            text
            :icon="Delete"
            :aria-label="'删除 ' + c.device.name"
            @click="demoOnly('删除', c.device)"
          >删除</el-button>
        </div>
      </article>
    </div>

    <div v-if="total > pageSize" class="edc-pager">
      <el-pagination
        :current-page="page"
        :page-size="pageSize"
        :total="total"
        :page-sizes="[12, 24, 48]"
        layout="total, sizes, prev, pager, next"
        @current-change="onPageChange"
        @size-change="onSizeChange"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import type { Component } from 'vue'
import {
  View, Edit, Delete, Connection, DataLine, Refresh, Search, Warning, Location,
} from '@element-plus/icons-vue'
import { edgeDeviceApi } from '@/api/edgeDevice'
import type { EdgeDevice, EdgeDeviceListParams } from '@/api/edgeDevice'
import { deviceTypeOptions, getDeviceTypeLabel, getDeviceTypeIcon, getDeviceTypeTone } from '@/utils/deviceType'
import { getDriverTree, findDriverLeaf } from '@/api/driver'
import type { DriverTreeNode } from '@/api/driver'
import { sensorNameMap, sensorUnitMap } from '@/utils/sensor'
import { feedback, extractErrorMessage } from '@/utils/feedback'

const router = useRouter()

// ── 状态文案 / 语义色：文字+颜色双通道；离线/禁用是正常工况，用中性色不用危险色 ──
const STATUS_LABELS: Record<string, string> = {
  active: '在线',
  online: '在线',
  pending: '等待数据',
  initializing: '初始化中',
  warning: '警告',
  error: '故障',
  disabled: '已禁用',
  offline: '离线',
}
const statusOptions = [
  { value: 'active', label: '在线' },
  { value: 'pending', label: '等待数据' },
  { value: 'warning', label: '警告' },
  { value: 'error', label: '故障' },
  { value: 'offline', label: '离线' },
  { value: 'disabled', label: '已禁用' },
]
function statusLabel(s: string): string {
  return STATUS_LABELS[s] || s || '未知'
}
function statusTone(s: string): string {
  switch (s) {
    case 'active':
    case 'online': return 'is-success'
    case 'pending':
    case 'initializing': return 'is-info'
    case 'warning': return 'is-warning'
    case 'error': return 'is-danger'
    default: return 'is-muted'
  }
}
// 色调改为走全局能力（src/utils/deviceType.ts），本页不再自维护一套映射 ——
// 之前这里与全局各有一份，是「图标全站统一」要消灭的重复源。
const typeTone = (t: string): string => 'tone-' + getDeviceTypeTone(t)

// ── 位置信息（通道名取 channel_hardware_id，不用设备地址冒充 —— 见 EdgeDevice 注释） ──
function nodeLabel(d: EdgeDevice): string {
  return d.node && d.node.name ? d.node.name : '#' + d.node_id
}
function busLabel(d: EdgeDevice): string {
  if (d.channel_hardware_id) return d.channel_hardware_id
  if (d.channel && d.channel.hardware_id) return d.channel.hardware_id
  if (d.channel && d.channel.bus_type) return d.channel.bus_type.toUpperCase()
  return '—'
}
/** 驱动能力快照（/device-configs/tree）：判定型号是否真的消费物理地址 */
const driverTree = ref<DriverTreeNode[]>([])
function busTypeOf(d: EdgeDevice): string {
  return ((d.channel && d.channel.bus_type) || d.hardware_type || '').toLowerCase()
}
/**
 * 设备地址只在「真的有地址参数」时展示 —— UART 是点对点总线，**没有地址**，
 * 不得拿 hardware_id 冒充（它在 UART 上只是遗留默认地址 1，你截图那版就是这个问题）。
 * 真源：/device-configs/tree 的 requires_target_address（Modbus 类为 true）；
 * I2C 器件的 hardware_id 是器件地址（0x44 / 0x76），仍然展示。
 */
function addressValue(d: EdgeDevice): string {
  const hw = d.hardware_id ? String(d.hardware_id) : ''
  if (!hw) return ''
  const cap = findDriverLeaf(driverTree.value, d.device_type)?.requires_target_address
  if (cap === true) return hw
  if (busTypeOf(d) === 'i2c') return hw
  return ''
}

// ── 类型图标：直接走全局单源（src/utils/deviceType.ts）──
// 本页原有的本地 DEVICE_ICONS 已上移到全局，避免"同一类型两套图标"漂移。
// 图标按含义挑，不再是风速=闪电、逆变器=图表、温湿度=仪表盘那类错配。
const deviceIcon = (type: string): Component => getDeviceTypeIcon(type)

// ── 相对时间 ──
function relTime(iso: string | null): string {
  if (!iso) return '—'
  const t = new Date(iso).getTime()
  if (!Number.isFinite(t)) return '—'
  const s = Math.max(0, (Date.now() - t) / 1000)
  if (s < 60) return '刚刚'
  const m = s / 60
  if (m < 60) return Math.floor(m) + ' 分钟前'
  const h = m / 60
  if (h < 24) return Math.floor(h) + ' 小时前'
  return Math.floor(h / 24) + ' 天前'
}

// ── 最新读数：把后端字段名翻成中文（改前直接打 cell_count: 16.00 这类内部键名） ──
const EXTRA_LABELS: Record<string, string> = {
  cell_count: '电芯数量',
  cycle_count: '循环次数',
  nominal_capacity: '标称容量',
  remaining_capacity: '剩余容量',
  software_version: '固件版本',
  balancing: '均衡状态',
}
const EXTRA_UNITS: Record<string, string> = {
  cell_count: '串',
  cycle_count: '次',
  nominal_capacity: 'Ah',
  remaining_capacity: 'Ah',
}
function readingLabel(key: string): string {
  if (sensorNameMap[key]) return sensorNameMap[key]
  const cell = /^cell_voltage_(\d+)$/.exec(key) || /^cell_v_(\d+)$/.exec(key)
  if (cell) return '单体' + cell[1] + '电压'
  const temp = /^temp_(\d+)$/.exec(key) || /^temperature_(\d+)$/.exec(key)
  if (temp) return '探头' + temp[1] + '温度'
  return EXTRA_LABELS[key] || key
}
function readingUnit(key: string): string {
  if (sensorUnitMap[key]) return sensorUnitMap[key]
  if (/^cell_voltage_/.test(key) || /^cell_v_/.test(key)) return 'V'
  if (/^temp_/.test(key) || /^temperature_/.test(key)) return '°C'
  return EXTRA_UNITS[key] || ''
}
function formatReadingValue(key: string, v: unknown): string {
  const n = Number(v)
  if (!Number.isFinite(n)) return String(v)
  if (/^cell_voltage_/.test(key) || /^cell_v_/.test(key)) return n.toFixed(3)
  if (key === 'total_voltage') return n.toFixed(2)
  if (key === 'rsoc' || key === 'soc' || key === 'battery_capacity' || key === 'output_load_percent') return n.toFixed(1)
  if (key === 'current' || key === 'battery_charge_current' || key === 'battery_discharge_current') return n.toFixed(2)
  if (Math.abs(n) >= 100) return n.toFixed(0)
  return n.toFixed(1)
}
const HIDDEN_KEYS = new Set(['timestamp', 'device_id', 'device_type', 'channel_id', 'raw_data'])
const BMS_PRIORITY = ['rsoc', 'soc', 'total_voltage', 'current', 'cell_voltage_max', 'cell_voltage_min', 'remaining_capacity']
const INVERTER_PRIORITY = ['output_active_power', 'output_load_percent', 'grid_voltage', 'battery_voltage', 'battery_capacity', 'daily_energy', 'max_temp']
const GENERIC_PRIORITY = ['temperature', 'humidity', 'pressure', 'wind_speed', 'light', 'pm25', 'co2', 'rainfall']
function priorityFor(t: string): string[] {
  if (t.indexOf('bms') >= 0 || t === 'battery') return BMS_PRIORITY
  if (t === 'inverter') return INVERTER_PRIORITY
  return GENERIC_PRIORITY
}
interface Reading { key: string; label: string; value: string; unit: string }
function pickReadings(d: EdgeDevice, limit = 3): Reading[] {
  const data = d.last_data
  if (!data) return []
  const keys = Object.keys(data).filter((k) => !HIDDEN_KEYS.has(k))
  const priority = priorityFor(d.device_type)
  const ordered = priority.filter((k) => keys.indexOf(k) >= 0).concat(keys.filter((k) => priority.indexOf(k) < 0))
  return ordered.slice(0, limit).map((k) => ({ key: k, label: readingLabel(k), value: formatReadingValue(k, data[k]), unit: readingUnit(k) }))
}

// ── 列表数据（真实后端） ──
const loading = ref(false)
const devices = ref<EdgeDevice[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(12)
const keyword = ref('')
const typeFilter = ref('')
const statusFilter = ref('')
const loadError = ref('')

async function fetchDevices() {
  loading.value = true
  loadError.value = ''
  try {
    const params: EdgeDeviceListParams = { page: page.value, page_size: pageSize.value }
    const kw = keyword.value.trim()
    if (kw) params.search = kw
    if (typeFilter.value) params.device_type = typeFilter.value
    if (statusFilter.value) params.status = statusFilter.value
    const res = await edgeDeviceApi.getList(params)
    devices.value = res.items
    total.value = res.total
  } catch (e) {
    loadError.value = extractErrorMessage(e, '加载边缘设备失败')
    devices.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

let debounceTimer: ReturnType<typeof setTimeout> | null = null
function scheduleFetch() {
  if (debounceTimer) clearTimeout(debounceTimer)
  debounceTimer = setTimeout(() => { void fetchDevices() }, 300)
}
watch([keyword, typeFilter, statusFilter], () => {
  page.value = 1
  scheduleFetch()
})
function onPageChange(p: number) {
  page.value = p
  void fetchDevices()
}
function onSizeChange(s: number) {
  pageSize.value = s
  page.value = 1
  void fetchDevices()
}

// ── 卡片视图模型：一次算好，模板不重复计算 ──
interface CardView {
  device: EdgeDevice
  icon: Component
  typeLabel: string
  typeTone: string
  statusText: string
  statusTone: string
  node: string
  bus: string
  address: string
  readings: Reading[]
  hasData: boolean
  updated: string
  offline: boolean
}
const cards = computed<CardView[]>(() =>
  devices.value.map((d) => ({
    device: d,
    icon: deviceIcon(d.device_type),
    typeLabel: getDeviceTypeLabel(d.device_type),
    typeTone: typeTone(d.device_type),
    statusText: statusLabel(d.status),
    statusTone: statusTone(d.status),
    node: nodeLabel(d),
    bus: busLabel(d),
    address: addressValue(d),
    readings: pickReadings(d),
    hasData: !!d.last_data,
    updated: relTime(d.last_data_time),
    offline: d.status === 'offline' || d.status === 'disabled',
  })),
)

// ── 行为 ──
function openDetail(d: EdgeDevice) {
  void router.push('/edge-device/' + d.id)
}
async function copyText(text: string, what: string) {
  try {
    await navigator.clipboard.writeText(text)
    feedback.success('已复制' + what + '：' + text)
  } catch {
    feedback.error('复制失败')
  }
}
function demoOnly(action: string, d: EdgeDevice) {
  feedback.info('演示页只评审卡片外观：' + action + '「' + d.name + '」请走真实列表页')
}

async function loadDriverTree() {
  try {
    driverTree.value = await getDriverTree()
  } catch {
    driverTree.value = [] // 能力未知时 addressValue 只对 I2C 兜底，不阻塞列表
  }
}
onMounted(() => {
  void loadDriverTree()
  void fetchDevices()
})
onUnmounted(() => { if (debounceTimer) clearTimeout(debounceTimer) })
</script>

<style scoped>
.edc-page { padding: 0; }
.edc-banner {
  margin-bottom: 14px;
  padding: 4px 12px;
  background: repeating-linear-gradient(45deg, #b45309, #b45309 12px, #d97706 12px, #d97706 24px);
  color: #fff;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.5px;
  text-align: center;
  border-radius: var(--radius-sm);
}

.edc-head { display: flex; align-items: flex-end; gap: 16px; flex-wrap: wrap; margin-bottom: 16px; }
.edc-title { margin: 0; font-size: 18px; font-weight: 600; color: var(--el-text-color-primary); }
.edc-sub { margin: 4px 0 0; font-size: 12px; color: var(--el-text-color-secondary); }
.edc-sub b { color: var(--el-text-color-primary); }
.edc-toolbar { margin-left: auto; display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.edc-search { width: 220px; }
.edc-select { width: 150px; }
.edc-error { margin-bottom: 16px; }
.edc-error-detail { margin-top: 4px; font-size: 12px; line-height: 1.6; }

.edc-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(320px, 1fr)); gap: 16px; }

/* ── 卡片：沿用 .card 语言（8px 圆角 / token），hover 轻微上浮 ── */
.edc-card {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 16px 18px;
  background: var(--card-bg);
  border: 1px solid var(--card-border);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-sm);
  transition: transform 0.2s ease, box-shadow 0.2s ease, border-color 0.2s ease;
}
.edc-card:hover { transform: translateY(-2px); border-color: var(--el-color-primary-light-5); box-shadow: var(--shadow-md); }
.edc-card.is-offline { opacity: 0.82; }

.edc-top { display: flex; align-items: flex-start; gap: 12px; }
/* 尺寸/居中/圆角由 theme.css 的 .device-type-icon 给（全站统一），
   本页只覆盖 size：卡片头用 42px，与默认一致故仅作显式声明。 */
.edc-icon { --device-icon-size: 42px; }
/* .tone-* 前景/底色搬到 theme.css 的 .device-type-icon（全站统一，且暗色下 primary
   必须换成 --icon-primary，见该处注释）——此处不再自维护一套颜色。 */

.edc-ident { flex: 1; min-width: 0; }
.edc-name-row { display: flex; align-items: center; gap: 8px; }
.edc-name {
  margin: 0; font-size: 16px; font-weight: 600; color: var(--el-text-color-primary);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.edc-type { margin: 6px 0 0; font-size: 12px; color: var(--el-text-color-secondary); }

/* 状态：文字 + 颜色双通道；离线/禁用走中性色（is-muted），不是故障色 */
.edc-status {
  display: inline-flex; align-items: center; gap: 5px; flex-shrink: 0;
  padding: 2px 8px; border-radius: 999px; font-size: 12px; white-space: nowrap;
}
.edc-status .dot { width: 6px; height: 6px; border-radius: 50%; background: currentColor; }
.edc-status.is-success { color: var(--el-color-success); background: var(--el-color-success-light-9); }
.edc-status.is-info { color: var(--el-color-info); background: var(--el-color-info-light-9); }
.edc-status.is-warning { color: var(--el-color-warning); background: var(--el-color-warning-light-9); }
.edc-status.is-danger { color: var(--el-color-danger); background: var(--el-color-danger-light-9); }
.edc-status.is-muted { color: var(--el-text-color-secondary); background: var(--el-fill-color-light); }

.edc-meta { display: flex; flex-wrap: wrap; gap: 8px; }
.edc-chip {
  display: inline-flex; align-items: center; gap: 6px; min-width: 0;
  padding: 6px 10px; border-radius: var(--radius-sm);
  background: var(--el-fill-color-lighter);
  font-size: 12px; color: var(--el-text-color-secondary);
}
button.edc-chip { border: none; font-family: inherit; cursor: pointer; }
button.edc-chip:hover { background: var(--el-fill-color-light); color: var(--el-color-primary); }
.chip-value { color: var(--el-text-color-primary); font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 190px; }
.chip-value.mono { font-family: 'SF Mono', Consolas, Monaco, monospace; font-weight: 500; }

.edc-readings { display: flex; gap: 8px; }
.edc-reading {
  flex: 1 1 0; min-width: 0;
  display: flex; flex-direction: column; gap: 2px;
  padding: 8px 10px; border-radius: var(--radius-sm);
  background: var(--el-fill-color-lighter);
}
.r-label { font-size: 12px; color: var(--el-text-color-secondary); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.r-value { font-size: 16px; font-weight: 600; color: var(--el-text-color-primary); font-variant-numeric: tabular-nums; white-space: nowrap; }
.r-unit { margin-left: 2px; font-size: 12px; font-weight: 400; color: var(--el-text-color-secondary); }
.edc-readings.is-empty {
  align-items: center; justify-content: center; gap: 6px;
  padding: 14px; border-radius: var(--radius-sm);
  background: var(--el-fill-color-lighter); color: var(--el-text-color-secondary); font-size: 13px;
}
.edc-time { margin: 0; font-size: 12px; color: var(--el-text-color-secondary); }

/* 操作区固定贴底：卡片在网格里等高，底边按钮对齐 */
.edc-actions {
  margin-top: auto; padding-top: 12px;
  display: flex; align-items: center; justify-content: flex-end; gap: 4px;
  border-top: 1px solid var(--el-border-color-lighter);
}

.edc-pager { display: flex; justify-content: center; padding: 20px 0; }

@media (max-width: 768px) {
  .edc-toolbar { margin-left: 0; width: 100%; }
  .edc-search, .edc-select { width: 100%; }
  .edc-grid { grid-template-columns: minmax(0, 1fr); }
  button.edc-chip, .edc-actions .el-button:not(.is-circle) { min-height: 44px; }
}
@media (pointer: coarse) {
  button.edc-chip, .edc-actions .el-button:not(.is-circle) { min-height: 44px; }
}
</style>
