<template>
  <!-- key 用 id（后端稳定合成）；WS 实时行 id 为 0 ⇒ 回落行号 -->
  <div v-for="(row, rowIndex) in rows" :key="row.id || rowIndex" class="data-card">
    <div class="data-card-head">
      <span class="data-card-time">{{ rowTimeText(row) }}</span>
    </div>
    <div class="data-card-metrics">
      <span v-for="metric in summarizeRow(row, categories)" :key="metric.key" class="data-card-metric">
        <span class="metric-label">{{ metric.label }}</span>
        <span class="metric-value">{{ metric.value }}<i v-if="metric.unit" class="metric-unit">{{ metric.unit }}</i></span>
      </span>
      <span v-if="summarizeRow(row, categories).length === 0" class="data-card-metric muted">无可解析数值</span>
    </div>
    <div class="data-card-foot">
      <el-button
        link
        type="primary"
        size="small"
        data-testid="row-expand"
        @click="emit('expand', row)"
      >
        查看完整数据
      </el-button>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * 移动端历史数据卡片列表（原 DataPanel.vue 的「摘要 3~4 个关键指标 + 查看完整数据」段，纯搬移）。
 *
 * 为什么单独成组件：这一段的存在理由是**布局缺陷**（393 下 80px 的数据列装 638 字符 ⇒
 * 单行 2041px、整页 42689px），它自己带一整套 .data-card* 样式与摘要口径；留在页面里
 * 和桌面表格（el-table）挤在同一个 v-if/v-else 分支里，两者任一改动都要重新读全段。
 *
 * 分页器**不在这里**：顶部那条精简分页器留在页面（listPageConventions 门禁按页面文件
 * 登记列表页；分页绑定必须留在页面），本组件只负责卡片本身。
 *
 * 注意：本组件是多根节点（v-for 直接作根），不要在外面套一层 div —— 那会改变
 * .data-card-list 的 flex 子项结构与拆分前的 DOM。
 */
import { rowTimeText, summarizeRow } from '../rowDisplay'
import type { CollectedRow } from '../historyRows'
import type { MeasurementCategory } from '../types'

defineProps<{
  /** 当前页的行（一次采集一行）。 */
  rows: CollectedRow[]
  /** 设备可用指标类别：摘要卡片的单位来源（原从页面 availableCategories 直接读）。 */
  categories: MeasurementCategory[]
}>()

const emit = defineEmits<{
  /** 「查看完整数据」：把整行交给页面的详情弹层（弹层归页面持有，避免两处各开一个）。 */
  (e: 'expand', row: CollectedRow): void
}>()
</script>

<style scoped>
/* 移动端历史数据卡片：摘要 3~4 个关键指标 + 「查看完整数据」（P0：80px 数据列不可用） */
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

/* 存量规则（.data-card-ok / .data-card-raw）：拆分前就已无 DOM 使用（状态/原始数据
   两段在 2026-11-11 原始数据退役时删掉）。按"纯搬移"原样跟随本段样式走，不顺手删改。 */
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

/* 选择器带 .data-card-list（页面上的列表容器）：scoped 只对**最后一段**加作用域属性，
   祖先类名仍按真实 DOM 匹配，因此搬进子组件后规则照旧生效。 */
.data-card-list .muted {
  color: var(--el-text-color-placeholder);
}
</style>
