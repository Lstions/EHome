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
      <!-- P0：趋势序列是**异步**构建的（historical-batch 生产实测挂起 38s+，超时后回退
           还要 ~10–30s）。改前 chartSeries 未就绪时直接渲染 el-empty「暂无趋势数据」
           —— 有数据却报"暂无"的假空态。现在：加载中 → 骨架；失败 → 错误态 + 重试；
           只有确实没有可绘制的数值时才显示空态。 -->
      <div class="chart-area">
        <el-skeleton v-if="chartLoading" :rows="6" animated data-testid="chart-skeleton" />
        <template v-else>
          <EmptyState
            v-if="chartFailed"
            kind="error"
            :icon="WarningFilled"
            title="趋势数据加载失败"
            description="获取历史趋势数据失败，可重试；若持续失败可缩小时间范围后再查询。"
            :quick-actions="[{ label: '重试', type: 'primary', handler: retryChart }]"
            data-testid="chart-error"
          />
          <template v-else-if="visibleChartSeries.length > 0">
            <!-- 393：多指标 4 轴 + 20 条图例在 303px 画布里读不出（审计实测）→ 移动端
                 单指标 + 紧凑高度；卡片头已有标题，不再给 canvas 传 title（去重复标题）。 -->
            <div v-if="isMobile" class="mobile-chart-head">
              <el-select v-model="mobileMetric" data-testid="chart-metric" style="width: 100%;">
                <el-option v-for="item in chartMetricOptions" :key="item.code" :label="item.label" :value="item.code" />
              </el-select>
              <p class="mobile-chart-hint">移动端一次显示 1 个指标；全部指标请在桌面查看。</p>
            </div>
            <LineChart
              :series="visibleChartSeries"
              :realtime="realtimeEnabled"
              :height="chartHeight"
            />
          </template>
          <el-empty v-else description="该时间范围内暂无可绘制的数值趋势" />
        </template>
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
          height="300px"
        />
      </div>
      <el-empty v-else description="请选择至少2个设备进行对比" />
    </el-card>

    <EmptyState
      v-if="!queryForm.deviceId"
      kind="initial"
      :icon="Cpu"
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
        <!--
          降采样提示（2026-11-11 原始数据退役配套）。
          服务端 max_points 是**按行**（一字段一行）抽的 ⇒ 抽得越狠，每条采集剩下
          的字段越少。不提示的话「这次采集只有 total_voltage」会被当成设备只上报了
          一个字段，把排障方向带偏。 -->
        <el-alert
          v-if="historyDownsampled"
          type="warning"
          :closable="false"
          show-icon
          data-testid="history-downsampled"
          title="数据量较大，本表已按服务端降采样显示"
          description="降采样按「字段行」抽取，因此部分采集记录只保留了少数指标；缩小时间范围可看到完整字段。"
          style="margin-bottom: 12px;"
        />
        <EmptyState
          v-if="!historyData || historyData.length === 0"
          kind="empty"
          :icon="DataAnalysis"
          title="该时间范围内暂无数据"
          description="可调整时间范围，或确认设备已完成采集与同步。"
        />
        <!-- P0（B-data 审计）：393 下「数据」列只有 80px 宽，而 formatData 把整行约
             638 字符的全量 K/V 串塞进去 ⇒ 逐字换行、单行高 2041px、20 行整页 42689px，
             分页器在 4 万像素外不可用。窄屏改为「摘要 3~4 个关键指标 + 查看完整数据」
             卡片列表：不 ellipsis 丢数据，完整 K/V 全量放进详情弹层。 -->
        <div v-else-if="isMobile" data-testid="data-card-list" class="data-card-list">
          <!-- 分页在两个位置都可见：卡片列表会占多屏，顶部再放一条精简分页器，避免
               "翻页要滚到底"（审计要求：分页必须可见可点）。 -->
          <el-pagination
            v-if="total > pageSize"
            v-model:current-page="currentPage"
            v-model:page-size="pageSize"
            :total="total"
            :pager-count="5"
            layout="prev, pager, next"
            class="data-card-pagination"
            data-testid="data-pagination-top"
            @current-change="handlePageChange"
          />
          <HistoryRowCards
            :rows="historyData"
            :categories="availableCategories"
            @expand="openRowDetail"
          />
        </div>

        <HistoryDataTable
          v-else
          :rows="historyData"
          :page="currentPage"
          :total="total"
        />

        <el-pagination
          v-model:current-page="currentPage"
          v-model:page-size="pageSize"
          :total="total"
          :page-sizes="[20, 50, 100]"
          layout="total, sizes, prev, pager, next, jumper"
          style="margin-top: 20px; justify-content: flex-end;"
          @current-change="handlePageChange"
          @size-change="handlePageChange"
        />
      </template>
    </el-card>

    <!-- 移动端「查看完整数据」弹层：完整 K/V 全量展示，不丢任何指标（P0 配套）。 -->
    <RowDetailDialog
      v-model="rowDetailVisible"
      :row="rowDetailRow"
      :categories="availableCategories"
    />
  </div>
