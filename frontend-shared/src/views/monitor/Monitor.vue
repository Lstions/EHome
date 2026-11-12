<template>
  <!-- 根容器承载「刷新中」状态（F31 残余）：
       isLoading 只描述「首屏尚未落定」，刷新期间恒为 false，骨架分支不渲染；
       而刷新期间 DOM 上既无骨架也无错误态，.stat-value 显示的是**上一次成功的陈旧值**
       —— 门禁无法分辨陈旧值与新鲜值，会在一份不代表终态的 DOM 上做裁切/溢出判定。
       故此处把「请求在飞」独立暴露出来：aria-busy 是对辅助技术的标准表达，
       data-test 是本仓既有稳定约定（EP 的 el-table/el-loading 都不设 aria-busy）。
       两者只表达状态，不改变视觉：刷新中不显示骨架/遮罩，保留陈旧值（F28 已裁决）。 -->
  <div
    class="monitor-container"
    :aria-busy="isRefreshing"
    :data-test="isRefreshing ? 'monitor-refreshing' : undefined"
  >
    <!-- 页头（I-6 标题统一）：原工具栏内的 <h2>系统监控</h2>（以及其内的 DataAnalysis 图标）
         已**移除**，标题改由 PageHeader 承担 —— 同页不得出现两个「系统监控」。
         刷新间隔选择器与手动刷新属于**页级操作**，按既有范式移入 #extra
         （channel/ChannelList.vue、firmware/FirmwareManage.vue、data-source/DataSourceList.vue
         均把页级主操作放 #extra）。PageHeader 的 .page-header-right 自带
         display:flex + gap:8px，与原 .toolbar 的 space-between 布局等价；
         .toolbar-actions 自身仍是 flex 容器，其窄屏规则（768px 换行、480px 纵向拉伸、
         .el-select{flex:1}）不依赖已移除的 .toolbar 父级，故整组保留、行为不变。 -->
    <PageHeader title="系统监控">
      <template #extra>
        <div class="toolbar-actions">
          <el-select
            v-model="refreshInterval"
            class="refresh-interval-select"
            size="default"
            aria-label="自动刷新间隔"
            @change="handleIntervalChange"
          >
            <el-option label="5秒" :value="5000" />
            <el-option label="10秒" :value="10000" />
            <el-option label="30秒" :value="30000" />
            <el-option label="1分钟" :value="60000" />
            <el-option label="关闭自动刷新" :value="0" />
          </el-select>
          <el-button type="primary" :icon="Refresh" @click="fetchMetrics">手动刷新</el-button>
        </div>
      </template>
    </PageHeader>

    <!-- 接口失败：常驻错误态 + **配套**重试入口（范式同 views/dashboard/Dashboard.vue）。
         只弹一条 toast 不算错误态：自动刷新（默认 10s）会不断覆盖它，
         用户回头看页面只剩一片 0，与"系统真的空闲"逐字段相同（F28 / U-1 第 4 处）。 -->
    <el-alert
      v-if="loadError"
      class="monitor-error-alert"
      type="error"
      show-icon
      :closable="false"
      data-test="monitor-error"
    >
      <template #title>
        <span class="monitor-error-text">获取监控数据失败：{{ errorDetail }}</span>
        <el-button link type="primary" size="small" data-test="monitor-retry" @click="retryLoad">重试</el-button>
      </template>
    </el-alert>

    <!-- 统计卡片
         规范 §4.3 统计卡 MUST「统计值必须标明范围」。
         本页四个 KPI 取自两种不同的统计口径，必须逐个标注，否则用户会把
         「进程启动以来的计数器」误读成「库里累计的数据量」：
           · HTTP 请求总数 / 数据点采集总数 —— Prometheus Counter，**进程启动以来**的累计值，
             服务重启即归零（后代 handler_metrics.go:82/106 readCounterTotal）
           · 设备/节点在线状态 —— 直接 COUNT 全表，**全局**口径，与页面筛选无关
         is-stale：接口未成功时不得让"在线"绿点继续宣称健康（见 metric()）。 -->
    <div class="stat-cards" :class="{ 'is-stale': !metricsReady }">
      <el-row :gutter="16">
        <el-col :xs="12" :sm="12" :md="6">
          <el-card shadow="hover" class="stat-card http">
            <div class="stat-icon"><el-icon><Connection /></el-icon></div>
            <div class="stat-content">
              <div class="stat-value">{{ metricText(metrics?.http?.requests_total, true) }}</div>
              <div class="stat-label">HTTP 请求总数<span class="stat-scope">{{ SCOPE_PROCESS }}</span></div>
            </div>
          </el-card>
        </el-col>
        <el-col :xs="12" :sm="12" :md="6">
          <el-card shadow="hover" class="stat-card device">
            <div class="stat-icon"><el-icon><Monitor /></el-icon></div>
            <div class="stat-content">
              <div class="stat-value">
                <span :class="onlineValueClass(metrics?.device?.online, deviceTotal)">{{ metric(metrics?.device?.online) }}</span>
                <span class="separator">/</span>
                <span>{{ deviceTotalText }}</span>
              </div>
              <div class="stat-label">设备在线状态<span class="stat-scope">{{ SCOPE_GLOBAL }}</span></div>
            </div>
          </el-card>
        </el-col>
        <el-col :xs="12" :sm="12" :md="6">
          <el-card shadow="hover" class="stat-card collector">
            <div class="stat-icon"><el-icon><Cpu /></el-icon></div>
            <div class="stat-content">
              <div class="stat-value">
                <span :class="onlineValueClass(metrics?.node?.online, nodeTotal)">{{ metric(metrics?.node?.online) }}</span>
                <span class="separator">/</span>
                <span>{{ nodeTotalText }}</span>
              </div>
              <div class="stat-label">节点在线状态<span class="stat-scope">{{ SCOPE_GLOBAL }}</span></div>
            </div>
          </el-card>
        </el-col>
        <el-col :xs="12" :sm="12" :md="6">
          <el-card shadow="hover" class="stat-card data">
            <div class="stat-icon"><el-icon><DataLine /></el-icon></div>
            <div class="stat-content">
              <div class="stat-value">{{ metricText(metrics?.data?.points_collected, true) }}</div>
              <div class="stat-label">数据点采集总数<span class="stat-scope">{{ SCOPE_PROCESS }}</span></div>
            </div>
          </el-card>
        </el-col>
      </el-row>
    </div>

    <!-- 控制面健康：与接口成败解耦，始终渲染。
         失败/未加载时整卡进入未知态（标签「状态未知」、9 个读数「—」），
         绝不显示「正常」——那正是本缺陷（把接口故障说成健康）。
         范式同 Dashboard 的「异常摘要」卡：先声明未知，再谈结论。
         位置与 .detail-panels 平级（该容器本就没有自身样式），故外观不变；
         放在 v-if 链**之前**是为了让三支分支保持连续（v-else 必须紧邻）。 -->
    <MonitorControlHealth
      :metrics="metrics"
      :metrics-ready="metricsReady"
      :tag="controlHealthTag"
    />

    <!--
      历史数据回填进度（契约 §4.6）。

      **位置刻意在此**：启动门禁在 ready=true 时就放行到业务页，而回填恰在 ready
      之后才真正开跑（生产实测 20~40 分钟）——维护页在那个窗口里早已消失，
      业务页才是用户唯一能看到进度的地方。

      显示条件见 views/maintenance/backfill.ts 的 shouldShowBackfill：
        · running  → 进度卡（用户在等这个数）；
        · failed   → 错误卡（历史数据没迁完，必须告知）；
        · disabled → 信息卡（运维关阀 ⇒ 老数据永远不会迁移，同样必须告知，
                     静默隐藏等于让"数据没迁"永不可见）；
        · done/idle → 整卡不渲染（没有未完成的工作可汇报，不该占首屏）。
      因此对绝大多数请求零视觉影响。轮询是本组件独立的一条（5s），与页头的
      指标刷新间隔解耦：指标可以关自动刷新或调到 60s，回填进度不能跟着变慢。
    -->
    <BackfillProgressCard />

    <!-- 失败时不得渲染「0 项需要关注」——那是把"未知"说成"正常" -->
    <el-alert
      v-if="controlAlertVisible"
      class="control-alert"
      type="warning"
      show-icon
      :closable="false"
      data-test="monitor-control-alert"
      :title="`控制面有 ${controlAttention} 项需要关注`"
    />

    <!-- 详细面板：接口失败时整块换成错误态，绝不留一排 0 冒充真实读数 -->
    <div v-if="loadError" class="detail-error" data-test="monitor-detail-error">
      <el-icon class="detail-error-icon" aria-hidden="true"><WarningFilled /></el-icon>
      <p class="detail-error-title">监控指标加载失败</p>
      <p class="detail-error-desc">
        HTTP / MQTT / 设备 / 节点 / 数据采集 / WebSocket / 控制面 各项读数当前均不可用，
        页面不再显示任何具体数值，以免把接口故障误读成"系统空闲"。
      </p>
      <el-button type="primary" data-test="monitor-detail-retry" @click="retryLoad">重试</el-button>
    </div>

    <div v-else-if="isLoading" class="detail-skeleton" data-test="monitor-loading" aria-busy="true">
      <el-row :gutter="16">
        <el-col v-for="i in 4" :key="i" :xs="24" :sm="12">
          <SkeletonCard variant="card" animated />
        </el-col>
      </el-row>
    </div>

    <MonitorDetailPanels
      v-else
      :metrics="metrics"
      :metrics-ready="metricsReady"
      :is-mobile="isMobile"
      :device-online-percent="deviceOnlinePercent"
      :device-offline-percent="deviceOfflinePercent"
      :device-pending-percent="devicePendingPercent"
      :node-online-percent="nodeOnlinePercent"
      :node-offline-percent="nodeOfflinePercent"
    />

    <!-- 更新时间：失败时明确标注数据已陈旧，否则页脚会替接口"背书" -->
    <div class="footer-info">
      <span>最后更新: {{ lastUpdateTime }}</span>
      <span v-if="loadError" class="footer-stale" data-test="monitor-stale">
        （监控接口不可用，显示的数据已过期）
      </span>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * 2026-11-12 纯搬移式拆分（1067 → 534 行）：行为零变化 ——
 * DOM / class / data-test / aria / 文案 / 请求体 / 轮询时序全部不变。
 *
 * 拆分产物与职责：
 *   · composables/useMonitor.ts            指标取数、轮询、三态与全部派生值；
 *   · monitorDisplay.ts                    展示口径纯函数（值/「—」、K/M 缩写、
 *                                          在线数配色、范围词常量），页面与两个子组件共用；
 *   · components/MonitorControlHealth.vue  「控制面健康」卡；
 *   · components/MonitorDetailPanels.vue   详细面板六卡（HTTP/MQTT/设备/节点/数据采集/WS）。
 *
 * **刻意留在页面**（不是漏拆，是门禁按页面文件钉住）：
 *   · PageHeader + .toolbar-actions + .refresh-interval-select —— Monitor.spec 的 390px
 *     「真实级联」用例只编译 Monitor.vue 的 <style>，在合成的
 *     .monitor-container > .page-header > .page-header-left/-right 上断言
 *     flex-wrap / width / justify-content；搬进子组件后该用例会失去被测对象；
 *   · 统计卡四张（.stat-cards / .stat-card*）—— 同一套 CSS 门禁断言
 *     .stat-content 的 52px（窄屏 40px）与 .stat-card .stat-icon 的 z-index/pointer-events，
 *     且选择器必须编译成 [data-v-*] 形式；父组件 scoped 样式不会作用到子组件内部 DOM，
 *     改 :deep 又会改变编译后的选择器文本，故模板与样式必须一起留在页面；
 *   · 三态链（detail-error / detail-skeleton / 详细面板）—— 「接口失败不得伪装成 0」
 *     的判据按整页 DOM 断言，三支必须彼此紧邻（v-if / v-else-if / v-else）；
 *   · 根容器的 aria-busy + data-test 刷新标记 —— F31 残余的机械判定点，源码门禁逐字断言。
 */
