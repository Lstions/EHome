<template>
  <div class="dashboard">
    <PageHeader title="仪表盘" />

    <!-- 概览接口失败：常驻错误态 + 重试入口（范式同 views/data-source/DataSourceList.vue） -->
    <el-alert
      v-if="overviewError"
      type="error"
      :closable="false"
      show-icon
      class="dashboard-error-alert"
      data-test="dashboard-error"
    >
      <template #title>
        <span class="dashboard-error-text">获取概览数据失败：{{ overviewError }}</span>
        <el-button link type="primary" size="small" data-test="dashboard-retry" @click="retryLoad">重试</el-button>
      </template>
    </el-alert>

    <template v-if="loading">
      <!-- 骨架态与内容态共用同一外层容器与同一结构类（.dashboard-stats / .stat-card /
           .stat-content / .stat-icon / .stat-info / .stat-value / .stat-label）。
           高度因此由构造决定地相等：同一 el-card 边框+padding，同一行盒高度
           （.stat-value/.stat-label 显式 line-height，骨架条显式同值 height），
           而不是靠"90 ↔ 102"这类魔法数字互相追赶。
           aria-hidden="true" 是必须的：骨架是装饰，不应被读屏播报
           （内容态那 4 张卡的 role/tabindex/aria-label 不受影响）。 -->
      <div class="dashboard-stats" data-test="dashboard-stats-skeleton">
        <el-card v-for="i in 4" :key="i" shadow="hover" class="stat-card" aria-hidden="true">
          <div class="stat-content">
            <div class="stat-icon skeleton-bar"></div>
            <div class="stat-info">
              <p class="stat-value skeleton-bar skeleton-value"></p>
              <p class="stat-label skeleton-bar skeleton-label"></p>
            </div>
          </div>
        </el-card>
      </div>
    </template>
    <template v-else>
      <div class="dashboard-stats" data-test="dashboard-stats-loaded">
        <el-card shadow="hover" class="stat-card" role="link" tabindex="0" aria-label="查看节点总数" @click="router.push('/node')" @keydown.enter.prevent="router.push('/node')" @keydown.space.prevent="router.push('/node')">
          <div class="stat-content">
            <div class="stat-icon" style="color: var(--el-color-primary);">
              <el-icon :size="32"><Connection /></el-icon>
            </div>
            <div class="stat-info">
              <p class="stat-value">{{ metric(overview.nodes?.total) }}</p>
              <p class="stat-label">节点总数</p>
            </div>
          </div>
        </el-card>

        <el-card shadow="hover" class="stat-card" role="link" tabindex="0" aria-label="查看在线节点" @click="router.push('/node?status=online')" @keydown.enter.prevent="router.push('/node?status=online')" @keydown.space.prevent="router.push('/node?status=online')">
          <div class="stat-content">
            <div class="stat-icon" style="color: var(--el-color-success);">
              <el-icon :size="32"><CircleCheck /></el-icon>
            </div>
            <div class="stat-info">
              <p class="stat-value">{{ metric(overview.nodes?.online) }}</p>
              <p class="stat-label">在线节点</p>
            </div>
          </div>
        </el-card>

        <el-card shadow="hover" class="stat-card" role="link" tabindex="0" aria-label="查看设备总数" @click="router.push('/edge-device')" @keydown.enter.prevent="router.push('/edge-device')" @keydown.space.prevent="router.push('/edge-device')">
          <div class="stat-content">
            <div class="stat-icon" style="color: var(--el-color-warning);">
              <el-icon :size="32"><Cpu /></el-icon>
            </div>
            <div class="stat-info">
              <p class="stat-value">{{ metric(overview.edge_devices?.total) }}</p>
              <p class="stat-label">设备总数</p>
            </div>
          </div>
        </el-card>

        <el-card shadow="hover" class="stat-card" role="link" tabindex="0" aria-label="查看在线设备" @click="router.push('/edge-device?status=online')" @keydown.enter.prevent="router.push('/edge-device?status=online')" @keydown.space.prevent="router.push('/edge-device?status=online')">
          <div class="stat-content">
            <div class="stat-icon" style="color: var(--el-color-success);">
              <el-icon :size="32"><CircleCheck /></el-icon>
            </div>
            <div class="stat-info">
              <p class="stat-value">{{ metric(overview.edge_devices?.online) }}</p>
              <p class="stat-label">在线设备</p>
            </div>
          </div>
        </el-card>
      </div>
    </template>

    <!-- 告警 / 异常摘要 -->
    <el-row v-if="overview" :gutter="20" style="margin-top: 20px;">
      <el-col :span="24">
        <el-card shadow="hover" class="alert-summary">
          <template #header>
            <div style="display: flex; align-items: center; gap: 8px;">
              <el-icon :color="summaryIconColor" :size="20">
                <component :is="summaryIcon" />
              </el-icon>
              <span>异常摘要</span>
              <el-tag size="small" :type="summaryTagType" data-test="dashboard-summary-tag">{{ summaryTagText }}</el-tag>
            </div>
          </template>
          <el-skeleton v-if="loading" :rows="2" animated />
          <!-- 概览未知时不得断言运行正常：显式呈现未知态 -->
          <div v-else-if="overviewError" class="alert-error" data-test="dashboard-summary-error">
            <el-icon color="var(--el-color-danger)" :size="20"><WarningFilled /></el-icon>
            <span>概览数据获取失败，节点与设备状态未知。</span>
          </div>
          <div v-else-if="hasAlerts" class="alert-list">
            <div v-if="offlineCollectors > 0" class="alert-item" @click="router.push('/node?status=offline')">
              <el-icon color="var(--el-color-danger)" :size="28"><Connection /></el-icon>
              <div>
                <div class="alert-value">{{ offlineCollectors }}</div>
                <div class="alert-label">离线节点</div>
              </div>
            </div>
            <div v-if="offlineDevices > 0" class="alert-item" @click="router.push('/edge-device?status=offline')">
              <el-icon color="var(--el-color-danger)" :size="28"><Cpu /></el-icon>
              <div>
                <div class="alert-value">{{ offlineDevices }}</div>
                <div class="alert-label">离线设备</div>
              </div>
            </div>
            <div v-if="dataErrorCount > 0" class="alert-item" @click="router.push('/data')">
              <el-icon color="var(--el-color-warning)" :size="28"><WarningFilled /></el-icon>
              <div>
                <div class="alert-value">{{ dataErrorCount }}</div>
                <div class="alert-label">采集错误（近 1h）</div>
              </div>
            </div>
          </div>
          <div v-else class="alert-ok">
            <el-icon color="var(--el-color-success)" :size="20"><CircleCheck /></el-icon>
            <span>节点与设备均在线，暂无采集错误。</span>
          </div>
        </el-card>
      </el-col>
    </el-row>

    <!-- 24h 趋势图表 -->
    <el-row :gutter="20" style="margin-top: 20px;">
      <el-col :span="24">
        <el-card shadow="hover">
          <template #header>
            <div class="trend-card-header">
              <!-- 类别已由右侧下拉显示，卡头只写时间档，避免同一屏出现两次「总电压」 -->
              <span>{{ trendRangeLabel }}趋势</span>
              <div class="trend-controls">
                <!-- 统一时间档控件（2026-10-21）：不再各页各写一套 el-radio-group，
                     否则同一控件在仪表盘/BMS/逆变器三处字号与高度都不同。仪表盘只有固定档，无「自定义」。 -->
                <TimeRangeSelector
                  v-model="trendRange"
                  :custom-range="null"
                  :show-custom="false"
                  @change="fetchTrendData"
                />
                <el-select v-model="trendCategory" size="small" class="trend-category-select" @change="selectedTrendByUser = true">
                  <el-option
                    v-for="cat in availableTrendCategories"
                    :key="cat.value"
                    :label="cat.label"
                    :value="cat.value"
                  />
                </el-select>
              </div>
            </div>
          </template>
          <el-skeleton v-if="trendLoading" :rows="4" animated />
          <div v-else style="min-height: 240px;">
            <!-- 2026-10-21：不再给 LineChart 传 canvas 内标题 ——
                 卡片头已写「最近 24 小时趋势」+ 类别下拉已显示当前类别，
                 图内再画一个 20px 大标题既重复又挤掉 60px 绘图区（真机截图实测）。 -->
            <LineChart
              v-if="trendSeries.length > 0"
              :series="trendSeries"
              height="300px"
            />
            <EmptyState
              v-else-if="overviewError"
              kind="error"
              icon="WarningFilled"
              size="small"
              data-test="dashboard-trend-error"
              title="趋势数据加载失败"
              description="概览接口不可用，无法确定设备列表，趋势图暂时无法加载。"
              :quick-actions="[
                { label: '重试', type: 'primary', handler: retryLoad }
              ]"
            />
            <EmptyState
              v-else
              icon="TrendCharts"
              size="small"
              title="暂无趋势数据"
              description="需要设备数据才能显示趋势图表"
              :quick-actions="[
                { label: '查看设备', icon: Cpu, type: 'primary', handler: () => router.push('/edge-device') }
              ]"
            />
          </div>
        </el-card>
      </el-col>
    </el-row>

    <!-- 真实节点状态变化 -->
    <!-- 2026-11-12 纯搬移：「节点状态变化」卡整块搬进 components/StatusHistoryCard.vue。
         它是本页唯一不被源码文本门禁点名的自包含模板片段 —— 统计卡（骨架几何门禁）、
         异常摘要（IconActionAccessibilityGate 登记的三处 @click）、趋势卡
         （TimeRangeSelectorUnified）、最新数据表（F8 横滚 + UiAudit 摘要门禁）
         都被钉在页面。外层 el-row/el-col 与 20px 上间距属页面级栅格，留在页面。 -->
    <el-row :gutter="20" style="margin-top: 20px;">
      <el-col :span="24">
        <StatusHistoryCard
          :events="statusHistory"
          :unknown="statusHistoryUnknown"
          :hint="statusHistoryHint"
          @retry="retryLoad"
        />
      </el-col>
    </el-row>

    <!-- 最新数据表格（同时显示原始设备数据和解析后数据） -->
    <el-row :gutter="20" style="margin-top: 20px;">
      <el-col :span="24">
        <el-card shadow="hover">
          <template #header>
            <div style="display: flex; justify-content: space-between; align-items: center;">
              <span>最新数据</span>
              <el-button size="small" @click="handleRefresh" :loading="refreshing">
                <el-icon><Refresh /></el-icon>
                刷新
              </el-button>
            </div>
          </template>
          <el-skeleton v-if="loading" :rows="5" animated />
          <!-- 移动端宽表：横向滚动 + 滑动提示（theme.css .mobile-table-wrapper）。
               本表 5 列合计 880px（设备名称 150 / 所属节点 150 / 解析数据 280 /
               原始数据 200），390px 视口下表格盒约 300px；「解析数据」「原始数据」
               两列几乎完全落在盒外且无横滚容器（el-table overflow:hidden）。 -->
          <!--
            2026-10-21 全站 UI 审计修复（P1）：
              393 下这张 6 列表格自然宽 960px，视口内只有 303px；「解析数据」列把整行 JSON 直接塞进单元格，
              单行高 500~776px（数据多时更高），可见的前两列下方是 400~700px 空白 —— 等于手机上读不到任何一条数据。
              修法：窄屏改**卡片列表**（设备名/节点/采集时间/状态 + 解析数据摘要 + 「查看完整数据」弹层），
              桌面保持原表格逐像素不变。摘要只截断展示、完整内容进弹层 —— 不拿信息换整洁。
          -->
          <div v-if="isMobile && (overview.latest_data || []).length > 0" class="latest-cards" data-test="dashboard-latest-cards">
            <div v-for="row in overview.latest_data" :key="row.device_id" class="latest-card">
              <div class="lc-head">
                <router-link :to="`/edge-device/${row.device_id}`" class="device-link">{{ row.device_name || `设备 ${row.device_id}` }}</router-link>
                <el-tag size="small" type="success">在线</el-tag>
              </div>
              <div class="lc-meta">
                <span>所属节点：{{ displayNodeName(row) }}</span>
                <span>{{ formatTime(row.collected_at) }}</span>
              </div>
              <p class="lc-data">{{ formatDataSummary(row.data) }}</p>
              <el-button size="small" text type="primary" @click="openDataDetail(row)">查看完整数据</el-button>
            </div>
          </div>
          <div v-else-if="(overview.latest_data || []).length > 0" class="mobile-table-wrapper">
            <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
          <el-table :data="overview.latest_data" stripe>
            <el-table-column prop="device_name" label="设备名称" width="150">
              <template #default="{ row }">
                <router-link :to="`/edge-device/${row.device_id}`" class="device-link">
                  {{ row.device_name }}
                </router-link>
              </template>
            </el-table-column>
            <el-table-column prop="node_name" label="所属节点" width="150">
              <template #default="{ row }">
                <span>{{ displayNodeName(row) }}</span>
              </template>
            </el-table-column>
            <!-- 解析后数据 -->
            <el-table-column label="解析数据" width="280">
              <template #default="{ row }">
                <span>{{ formatData(row.data) }}</span>
              </template>
            </el-table-column>
            <!-- 原始数据 -->
            <el-table-column label="原始数据" width="200">
              <template #default="{ row }">
                <span class="raw-data">{{ formatRawData(row.raw_data) }}</span>
              </template>
            </el-table-column>
            <el-table-column prop="collected_at" label="采集时间">
              <template #default="{ row }">
                <span>{{ formatTime(row.collected_at) }}</span>
              </template>
            </el-table-column>
            <el-table-column label="状态" width="100">
              <template #default>
                <el-tag size="small" type="success">在线</el-tag>
              </template>
            </el-table-column>
          </el-table>
          </div>
          <EmptyState
            v-else-if="overviewError"
            kind="error"
            icon="WarningFilled"
            data-test="dashboard-latest-error"
            title="最新数据加载失败"
            description="概览接口返回失败，当前没有可信的数据状态。"
            :quick-actions="[
              { label: '重试', type: 'primary', handler: retryLoad }
            ]"
          />
          <EmptyState
            v-else
            icon="FolderOpened"
            title="暂无数据"
            description="添加节点和设备后，数据将在此处显示"
            :quick-actions="[
              { label: '查看节点', icon: Connection, type: 'primary', handler: () => router.push('/node') },
              { label: '查看设备', icon: Cpu, handler: () => router.push('/edge-device') }
            ]"
          />
        </el-card>
      </el-col>
    </el-row>

    <!-- 完整数据弹层（窄屏卡片摘要的补充）：完整 K/V 逐条列出，不截断 -->
    <el-dialog v-model="dataDetailVisible" :title="`${dataDetailRow?.device_name || '设备'} · 完整数据`" width="min(560px, 92vw)" append-to-body>
      <div class="data-detail-list">
        <div v-for="([k, v], i) in Object.entries(dataDetailRow?.data || {})" :key="i" class="data-detail-row">
          <span class="dd-key">{{ sensorNameMap[k] || k }}</span>
          <span class="dd-val">{{ sensorUnitMap[k] ? `${v} ${sensorUnitMap[k]}` : v }}</span>
        </div>
        <p v-if="Object.keys(dataDetailRow?.data || {}).length === 0" class="dd-empty">该设备本次未上报解析数据</p>
      </div>
      <template #footer>
        <el-button @click="dataDetailVisible = false">关闭</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { defineAsyncComponent, ref, onMounted, onUnmounted, computed, watch } from 'vue'