</template>

<script lang="ts">
/**
 * 行数据解析已搬到 ./rowData.ts（纯函数、无 Vue 依赖）。
 *
 * 为什么这里还要具名再导出：DataPanelRowData.spec.ts 的读取路径是
 * `import DataPanel, { parseRowData } from '@/views/data/DataPanel.vue'` ——
 * 门禁断言文本一字未改，搬迁只改实现位置，不改对外读取路径。
 *
 * 为什么用 `export { … } from` 而不是"先 import 再 export"：<script setup> 里也要
 * import 同一个名字（CSV 导出用），两块合并成一个模块后重复 import 会重复声明。
 */
export { parseRowData } from './rowData'
export type { RowData } from './rowData'
</script>

<script setup lang="ts">
/**
 * 数据面板（/data）：设备 + 时间范围 → 统计卡 / 趋势图 / 历史表 / 实时流 / 多设备对比。
 *
 * ============ 2026-11-12 纯搬移式拆分（行为零变化）============
 * 本文件只保留**页面级约定**，其余按"状态从属"搬进 composables/、按"自包含渲染单元"
 * 搬进 components/：
 *   · ./rowData.ts             行数据解析（纯函数，唯一取数入口）
 *   · ./rowDisplay.ts          行展示口径（时间/指标名/单位/摘要），三个子组件共用
 *   · ./types.ts ./envelope.ts 共享响应类型与 envelope 解包
 *   · composables/useDataPanelStats.ts     统计概览卡（两种范围口径）
 *   · composables/useDataPanelChart.ts     趋势序列 + 三个异步状态位 + 移动端单指标
 *   · composables/useDataPanelCompare.ts   多设备对比（独立取数链 + 两个 watch）
 *   · composables/useDataPanelRealtime.ts  WS 订阅 / 防抖重算 / 图表增量 + 生命周期清理
 *   · components/HistoryDataTable.vue      桌面表格（含 .mobile-table-wrapper 壳）
 *   · components/HistoryRowCards.vue       移动端摘要卡片
 *   · components/RowDetailDialog.vue       单条数据完整内容弹层
 *
 * 页面**必须**保留的（门禁/契约判据，删了就变红或退出分母）：
 *   · PageHeader（listPageConventions 规则③）；
 *   · 两个 <el-pagination> 的 :total + current-page 绑定（规则①/②，页面靠它们留在分母里）；
 *   · 本地切片 applyPage()（EXEMPT_LOCAL_SLICE 登记的就是这一行，切片搬走豁免即"腐烂"）；
 *   · fetchDevices() 里的 edgeDeviceStore.getCachedList(params)（DataPanel.spec 源码断言）；
 *   · 统计卡的 .stat-* 与移动端 @media 段（DataPanel.spec 的 §4.2.5/§4.3 源码断言）；
 *   · 路由参数解析（?device= / ?range=）与页面级取数入口 handleQuery/fetchData。
 */
import { computed, onMounted, reactive, ref } from 'vue'
import { feedback } from '@/utils/feedback'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { Download, Connection, DataAnalysis, DocumentChecked, Timer, Cpu, WarningFilled } from '@element-plus/icons-vue'
import PageHeader from '@/components/common/PageHeader.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import LineChart from '@/components/charts/LineChart.vue'
import { useResponsive } from '@/composables/useResponsive'
import { edgeDeviceApi, type EdgeDevice } from '@/api/edgeDevice'
import { useEdgeDeviceStore } from '@/stores/edgeDevice'
import client from '@/api/client'
import { aggregateHistoryRows, looksDownsampled, type CollectedRow } from './historyRows'
import { logger } from '@/utils/logger'
import { sensorNameMap, sensorUnitMap } from '@/utils/sensor'
import { parseRowData } from './rowData'
import { formatTime } from './rowDisplay'
import { unwrapList } from './envelope'
import type { MeasurementCategory } from './types'
import HistoryDataTable from './components/HistoryDataTable.vue'
import HistoryRowCards from './components/HistoryRowCards.vue'
import RowDetailDialog from './components/RowDetailDialog.vue'
import { useDataPanelStats } from './composables/useDataPanelStats'
import { useDataPanelChart } from './composables/useDataPanelChart'
import { useDataPanelCompare } from './composables/useDataPanelCompare'
import { useDataPanelRealtime } from './composables/useDataPanelRealtime'

