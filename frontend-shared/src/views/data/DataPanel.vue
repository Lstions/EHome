<template>
  <div class="data-panel">
    <PageHeader title="数据面板" />

    <el-card>
      <el-form :inline="true" :model="queryForm">
        <el-form-item label="设备">
          <el-select
            v-model="queryForm.deviceId"
            placeholder="请选择设备"
            clearable
            filterable
            style="width: 200px;"
          >
            <el-option
              v-for="device in deviceList"
              :key="device.id"
              :label="device.name"
              :value="device.id"
            />
          </el-select>
        </el-form-item>
        <el-form-item label="时间范围">
          <el-select
            v-model="queryForm.timeRange"
            placeholder="请选择时间范围"
            style="width: 150px;"
          >
            <el-option label="最近1小时" value="1h" />
            <el-option label="最近24小时" value="24h" />
            <el-option label="最近7天" value="7d" />
            <el-option label="最近30天" value="30d" />
          </el-select>
        </el-form-item>
        <el-form-item>
          <el-button type="primary" data-test="query" @click="handleQuery" :loading="loading">查询</el-button>
          <el-tooltip content="请先查询数据" placement="top" :disabled="canExport">
            <span>
              <el-button @click="handleExport" :disabled="!canExport">
                <el-icon><Download /></el-icon>
                导出CSV
              </el-button>
            </span>
          </el-tooltip>
        </el-form-item>
      </el-form>
    </el-card>

    <!-- 统计概览卡片
         规范 §4.3 统计卡 MUST「统计值必须标明范围」：本卡片组里**两种范围混排**，
         所以范围词必须逐个标注、不能共用一句页脚说明：
           · 最新<指标>  —— 取自当前页最新一条记录（本页）
           · 本次数据点  —— total.value，是服务端按时间范围筛出的**全量总数**（当前筛选）
           · 采集覆盖时长 —— calculateStats() 只遍历 historyData（当前页 20 条）
         改前三个标签都没有范围词，与分页器「共 N 条」并排显示时，
         用户会把「本页覆盖时长」读成「30 天只采到 9 分钟」，从而误判数据完整性。 -->
    <div v-if="queryForm.deviceId && historyData.length > 0" class="data-stats">
      <el-card v-for="stat in dynamicStats" :key="stat.code" class="stat-card" shadow="hover">
        <div class="stat-content">
          <div class="stat-icon">
            <el-icon><DataAnalysis /></el-icon>
          </div>
          <div class="stat-info">
            <p class="stat-label">{{ stat.label }}<span class="stat-scope">{{ SCOPE_PAGE }}</span></p>
            <p class="stat-value">{{ stat.value }}<span v-if="stat.unit" class="stat-unit">{{ stat.unit }}</span></p>
          </div>
        </div>
      </el-card>
      <el-card class="stat-card" shadow="hover">
        <div class="stat-content">
          <div class="stat-icon"><el-icon><DocumentChecked /></el-icon></div>
          <div class="stat-info">
            <p class="stat-label">数据点总数<span class="stat-scope">{{ SCOPE_FILTERED }}</span></p>
            <p class="stat-value" data-testid="stat-total-points">{{ total }}</p>
          </div>
        </div>
      </el-card>
      <el-card class="stat-card" shadow="hover">
        <div class="stat-content">
          <div class="stat-icon"><el-icon><Timer /></el-icon></div>
          <div class="stat-info">
            <p class="stat-label">采集覆盖时长<span class="stat-scope">{{ SCOPE_PAGE }}</span></p>
            <p class="stat-value" data-testid="stat-duration">{{ latestStats.duration }}</p>
          </div>
        </div>
      </el-card>
    </div>

    <!-- 实时数据开关 -->
    <el-card style="margin-top: 20px;" v-if="queryForm.deviceId">
      <template #header>
        <div style="display: flex; justify-content: space-between; align-items: center;">
          <span>实时数据</span>
          <el-switch
            v-model="realtimeEnabled"
            aria-label="实时数据开关"
            active-text="开启"
            inactive-text="关闭"
            @change="toggleRealtime"
          />
        </div>
      </template>
      <div class="realtime-indicator" v-if="realtimeEnabled">
        <el-tag type="success" size="small">
          <el-icon><Connection /></el-icon>
          实时接收中
        </el-tag>
        <span class="realtime-count">共 {{ realtimeCount }} 条</span>
      </div>
      <el-empty v-else description="点击开关开启实时数据接收" />
    </el-card>

    <!-- 历史趋势图表 -->
    <el-card style="margin-top: 20px;" v-if="queryForm.deviceId && historyData.length > 0">
      <template #header>
        <span>历史数据趋势</span>
      </template>
      <div style="min-height: 350px;">
        <LineChart
          v-if="chartSeries.length > 0"
          :series="chartSeries"
          :realtime="realtimeEnabled"
          title="历史数据趋势"
          height="350px"
        />
        <el-empty v-else description="暂无趋势数据" />
      </div>
    </el-card>

    <!-- 多设备对比 -->
    <el-card style="margin-top: 20px;" v-if="compareMode">
      <template #header>
        <div style="display: flex; justify-content: space-between; align-items: center;">
          <span>多设备对比</span>
          <el-button size="small" @click="compareMode = false">关闭对比</el-button>
        </div>
      </template>
      <el-checkbox-group v-model="compareDevices" :min="2">
        <el-checkbox
          v-for="device in deviceList"
          :key="device.id"
          :value="device.id"
          :label="device.name"
          style="margin-right: 16px;"
        />
      </el-checkbox-group>
      <div v-if="compareDevices.length >= 2" style="margin-top: 12px; display: flex; align-items: center; gap: 12px;">
        <span style="font-size: 14px; color: var(--el-text-color-regular);">对比类别：</span>
        <el-select v-model="queryForm.compareCategory" size="small" style="width: 180px;" :disabled="compareCategories.length === 0">
          <el-option
            v-for="category in compareCategories"
            :key="category.code"
            :label="`${sensorNameMap[category.code] || category.code}${category.unit ? ` (${category.unit})` : ''}`"
            :value="category.code"
          />
        </el-select>
        <span v-if="compareCategories.length === 0" class="compare-category-hint">所选设备没有可安全比较的同单位指标</span>
      </div>
      <div v-if="compareDevices.length >= 2" style="margin-top: 20px; min-height: 300px;">
        <LineChart
          v-if="compareSeries.length > 0"
          :series="compareSeries"
          title="多设备对比"
          height="300px"
        />
      </div>
      <el-empty v-else description="请选择至少2个设备进行对比" />
    </el-card>

    <EmptyState
      v-if="!queryForm.deviceId"
      kind="initial"
      icon="Cpu"
      title="选择设备后查看数据"
      description="选择设备和时间范围后，可查看历史趋势与实时数据。"
      :quick-actions="[
        { label: '管理边缘设备', icon: Cpu, type: 'primary', handler: () => router.push('/edge-device') }
      ]"
    />

    <!-- 历史数据表格（未选择设备时由上方引导空状态接管，整个卡片不渲染） -->
    <el-card style="margin-top: 20px;" v-if="queryForm.deviceId">
      <template #header>
        <div style="display: flex; justify-content: space-between; align-items: center;">
          <span>历史数据</span>
          <el-button
            v-if="!compareMode && deviceList.length > 1"
            size="small"
            @click="compareMode = true"
          >
            多设备对比
          </el-button>
        </div>
      </template>
      <el-skeleton v-if="loading" :rows="5" animated />
      <template v-else>
        <EmptyState
          v-if="!historyData || historyData.length === 0"
          kind="empty"
          icon="DataAnalysis"
          title="该时间范围内暂无数据"
          description="可调整时间范围，或确认设备已完成采集与同步。"
        />
        <div v-else class="mobile-table-wrapper">
          <p class="mobile-table-hint">左右滑动查看完整历史数据</p>
          <el-table :data="historyData" stripe>
            <el-table-column prop="created_at" label="采集时间" width="180">
              <template #default="{ row }">
                <span>{{ formatTime(row.timestamp || row.created_at || row.collected_at) }}</span>
              </template>
            </el-table-column>
            <!-- 两列都经 parseRowData(row) 这**唯一**入口取数：
                 接口 GET /edge-devices/:id/data 只返回 data_json（无 parsed_data/data/raw_data），
                 改前表格读 row.parsed_data || row.data ⇒ 恒 undefined ⇒ 恒「—」。
                 解析失败（坏 JSON / 空）返回 null ⇒ 仍显示「—」，与"值真的是 0"区分开。 -->
            <el-table-column prop="data" label="数据">
              <template #default="{ row }">
                <!-- 判据是 .values 而不是整个返回对象：后者恒为真对象，
                     会让"解析不出来"也能匹配 v-if 分支（只有 formatData 兜住才没露馅）。 -->
                <span v-if="parseRowData(row).values" data-testid="row-values">{{ formatData(parseRowData(row).values) }}</span>
                <span v-else style="color: var(--el-text-color-placeholder);" data-testid="values-unknown">{{ UNKNOWN }}</span>
              </template>
            </el-table-column>
            <el-table-column label="原始数据" width="120" align="center">
              <template #default="{ row }">
                <span v-if="parseRowData(row).rawHex" style="font-size: 12px; color: var(--el-text-color-secondary);" data-testid="row-raw-hex">{{ formatRawData(parseRowData(row).rawHex) }}</span>
                <span v-else style="color: var(--el-text-color-placeholder);" data-testid="raw-unknown">{{ UNKNOWN }}</span>
              </template>
            </el-table-column>
            <el-table-column label="状态" width="100" align="center">
              <template #default="{ row }">
                <el-tag v-if="row.error_code && row.error_code > 0"
                        :type="getErrorInfo(row.error_code).type"
                        size="small">
                  {{ getErrorInfo(row.error_code).label }}
                </el-tag>
                <span v-else style="color: var(--el-color-success);">正常</span>
              </template>
            </el-table-column>
          </el-table>
        </div>

        <el-pagination
          v-model:current-page="currentPage"
          v-model:page-size="pageSize"
          :total="total"
          :page-sizes="[20, 50, 100]"
          layout="total, sizes, prev, pager, next, jumper"
          style="margin-top: 20px; justify-content: flex-end;"
          @current-change="fetchData"
          @size-change="fetchData"
        />
      </template>
    </el-card>
  </div>
