<template>
    <el-row class="control-health" :class="{ 'is-stale': !metricsReady }">
      <el-col :span="24">
        <el-card shadow="hover">
          <template #header>
            <div class="card-header">
              <span><el-icon><Operation /></el-icon> 控制面健康</span>
              <el-tag :type="tag.type" size="small" data-test="monitor-control-tag">
                {{ tag.text }}
              </el-tag>
            </div>
          </template>
          <div class="control-grid">
            <div class="control-metric"><span>操作总数</span><strong>{{ metricText(metrics?.control?.operations_total, true) }}</strong></div>
            <div class="control-metric"><span>活跃操作</span><strong>{{ metric(metrics?.control?.active) }}</strong></div>
            <div class="control-metric">
              <el-tooltip content="Outbox 待处理：已写入待发送队列、等待投递的控制消息数" placement="top">
                <span class="term">Outbox 待处理</span>
              </el-tooltip>
              <strong>{{ metric(metrics?.control?.outbox_pending) }}</strong>
            </div>
            <div class="control-metric">
              <el-tooltip content="Outbox 租约中：已被投递任务领取（租约锁定）、正在发送中的消息数" placement="top">
                <span class="term">Outbox 租约中</span>
              </el-tooltip>
              <strong>{{ metric(metrics?.control?.outbox_leased) }}</strong>
            </div>
            <div class="control-metric" :class="{ attention: (metrics?.control?.unresolved_unknown || 0) > 0 }">
              <el-tooltip content="执行结果未知（UNKNOWN）且尚未人工处置的操作数" placement="top">
                <span class="term">未处置 UNKNOWN</span>
              </el-tooltip>
              <strong>{{ metric(metrics?.control?.unresolved_unknown) }}</strong>
            </div>
            <div class="control-metric" :class="{ attention: (metrics?.control?.capability_stale_nodes || 0) > 0 }">
              <el-tooltip content="能力快照超过有效期未上报的节点数" placement="top">
                <span class="term">能力快照过期</span>
              </el-tooltip>
              <strong>{{ metric(metrics?.control?.capability_stale_nodes) }}</strong>
            </div>
            <div class="control-metric" :class="{ attention: (metrics?.control?.audit_write_failures || 0) > 0 }">
              <el-tooltip content="审计日志写入失败的次数" placement="top">
                <span class="term">审计写失败</span>
              </el-tooltip>
              <strong>{{ metric(metrics?.control?.audit_write_failures) }}</strong>
            </div>
            <div class="control-metric"><span>操作成功</span><strong>{{ metric(metrics?.control?.succeeded) }}</strong></div>
            <!-- 2026-10-21 审计 C-ops P1：失败数是**进程启动以来的累计计数器**，与
                 「未处置 UNKNOWN / 能力快照过期 / 审计写失败」这三个**待处置条件**不同口径。
                 改前给它加 attention 红字却不计入 controlAttention ⇒ 同卡出现"横幅说 1 项、
                 红字却是 6"的自相矛盾。现改为中性计数 + tooltip 说明口径，红字只保留给
                 真正计入「需关注」的条件项。detailed 理由见审计修复报告。 -->
            <div class="control-metric">
              <el-tooltip content="累计计数：进程启动以来操作失败的次数（含历史已排查项），不是待处置条件，不计入「需关注」；只有未处置 UNKNOWN / 能力快照过期 / 审计写失败 才计入。" placement="top">
                <span class="term">操作失败</span>
              </el-tooltip>
              <strong>{{ metric(metrics?.control?.failed) }}</strong>
            </div>
          </div>
        </el-card>
      </el-col>
    </el-row>
</template>

<script setup lang="ts">
/**
 * 「控制面健康」卡（原 Monitor.vue 的 123~182 行整块搬入，DOM / class / data-test /
 * 文案 / tooltip 一字未改）。
 *
 * 边界依据（为什么这块可以搬）：
 *   · 自包含：一处 el-card + 九个 .control-metric 读数，没有页面级编排、没有请求、
 *     没有 v-if 分支（失败时由 metricsReady 让整卡进入未知态，判据经 props 下传）；
 *   · 卡片自己的口径（「需关注 / 正常 / 状态未知」标签）不在这里重算 —— 派生值由页面
 *     唯一决定，避免「横幅计数」与「卡内红字」出现两个口径（审计 C-ops P1 的教训）；
 *   · 样式（.control-health / .control-grid / .control-metric*）随卡片一起搬，
 *     页面对这些选择器已无引用（搬完 grep 确认无孤儿选择器）。
 */
import { Operation } from '@element-plus/icons-vue'
import type { MetricsSummary } from '@/api/monitor'
import { metricOf, metricTextOf } from '../monitorDisplay'

const props = defineProps<{
  /** 指标摘要；null = 尚未拉取（渲染「—」而不是 0） */
  metrics: MetricsSummary | null
  /** 指标是否可信（接口成功且未失败）：决定整卡是否降为未知态 */
  metricsReady: boolean
  /** 「状态未知 / 需关注 / 正常」标签，口径由页面 useMonitor.controlHealthTag 唯一决定 */
  tag: { type: 'info' | 'warning' | 'success'; text: string }
}>()

/** 取值口径：与页面、详细面板共用 monitorDisplay.metricOf（未就绪一律「—」）。 */
const metric = (value: number | null | undefined, isCounter = false) =>
  metricOf(value, props.metricsReady, isCounter)
const metricText = (value: number | null | undefined, isCounter = false) =>
  metricTextOf(value, props.metricsReady, isCounter)
</script>

<style scoped>
/* 控制面健康卡样式：原先写在 Monitor.vue 的 scoped 样式里，随片段一起搬。
   .card-header 被「详细面板」共用，收在 monitor.shared.css（见文件末尾最后一个 style 块）。 */
.control-health {
  margin-bottom: 20px;
}

.control-health .card-header {
  justify-content: space-between;
}

.control-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 8px;
}

.control-metric {
  min-width: 0;
  padding: 12px 14px;
  display: flex;
  flex-direction: column;
  gap: 6px;
  background: var(--el-fill-color-light);
  border-radius: 8px;
}

.control-metric span {
  color: var(--el-text-color-secondary);
  font-size: 13px;
}

.control-metric .term {
  cursor: help;
  text-decoration: underline dotted var(--el-text-color-placeholder);
  text-underline-offset: 3px;
}

.control-metric strong {
  font-size: 22px;
  overflow-wrap: anywhere;
}

.control-metric.attention strong {
  color: var(--el-color-danger);
}

/* 接口未成功：整卡未知态，attention 红字同样降为中性（不能一边「—」一边报警） */
.control-health.is-stale .control-metric.attention strong {
  color: var(--el-text-color-secondary);
}

@media (max-width: 900px) {
  .control-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
</style>

<style scoped src="./monitor.shared.css"></style>