import { feedback } from '@/utils/feedback'
import { UNKNOWN } from '@/utils/format'
import { useRouter } from 'vue-router'
// CloseBold 随「异常摘要图标三态」搬进 composables/useDashboardOverview.ts；
// 模板仍直接使用 Cpu / CircleCheck / Refresh / Connection / WarningFilled。
import { Cpu, CircleCheck, Refresh, Connection, WarningFilled } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import PageHeader from '@/components/common/PageHeader.vue'
// 骨架态改为复用 .stat-card 自带结构（不再使用 SkeletonCard 的 stat 变体），
// 但 SkeletonCard.vue 的 variant="stat" 仍被 NodeList.vue / EdgeDeviceList.vue 使用，保留。
import EmptyState from '@/components/common/EmptyState.vue'
// 异步拆分：LineChart（echarts 核心）渲染耗时且仅趋势区使用，
// 独立 chunk 延迟加载，避免阻塞仪表盘首屏。
const LineChart = defineAsyncComponent(() => import('@/components/charts/LineChart.vue'))
import client, { type ApiEnvelope } from '@/api/client'
import { logger } from '@/utils/logger'
import { sensorNameMap, sensorUnitMap, SENSOR_ORDER } from '@/utils/sensor'
import { useResponsive } from '@/composables/useResponsive'
import TimeRangeSelector from '@/components/charts/TimeRangeSelector.vue'
// 2026-11-12 纯搬移式拆分（Dashboard.vue 1205 → 1045 行；三个新文件 76 / 227 / 88 行）：
//   · composables/useDashboardOverview —— 概览三态 / 状态历史 / WS 防抖刷新 / 告警摘要派生值；
//   · dashboardFormat —— 展示口径纯函数（最新数据宽表与窄屏卡片共用同一份）；
//   · components/StatusHistoryCard —— 唯一不受源码文本门禁约束的自包含模板片段。
// 不可切（门禁钉住，详见各段注释）：统计卡 + .stat-* 全部 CSS（DashboardSkeletonGeometry）、
// 异常摘要卡（IconActionAccessibilityGate 登记的三处 @click）、趋势卡含 TimeRangeSelector
// 与「趋势类别自愈」watch（TimeRangeSelectorUnified + UiAuditSystemicFixes）、
// 最新数据表 + 完整数据弹层（F8 横滚 + UiAudit 的 latest-cards/dataDetailVisible/
// SUMMARY_PRIORITY/查看完整数据）。
import { useDashboardOverview } from './composables/useDashboardOverview'
import { displayNodeName, formatData, formatRawData, formatTime } from './dashboardFormat'
import StatusHistoryCard from './components/StatusHistoryCard.vue'