</template>

<script lang="ts">
/**
 * 行数据解析（纯函数，**本页唯一**的取数入口）。
 *
 * 为什么单独开一个非 setup 的 script 块：模板、统计卡（dynamicStats）、趋势兜底、CSV 导出、
 * 实时条目都要取同一份数据，散落多处必然再次漂移（本次缺陷正是"表格读 parsed_data、接口只给
 * data_json"）。函数放在模块作用域后，script setup 块内可直接调用、模板也能解析 ——
 * 已用本仓 @vue/compiler-sfc 3.5.40 实测确认（两块共享作用域，导出照常生成）。
 *
 * 为什么不用"把函数 defineExpose 出去、让测试调它"的既有范式：本缺陷的回归点是
 * **表格列恒显示 —**，只暴露函数等于不守渲染路径；测试必须打到真实渲染出的单元格。
 *
 * 契约（规范 §3.2.5 不得以本地默认值伪造事实）：
 *   · 取不到 / JSON 坏 ⇒ values = null ⇒ 界面显示「—」；
 *   · 值是 0 ⇒ values = { rainfall: 0 } ⇒ 界面显示「rainfall: 0.00」。
 *   二者绝不互相顶替，尤其**不得**把解析失败落成 0。
 */

/** parseRowData 的返回：数值指标 + 原始帧 hex。 */
export interface RowData {
  /**
   * 指标名 → **展示值**（数值或字符串）；解析不出来时为 null
   * （不是空对象 —— 空对象会与"真的没有指标"混淆）。
   *
   * 允许字符串是因为部分传感器的真值本就是字符串：后端 SensorData 有
   * `StringValue` 字段（drivers/registry.go:13），此时 `Value` 恒为 0 而真值在
   * StringValue 里 —— 典型是 BMS 的 hardware_version（"V19"）与 serial_number。
   * 只取 Value 会把它显示成 **0**，比「—」更糟：0 看起来像真实读数。
   */
  values: Record<string, number | string> | null
  /**
   * 指标名 → **纯数值**，供统计卡 / 趋势图 / 数值专用消费者使用。
   *
   * 单独给出是为了不让字符串读数污染数值计算：若把 "V19" 混进趋势，
   * 它会被当成 0 ⇒ 拉低均值并在图上画出掉到 0 的假线。
   */
  numbers: Record<string, number> | null
  /** 原始帧 hex，**不带 0x 前缀**（formatRawData 依赖该约定），没有则为 null */
  rawHex: string | null
}