import { Connection, Monitor, Cpu, DataLine, Refresh, WarningFilled } from '@element-plus/icons-vue'
import PageHeader from '@/components/common/PageHeader.vue'
import SkeletonCard from '@/components/common/SkeletonCard.vue'
import BackfillProgressCard from '@/views/maintenance/BackfillProgressCard.vue'
import { useResponsive } from '@/composables/useResponsive'
import MonitorControlHealth from './components/MonitorControlHealth.vue'
import MonitorDetailPanels from './components/MonitorDetailPanels.vue'
import { useMonitor } from './composables/useMonitor'
import { metricOf, metricTextOf, onlineValueClass, SCOPE_PROCESS, SCOPE_GLOBAL } from './monitorDisplay'

const { isMobile } = useResponsive()

const {
  // 三态与取数
  metrics,
  isLoading,
  isRefreshing,
  loadError,
  errorDetail,
  refreshInterval,
  lastUpdateTime,
  metricsReady,
  // 控制面与设备/节点的派生口径
  controlAttention,
  controlHealthTag,
  controlAlertVisible,
  deviceTotal,
  deviceTotalText,
  nodeTotal,
  nodeTotalText,
  deviceOnlinePercent,
  deviceOfflinePercent,
  devicePendingPercent,
  nodeOnlinePercent,
  nodeOfflinePercent,
  // 动作
  fetchMetrics,
  retryLoad,
  handleIntervalChange,
} = useMonitor()