const router = useRouter()
const { isMobile } = useResponsive()

/** 最新数据详情弹层（窄屏卡片「查看完整数据」用）：摘要截断展示，完整 K/V 进弹层，不丢数据。 */
const dataDetailVisible = ref(false)
const dataDetailRow = ref<{ device_name?: string; data?: Record<string, any>; raw_data?: any } | null>(null)
const openDataDetail = (row: any) => {
  dataDetailRow.value = row
  dataDetailVisible.value = true
}

// 刷新按钮的 loading：只服务「最新数据」卡头的刷新入口（页面级动作）。
// 概览自身的 loading / 三态由 composable 提供。
const refreshing = ref(false)

/**
 * 概览三态 + 状态历史 + WS 防抖刷新 + 告警摘要派生值整块搬入 composable。
 * 边界依据见该文件头：这些值描述的是**同一个** overview 的三态语义，
 * 拆开放会让「接口失败不得报运行正常」出现两个版本。
 * WS「数据更新」还要驱动趋势图 2s 防抖刷新 —— 趋势状态与「类别自愈」watch 被
 * UiAuditSystemicFixes 的源码文本门禁钉在页面，故用闭包回灌（调用时机不变）。
 */
const {
  overview,
  loading,
  overviewError,
  statusHistory,
  statusHistoryUnknown,
  offlineCollectors,
  offlineDevices,
  dataErrorCount,
  metricsReady,
  hasAlerts,
  summaryTagText,
  summaryTagType,
  summaryIcon,
  summaryIconColor,
  statusHistoryHint,
  fetchOverview,
  fetchStatusHistory,
  subscribeWebSocket,
  disposeOverview,
} = useDashboardOverview({
  onDataUpdate: () => scheduleTrendRefresh(),
})