/** GET /api/v1/edge-devices/:id/data 的真实行形状（另有 id/device_id/node_id/timestamp/created_at） */
interface DeviceDataRow {
  data_json?: unknown
  parsed_data?: unknown
  data?: unknown
  raw_data?: unknown
  raw_hex?: unknown
}

/**
 * 只认 number（并兼容既有的 { value } 包装）。刻意**不**做 Number(str)：
 * "0.5" / "abc" 这类字符串被静默转成 0.5 / NaN 都属于"以本地推断伪造事实"。
 */
const asNumber = (value: unknown): number | null => {
  if (typeof value === 'number') return Number.isFinite(value) ? value : null
  if (value && typeof value === 'object' && 'value' in value) {
    return asNumber((value as { value?: unknown }).value)
  }
  return null
}

/**
 * 单个传感器的展示值。
 *
 * 口径与本仓既有正确范式一致（views/edge-device/shared/DeviceControlPanel.vue:76
 * 的 `value.string_value || value.value`）：**StringValue 非空时优先**，否则取 Value。
 *
 * 为什么不能只取 Value：后端 SensorData.StringValue 承载字符串型读数
 * （jiabaida_parse.go 的 hardware_version / serial_number，其 Value 恒为 0），
 * 只取 Value 会把 "V19" 显示成 0。
 * 刻意**不**做 Number(str)：把 "0.5" 静默转成数字属于以本地推断伪造事实。
 * 返回 null 表示"这条传感器没有可信值"，与"值是 0"区分。
 */
const asDisplayValue = (sensor: { Value?: unknown; StringValue?: unknown }): number | string | null => {
  const sv = sensor.StringValue
  if (typeof sv === 'string' && sv.trim() !== '') return sv
  return asNumber(sensor.Value)
}

/** hex 字符串规整：容忍 0x/0X 前缀与大小写，空串视为"没有" */
const asHex = (value: unknown): string | null => {
  if (typeof value !== 'string') return null
  const hex = value.trim().replace(/^0x/i, '')
  return hex.length > 0 ? hex : null
}

/**
 * 非指标字段（行元数据 + 原始帧）。扁平兜底分支必须排除它们，否则会把
 * id / device_id / logical_device_id 这类**元数据数字**当成传感器读数渲染出来
 * （实测真实行就带这三个数字键 —— 本文件新增用例抓到过）。
 */
const NON_METRIC_KEYS = new Set([
  'id', 'device_id', 'node_id', 'logical_device_id', 'channel_id', 'error_code',
  'timestamp', 'created_at', 'collected_at', 'updated_at',
  'raw_data', 'raw_hex', 'data_json', 'parsed_data',
])

export function parseRowData(row: unknown): RowData {
  if (!row || typeof row !== 'object') return { values: null, numbers: null, rawHex: null }
  const r = row as DeviceDataRow

  let values: Record<string, number | string> | null = null
  // raw_hex 也可能来自 data_json 内部（真实帧就在 sensors 的同一层）
  let rawHex = asHex(r.raw_hex) ?? asHex(r.raw_data)
  const hasDataJson = typeof r.data_json === 'string' && r.data_json.trim() !== ''

  // ① 现代契约：data_json = {"channel_id":1,"raw_hex":"01030200057847","sensors":[{"Name":"rainfall","Value":0.5}]}
  if (hasDataJson) {
    let parsed: unknown = null
    try {
      parsed = JSON.parse(r.data_json as string)
    } catch {
      // 坏 JSON：**不抛**，values 保持 null ⇒ 表格显示「—」而不是 0
      parsed = null
    }
    if (parsed && typeof parsed === 'object') {
      const dj = parsed as { sensors?: unknown; raw_hex?: unknown; raw_data?: unknown }
      rawHex = rawHex ?? asHex(dj.raw_hex) ?? asHex(dj.raw_data)
      const sensors: unknown[] = Array.isArray(dj.sensors) ? dj.sensors : []
      const entries: Array<[string, number | string]> = []
      for (const sensor of sensors) {
        if (!sensor || typeof sensor !== 'object') continue
        const s = sensor as { Name?: unknown; Value?: unknown; StringValue?: unknown }
        const value = asDisplayValue(s)
        if (typeof s.Name === 'string' && s.Name !== '' && value !== null) entries.push([s.Name, value])
      }
      // 空 sensors 与"解析失败"同处理：都没有可信数值可展示
      if (entries.length > 0) values = Object.fromEntries(entries)
    }
    // 刻意**不再**退到扁平兜底：行是 data_json 形态却解不出 sensors 时，
    // 去扫行顶层的数字（id/device_id...）就是把元数据伪造成读数 —— 宁可为「—」。
    return { values, numbers: numericOnly(values), rawHex }
  }

  // ② 兼容 { data: {...} } 与扁平 {...} 两种既有形状（既有单测的 fixture 走这里）
  const flat = r.data && typeof r.data === 'object' && !Array.isArray(r.data)
    ? r.data as Record<string, unknown>
    : r
  const entries = Object.entries(flat)
    .filter(([key]) => !NON_METRIC_KEYS.has(key))
    .map(([key, value]) => [key, asNumber(value)] as const)
    .filter((entry): entry is readonly [string, number] => entry[1] !== null)
  if (entries.length > 0) values = Object.fromEntries(entries)

  return { values, numbers: numericOnly(values), rawHex }
}