const router = useRouter()
const route = useRoute()

/**
 * 窄屏（<768px）分支：历史数据表 → 「摘要 + 查看完整数据」卡片；趋势图 → 单指标 + 紧凑高度。
 * 断点与 theme.css 一致（useResponsive 的 isMobile = width < 768）。
 *
 * 这里只调一次并向下传：useResponsive 每次调用都会注册一对 window resize 监听，
 * 子组件/composable 再各自调一次就是重复监听。
 */
const { isMobile } = useResponsive()

// ── 移动端「查看完整数据」详情弹层 ──
const rowDetailVisible = ref(false)
const rowDetailRow = ref<Record<string, any> | null>(null)

/** A5：`?range=` 的合法取值，与模板里四个 el-option（1h/24h/7d/30d）逐字一致 —— 改模板须同步改这里。 */
const TIME_RANGE_VALUES = ['1h', '24h', '7d', '30d'] as const

const deviceList = ref<EdgeDevice[]>([])
/**
 * 当前页的历史行（一次采集一行）。
 *
 * ⚠ 这**不是**接口原始行：/devices/:id/history 返回一字段一行，必须先经
 * aggregateHistoryRows() 转置成"一次采集一行"，否则表格一条采集会炸成 N 行。
 */
const historyData = ref<CollectedRow[]>([])
/** 当前查询窗口内的**全部**采集行（分页从它切片，historyData 是它的一页）。 */
const allHistoryRows = ref<CollectedRow[]>([])
const loading = ref(false)
const currentPage = ref(1)
const pageSize = ref(20)
const total = ref(0)
/**
 * 本表请求服务端降采样的上限（**行**，不是采集数）。
 *
 * 为什么是 2000 而不是趋势图的 500：
 *   · 趋势图要的是"形状"，500 个点足够画线；
 *   · 表格要的是"明细"，且 /devices/:id/history 一行 = **一个字段**。
 *     一台 29 字段设备，2000 行只够约 69 次采集（2000/29）—— 已经是表格
 *     一页（20 条）的 3.4 倍。再小就会让每次采集只剩几个字段，表格看起来像
 *     "设备只上报了一半指标"。
 *
 * 为什么必须传它：服务端没有分页，不传就是把整个窗口（29 字段 × 24h ≈ 246
 * 万行）一次拉回浏览器。传了才在服务端抽样，响应体可控。
 */
const HISTORY_MAX_POINTS = 2000
/**
 * 最近一次响应是否被服务端降采样过。
 *
 * 降采样是**按行**（一字段一行）抽的 ⇒ 抽得越狠，每条采集剩下的字段越少。
 * 界面必须明说，否则"只剩 1 个指标"会被误认为设备只上报了一个字段。
 */
const historyDownsampled = ref(false)

const availableCategories = ref<MeasurementCategory[]>([])

/**
 * 统计卡范围词（规范 §4.3 统计卡 MUST）。
 *
 * 为什么是常量而不是模板里的字面量：同一卡片组里存在**两种范围**，
 * 验收时要能机械地数出"每个统计值都带了范围词"，字面量散落则无法审计。
 *   · SCOPE_PAGE     —— 值只由当前页 historyData（pageSize 条）算出
 *   · SCOPE_FILTERED —— 值由服务端按「设备 + 时间范围」筛出的全量得出
 *
 * 刻意不搬进 useDataPanelStats：范围词是**渲染契约**（DataPanel.spec 的 DOM 断言
 * 与 §4.3 门禁都按文本判定），藏在 composable 里就不可文本审计了。
 */
const SCOPE_PAGE = '本页'
const SCOPE_FILTERED = '当前筛选'

const queryForm = reactive({
  deviceId: null as number | null,
  timeRange: '24h',
  compareCategory: 'temperature' as string
})

const canExport = computed(() => !!queryForm.deviceId && historyData.value.length > 0)

const edgeDeviceStore = useEdgeDeviceStore()

// ── 四块状态各自成套：统计卡 / 趋势图 / 多设备对比 / 实时流 ──
// 依赖方向单向：composable 只知道「设备 + 时间范围 + 整窗行 + 指标类别」，
// 不反向 import 页面（避免循环依赖，也便于单测）。
const { latestStats, dynamicStats, calculateStats } = useDataPanelStats(historyData, total, availableCategories)