/** 页面侧取值口径：与两个子组件共用 monitorDisplay.metricOf/metricTextOf（未就绪一律「—」）。 */
const metric = (value: number | null | undefined, isCounter = false) =>
  metricOf(value, metricsReady.value, isCounter)
const metricText = (value: number | null | undefined, isCounter = false) =>
  metricTextOf(value, metricsReady.value, isCounter)
</script>

<style scoped>
/* 页面级样式：拆分后只保留页面自己渲染的部分。
   统计卡（.stat-cards/.stat-card*）、页头操作区、错误/骨架/页脚留在页面；
   拆到子组件的规则（.control-* / .desc-* / .status-* / .error-text / .card-header）
   随片段一起搬走，本文件不再出现这些选择器 —— 搬完 grep 确认无孤儿选择器。 */
.monitor-container {
  padding: 0;
}

/* 注意：原 .toolbar / .toolbar h2 规则已随标题迁入 PageHeader 一并删除。
   保留会形成"死选择器"——匹配不到任何元素，读代码的人会误以为页头仍由 .toolbar 承担。 */
.toolbar-actions {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
}

.refresh-interval-select {
  width: 130px;
}

.stat-cards {
  margin-bottom: 20px;
}

.monitor-error-alert {
  margin-bottom: 20px;
}