/** 从展示值里挑出纯数值子集（统计卡 / 趋势图专用），字符串读数不得进入。 */
function numericOnly(values: Record<string, number | string> | null): Record<string, number> | null {
  if (!values) return null
  const out: Record<string, number> = {}
  for (const [key, value] of Object.entries(values)) {
    if (typeof value === 'number') out[key] = value
  }
  return Object.keys(out).length > 0 ? out : null
}
</script>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onUnmounted, watch } from 'vue'
import { feedback } from '@/utils/feedback'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { Download, Connection, DataAnalysis, DocumentChecked, Timer, Cpu } from '@element-plus/icons-vue'
import PageHeader from '@/components/common/PageHeader.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import LineChart from '@/components/charts/LineChart.vue'
import { edgeDeviceApi, type EdgeDevice } from '@/api/edgeDevice'
import { useEdgeDeviceStore } from '@/stores/edgeDevice'
import client from '@/api/client'
import { getErrorInfo } from '@/utils/errorCode'
import { useWebSocketStore, type WebSocketMessage } from '@/stores/websocket'
import { WS_EVENT } from '@/events/events'
import { logger } from '@/utils/logger'
import { sensorNameMap, sensorUnitMap } from '@/utils/sensor'
import { UNKNOWN } from '@/utils/format'

const router = useRouter()
const deviceList = ref<EdgeDevice[]>([])
const historyData = ref<any[]>([])
const chartSeries = ref<any[]>([])
const loading = ref(false)
const currentPage = ref(1)
const pageSize = ref(20)
const total = ref(0)

// 实时数据
const realtimeEnabled = ref(false)
const realtimeCount = ref(0)
const realtimeData = ref<any[]>([])

// 多设备对比
const compareMode = ref(false)
const compareDevices = ref<number[]>([])
const compareSeries = ref<any[]>([])
interface MeasurementCategory {
  code: string
  unit: string
}
interface MeasurementPoint {
  timestamp?: string
  created_at?: string
  value: number
}
interface MeasurementBatch {
  category: string
  data: MeasurementPoint[]
}
interface RealtimeDataPayload {
  edge_device_id?: number
  collected_at?: string
  data?: Record<string, unknown>
  sensors?: Record<string, unknown>
  /** WS 帧里的原始 hex（stores/websocket.ts 的 payload 契约），随行落库给 parseRowData 解析 */
  raw_hex?: unknown
  raw_data?: unknown
}
const availableCategories = ref<MeasurementCategory[]>([])
const compareCategories = ref<MeasurementCategory[]>([])

/**
 * 统计卡范围词（规范 §4.3 统计卡 MUST）。
 *
 * 为什么是常量而不是模板里的字面量：同一卡片组里存在**两种范围**，
 * 验收时要能机械地数出"每个统计值都带了范围词"，字面量散落则无法审计。
 *   · SCOPE_PAGE     —— 值只由当前页 historyData（pageSize 条）算出
 *   · SCOPE_FILTERED —— 值由服务端按「设备 + 时间范围」筛出的全量得出
 */
const SCOPE_PAGE = '本页'
const SCOPE_FILTERED = '当前筛选'

// 统计概览数据
const latestStats = reactive({
  totalPoints: 0,
  duration: UNKNOWN
})

/** 后端 envelope 解包：数组统一从 data 取（兼容裸数组返回） */
function unwrapList<T>(res: unknown): T[] {
  if (Array.isArray(res)) return res as T[]
  if (res && typeof res === 'object' && Array.isArray((res as { data?: unknown }).data)) {
    return (res as { data: T[] }).data
  }
  return []
}

const dynamicStats = computed(() => {
  const latest = [...historyData.value]
    .sort((a, b) => String(b.timestamp || b.created_at || b.collected_at || '').localeCompare(String(a.timestamp || a.created_at || a.collected_at || '')))[0]
  // 与表格列共用 parseRowData：改前读 latest.parsed_data || latest.data，
  // 而接口只给 data_json ⇒ source 恒 {} ⇒ 统计卡恒不渲染（数据非空却一张卡都没有）。
  // 统计卡是**数值指标**视图：用 numbers（纯数值）而不是 values（可能含字符串），
  // 否则 hardware_version="V19" 会被当成 0 计入并污染统计。
  const source = parseRowData(latest).numbers ?? {}
  return availableCategories.value
    .map((category) => {
      const value = source[category.code] ?? null
      if (value === null) return null
      return {
        code: category.code,
        label: `最新${sensorNameMap[category.code] || category.code}`,
        unit: category.unit || sensorUnitMap[category.code] || '',
        value: Number.isInteger(value) ? value : Number(value.toFixed(2)),
      }
    })
    .filter((stat): stat is { code: string; label: string; unit: string; value: number } => stat !== null)
    .slice(0, 3)
})

