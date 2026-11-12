<template>
  <!--
    2026-11-12 纯搬移：原 Dashboard.vue「节点状态变化」卡的整块模板（el-card / 时间线 /
    两种空态 / 状态条数提示）原样搬入本组件，标签、class、文案、data-test 一字未改。
    外层 el-row :gutter="20" style="margin-top: 20px" 与 el-col 属页面级栅格，留在页面。
    边界依据见文件末的「为什么这块可以搬」。
  -->
  <el-card shadow="hover">
    <template #header>
      <div style="display: flex; justify-content: space-between; align-items: center;">
        <span>节点状态变化</span>
        <span class="status-history-hint">{{ hint }}</span>
      </div>
    </template>
    <el-timeline v-if="events.length > 0">
      <el-timeline-item
        v-for="event in events"
        :key="event.id"
        :timestamp="formatTime(event.created_at)"
        :type="event.new_status === 'online' ? 'success' : 'warning'"
      >
        <strong>{{ event.node_name || event.node_id }}</strong>
        <el-tag size="small" :type="event.new_status === 'online' ? 'success' : 'info'" style="margin-left: 8px;">
          {{ event.new_status === 'online' ? '在线' : '离线' }}
        </el-tag>
      </el-timeline-item>
    </el-timeline>
    <EmptyState
      v-else-if="unknown"
      kind="error"
      icon="WarningFilled"
      size="small"
      data-test="dashboard-history-error"
      title="状态变化记录加载失败"
      description="无法确认节点是否发生过状态变化。"
      :quick-actions="[
        { label: '重试', type: 'primary', handler: () => emit('retry') }
      ]"
    />
    <EmptyState
      v-else
      icon="Connection"
      size="small"
      title="暂无状态变化记录"
      description="节点首次上线、恢复或离线后，状态变化会显示在这里。"
    />
  </el-card>
</template>

<script setup lang="ts">
import EmptyState from '@/components/common/EmptyState.vue'
import { formatTime } from '../dashboardFormat'

/**
 * 事件的字段与 composables/useDashboardOverview.ts 的 NodeStatusEvent 结构一致。
 * 这里刻意**不 import 该类型**：子组件不应反向依赖页面级 composable（那会让
 * 「卡」与「取数」重新耦合）；结构相同即兼容，改字段时两处一起改制即可。
 */
defineProps<{
  events: Array<{ id: number; node_id: string; node_name?: string; new_status: string; created_at: string }>
  unknown: boolean
  hint: string
}>()

/** 错误态「重试」：原页面直接绑 retryLoad，搬出后由页面回接同一函数。 */
const emit = defineEmits<{ (e: 'retry'): void }>()
</script>

<style scoped>
/*
 * 随模板片段一起搬走的页面 scoped 规则：父组件的 scoped 样式打不进子组件内部 DOM，
 * 不跟着搬会让 .status-history-hint 变回默认黑色/字号。
 */
.status-history-hint {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
</style>

<!--
  为什么这块可以搬（与「为什么别处不能搬」对照）：
    · 无源码文本门禁点名本卡：骨架几何门禁只认统计卡、TimeRangeSelectorUnified 只认趋势卡、
      MobileTableWrapper 的分母是「真的出现表格元素」的 .vue（本卡用的是时间线，不是表格，
      注释里也不要写出该标签原文，否则会被 grep 口径误收）、IconActionAccessibilityGate
      的登记项只覆盖异常摘要卡里的三个 @click、UiAuditSystemicFixes 只认趋势自愈与最新数据卡；
    · 本卡的数据输入只有 events / unknown / hint，动作输出只有 retry，
      没有任何页面级路由参数、分页或 PageHeader 依赖。
-->
