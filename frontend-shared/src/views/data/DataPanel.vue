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
          <!-- key 用 id（后端稳定合成）；WS 实时行 id 为 0 ⇒ 回落行号 -->
          <div v-for="(row, rowIndex) in historyData" :key="row.id || rowIndex" class="data-card">
            <div class="data-card-head">
              <span class="data-card-time">{{ rowTimeText(row) }}</span>
            </div>
            <div class="data-card-metrics">
              <span v-for="metric in summarizeRow(row)" :key="metric.key" class="data-card-metric">
                <span class="metric-label">{{ metric.label }}</span>
                <span class="metric-value">{{ metric.value }}<i v-if="metric.unit" class="metric-unit">{{ metric.unit }}</i></span>
              </span>
              <span v-if="summarizeRow(row).length === 0" class="data-card-metric muted">无可解析数值</span>
            </div>
            <div class="data-card-foot">
              <el-button
                link
                type="primary"
                size="small"
                data-testid="row-expand"
                @click="openRowDetail(row)"
              >
                查看完整数据
              </el-button>
            </div>
          </div>
        </div>

        <!-- data-page / data-total 是**可观测的翻页状态**：
             分页改成前端切片后，"当前在第几页"不再体现在请求参数里，
             而 el-table 的行在单测替身里不渲染插槽 —— 没有这两个属性就
             无法从外部验证"换条件后回到第 1 页"这条 MUST（§3.2.6）。 -->
        <div
          v-else
          class="mobile-table-wrapper"
          data-testid="history-table"
          :data-page="currentPage"
          :data-total="total"
        >
          <p class="mobile-table-hint">左右滑动查看完整历史数据</p>
          <el-table :data="historyData" stripe>
            <el-table-column prop="created_at" label="采集时间" width="180">
              <template #default="{ row }">
                <span>{{ formatTime(row.timestamp || row.created_at || row.collected_at) }}</span>
              </template>
            </el-table-column>
            <!-- 经 parseRowData(row) 这**唯一**入口取数：
                 行里的指标在 data 下（历史行由 aggregateHistoryRows 产出、实时行由
                 WS payload 构造）。解析不出来返回 null ⇒ 显示「—」，
                 与"值真的是 0"区分开。 -->
            <el-table-column prop="data" label="数据">
              <template #default="{ row }">
                <!-- 判据是 .values 而不是整个返回对象：后者恒为真对象，
                     会让"解析不出来"也能匹配 v-if 分支（只有 formatData 兜住才没露馅）。 -->
                <span v-if="parseRowData(row).values" data-testid="row-values">{{ formatData(parseRowData(row).values) }}</span>
                <span v-else style="color: var(--el-text-color-placeholder);" data-testid="values-unknown">{{ UNKNOWN }}</span>
              </template>
            </el-table-column>
            <!--
              2026-11-11 原始数据退役，本表删除两列：

              · 「原始数据」列（原渲染 data_json.raw_hex）：device_data 整表删除，
                原始帧字节不再有 REST 数据源。帧表的 raw 列只对**关键帧**填充
                （internal/telemetry/dualwrite.go 的 appendEncoded 显式判
                FrameKind==Key），且没有对外接口 —— 不能拿来当列数据源，
                否则绝大多数行会是「—」，比删列更误导。
              · 「状态/错误码」列（原渲染 row.error_code）：这是**既有 bug** 的清理 ——
                /edge-devices/:id/data 的响应从来没有 error_code 字段
                （实测键：created_at, data_json, device_id, id, node_id, timestamp），
                该列恒为 undefined ⇒ 恒显示「正常」。删端点时顺手删掉它，
                不是本次改造引入的问题（方案 §1.5 已记录）。
            -->
          </el-table>
        </div>

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
    <el-dialog
      v-model="rowDetailVisible"
      title="单条数据完整内容"
      align-center
      width="92%"
      data-testid="row-detail-dialog"
    >
      <div class="row-detail">
        <div class="row-detail-meta">
          <span>采集时间：{{ rowTimeText(rowDetailRow) }}</span>
        </div>
        <!-- F8：弹层内 4 列表最小宽 400px > 393 弹窗可视宽，必须自带横滚容器 + 提示。 -->
        <div class="mobile-table-wrapper">
          <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
          <el-table :data="rowDetailEntries(rowDetailRow)" size="small" max-height="420" data-testid="row-detail-table">
            <el-table-column prop="label" label="指标" min-width="110" show-overflow-tooltip />
            <el-table-column prop="key" label="标识" min-width="110" show-overflow-tooltip />
            <el-table-column prop="value" label="值" width="110" align="right" />
            <el-table-column prop="unit" label="单位" width="70" align="center" />
          </el-table>
        </div>
        <p v-if="rowDetailEntries(rowDetailRow).length === 0" class="row-detail-empty">
          该条数据无法解析出指标。
        </p>
      </div>
      <template #footer>
        <el-button type="primary" @click="rowDetailVisible = false">关闭</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script lang="ts">