const {
  chartSeries,
  chartLoading,
  chartFailed,
  mobileMetric,
  chartMetricOptions,
  visibleChartSeries,
  chartHeight,
  buildChartSeries,
  retryChart,
} = useDataPanelChart({
  deviceId: () => queryForm.deviceId,
  timeRange: () => queryForm.timeRange,
  categories: availableCategories,
  rows: allHistoryRows,
  isMobile,
})

const { compareMode, compareDevices, compareSeries, compareCategories } = useDataPanelCompare({
  deviceList,
  queryForm,
})

/**
 * 分页**改成本地**：/devices/:id/history 没有 page/page_size 参数。
 *
 * 这不是"偷懒不接服务端分页"——该端点根本没有分页能力（它按时间窗一次性
 * 解码返回，见 backend/internal/api/handler_data.go 的 /devices/:id/history）。
 * 要在前端保持"翻页可用"，只能整窗取回后切片。
 *
 * 代价（已权衡）：一次取 max_points=2000 行，约 10~20 次采集的量级；
 * 对表格一页 20 条而言足够翻好几页。若不做降采样上限，24h × 29 字段 ≈ 246 万行
 * 会把浏览器拖死 —— 所以 max_points 是这条路的**必要**参数，不是优化。
 *
 * ⚠ 这个 .slice(start, start + pageSize) **必须留在页面文件**：listPageConventions
 * 门禁的 EXEMPT_LOCAL_SLICE 按文件登记"已知误报"，切片搬进 composable 后页面
 * 就不再有对应 finding，豁免表会被判定为"腐烂"（登记了却没有事可免）。
 */
function applyPage() {
  const start = (currentPage.value - 1) * pageSize.value
  historyData.value = allHistoryRows.value.slice(start, start + pageSize.value)
  calculateStats()
}

/**
 * 翻页 / 改每页条数的入口 —— **只重排当前页，不重新请求**。
 *
 * 改造前这两个事件直连 fetchData()：那时靠服务端分页，翻页必须发请求。
 * 现在数据已经整窗在手（前端切片），再发请求是纯浪费 —— 而 /devices/:id/history
 * 每次都要解码整个时间窗，代价远高于一次数组切片。
 */
function handlePageChange() {
  // size 变化后当前页可能越界（第 7 页 + 每页 100 条 ⇒ 只剩 2 页）：夹回有效范围，
  // 否则会显示空表却没有任何解释。
  const maxPage = Math.max(1, Math.ceil(allHistoryRows.value.length / pageSize.value))
  if (currentPage.value > maxPage) currentPage.value = maxPage
  applyPage()
}

const { realtimeEnabled, realtimeCount, toggleRealtime } = useDataPanelRealtime({
  deviceId: () => queryForm.deviceId,
  rows: allHistoryRows,
  total,
  applyPage,
  calculateStats,
  chartSeries,
})

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

/**
 * 页面级取数入口（**留在页面**，不搬进 composable）。
 *
 * 它把四件事串成一次事务：整窗取回 → 本地切片（total / applyPage）→ 统计重算 →
 * 趋势重建。四步共享同一个 loading 与同一个「413 走缩小时间范围」分支；拆进
 * composable 就要把 total / applyPage / calculateStats / buildChartSeries 四个
 * 回调再传回去，边界反而更模糊。时间窗 switch 与 buildChartSeries 里的那份
 * 逐字相同，是既有实现（两处都必须按"查询时刻"取 now），本次搬移不合并。
 */
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

    // ⚠ 换源（2026-11-11 原始数据退役）：
    //   旧  GET /edge-devices/:id/data        —— device_data 分页，一行一次采集
    //   新  GET /devices/:id/history          —— 帧表解码，**一字段一行**、无分页
    //
    // 两个差异都在这一层抹平，下游（表格/统计卡/CSV）形状不变：
    //   ① 一字段一行 → aggregateHistoryRows() 按 timestamp 聚合成一次采集一行；
    //   ② 无分页 → 整窗取回后在前端切片（allHistoryRows + 分页器改成本地翻页）。
    const rows = await edgeDeviceApi.getDeviceHistory(queryForm.deviceId, {
      start_time: startTime.toISOString(),
      end_time: endTime.toISOString(),
      max_points: HISTORY_MAX_POINTS,
    })

    allHistoryRows.value = aggregateHistoryRows(rows)
    total.value = allHistoryRows.value.length
    historyDownsampled.value = looksDownsampled(rows.length, HISTORY_MAX_POINTS)
    applyPage()

    await loadDeviceCategories()
    calculateStats()

    // 构建图表数据：从 unified_data API 获取解析后的数值数据
    buildChartSeries()
  } catch (error) {
    // 413 是**服务端主动拒绝**（帧数 × 字段数超 300 万行的内存护栏），
    // 与"网络失败/接口 500"是两回事：它不该被说成"获取历史数据失败"，
    // 因为用户缩小时间范围就能成功 —— 文案必须给出这个可执行动作。
    if (isPayloadTooLarge(error)) {
      feedback.error('时间范围内的数据量过大，服务端已拒绝：请缩小时间范围后重试')
      allHistoryRows.value = []
      historyData.value = []
      total.value = 0
      historyDownsampled.value = false
      return
    }
    feedback.error('获取历史数据失败')
  } finally {
    loading.value = false
  }
}