// 趋势图相关
const trendLoading = ref(false)
const trendCategory = ref('temperature')
const trendSeries = ref<any[]>([])
// statusHistory / statusHistoryUnknown 随状态历史取数搬进 composable（页面只渲染）。
const trendRange = ref<'1h' | '24h' | '7d'>('24h')
const trendRangeLabel = computed(() => {
  const map: Record<string, string> = { '1h': '最近 1 小时', '24h': '最近 24 小时', '7d': '最近 7 天' }
  return map[trendRange.value] || ''
})


// 传感器映射已提取到 utils/sensor.ts (S6 fix)

// 缓存已见过的传感器类型集合，只增不减，避免 WS 数据更新导致选项忽多忽少
const seenCategoryKeys = ref<Set<string>>(new Set())

// 监听 latest_data 变化，累积已见过的传感器类型
watch(() => overview.value.latest_data, (newData) => {
  for (const item of (newData || [])) {
    if (item.data && typeof item.data === 'object') {
      for (const key of Object.keys(item.data)) {
        const val = item.data[key]
        if (typeof val === 'number' || (typeof val === 'object' && val !== null && 'value' in val)) {
          seenCategoryKeys.value.add(key)
        }
      }
    }
  }
}, { deep: true, immediate: true })

// 从缓存集合生成可用传感器类型列表；始终包含当前选中项，保证下拉显示中文标签而非字段 key
const availableTrendCategories = computed(() => {
  const knownOrder = SENSOR_ORDER
  const keys = [...seenCategoryKeys.value]
  if (trendCategory.value && !keys.includes(trendCategory.value)) {
    keys.push(trendCategory.value)
  }
  return keys.sort((a, b) => {
    const ia = knownOrder.indexOf(a)
    const ib = knownOrder.indexOf(b)
    if (ia !== -1 && ib !== -1) return ia - ib
    if (ia !== -1) return -1
    if (ib !== -1) return 1
    return a.localeCompare(b)
  }).map(key => ({
    value: key,
    label: sensorNameMap[key] || key
  }))
})