/**
 * 行数据解析（纯函数，**本页唯一**的取数入口）。
 *
 * 为什么单独开一个非 setup 的 script 块：模板、统计卡（dynamicStats）、趋势兜底、CSV 导出、
 * 实时条目都要取同一份数据，散落多处必然再次漂移（历史缺陷正是"表格读 parsed_data、接口只给
 * data_json"）。函数放在模块作用域后，script setup 块内可直接调用、模板也能解析 ——
 * 已用本仓 @vue/compiler-sfc 3.5.40 实测确认（两块共享作用域，导出照常生成）。
 *
 * 为什么不用"把函数 defineExpose 出去、让测试调它"的既有范式：缺陷的回归点是
 * **表格列恒显示 —**，只暴露函数等于不守渲染路径；测试必须打到真实渲染出的单元格。
 *
 * 契约（规范 §3.2.5 不得以本地默认值伪造事实）：
 *   · 取不到 / 形状不认识 ⇒ values = null ⇒ 界面显示「—」；
 *   · 值是 0 ⇒ values = { rainfall: 0 } ⇒ 界面显示「rainfall: 0.00」。
 *   二者绝不互相顶替，尤其**不得**把解析失败落成 0。
 *
 * 行形状的**唯一真源**见 ./historyRows.ts —— 历史行的聚合规则写在那里。
 */

/** parseRowData 的返回：数值指标。 */
export interface RowData {
  /**
   * 指标名 → **展示值**（数值或字符串）；解析不出来时为 null
   * （不是空对象 —— 空对象会与"真的没有指标"混淆）。
   *
   * 允许字符串是因为部分传感器的真值本就是字符串：后端 SensorData 有
   * `StringValue` 字段（drivers/registry.go:13），此时 `Value` 恒为 0 而真值在
   * StringValue 里 —— 典型是 BMS 的 hardware_version（"V19"）与 serial_number。
   * 只取 Value 会把它显示成 **0**，比「—」更糟：0 看起来像真实读数。
   *
   * ⚠ 2026-11-11 原始数据退役后，**本页已不可能收到字符串读数**：
   * 唯一承载 StringValue 的 device_data.data_json.str_values 随表删除，
   * 而 /devices/:id/history 只返回 float64 的 value。类型保留字符串分支，
   * 是因为实时 WS 推送（handleDataUpdate）仍可能带上字符串字段，
   * 且行解析不该对"值是不是数字"做两次不同的假设。
   */
  values: Record<string, number | string> | null
  /**
   * 指标名 → **纯数值**，供统计卡 / 趋势图 / 数值专用消费者使用。
   *
   * 单独给出是为了不让字符串读数污染数值计算：若把 "V19" 混进趋势，
   * 它会被当成 0 ⇒ 拉低均值并在图上画出掉到 0 的假线。
   */
  numbers: Record<string, number> | null
}