.monitor-error-text {
  margin-right: 8px;
}

/* 接口未成功：不再存在"健康色"，统计卡一律降为中性色 */
.stat-cards.is-stale .stat-value,
.stat-cards.is-stale .stat-value .online,
.stat-cards.is-stale .stat-value .online-none {
  color: var(--el-text-color-secondary);
}

.detail-error {
  margin-top: 20px;
  padding: 32px 24px;
  text-align: center;
  background: var(--el-fill-color-light);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
}

.detail-error-icon {
  font-size: 40px;
  color: var(--el-color-danger);
}

.detail-error-title {
  margin: 12px 0 4px;
  font-size: 16px;
  font-weight: bold;
  color: var(--el-text-color-primary);
}

.detail-error-desc {
  max-width: 640px;
  margin: 0 auto 16px;
  font-size: 13px;
  line-height: 1.7;
  color: var(--el-text-color-secondary);
}

.detail-skeleton {
  margin-top: 20px;
}

.detail-skeleton .el-col {
  margin-bottom: 16px;
}

.control-alert {
  margin-bottom: 20px;
}

.stat-card {
  position: relative;
  overflow: hidden;
}

.stat-card .stat-icon {
  position: absolute;
  right: 20px;
  top: 50%;
  transform: translateY(-50%);
  font-size: 48px;
  opacity: 0.3;
  /* 2026-10-21 审计 C-ops P2：装饰图标是 absolute + opacity .3，改前绘在文字层之上，
     且与 value/label 布局盒相交（393 下「全局」chip 有 19.5px 落在图标下面）。
     两道保险：① 文字层 z-index 更高 + 图标 pointer-events:none，永不遮字/挡点击；
     ② .stat-content 右侧留出图标宽度（下面 padding-right），从几何上不再相交。 */
  z-index: 0;
  pointer-events: none;
}

.stat-card.http .stat-icon { color: var(--el-color-primary); }
.stat-card.device .stat-icon { color: var(--el-color-success); }
.stat-card.collector .stat-icon { color: var(--el-color-warning); }
.stat-card.data .stat-icon { color: var(--el-text-color-secondary); }

.stat-card :deep(.el-card__body) {
  display: flex;
  align-items: center;
  padding: 20px;
}

.stat-content {
  flex: 1;
  min-width: 0;
  /* 给右上角装饰图标留出真实布局空间（icon 48px 宽、right 20px ⇒ 距内容区右界 48px）。
     不留位时 el 盒会与图标相交；393 下范围词「全局」实测 19.5px 被图标压住。 */
  padding-right: 52px;
  position: relative;
  z-index: 1;
}