/*
 * 趋势默认类别自愈（2026-10-20 生产实测）：
 *   `trendCategory` 初值是硬编码的 `'temperature'`，但**没有任何设备上报这个键** ——
 *   BMS 用 `temperature_1/2/3`，逆变器用 `pv_temp` / `inverter_temp`。
 *   于是仪表盘「最近 24 小时趋势」永久显示「暂无趋势数据 / 需要设备数据才能显示趋势图表」，
 *   而库里 24h 数据有几千点 —— 看起来像采集坏了。
 *   修法：真实键集合变化后，若当前类别不在其中，自动切到第一个真实类别并重取。
 *   用户手动选过（selectedTrendByUser）就不再自动改，避免和用户操作打架。
 *   注意：回调里调用的 fetchTrendData 定义在下方，故不能用 immediate（会 TDZ）。
 */
const selectedTrendByUser = ref(false)
watch(availableTrendCategories, (list) => {
  if (selectedTrendByUser.value || list.length === 0) return
  const realKeys = seenCategoryKeys.value
  if (realKeys.has(trendCategory.value)) return
  // 优先连续物理量：真实数据里第一个有值的键常常是 alarm_*（0/1 状态位），
  // 画出来是一条贴底的直线，看起来像「没数据」。按业务优先级挑一个连续量作默认。
  const preferred = [
    'total_voltage', 'battery_voltage', 'soc', 'rsoc', 'current',
    'output_active_power', 'grid_voltage', 'pv_power', 'temperature_1', 'temperature',
  ].find((k) => realKeys.has(k))
  const first = preferred
    ? { value: preferred }
    : list.find((c) => realKeys.has(c.value))
  if (first) {
    trendCategory.value = first.value
    void fetchTrendData()
  }
})

// 告警摘要的计算属性（offlineCollectors / offlineDevices / dataErrorCount /
// metricsReady / hasAlerts）与摘要标签/图标、状态条数提示，全部在
// composables/useDashboardOverview.ts：它们与 overview 的三态判定同属一套语义，
// 留在页面就会出现两份「未知 vs 0」的口径。

/** 未知态统一显示 '—'：0 是"确实是 0"，'—' 是"接口没说"。 */
function metric(value: number | undefined): string | number {
  return metricsReady.value ? (value ?? '—') : '—'
}

// 趋势图 30s 自动刷新定时器；WS 退订句柄与概览 1s 防抖定时器随 composable 搬走。
let trendRefreshTimer: ReturnType<typeof setInterval> | null = null