const calculateStats = () => {
  const items = historyData.value
  latestStats.totalPoints = total.value
  if (!items || items.length === 0) {
    // 空数组是「本页没有数据」，不是「时长为 0」；用统一未知占位符而非 '--'
    // （'--' 是第三种占位写法，规范 §3.4.5 要求未知值统一为 '—'）。
    latestStats.duration = UNKNOWN
    return
  }

  const times = items
    .map(item => item.timestamp || item.created_at || item.collected_at)
    .filter(Boolean)
    .map(t => new Date(t).getTime())
    .filter(t => !Number.isNaN(t))
    .sort((a, b) => a - b)
  if (times.length < 2) {
    latestStats.duration = '< 1分钟'
    return
  }

  const diffMin = Math.floor((times[times.length - 1] - times[0]) / 60000)
  if (diffMin < 60) {
    latestStats.duration = `${diffMin}分钟`
  } else if (diffMin < 1440) {
    latestStats.duration = `${Math.floor(diffMin / 60)}小时${diffMin % 60}分钟`
  } else {
    latestStats.duration = `${Math.floor(diffMin / 1440)}天${Math.floor((diffMin % 1440) / 60)}小时`
  }
}

const queryForm = reactive({
  deviceId: null as number | null,
  timeRange: '24h',
  compareCategory: 'temperature' as string
})

const canExport = computed(() => !!queryForm.deviceId && historyData.value.length > 0)

const wsStore = useWebSocketStore()
const edgeDeviceStore = useEdgeDeviceStore()
let unsubscribeData: (() => void) | null = null

const loadDeviceCategories = async () => {
  if (!queryForm.deviceId) {
    availableCategories.value = []
    return
  }
  try {
    const response = await client.get<unknown, MeasurementCategory[]>('/api/v1/unified-data/categories', {
      params: { device_pk: queryForm.deviceId },
    })
    availableCategories.value = unwrapList<MeasurementCategory>(response)
      .filter((item): item is MeasurementCategory => typeof item?.code === 'string' && item.code.length > 0)
      .map(item => ({ code: item.code, unit: item.unit || sensorUnitMap[item.code] || '' }))
  } catch (error) {
    logger.warn('获取设备指标类别失败', { error: String(error) })
    availableCategories.value = []
  }
}

const loadCompareCategories = async () => {
  if (compareDevices.value.length < 2) {
    compareCategories.value = []
    return
  }
  try {
    const categoryLists = await Promise.all(compareDevices.value.map(async (deviceId) => {
      const response = await client.get<unknown, MeasurementCategory[]>('/api/v1/unified-data/categories', { params: { device_pk: deviceId } })
      return unwrapList<MeasurementCategory>(response)
    }))
    const [first, ...rest] = categoryLists
    compareCategories.value = (first || []).filter((candidate: MeasurementCategory) =>
      rest.every(list => list.some((item: MeasurementCategory) => item.code === candidate.code && item.unit === candidate.unit)),
    )
    if (!compareCategories.value.some(category => category.code === queryForm.compareCategory)) {
      queryForm.compareCategory = compareCategories.value[0]?.code || ''
    }
  } catch (error) {
    logger.warn('获取可比较指标失败', { error: String(error) })
    compareCategories.value = []
    queryForm.compareCategory = ''
  }
}

const fetchDevices = async () => {
  try {
    const params = { page: 1, page_size: 500 }
    await edgeDeviceStore.fetchList(params)
    deviceList.value = edgeDeviceStore.getCachedList(params)?.items || []
  } catch {
    feedback.error('获取设备列表失败')
  }
}

/**
 * 查询按钮入口 —— 与分页控件的翻页入口分离。
 *
 * §3.2.6 MUST: 设备/时间范围是「会改变查询范围的输入」, 其变化必须重置分页派生状态。
 * 改前 currentPage 是独立 ref, 只有分页组件会改它: 用户翻到第 3 页后换设备/换时间范围,
 * 新查询仍以 page=3 发出 (实测网络原文 /edge-devices/2/data?...&page=3), 第 3 页在更小的
 * 结果集里可能直接越界 → 显示空表却没有任何解释。
 *
 * 为什么在这里重置而不是 watch(queryForm): 用户点「查询」也可能只是想刷新当前页,
 * 但那与「换条件」无法区分; 而按查询按钮重新查询本身就应当从第 1 页开始 —— 这是
 * 用户对"新一次查询"的预期。分页控件的 @current-change 不经过本函数, 因此翻页不受影响。
 */
const handleQuery = () => {
  currentPage.value = 1
  void fetchData()
}

const fetchData = async () => {
  if (!queryForm.deviceId) {
    ElMessage.warning('请先选择设备')
    return
  }

  loading.value = true
  try {
    const endTime = new Date()
    const startTime = new Date()

    switch (queryForm.timeRange) {
      case '1h':
        startTime.setHours(startTime.getHours() - 1)
        break
      case '24h':
        startTime.setHours(startTime.getHours() - 24)
        break
      case '7d':
        startTime.setDate(startTime.getDate() - 7)
        break
      case '30d':
        startTime.setDate(startTime.getDate() - 30)
        break
    }

    const response = await edgeDeviceApi.getHistoryData(queryForm.deviceId, {
      start_time: startTime.toISOString(),
      end_time: endTime.toISOString(),
      page: currentPage.value,
      page_size: pageSize.value
    })

    historyData.value = response.items || []
    total.value = response.total || 0

    await loadDeviceCategories()
    calculateStats()

    // 构建图表数据：从 unified_data API 获取解析后的数值数据
    buildChartSeries()
  } catch {
    feedback.error('获取历史数据失败')
  } finally {
    loading.value = false
  }
}