/** 是否 413（后端内存护栏拒绝）。axios 把它包成 ApiError，status 挂在 error.status。 */
function isPayloadTooLarge(error: unknown): boolean {
  const status = (error as { status?: unknown; response?: { status?: unknown } } | null)?.status
  if (status === 413) return true
  return (error as { response?: { status?: unknown } } | null)?.response?.status === 413
}

// 导出 CSV
const handleExport = () => {
  if (!queryForm.deviceId) return

  const device = deviceList.value.find(d => d.id === queryForm.deviceId)
  const deviceName = device?.name || queryForm.deviceId

  // 2026-11-11 原始数据退役：CSV 同步删掉「原始数据」列（raw_hex 无数据源）
  // 与「错误码」列（后端从未返回 error_code，该列一直是常量 0）。
  //
  // 导出范围仍是**当前页**（与表格同源）。这不是本次引入的限制：改造前
  // historyData 就只装接口当前页的 20 行（见 docs/分析/功能与UIUX优化方案独立复核
  // -2026-09-23.md 的 R3-4），只是现在"当前页"由前端切片算出。
  const rows = [['时间', '数据']]
  for (const item of historyData.value) {
    const { values } = parseRowData(item)
    rows.push([
      formatTime(item.timestamp || item.created_at || item.collected_at || ''),
      values ? JSON.stringify(values) : '',
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

function openRowDetail(row: Record<string, any>) {
  rowDetailRow.value = row
  rowDetailVisible.value = true
}

onMounted(() => {
  void (async () => {
    // A5：先等设备列表就绪再消费 `?device=` —— 否则预填的 id 在 deviceList 里找不到，
    // el-select 会因选项缺失而显示空白（看起来像"深链没生效"）。
    await fetchDevices()
    applyQueryPrefill()
  })()
})

/**
 * A5：消费 `?device=<device_pk>&range=<24h|7d|30d>` 深链。
 *
 * 改前 /data 的 route.query 零消费：从设备详情或图表"查看历史"跳进来，
 * 用户还得手动选设备 + 选时间范围（4 步），URL 上的参数被完全忽略。
 * 非法值一律忽略（不报错、不改成别的设备），保持"无 query 时行为与改前一致"。
 */
function applyQueryPrefill() {
  // 深链只是"锦上添花"：route 缺失/异常时绝不能让整页取数挂掉（onMounted 里是异步调用，
  // 抛出去会变成未处理拒绝——测试全绿但 vitest 退出码 1 的那种隐蔽故障）。
  const query = route?.query
  if (!query) return
  const rawDevice = query.device
  const rawRange = query.range
  let applied = false

  if (typeof rawDevice === 'string' && rawDevice.trim() !== '') {
    const parsed = Number(rawDevice)
    // 只接受**在设备列表里真实存在**的 id：否则 el-select 显示空值，
    // 用户会以为页面坏了。列表未含该 id 时安静忽略。
    if (Number.isFinite(parsed) && deviceList.value.some(d => d.id === parsed)) {
      queryForm.deviceId = parsed
      applied = true
    }
  }
  if (typeof rawRange === 'string' && (TIME_RANGE_VALUES as readonly string[]).includes(rawRange)) {
    queryForm.timeRange = rawRange
    applied = true
  }
  // 有 device 才自动查（复用 handleQuery，一次请求，不另写取数路径）。
  if (applied && queryForm.deviceId) handleQuery()
}

// 订阅解绑与防抖清理在 useDataPanelRealtime 内部的 onUnmounted 完成（同一生命周期）。
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

.chart-area {
  min-height: 350px;
}

/* 移动端趋势图：单指标选择器 + 说明（P1：4 轴 + 20 图例在 303px 画布读不出） */
.mobile-chart-head {
  margin-bottom: 8px;
}

.mobile-chart-hint {
  margin: 6px 0 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

/* 移动端历史数据卡片：摘要 3~4 个关键指标 + 「查看完整数据」（P0：80px 数据列不可用） */
.data-card-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.data-card-pagination {
  display: flex;
  justify-content: center;
  margin-bottom: 4px;
}
</style>