// 获取趋势数据
const fetchTrendData = async () => {
  trendLoading.value = true
  try {
    const endTime = new Date()
    const rangeMs: Record<string, number> = {
      '1h': 60 * 60 * 1000,
      '24h': 24 * 60 * 60 * 1000,
      '7d': 7 * 24 * 60 * 60 * 1000,
    }
    const startTime = new Date(endTime.getTime() - (rangeMs[trendRange.value] || rangeMs['24h']))

    const unitMap = sensorUnitMap

    // 从概览数据中获取设备列表，逐个查询
    const deviceIds = overview.value.latest_data?.map(d => d.device_id).filter(Boolean) || []
    if (deviceIds.length === 0) {
      logger.warn('没有可用的设备数据，无法加载趋势图')
      trendSeries.value = []
      // 概览失败时这里没有可信的设备列表，保持错误态交由模板呈现，不额外写错误文案。
      return
    }

    const series: any[] = []
    const promises = deviceIds.slice(0, 5).map(deviceId =>
      client.get<unknown, ApiEnvelope<any[]>>('/api/v1/unified-data/historical', {
        params: {
          device_pk: deviceId,
          category: trendCategory.value,
          start_time: startTime.toISOString(),
          end_time: endTime.toISOString(),
          max_points: 500
        }
      }).then(res => ({
        deviceId,
        deviceName: overview.value.latest_data?.find(d => d.device_id === deviceId)?.device_name || `设备${deviceId}`,
        data: res.data ?? []
      }))
    )

    const results = await Promise.all(promises)
    for (const r of results) {
      if (r.data && r.data.length > 0) {
        series.push({
          name: r.deviceName,
          unit: unitMap[trendCategory.value] || '',
          data: r.data
            .filter((item: any) => {
              const t = item.timestamp || item.created_at
              return t && !t.startsWith('0001-01-01')
            })
            .map((item: any) => ({
              time: item.timestamp || item.created_at,
              value: item.value
            }))
            .sort((a: any, b: any) => a.time.localeCompare(b.time))
        })
      }
    }

    trendSeries.value = series
  } catch (error) {
    logger.error('获取趋势数据失败', { error: String(error) })
    trendSeries.value = []
  } finally {
    trendLoading.value = false
  }
}


// 状态历史取数 / 概览取数 / WS 1s 防抖刷新都随 useDashboardOverview 搬走
// （它们与 overview 三态共用同一个定时器与同一组退订句柄，拆分即破时序）。

// 趋势图防抖刷新 — WS数据到达时触发，2s防抖避免频繁刷新
let trendDirty = false
let trendDebounceTimer: ReturnType<typeof setTimeout> | null = null
const scheduleTrendRefresh = () => {
  trendDirty = true
  if (trendDebounceTimer) clearTimeout(trendDebounceTimer)
  trendDebounceTimer = setTimeout(() => {
    if (trendDirty && !trendLoading.value) {
      trendDirty = false
      fetchTrendData()
    }
  }, 2000)
}

/**
 * 错误态的重试入口：清空错误后重新拉取概览与其派生数据。
 * 留在页面（而不是 composable）的理由：这是一次动作同时驱动**三个**数据源，
 * 顺序就是契约 —— 趋势查询的设备列表来自 overview.latest_data，必须等概览先回来。
 */
const retryLoad = async () => {
  overviewError.value = ''
  statusHistoryUnknown.value = false
  await fetchOverview()
  await Promise.all([fetchTrendData(), fetchStatusHistory()])
}

const handleRefresh = async () => {
  refreshing.value = true
  try {
    // Trend queries derive their device list from overview.latest_data, so refresh
    // the overview first to avoid issuing a trend request with stale devices.
    await fetchOverview()
    await Promise.all([fetchTrendData(), fetchStatusHistory()])
    ElMessage.success('数据已刷新')
  } catch (error) {
    feedback.handleError(error, '刷新失败')
  } finally {
    refreshing.value = false
  }
}

/**
 * 窄屏摘要：只展示前 4 个指标（如 total_voltage: 52.34 · rsoc: 68 …），
 * 完整内容通过「查看完整数据」弹层给出 —— 摘要不写省略号截断配置值本身。
 */
/** 摘要优先展示「人真正关心」的指标（字母序前 4 个往往是 cell_voltage_1/10/11，全是 3.27V）。 */
const SUMMARY_PRIORITY = [
  'total_voltage', 'current', 'rsoc', 'soc', 'remaining_capacity',
  'output_active_power', 'grid_voltage', 'battery_voltage', 'battery_capacity',
  'max_temp', 'temperature', 'humidity',
]
const formatDataSummary = (data: Record<string, any>) => {
  if (!data || typeof data !== 'object') return UNKNOWN
  const keys = Object.keys(data)
  const ordered = [
    ...SUMMARY_PRIORITY.filter((k) => keys.includes(k)),
    ...keys.filter((k) => !SUMMARY_PRIORITY.includes(k)),
  ].slice(0, 4)
  if (ordered.length === 0) return UNKNOWN
  return ordered
    .map((k) => {
      const v = data[k]
      const val = typeof v === 'number' ? Number(v.toFixed(2)) : v
      return `${sensorNameMap[k] || k} ${val}${sensorUnitMap[k] ? ` ${sensorUnitMap[k]}` : ''}`
    })
    .join(' · ')
}

// formatData / formatRawData / formatTime / displayNodeName 已搬到 dashboardFormat.ts
// （最新数据宽表与窄屏卡片共用同一份展示口径）。SUMMARY_PRIORITY + formatDataSummary
// 仍留在页面：UiAuditSystemicFixes 的源码文本门禁要求 Dashboard.vue 里必须出现
// SUMMARY_PRIORITY，它锚定的正是「窄屏摘要不截断配置值」这条审计结论。