const buildChartSeries = async () => {
  if (historyData.value.length === 0) {
    chartSeries.value = []
    return
  }

  // 尝试从 unified_data API 获取解析后的数值数据
  try {
    const endTime = new Date()
    const startTime = new Date()
    switch (queryForm.timeRange) {
      case '1h':
        startTime.setHours(startTime.getHours() - 1)
        break
      case '24h':
        startTime.setHours(startTime.getHours() - 24)
        break
      case '7d':
        startTime.setDate(startTime.getDate() - 7)
        break
      case '30d':
        startTime.setDate(startTime.getDate() - 30)
        break
    }

    const categoryNames = availableCategories.value.map(category => category.code)
    if (categoryNames.length === 0) {
      chartSeries.value = []
      return
    }

    const series: any[] = []

    // 批量请求：1 个请求替代 19 个并行请求，服务端降采样 max_points=500
    const batchParams = {
      device_pk: queryForm.deviceId,
      categories: categoryNames.join(','),
      start_time: startTime.toISOString(),
      end_time: endTime.toISOString(),
      max_points: 500
    }

    try {
      const batchRes = await client.get<unknown, MeasurementBatch[]>('/api/v1/unified-data/historical-batch', {
        params: batchParams
      })
      // 批量 API 返回格式: [{category: "temperature", data: [{...}]}, ...]
      const batchResults = unwrapList<MeasurementBatch>(batchRes)
      if (batchResults.length > 0) {
        for (const result of batchResults) {
          const cat = result.category
          if (!cat) continue
          const items = (result.data || []).filter((item) => {
            const t = item.timestamp || item.created_at
            return t && !t.startsWith('0001-01-01')
          })
          if (items.length > 0) {
            const catName = sensorNameMap[cat] || cat
            const catUnit = sensorUnitMap[cat] || ''
            series.push({
              name: catName,
              unit: catUnit,
              category: cat,
              data: items.map((item) => ({
                time: item.timestamp || item.created_at,
                value: item.value
              }))
            })
          }
        }
      }

      // 如果批量 API 未返回任何数据，尝试 fallback 逐个请求
      if (series.length === 0) {
        throw new Error('batch API returned no data, falling back')
      }
    } catch {
      // Fallback: 批量 API 失败时逐个请求 + max_points=500
      const fallbackPromises = categoryNames.map(cat =>
        client.get<unknown, MeasurementPoint[]>('/api/v1/unified-data/historical', {
          params: {
            device_pk: queryForm.deviceId,
            category: cat,
            start_time: startTime.toISOString(),
            end_time: endTime.toISOString(),
            max_points: 500
          }
        }).then(res => ({ cat, data: unwrapList<MeasurementPoint>(res) }))
          .catch(() => ({ cat, data: [] as MeasurementPoint[] }))
      )

      const fallbackResults = await Promise.all(fallbackPromises)
      for (const r of fallbackResults) {
        if (r.data && r.data.length > 0) {
          const filteredData = r.data
            .filter((item) => {
              const t = item.timestamp || item.created_at
              return t && !t.startsWith('0001-01-01')
            })
          if (filteredData.length > 0) {
            const catName = sensorNameMap[r.cat] || r.cat
            const catUnit = sensorUnitMap[r.cat] || ''
            series.push({
              name: catName,
              unit: catUnit,
              category: r.cat,
              data: filteredData.map((item) => ({
                time: item.timestamp || item.created_at,
                value: item.value
              }))
            })
          }
        }
      }
    }

    chartSeries.value = series
  } catch (error) {
    logger.error('获取趋势数据失败', { error: String(error) })
    // Fallback: 从 data_json 提取数值字段（与表格/统计卡同一解析入口）
    // 趋势是数值图：同样只看 numbers，字符串读数不能进（否则画出掉到 0 的假线）
    const numericKeys = Object.keys(parseRowData(historyData.value[0]).numbers ?? {})

    if (numericKeys.length > 0) {
      chartSeries.value = numericKeys.map(key => ({
        name: key,
        unit: '',
        category: key,
        data: historyData.value
          .filter((item) => {
            const t = item.timestamp || item.collected_at || item.created_at
            return t && !t.startsWith('0001-01-01')
          })
          // 该行没这个指标 ⇒ null，**不是 0**：折线应当断开，而不是伪造一个落零点
          .map(item => ({
            time: item.timestamp || item.collected_at || item.created_at,
            value: parseRowData(item).numbers?.[key] ?? null,
          }))
      }))
    } else {
      chartSeries.value = []
    }
  }
}

// 实时数据处理
// 性能优化：防抖 calculateStats，避免每条WS消息都触发重计算
let statsDirty = false
let debounceTimer: ReturnType<typeof setTimeout> | null = null
const DEBOUNCE_MS = 2000  // 2秒内多条消息只触发一次重算

const flushDebounced = () => {
  if (statsDirty) {
    statsDirty = false
    calculateStats()
  }
}

const scheduleDebounced = () => {
  if (debounceTimer) clearTimeout(debounceTimer)
  debounceTimer = setTimeout(flushDebounced, DEBOUNCE_MS)
}

