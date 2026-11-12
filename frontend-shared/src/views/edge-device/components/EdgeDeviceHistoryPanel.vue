<!--
  历史设备视图（已删除但数据仍保留）：固定表格 + 概览 + 空态 + 分页。

  为什么切成子组件：这一整块只依赖「历史列表」的 8 个只读值，交互全部是单向动作
  （重试 / 翻页 / 恢复 / 彻底删除 / 打开恢复弹窗）。用 props 进、emit 出之后，
  父页面不再渲染这 110 行表格，而历史状态的归属仍在 useEdgeDeviceHistory（唯一真源）。

  注意：el-table 的 ref 留在这里 —— 全选/清空要同步表格内部勾选状态，
  所以通过 defineExpose 暴露两个命令式入口，由页面协调（见 EdgeDeviceList.vue 的 selectAllPage）。
-->
<template>
  <el-alert type="info" :closable="false" show-icon class="history-notice" data-test="history-notice">
    <template #title>
      历史设备 = 已删除但数据仍保留的设备（默认删除不删数据）。「恢复」可重新投入使用
      （id 不变，历史连续）；「彻底删除」会删除记录与<strong>全部历史数据</strong>，
      由后台分批清理，<strong>开始后不可恢复</strong>。
    </template>
  </el-alert>

  <div class="history-overview" data-test="history-overview">
    共 <strong>{{ total }}</strong> 台历史设备
    <span v-if="purgingCount > 0">（本页 <strong>{{ purgingCount }}</strong> 台已提交/清理中）</span>
  </div>

  <el-alert v-if="error" type="error" :closable="false" show-icon class="history-error" data-test="history-error">
    <template #title>
      <span>历史设备加载失败：{{ error }}</span>
      <el-button link type="primary" size="small" data-test="history-retry" @click="emit('retry')">重试</el-button>
    </template>
  </el-alert>

  <el-card v-if="!error" shadow="hover" data-test="history-card">
    <div class="mobile-table-wrapper">
      <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
      <el-table
        ref="tableRef"
        :data="items"
        v-loading="loading"
        row-key="id"
        stripe
        data-test="history-table"
        @selection-change="onSelectionChange"
      >
        <el-table-column type="selection" width="50" />
        <el-table-column prop="id" label="ID" width="60" />
        <el-table-column prop="name" label="名称" min-width="140" show-overflow-tooltip />
        <el-table-column label="型号" width="130">
          <template #default="{ row }">
            <el-tag size="small">{{ getDeviceTypeLabel(asDeletedDevice(row).device_type) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="原节点·通道" width="180" show-overflow-tooltip>
          <template #default="{ row }">
            {{ asDeletedDevice(row).node_id || '—' }} / #{{ asDeletedDevice(row).channel_id }}
          </template>
        </el-table-column>
        <el-table-column label="数据量" width="120">
          <template #default="{ row }">{{ formatHistoryRows(asDeletedDevice(row)) }}</template>
        </el-table-column>
        <el-table-column label="最后采集" width="120">
          <template #default="{ row }">{{ formatRelativeTime(asDeletedDevice(row).last_data_at) }}</template>
        </el-table-column>
        <el-table-column label="删除时间" width="170">
          <template #default="{ row }">{{ formatHistoryDeletedAt(asDeletedDevice(row)) }}</template>
        </el-table-column>
        <el-table-column label="清理状态" width="160">
          <template #default="{ row }">
            <el-tag :type="historyStatusTagType(asDeletedDevice(row))" size="small" data-test="history-status">
              {{ historyStatusLabel(asDeletedDevice(row)) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="170" fixed="right">
          <template #default="{ row }">
            <el-button
              size="small"
              class="touch-target"
              :disabled="!canRestoreHistory(asDeletedDevice(row)) || deleting"
              :title="restoreDisabledReason(asDeletedDevice(row))"
              :aria-label="`恢复 ${asDeletedDevice(row).name}`"
              data-test="history-restore"
              @click="onRestore(asDeletedDevice(row))"
            >恢复</el-button>
            <el-button
              size="small"
              type="danger"
              text
              class="touch-target"
              :disabled="deleting"
              :aria-label="`彻底删除 ${asDeletedDevice(row).name}`"
              data-test="history-delete"
              @click="onDeleteRow(asDeletedDevice(row))"
            >彻底删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>
  </el-card>

  <!-- 空态：说明历史设备是什么、怎么产生、如何恢复 -->
  <div v-if="!loading && !error && items.length === 0" class="history-empty" data-test="history-empty">
    <el-icon :size="32"><RefreshLeft /></el-icon>
    <h3>暂无历史设备</h3>
    <p>在役设备被「删除」后会进入这里：记录变为历史设备，<strong>数据默认保留</strong>，可随时恢复。</p>
    <p>只有删除时勾选「同时删除历史数据」，数据才会在后台清理；清理开始后不可恢复。</p>
    <p>也可以对历史设备执行「彻底删除」：记录与全部历史数据由后台分批清理，<strong>不可恢复</strong>。</p>
    <el-button type="primary" size="small" data-test="history-empty-restore" @click="emit('open-restore')">
      打开「恢复已删除设备」
    </el-button>
  </div>

  <div v-if="total > 0" class="pagination-wrapper" data-test="history-pagination">
    <el-pagination
      :current-page="page"
      :page-size="pageSize"
      :total="total"
      layout="total, prev, pager, next"
      @current-change="onPageChange"
    />
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { RefreshLeft } from '@element-plus/icons-vue'
import type { DeletedEdgeDevice } from '@/api/edgeDevice'
import {
  asDeletedDevice,
  canRestoreHistory,
  formatHistoryDeletedAt,
  formatHistoryRows,
  historyStatusLabel,
  historyStatusTagType,
  restoreDisabledReason,
} from '../composables/useEdgeDeviceHistory'
import { formatRelativeTime, getDeviceTypeLabel } from '../composables/useDeviceListPresentation'

const props = defineProps<{
  items: DeletedEdgeDevice[]
  total: number
  page: number
  pageSize: number
  loading: boolean
  error: string
  purgingCount: number
  deleting: boolean
}>()

const emit = defineEmits<{
  (e: 'retry'): void
  (e: 'page-change', page: number): void
  (e: 'selection-change', rows: DeletedEdgeDevice[]): void
  (e: 'restore', row: DeletedEdgeDevice): void
  (e: 'delete-row', row: DeletedEdgeDevice): void
  (e: 'open-restore'): void
}>()

// el-table 实例只在本组件内使用；页面通过下面的 defineExpose 命令式调用，
// 而不是把 ref 传进来（传 ref 等于让父组件伸手进子组件内部）。
const tableRef = ref<any>()

const onSelectionChange = (rows: DeletedEdgeDevice[]) => emit('selection-change', rows)
const onPageChange = (next: number) => emit('page-change', next)
const onRestore = (row: DeletedEdgeDevice) => emit('restore', row)
const onDeleteRow = (row: DeletedEdgeDevice) => emit('delete-row', row)

defineExpose({
  /** 清空表格内部勾选（页面「取消选择」与切换视图时调用）。 */
  clearTableSelection: () => { tableRef.value?.clearSelection?.() },
  /** 全选本页：返回 false 表示没有表格实例，调用方退回「直接赋值选择集」。 */
  selectAllRows: () => {
    const table = tableRef.value
    if (!table) return false
    table.clearSelection?.()
    for (const row of props.items) table.toggleRowSelection?.(row, true)
    return true
  },
})
</script>

<style scoped>
/* ── 历史设备视图（随模板片段从 EdgeDeviceList.vue 搬入）──
   父组件的 scoped 规则打不进子组件内部 DOM：片段搬走时样式必须跟着走，
   否则 .history-empty 的虚线框与居中会静默消失。 */
.history-notice {
  margin-bottom: 0;
}
.history-overview {
  font-size: 13px;
  color: var(--el-text-color-regular);
}
.history-error {
  margin-bottom: 0;
}
.history-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 32px 16px;
  text-align: center;
  border: 1px dashed var(--el-border-color);
  border-radius: 12px;
  background: var(--el-fill-color-lighter);
  color: var(--el-text-color-regular);
}
.history-empty h3 {
  margin: 0;
  font-size: 16px;
  color: var(--el-text-color-primary);
}
.history-empty p {
  margin: 0;
  font-size: 13px;
  line-height: 1.7;
  max-width: 640px;
}

/* 分页器容器：本组件自己渲染分页，父组件的同名规则不作用到这里，故一并搬入。 */
.pagination-wrapper {
  display: flex;
  justify-content: center;
  padding: 20px 0;
}
</style>