watch(trendCategory, () => {
  fetchTrendData()
})

// M11 fix: Extract category keys as a flat string for stable watch comparison.
// The original availableTrendCategories computed returns new object references on every
// evaluation, causing the watch to fire on every overview update even when values are identical.
const availableCategoryKeys = computed(() => {
  return availableTrendCategories.value.map(c => c.value).join(',')
})

watch(availableCategoryKeys, () => {
  const cats = availableTrendCategories.value
  if (cats.length > 0 && !cats.some(c => c.value === trendCategory.value)) {
    trendCategory.value = cats[0].value
  } else if (cats.length === 0) {
    trendSeries.value = []
  }
})

onMounted(async () => {
  await fetchOverview()
  await Promise.all([fetchTrendData(), fetchStatusHistory()])

  // WS 订阅与退订随 useDashboardOverview 搬走，订阅时机不变：
  // 先取首屏数据、再订阅，避免首帧被 WS 触发的静默刷新打断。
  subscribeWebSocket()

  // 30秒自动刷新趋势图
  trendRefreshTimer = setInterval(() => {
    if (trendSeries.value.length > 0 && !trendLoading.value) {
      fetchTrendData()
    }
  }, 30000)
})

onUnmounted(() => {
  // 概览 1s 防抖定时器与两条 WS 退订由 composable 自己收尾（与它们同生共死）；
  // 页面只收尾自己的两个趋势定时器。
  disposeOverview()
  if (trendDebounceTimer) {
    clearTimeout(trendDebounceTimer)
    trendDebounceTimer = null
  }
  if (trendRefreshTimer) {
    clearInterval(trendRefreshTimer)
    trendRefreshTimer = null
  }
})
</script>

<style scoped>
.dashboard {
  padding: 0;
}

.dashboard-stats {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 20px;
}

.stat-card {
  cursor: pointer;
  transition: transform 0.3s, box-shadow 0.3s;
}

.stat-card:hover {
  transform: translateY(-4px);
  box-shadow: var(--shadow-lg);
}
.stat-card:focus-visible {
  outline: 3px solid var(--el-color-primary);
  outline-offset: 2px;
}

.stat-content {
  display: flex;
  align-items: center;
  gap: 16px;
}

.stat-icon {
  width: 60px;
  height: 60px;
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  /* ⚠️ 死声明：模板中每个 .stat-icon 都带内联 `style="color: var(--el-color-*)"`，
     内联优先级高于本规则，故这里的 #fff 不会生效（且本块无 background）。
     保留仅为记录；勿据此推断图标是白字。 */
  color: #fff;
}

.stat-info {
  flex: 1;
}

.stat-label {
  margin: 8px 0 0;
  font-size: 14px;
  /* 显式行盒高度（≈原 normal 的 14×1.2≈16.8，视觉变化 <1px），
     使内容态与骨架态的高度由构造决定地相等，而不是靠 90/102 之类的魔法数字。 */
  line-height: 17px;
  color: var(--el-text-color-secondary);
  /* 中文标签防止逐字断行竖排 */
  word-break: keep-all;
  overflow-wrap: break-word;
}

.stat-value {
  margin: 0;
  font-size: 28px;
  font-weight: 600;
  /* 显式行盒高度（≈原 normal 的 28×1.2≈33.6，视觉变化 <1px）；与
     .stat-value.skeleton-bar 的 height 必须成对同值 —— 见同目录 __tests__ 源码门禁。 */
  line-height: 34px;
  color: var(--el-text-color-primary);
}

/* 骨架占位条：与它替代的那一行同高（空元素没有文本就没有行盒，必须显式给 height）。
   圆角/尺寸沿用 .stat-icon / .stat-value / .stat-label 自身的声明，这里只加 shimmer 背景。
   shimmer 动画：SkeletonCard.vue 的 @keyframes skeleton-shimmer 是 scoped 的（编译期会被
   重命名为 skeleton-shimmer-<hash> 并加 [data-v-hash] 前缀），本组件用不到；
   theme.css:994 的全局 @keyframes shimmer 键帧完全相同（0% 200% 0 → 100% -200% 0，
   且 background-size: 200% 100% 与 var(--skeleton-shimmer) 的 90deg 渐变匹配），
   故直接复用全局 shimmer，不再复制一份等价键帧。 */
.skeleton-bar {
  background: var(--skeleton-shimmer);
  background-size: 200% 100%;
  animation: shimmer 1.5s infinite;
}

.stat-icon.skeleton-bar {
  /* 只加 shimmer 背景；60×60、圆角 8px 沿用 .stat-icon 自身的声明 */
  flex-shrink: 0;
}

.stat-value.skeleton-bar {
  height: 34px;
  width: 60%;
  border-radius: 4px;
}

.stat-label.skeleton-bar {
  height: 17px;
  width: 40%;
  border-radius: 4px;
}

/* .status-history-hint 随「节点状态变化」卡搬进 components/StatusHistoryCard.vue：
   父组件 scoped 样式打不进子组件内部 DOM，规则必须跟着片段走。 */