const handleDataUpdate = (message: WebSocketMessage) => {
  const payload = message.payload as RealtimeDataPayload | undefined
  if (!payload || payload.edge_device_id !== queryForm.deviceId) return

  realtimeCount.value++
  // 形状必须与表格期望一致：历史行推入表格后由 parseRowData 解析，
  // raw_hex 放在行内部，否则实时行的「原始数据」列会因数据只在 payload 上而显示「—」。
  const newItem = {
    collected_at: payload.collected_at || new Date().toISOString(),
    timestamp: payload.collected_at || new Date().toISOString(),
    data: (payload.data || payload.sensors || {}) as Record<string, unknown>,
    raw_hex: asHex(payload.raw_hex) ?? asHex(payload.raw_data) ?? undefined,
    error_code: 0
  }

  // 添加到实时数据列表
  realtimeData.value.unshift(newItem)
  if (realtimeData.value.length > 50) {
    realtimeData.value = realtimeData.value.slice(0, 50)
  }

  // 同时更新历史数据表格（添加到顶部）
  historyData.value.unshift(newItem)
  if (historyData.value.length > pageSize.value) {
    historyData.value = historyData.value.slice(0, pageSize.value)
  }
  total.value++

  // 标记stats为脏（统计计算），但不标记chartDirty
  statsDirty = true
  scheduleDebounced()

  // 增量更新图表 — 直接append到现有series
  appendRealtimeData(payload)
}

// 新增函数: 增量更新图表series
const appendRealtimeData = (payload: RealtimeDataPayload) => {
  if (chartSeries.value.length === 0) return
  const data = payload.data || payload.sensors
  if (!data || typeof data !== 'object') return
  const time = payload.collected_at || new Date().toISOString()

  let updated = false
  for (const series of chartSeries.value) {
    const key = series.category
    if (key && data[key] !== undefined && typeof data[key] === 'number') {
      series.data.push({ time, value: data[key] })
      // 限制长度
      if (series.data.length > 500) {
        series.data = series.data.slice(-500)
      }
      updated = true
    }
  }
  // 触发响应式更新 — 重新赋值引用
  if (updated) {
    chartSeries.value = [...chartSeries.value]
  }
}

const toggleRealtime = (enabled: string | number | boolean) => {
  if (enabled) {
    // 订阅实时数据
    unsubscribeData = wsStore.subscribe(WS_EVENT.DATA_UPDATE, handleDataUpdate)
    realtimeCount.value = 0
    realtimeData.value = []
    ElMessage.success('已开启实时数据接收')
  } else {
    if (unsubscribeData) {
      unsubscribeData()
      unsubscribeData = null
    }
    ElMessage.info('已关闭实时数据接收')
  }
}

// 导出 CSV
const handleExport = () => {
  if (!queryForm.deviceId) return

  const device = deviceList.value.find(d => d.id === queryForm.deviceId)
  const deviceName = device?.name || queryForm.deviceId

  const rows = [['时间', '数据', '原始数据', '错误码']]
  for (const item of historyData.value) {
    // 导出与表格同源：JSON.parse 后再按行序列化，直接把 data_json 原串写进单元格没有意义
    const { values, rawHex } = parseRowData(item)
    rows.push([
      formatTime(item.timestamp || item.created_at || item.collected_at),
      values ? JSON.stringify(values) : '',
      rawHex ?? '',
      String(item.error_code || 0)
    ])
  }

  const csvContent = rows.map(row => row.map(cell => `"${cell}"`).join(',')).join('\n')
  const blob = new Blob(['\ufeff' + csvContent], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = `${deviceName}_${queryForm.timeRange}.csv`
  link.click()
  URL.revokeObjectURL(url)

  ElMessage.success('导出成功')
}

// 获取多设备对比数据
const fetchCompareData = async () => {
  if (compareDevices.value.length < 2) return

  compareSeries.value = []
  const endTime = new Date()
  const startTime = new Date(endTime.getTime() - 24 * 60 * 60 * 1000) // 固定 24h

  try {
    const promises = compareDevices.value.map(async (deviceId) => {
      const response = await client.get<unknown, MeasurementPoint[]>(`/api/v1/unified-data/historical`, {
        params: {
          device_pk: deviceId,
          category: queryForm.compareCategory,
          start_time: startTime.toISOString(),
          end_time: endTime.toISOString(),
          max_points: 500
        }
      })
      return {
        deviceId,
        deviceName: deviceList.value.find(d => d.id === deviceId)?.name || String(deviceId),
        data: unwrapList<MeasurementPoint>(response)
      }
    })

    const results = await Promise.all(promises)

    compareSeries.value = results.map(r => ({
      name: r.deviceName,
      unit: '',
      data: r.data
        .filter((item) => {
          const t = item.timestamp || item.created_at
          return t && !t.startsWith('0001-01-01')
        })
        .map((item) => ({
          time: item.timestamp || item.created_at,
          value: item.value
        }))
    }))
  } catch (error) {
    logger.error('获取对比数据失败', { error: String(error) })
  }
}

const formatTime = (time: string) => {
  return time ? new Date(time).toLocaleString('zh-CN') : UNKNOWN
}

const formatData = (data: Record<string, unknown> | null | undefined) => {
  // typeof + 键数双保险：null / undefined / 空对象 / 空数组 / 字符串一律「—」。
  // formatData 是显示层最后一道闸：即便上游把 values 传成空对象（"没有可信数值"），
  // 也会落回「—」而不是渲染成 "0" 或空白。
  if (!data || typeof data !== 'object' || Array.isArray(data)) return UNKNOWN

  // Filter out raw_data from display (shown in separate column)
  const entries = Object.entries(data).filter(([key]) => key !== 'raw_data')
  if (entries.length === 0) return UNKNOWN

  return entries
    .map(([key, value]) => {
      // 支持 {value, unit} 格式
      if (typeof value === 'object' && value !== null && 'value' in value) {
        const entry = value as { value: unknown; unit?: unknown }
        const val = entry.value
        const unit = typeof entry.unit === 'string' ? entry.unit : ''
        if (typeof val === 'number') {
          return `${key}: ${val.toFixed(2)} ${unit}`.trim()
        }
        return `${key}: ${val} ${unit}`.trim()
      }
      if (typeof value === 'number') {
        return `${key}: ${value.toFixed(2)}`
      }
      return `${key}: ${value}`
    })
    .join(', ')
}

// 参数放宽到可空：调用点（表格列）拿到的是 parseRowData 的 rawHex，类型就是 string | null；
// 函数体本来就以 !rawData 兜底返回 UNKNOWN，放宽类型不改变任何行为。
const formatRawData = (rawData?: string | Record<string, any> | null) => {
  if (!rawData) return UNKNOWN
  // raw_data may be a hex string from data field, or base64 from raw_data field
  if (typeof rawData === 'string') {
    // Detect hex pattern (all lowercase hex chars)
    if (/^[0-9a-f]+$/.test(rawData)) {
      const bytes = rawData.length / 2
      return `${bytes}B hex`
    }
    // base64 or other
    return rawData.length > 20 ? rawData.substring(0, 20) + '...' : rawData
  }
  return JSON.stringify(rawData)
}

watch(compareDevices, async () => {
  if (compareMode.value && compareDevices.value.length >= 2) {
    await loadCompareCategories()
    if (queryForm.compareCategory) await fetchCompareData()
  }
})

watch(() => queryForm.compareCategory, () => {
  if (compareMode.value && compareDevices.value.length >= 2) {
    fetchCompareData()
  }
})

onMounted(() => {
  fetchDevices()
})

onUnmounted(() => {
  if (unsubscribeData) {
    unsubscribeData()
  }
  if (debounceTimer) {
    clearTimeout(debounceTimer)
    debounceTimer = null
  }
})
</script>

<style scoped>
.data-panel {
  padding: 0;
}

.data-stats {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(190px, 1fr));
  gap: 16px;
  margin-top: 20px;
}