.stat-value {
  font-size: 28px;
  font-weight: bold;
  color: var(--el-text-color-primary);
}

.stat-value .online {
  color: var(--el-color-success);
}

.stat-value .online-none {
  color: var(--el-color-danger);
}

.stat-value .separator {
  margin: 0 4px;
  color: var(--el-text-color-secondary);
}

.stat-label {
  font-size: 14px;
  color: var(--el-text-color-secondary);
  margin-top: 4px;
}

/**
 * 范围词（§4.3 统计卡 MUST）。
 * 移动端 2×2 栅格下卡宽仅约 165px，范围词因此另起一行并禁止逐字断行（§4.2.5）。
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

.footer-info {
  margin-top: 20px;
  text-align: center;
  color: var(--el-text-color-secondary);
  font-size: 14px;
}

.footer-stale {
  color: var(--el-color-danger);
}

@media (max-width: 768px) {
  /* 页头窄屏契约：PageHeader 的 .page-header 是 flex 且**默认不换行**，而本页 #extra
     里有 select + button 两个控件。沿用原 .toolbar 在 768px 的"标题独占一行、操作区
     换到下一行"行为，避免操作区与标题在同一行互相挤压（390px 下标题 + 130px select
     已占满内容区）。范式同 views/node/NodeDetail.vue 的
     .collector-detail :deep(.page-header) { flex-wrap: wrap }。
     :deep 在本页可行：PageHeader 根节点会继承父组件 scope id（Vue 3 scoped 规则）。 */
  .monitor-container :deep(.page-header) {
    flex-wrap: wrap;
  }
  .monitor-container :deep(.page-header-left) {
    flex: 1;
    min-width: 0;
  }
  .monitor-container :deep(.page-header-right) {
    width: 100%;
    justify-content: flex-end;
    gap: 6px;
  }
  .toolbar-actions {
    flex-wrap: wrap;
    /* 2026-10-21 审计 C-ops P2：操作区必须占满 .page-header-right 的第二行，
       改前按 max-content 收缩（393 实测 113px vs 父 313px），右侧大片空白。 */
    width: 100%;
  }
  .toolbar-actions .el-select {
    flex: 1;
  }
  .stat-card :deep(.el-card__body) {
    padding: 16px;
  }
  .stat-card .stat-icon {
    font-size: 36px;
    right: 12px;
  }
  /* 窄屏图标 36px + right 12px ⇒ 距内容区右界 32px；留 40px 保证不压「全局」chip */
  .stat-content {
    padding-right: 40px;
  }
  .stat-value {
    font-size: 22px;
  }
  .stat-cards .el-col {
    margin-bottom: 12px;
  }
  /* 窄卡里范围词换行显示，避免与标签挤在同一行被压成逐字竖排 */
  .stat-scope {
    margin-left: 0;
    margin-top: 2px;
  }
}

@media (max-width: 480px) {
  /* 2026-10-21 审计 C-ops P2：改前这里把操作区改成纵向拉伸（每控件各占一整行），
     实测页头 159px（851 视口的 19%）且操作区仍只有 max-content 宽。
     改为**整行铺满的横向一行**：选择器弹性占满剩余宽度，刷新按钮保持内容宽。 */
  /* 保留 PageHeader 既有的 flex-end 对齐契约（Monitor.spec 390px 用例锁定）；
     操作区自身 width:100% 后，右对齐与否都不影响"铺满一行"。 */
  .toolbar-actions {
    width: 100%;
    flex-direction: row;
    flex-wrap: nowrap;
    align-items: center;
  }
  .toolbar-actions :deep(.el-select) {
    flex: 1 1 auto;
    width: auto;
    min-width: 0;
  }
  .toolbar-actions :deep(.el-button) {
    flex: 0 0 auto;
    width: auto;
  }
  /* 原 .toolbar h2 { font-size: 20px } 已删除：标题现由 PageHeader 承担，
     其自身窄屏规则已把 h2 收敛到 18px（组件内 @media (max-width: 768px)）。 */
  .stat-card :deep(.el-card__body) {
    padding: 16px;
  }
  .stat-value {
    font-size: 24px;
  }
  .stat-icon {
    font-size: 36px;
  }
}
</style>