.device-link {
  color: var(--el-color-primary);
  text-decoration: none;
}

.device-link:hover {
  text-decoration: underline;
}

.raw-data {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  font-family: monospace;
}

.trend-card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}

.trend-controls {
  display: flex;
  gap: 8px;
  align-items: center;
}

.trend-category-select {
  width: 140px;
}

.alert-list {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
}

.dashboard-error-alert {
  margin-bottom: 20px;
  border-radius: 8px;
}

.dashboard-error-text {
  margin-right: 8px;
}

.alert-error {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--el-color-danger);
  font-size: 14px;
}

.alert-ok {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--el-text-color-secondary);
  font-size: 14px;
}

.alert-item {
  flex: 1 1 220px;
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 16px;
  background: var(--bg-color-secondary);
  border-radius: 8px;
  cursor: pointer;
  transition: all 0.2s;
}
.alert-item:hover {
  background: var(--el-color-danger-light-9);
  transform: translateX(2px);
}
.alert-value {
  font-size: 22px;
  font-weight: 600;
  color: var(--el-color-danger);
  line-height: 1.2;
}
.alert-label {
  font-size: 12px;
  color: var(--text-color-secondary);
  margin-top: 2px;
  /* 防止移动端窄宽下中文逐字竖排 */
  word-break: keep-all;
  overflow-wrap: break-word;
}

/* 移动端：单行 4 列纵向紧凑小卡，压低统计卡占高让位给内容区 */
@media (max-width: 768px) {
  .dashboard-stats {
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 8px;
  }

  .stat-content {
    flex-direction: column;
    align-items: center;
    text-align: center;
    gap: 4px;
  }

  .stat-icon {
    width: 22px;
    height: 22px;
    border-radius: 6px;
    flex-shrink: 0;
  }

  .stat-icon :deep(.el-icon) {
    font-size: 13px;
  }

  .stat-info {
    min-width: 0;
    width: 100%;
  }

  .stat-label {
    font-size: 10px;
    /* 由 1.3 改为显式 13px：与 .stat-label.skeleton-bar 的 height 成对同值 */
    line-height: 13px;
    margin-top: 1px;
    max-height: 2.6em;
    overflow: hidden;
    word-break: keep-all;
    overflow-wrap: break-word;
  }

  .stat-value {
    font-size: 16px;
    /* 与 .stat-value.skeleton-bar 的 height 成对同值 */
    line-height: 19px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 100%;
  }

  .stat-value.skeleton-bar {
    height: 19px;
  }

  .stat-label.skeleton-bar {
    height: 13px;
  }

  :deep(.stat-card) {
    margin-bottom: 0;
  }

  :deep(.stat-card .el-card__body) {
    padding: 8px 4px;
  }

  /* 分段控件与指标下拉各自整行，避免"7天"折行、边框残缺 */
  .trend-controls {
    width: 100%;
    flex-direction: column;
    align-items: stretch;
    gap: 8px;
  }
  /* 窄屏类别下拉铺满整行（旧规则固定 140px，与上方整行的时间档左右不对齐） */
  .trend-controls .trend-category-select,
  .trend-controls :deep(.el-select) {
    width: 100%;
  }

  .trend-controls :deep(.el-radio-group) {
    display: flex;
    width: 100%;
  }

  .trend-controls :deep(.el-radio-button) {
    flex: 1;
  }

  .trend-controls :deep(.el-radio-button__inner) {
    width: 100%;
    padding-left: 8px;
    padding-right: 8px;
    text-align: center;
  }

  .trend-category-select {
    width: 100%;
  }
}

/* ---- narrow-screen latest-data cards (2026-10-21 audit fix) ---- */
.latest-cards { display: flex; flex-direction: column; gap: 8px; }
.latest-card {
  border: 1px solid var(--el-border-color-lighter);
  border-radius: var(--radius-md);
  padding: 10px 12px;
  background: var(--bg-color);
  display: flex; flex-direction: column; gap: 6px;
}
.lc-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.lc-head .device-link {
  /* tap target >=44px: audited original link was 28-29x37 */
  display: inline-flex; align-items: center; min-height: 44px;
  font-size: 15px; font-weight: 600;
}
.lc-meta {
  display: flex; flex-wrap: wrap; gap: 4px 12px;
  font-size: 12px; color: var(--el-text-color-regular);
}
.lc-data {
  margin: 0; font-size: 13px; line-height: 1.5;
  color: var(--el-text-color-primary);
  word-break: keep-all; overflow-wrap: anywhere;
}
.data-detail-list { max-height: 52vh; overflow-y: auto; }
.data-detail-row {
  display: grid; grid-template-columns: minmax(90px, 40%) 1fr; gap: 8px;
  padding: 6px 0; border-bottom: 1px solid var(--el-border-color-lighter);
  font-size: 13px;
}
.dd-key { color: var(--el-text-color-regular); }
.dd-val { color: var(--el-text-color-primary); overflow-wrap: anywhere; }
.dd-empty { margin: 12px 0; color: var(--el-text-color-regular); }
</style>