:deep(.el-pagination) {
  display: flex;
  justify-content: flex-end;
}

.realtime-indicator {
  display: flex;
  align-items: center;
  gap: 12px;
}

.realtime-count {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.stat-card {
  cursor: default;
  transition: transform 0.3s, box-shadow 0.3s;
}

.stat-card:hover {
  transform: translateY(-2px);
  box-shadow: var(--shadow-md);
}

.stat-content {
  display: flex;
  align-items: center;
  gap: 12px;
}

.stat-icon {
  width: 48px;
  height: 48px;
  border-radius: 10px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  background: var(--el-color-primary-light-9);
  color: var(--el-color-primary);
}

.stat-info {
  flex: 1;
  min-width: 0;
}

.stat-label {
  margin: 0 0 4px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

/**
 * 范围词（§4.3 统计卡 MUST）。
 * 用小号 chip 与标签区分层级（§4.2.4 文字层级清晰），且 word-break: keep-all
 * 防止「本页」在窄卡里被逐字竖排（§4.2.5）。
 */
.stat-scope {
  display: inline-block;
  margin-left: 6px;
  padding: 0 6px;
  border-radius: 8px;
  font-size: 11px;
  line-height: 16px;
  color: var(--el-text-color-secondary);
  background: var(--el-fill-color);
  white-space: nowrap;
  word-break: keep-all;
  vertical-align: 1px;
}

.stat-value {
  margin: 0;
  font-size: 22px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.stat-unit {
  font-size: 13px;
  font-weight: 400;
  color: var(--el-text-color-secondary);
  margin-left: 2px;
}

.compare-category-hint {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}

@media (max-width: 768px) {
  .data-stats {
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 12px;
  }

  :deep(.el-form--inline) {
    display: flex;
    flex-direction: column;
  }

  :deep(.el-form--inline .el-form-item) {
    margin-right: 0;
  }

  :deep(.el-form--inline .el-form-item__content),
  :deep(.el-form--inline .el-select) {
    width: 100%;
  }

  .realtime-indicator {
    flex-wrap: wrap;
  }

  /* 移动端 165px 卡宽：范围词换到标签下一行，避免把标签挤成逐字竖排（§4.2.5） */
  .stat-scope {
    margin-left: 0;
    margin-top: 2px;
  }

  /* 165px 卡里图标(48px) + gap(12px) + 内边距(40px) 只给标签留下 63px，
     「数据点总数」「采集覆盖时长」这两个 6 字标签因此会折成「……数」/「……长」——
     末行仅剩 1 个字，属规范 §4.2.5 明令禁止的逐字竖排观感（实测 390px 与 360px 均复现）。
     这里收窄图标与间距把标签让到 83px，**保持 12px 字号不变**（不为塞下而缩小到不可读，
     规范 §4.4.1 禁止靠缩小到不可读来解决响应式）。
     32px 图标与 8px 间距与 StatCard.vue 的移动端紧凑档（22-32px）同属一个视觉体系。 */
  .stat-icon {
    width: 32px;
    height: 32px;
    border-radius: 8px;
    font-size: 16px;
  }

  .stat-content {
    gap: 8px;
  }

  /* 卡片内边距从 Element Plus 默认的 20px 收到 16px。
     360px 档（本项目规范 §4.4.1 的核心下限）实测：图标32 + gap8 + 内边距40 仍只给标签 68px，
     而「采集覆盖时长」需 72px ⇒ 仍会折成末行只剩「长」一个字。
     收到 16px 后标签得 76px，360/390/414/768 四档全部单行显示。 */
  .stat-card :deep(.el-card__body) {
    padding: 16px;
  }
}
</style>