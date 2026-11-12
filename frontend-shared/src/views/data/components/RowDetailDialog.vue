<template>
  <el-dialog
    :model-value="modelValue"
    title="单条数据完整内容"
    align-center
    width="92%"
    data-testid="row-detail-dialog"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div class="row-detail">
      <div class="row-detail-meta">
        <span>采集时间：{{ rowTimeText(row) }}</span>
      </div>
      <!-- F8：弹层内 4 列表最小宽 400px > 393 弹窗可视宽，必须自带横滚容器 + 提示。 -->
      <div class="mobile-table-wrapper">
        <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
        <el-table :data="rowDetailEntries(row, categories)" size="small" max-height="420" data-testid="row-detail-table">
          <el-table-column prop="label" label="指标" min-width="110" show-overflow-tooltip />
          <el-table-column prop="key" label="标识" min-width="110" show-overflow-tooltip />
          <el-table-column prop="value" label="值" width="110" align="right" />
          <el-table-column prop="unit" label="单位" width="70" align="center" />
        </el-table>
      </div>
      <p v-if="rowDetailEntries(row, categories).length === 0" class="row-detail-empty">
        该条数据无法解析出指标。
      </p>
    </div>
    <template #footer>
      <el-button type="primary" @click="emit('update:modelValue', false)">关闭</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
/**
 * 单条数据完整内容弹层（原 DataPanel.vue 的「查看完整数据」el-dialog，纯搬移）。
 *
 * 为什么单独成组件：它是页面里唯一一个"打开/关闭 + 一条行"的自包含交互块，
 * 与历史表/卡片的分页、取数都无关；留在页面时页面要同时持有 rowDetailVisible /
 * rowDetailRow 两个状态与三段模板，混在历史卡片分支里。
 *
 * 归属：**显隐状态仍由页面持有**（openRowDetail 还在页面），这里只做 props 进、emit 出，
 * 避免"页面与卡片各开一个弹层"两份真源。
 *
 * DOM 契约不变：data-testid="row-detail-dialog" 与内部 4 列表格的位置一字未动；
 * .mobile-table-wrapper / .mobile-table-hint 必须跟表格同文件（MobileTableWrapper 门禁
 * 按单文件判断包裹关系）。
 */
import { rowDetailEntries, rowTimeText } from '../rowDisplay'
import type { MeasurementCategory } from '../types'

defineProps<{
  /** 是否显示（页面用 v-model 绑定 rowDetailVisible）。 */
  modelValue: boolean
  /** 要展示的那一条采集行（页面 openRowDetail 存入）。 */
  row: Record<string, any> | null
  /** 设备可用指标类别：单位来源。 */
  categories: MeasurementCategory[]
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', value: boolean): void
}>()
</script>

<style scoped>
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