/**
 * 表格行的形状。
 *
 * 两个来源，**形状已经统一**（无需再按响应格式分支）：
 *   · 历史行  —— aggregateHistoryRows() 的产物（一次采集一行，指标在 data 里）；
 *   · 实时行  —— handleDataUpdate() 由 WS payload 构造，同样把指标放进 data。
 */
interface DataRow {
  data?: unknown
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
 * 行内单个字段的展示值。
 *
 * 口径与本仓既有正确范式一致（views/edge-device/shared/DeviceControlPanel.vue:76
 * 的 `value.string_value || value.value`）：**字符串非空时优先**，否则取数值。
 *
 * 为什么保留字符串分支：实时 WS 推送的 payload 可能带字符串型读数
 * （device_data 时代是 StringValue；退役后仍有设备上报非数值文本字段）。
 * 只认数字会把真实文本读数丢掉，显示成「—」。
 * 刻意**不**做 Number(str)：把 "0.5" 静默转成数字属于以本地推断伪造事实。
 * 返回 null 表示"这个字段没有可信值"，与"值是 0"区分。
 */
const asDisplayRowValue = (value: unknown): number | string | null => {
  if (typeof value === 'string') return value.trim() !== '' ? value : null
  return asNumber(value)
}

/**
 * 非指标字段。扁平兜底分支必须排除它们，否则会把 id / device_id 这类
 * **元数据数字**当成传感器读数渲染出来（实测真实行就带这两个数字键 ——
 * 本文件新增用例抓到过）。
 *
 * 2026-10-23（逻辑设备移除）：原来这里还有一个 logical_device_id，
 * 后端已删除该列（契约 §2），故从排除表里一并去掉 —— 保留它等于给一个
 * 不可能再出现的键留位。
 *
 * 2026-11-11（原始数据退役）：raw_data / raw_hex / data_json / parsed_data
 * 一并从排除表里删掉。它们原本是 device_data 行上的字段，现在行来自
 * /devices/:id/history（一字段一行）或 WS 推送，都不再有这些键；
 * 留着一串永不出现的名字只会让后来者以为还存在那种形状。
 */
const NON_METRIC_KEYS = new Set([
  'id', 'device_id', 'node_id', 'channel_id',
  'timestamp', 'created_at', 'collected_at', 'updated_at',
])

/**
 * 行数据解析（**本页唯一**的取数入口）。
 *
 * 契约（规范 §3.2.5 不得以本地默认值伪造事实）：
 *   · 取不到 / 形状不认识 ⇒ values = null ⇒ 界面显示「—」；
 *   · 值是 0 ⇒ values = { rainfall: 0 } ⇒ 界面显示「rainfall: 0.00」。
 *   二者绝不互相顶替，尤其**不得**把解析失败落成 0。
 *
 * 2026-11-11 原始数据退役后**大幅简化**：原来有两条分支（解析 data_json 的
 * sensors / 扁平兜底），现在历史行与实时行的形状已经统一 —— 指标都直接放在
 * `data` 里（历史行由 aggregateHistoryRows 产出，实时行由 WS payload 构造）。
 * data_json 分支随 device_data 表一起删除，坏 JSON 的处理也就没有了对象。
 */
export function parseRowData(row: unknown): RowData {
  if (!row || typeof row !== 'object') return { values: null, numbers: null }
  const r = row as DataRow

  let values: Record<string, number | string> | null = null

  // ① 首选 { data: {...} }（历史行与实时行都是这个形状）
  // ② 兼容扁平 {...}（既有用例的 fixture 走这里）
  const flat = r.data && typeof r.data === 'object' && !Array.isArray(r.data)
    ? r.data as Record<string, unknown>
    : r
  const entries = Object.entries(flat)
    .filter(([key]) => !NON_METRIC_KEYS.has(key))
    .map(([key, value]) => [key, asDisplayRowValue(value)] as const)
    .filter((entry): entry is readonly [string, number | string] => entry[1] !== null)
  if (entries.length > 0) values = Object.fromEntries(entries)

  return { values, numbers: numericOnly(values) }
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
import { useWebSocketStore, type WebSocketMessage } from '@/stores/websocket'
import { WS_EVENT } from '@/events/events'
import { logger } from '@/utils/logger'
import { sensorNameMap, sensorUnitMap } from '@/utils/sensor'
import { UNKNOWN } from '@/utils/format'

const router = useRouter()
const route = useRoute()

/**
 * 窄屏（<768px）分支：历史数据表 → 「摘要 + 查看完整数据」卡片；趋势图 → 单指标 + 紧凑高度。
 * 断点与 theme.css 一致（useResponsive 的 isMobile = width < 768）。
 */
const { isMobile } = useResponsive()

// ── 趋势图异步态（P0：historical-batch 生产实测挂起 38s+，超时回退还需 10–30s）──
const chartLoading = ref(false)
const chartFailed = ref(false)
/** 只允许最后一次构建写回状态（快速连续查询时防旧请求回写）。 */
let chartLoadToken = 0
/** 移动端趋势图当前查看的指标（单指标，避免 4 轴 + 20 图例挤在 303px 画布里）。 */
const mobileMetric = ref('')

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
const chartSeries = ref<any[]>([])
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
  // ⚠ 不再声明 raw_hex / raw_data：WS 帧仍可能带这两个字段（stores/websocket.ts
  // 的 payload 契约由 ChannelTerminal 消费），但**本页已不消费它们** ——
  // 「原始数据」列随 device_data 退役已删除。声明一个从不读取的可选字段
  // 只会让后来者以为页面上还存在那种展示。
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

/**
 * 「最新」统计卡的指标优先级：改前取 availableCategories 的**字母序前 3**（BMS 上是
 * cell_voltage_1/10/11，三张卡都是 3.27V，等于没有信息量）。这里按业务优先级排序后取前 3。
 */
const STAT_METRIC_PRIORITY = [
  'total_voltage', 'current', 'rsoc', 'remaining_capacity', 'temperature_1',
  'power', 'cell_voltage_max', 'cell_voltage_min', 'total_power', 'frequency',
]
function statRank(code: string): number {
  const index = STAT_METRIC_PRIORITY.indexOf(code)
  return index < 0 ? STAT_METRIC_PRIORITY.length : index
}

const dynamicStats = computed(() => {
  const latest = [...historyData.value]
    .sort((a, b) => String(b.timestamp || b.created_at || b.collected_at || '').localeCompare(String(a.timestamp || a.created_at || a.collected_at || '')))[0]
  // 与表格列共用 parseRowData：改前读 latest.parsed_data || latest.data，
  // 而接口只给 data_json ⇒ source 恒 {} ⇒ 统计卡恒不渲染（数据非空却一张卡都没有）。
  // 统计卡是**数值指标**视图：用 numbers（纯数值）而不是 values（可能含字符串），
  // 否则 hardware_version="V19" 会被当成 0 计入并污染统计。
  const source = parseRowData(latest).numbers ?? {}
  return [...availableCategories.value]
    // 业务优先级排序（稳定排序：未列出的指标保持后端原顺序），不再按字母序取前 3。
    .sort((a, b) => statRank(a.code) - statRank(b.code))
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
    .filter((t): t is string => typeof t === 'string' && t !== '')
    .map(t => new Date(t).getTime())
    .filter(t => !Number.isNaN(t))
    .sort((a, b) => a - b)
  if (times.length < 2) {
    latestStats.duration = '< 1分钟'
    return
  }

  const diffMin = Math.floor((times[times.length - 1] - times[0]) / 60000)
  // P2（审计实测：本页 20 条跨 19s 却显示「0分钟」，像没采到数据）：
  // 不足 1 分钟统一走本页已有的「< 1分钟」写法，不显示 0。
  if (diffMin < 1) {
    latestStats.duration = '< 1分钟'
  } else if (diffMin < 60) {
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

/** 是否 413（后端内存护栏拒绝）。axios 把它包成 ApiError，status 挂在 error.status。 */
function isPayloadTooLarge(error: unknown): boolean {
  const status = (error as { status?: unknown; response?: { status?: unknown } } | null)?.status
  if (status === 413) return true
  return (error as { response?: { status?: unknown } } | null)?.response?.status === 413
}

/**
 * 小工具：把并发压到 limit（趋势图回退请求专用，见下方注释）。
 * 不用 p-limit 之类新依赖，保持零新增包。
 */
async function mapWithConcurrency<T, R>(items: T[], limit: number, worker: (item: T) => Promise<R>): Promise<R[]> {
  const results: R[] = new Array(items.length)
  let cursor = 0
  const run = async () => {
    while (cursor < items.length) {
      const index = cursor
      cursor += 1
      results[index] = await worker(items[index])
    }
  }
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, () => run()))
  return results
}

const buildChartSeries = async () => {
  // P0：趋势序列是**异步**构建的。改前 chartSeries 未就绪就渲染 el-empty「暂无趋势数据」，
  // 而生产实测 historical-batch 挂起 38s+、超时回退还要 10–30s ⇒ 有数据却报"暂无"。
  // 这里显式建模 chartLoading / chartFailed，并用请求令牌防止旧构建写回。
  const token = ++chartLoadToken
  chartLoading.value = true
  chartFailed.value = false
  /** 逐个类别回退时统计"请求本身失败"的条数：全失败且无序列 ⇒ 真失败（错误态）。 */
  let fallbackFailures = 0
  // 判据用 allHistoryRows（整窗）而不是 historyData（当前页）：
  // 趋势图画的是整个时间范围，用当前页判断"有没有数据"会让"第 2 页为空但
  // 第 1 页有数据"的窗口把整张图判成无数据。
  if (allHistoryRows.value.length === 0) {
    chartSeries.value = []
    chartLoading.value = false
    syncMobileMetric()
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
        params: batchParams,
        // 生产实测该端点会挂起（>38s 无响应）：给独立短超时，尽快转入逐个回退，
        // 避免整页趋势图长时间停在骨架（P0 配套；不影响全局 axios timeout）。
        timeout: 8000,
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
      // Fallback: 批量 API 失败时逐个请求 + max_points=500。
      // ⚠ 并发必须限流：浏览器同域只有 ~6 条 HTTP/1.1 连接，31 个回退请求全并行时，
      //   挂起的那几条会吃满连接池 ⇒ 用户点翻页发出的 /edge-devices/:id/data 请求被排队，
      //   实测点击分页后 8s 内一个请求都没发出去（页面一直骨架）。限 4 并发给交互请求留 2 条。
      const fallbackResults = await mapWithConcurrency(categoryNames, 4, async (cat) => {
        try {
          const res = await client.get<unknown, MeasurementPoint[]>('/api/v1/unified-data/historical', {
            params: {
              device_pk: queryForm.deviceId,
              category: cat,
              start_time: startTime.toISOString(),
              end_time: endTime.toISOString(),
              max_points: 500
            }
          })
          return { cat, data: unwrapList<MeasurementPoint>(res) }
        } catch {
          fallbackFailures += 1
          return { cat, data: [] as MeasurementPoint[] }
        }
      })
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
    // 所有类别请求都失败且没有任何序列 ⇒ 真失败；若请求成功但数据为空 ⇒ 真空数据（非错误）。
    chartFailed.value = series.length === 0
      && categoryNames.length > 0
      && fallbackFailures === categoryNames.length
  } catch (error) {
    logger.error('获取趋势数据失败', { error: String(error) })
    // Fallback: 直接用已聚合的历史行里出现过的数值字段建序列
    // （与表格/统计卡同一解析入口）。趋势是数值图：同样只看 numbers，
    // 字符串读数不能进（否则画出掉到 0 的假线）。
    const numericKeys = Object.keys(parseRowData(allHistoryRows.value[0]).numbers ?? {})

    if (numericKeys.length > 0) {
      chartSeries.value = numericKeys.map(key => ({
        name: key,
        unit: '',
        category: key,
        data: allHistoryRows.value
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
      chartFailed.value = false
    } else {
      chartSeries.value = []
      chartFailed.value = true
    }
  } finally {
    if (token === chartLoadToken) {
      chartLoading.value = false
      syncMobileMetric()
    }
  }
}

// ── 移动端趋势图：单指标 + 紧凑高度 ──
// P1（B-data 审计）：393 的 303px 画布里 4 根 Y 轴 + 20 条图例不可读（轴标签重叠、
// 图例被折叠成 1/20）；移动端改为单指标 + height 220（LineChart 的紧凑模式阈值 ≤220）。
const CHART_METRIC_PRIORITY = [
  'total_voltage', 'current', 'rsoc', 'remaining_capacity', 'temperature_1',
  'power', 'cell_voltage_max', 'cell_voltage_min',
]

const chartMetricOptions = computed(() => {
  const rank = (code: string) => {
    const index = CHART_METRIC_PRIORITY.indexOf(code)
    return index < 0 ? CHART_METRIC_PRIORITY.length : index
  }
  // 关键指标排前面（否则下拉第一屏全是 cell_voltage_1/10/11… 字母序噪声）。
  return [...chartSeries.value]
    .sort((a, b) => rank(String(a.category)) - rank(String(b.category)))
    .map((item) => ({
      code: String(item.category || ''),
      label: item.unit ? `${item.name} (${item.unit})` : String(item.name || item.category || ''),
    }))
})

const visibleChartSeries = computed(() => {
  if (!isMobile.value) return chartSeries.value
  const chosen = chartSeries.value.find(item => String(item.category) === mobileMetric.value)
  return chosen ? [chosen] : []
})

const chartHeight = computed(() => (isMobile.value ? '220px' : '350px'))

function pickDefaultMetric(series: Array<{ category?: unknown }>): string {
  for (const code of CHART_METRIC_PRIORITY) {
    if (series.some(item => String(item.category) === code)) return code
  }
  const first = series[0]
  return first ? String(first.category ?? '') : ''
}

/** 让移动端选中项始终落在当前 series 上（换设备/时间范围后自动回到优先指标）。 */
function syncMobileMetric() {
  if (mobileMetric.value && chartSeries.value.some(item => String(item.category) === mobileMetric.value)) return
  mobileMetric.value = pickDefaultMetric(chartSeries.value)
}

function retryChart() {
  void buildChartSeries()
}

// ── 移动端历史数据「摘要 3~4 个关键指标 + 查看完整数据」──
// P0（B-data 审计）：393 下「数据」列 80px 装 638 字符 ⇒ 单行 2041px、整页 42689px。
// 摘要只挑业务优先级高的指标；完整 K/V 全量（不 ellipsis 丢数据）放进详情弹层。
const SUMMARY_METRIC_PRIORITY = CHART_METRIC_PRIORITY

function metricUnit(code: string): string {
  const category = availableCategories.value.find(item => item.code === code)
  return category?.unit || sensorUnitMap[code] || ''
}

function formatMetricValue(value: number): string {
  return Number.isInteger(value) ? String(value) : value.toFixed(2)
}

interface RowSummaryMetric { key: string; label: string; value: string; unit: string }
function summarizeRow(row: Record<string, any>): RowSummaryMetric[] {
  const numbers = parseRowData(row).numbers
  if (!numbers) return []
  const rank = (code: string) => {
    const index = SUMMARY_METRIC_PRIORITY.indexOf(code)
    return index < 0 ? SUMMARY_METRIC_PRIORITY.length : index
  }
  return Object.keys(numbers)
    .sort((a, b) => rank(a) - rank(b))
    .slice(0, 4)
    .map(code => ({
      key: code,
      label: sensorNameMap[code] || code,
      value: formatMetricValue(numbers[code]),
      unit: metricUnit(code),
    }))
}

function rowTimeText(row: Record<string, any> | null): string {
  if (!row) return UNKNOWN
  const time = row.timestamp || row.created_at || row.collected_at
  return time ? formatTime(time) : UNKNOWN
}

interface RowDetailEntry { key: string; label: string; value: string; unit: string }
function rowDetailEntries(row: Record<string, any> | null): RowDetailEntry[] {
  const values = row ? parseRowData(row).values : null
  if (!values) return []
  return Object.entries(values).map(([key, raw]) => ({
    key,
    label: sensorNameMap[key] || key,
    value: String(raw ?? UNKNOWN),
    unit: metricUnit(key),
  }))
}

function openRowDetail(row: Record<string, any>) {
  rowDetailRow.value = row
  rowDetailVisible.value = true
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
  // 形状必须与表格期望一致：一次采集一行、指标在 data 里。
  //
  // 2026-11-11 原始数据退役：不再往行上挂 raw_hex / error_code ——
  // 「原始数据」「错误码」两列已删除（前者无数据源，后者一直是 undefined）。
  const timestamp = payload.collected_at || new Date().toISOString()
  const newItem: CollectedRow = {
    // WS 行没有后端合成 id，用时间戳当 key：同一时间戳的两次推送本就该合并
    id: 0,
    timestamp,
    created_at: timestamp,
    data: (payload.data || payload.sensors || {}) as Record<string, number>,
    collected_at: timestamp,
  }

  // 添加到实时数据列表（页面上的「共 N 条」计数用）
  realtimeData.value.unshift(newItem)
  if (realtimeData.value.length > 50) {
    realtimeData.value = realtimeData.value.slice(0, 50)
  }

  // 同时插入表格数据源（顶部），并整体重排当前页：
  // 直接改 historyData 会把"当前页切片"这个不变量破坏掉（第 2 页会跟着变），
  // 所以改 allHistoryRows 再 applyPage()。
  allHistoryRows.value.unshift(newItem)
  total.value++
  applyPage()

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

  const entries = Object.entries(data)
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

// 2026-11-11 原始数据退役：原 formatRawData()（把 raw_hex 渲染成 "7B hex"）
// 随「原始数据」列一并删除 —— 没有数据源了，留着它就是死代码。

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

.data-card {
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 10px;
  padding: 12px;
  background: var(--el-bg-color);
}

.data-card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  font-size: 13px;
  color: var(--el-text-color-secondary);
}

.data-card-time {
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.data-card-ok {
  color: var(--el-color-success);
  font-size: 12px;
}

.data-card-metrics {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 6px 10px;
  margin-top: 10px;
}

.data-card-metric {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 6px;
  min-width: 0;
  font-size: 13px;
}

.metric-label {
  color: var(--el-text-color-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.metric-value {
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.metric-unit {
  margin-left: 2px;
  font-style: normal;
  font-weight: 400;
  font-size: 11px;
  color: var(--el-text-color-secondary);
}

.data-card-foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-top: 10px;
  padding-top: 8px;
  border-top: 1px dashed var(--el-border-color-lighter);
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.data-card-raw {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.data-card-list .muted {
  color: var(--el-text-color-placeholder);
}

.row-detail-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 16px;
  margin-bottom: 12px;
  font-size: 13px;
  color: var(--el-text-color-secondary);
}

.row-detail-empty {
  margin: 12px 0 0;
  font-size: 13px;
  color: var(--el-text-color-secondary);
}
</style>