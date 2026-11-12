<template>
  <!-- 命令执行详情（F18：命令 ID 此前点了没反应）。
       只读：展示该次执行的终态与（若有）失败原因/人工裁决，不提供任何写操作
       —— 审计入口的职责是"看清楚"，写操作在设备页的命令面板里。 -->
  <el-dialog
    v-model="visible"
    title="命令执行详情"
    width="min(560px, 92vw)"
    data-test="command-detail-dialog"
  >
    <div v-if="loading" aria-busy="true"><el-skeleton :rows="4" animated /></div>
    <el-descriptions v-else-if="detail" :column="1" border size="small">
      <el-descriptions-item label="命令 ID">
        <span class="mono">{{ detail.command_id }}</span>
      </el-descriptions-item>
      <el-descriptions-item label="执行状态">
        <el-tag size="small" :type="detail.status === 'SUCCEEDED' ? 'success' : detail.status === 'FAILED' ? 'danger' : 'info'">
          {{ operationStatusText(detail.status) }}
        </el-tag>
      </el-descriptions-item>
      <el-descriptions-item label="动作">
        {{ detail.action_id }} <span class="text-muted">v{{ detail.action_version }}</span>
      </el-descriptions-item>
      <el-descriptions-item label="设备">
        #{{ detail.edge_device_id }} <span class="text-muted">({{ detail.node_id }})</span>
      </el-descriptions-item>
      <el-descriptions-item v-if="detail.final_reason" label="失败原因">
        {{ detail.final_reason }}
      </el-descriptions-item>
      <el-descriptions-item v-if="detail.manual_resolution" label="人工裁决">
        {{ detail.manual_resolution.outcome }}：{{ detail.manual_resolution.reason }}
      </el-descriptions-item>
      <el-descriptions-item label="创建时间">{{ detail.created_at }}</el-descriptions-item>
      <el-descriptions-item v-if="detail.completed_at" label="完成时间">{{ detail.completed_at }}</el-descriptions-item>
    </el-descriptions>
    <template #footer>
      <el-button type="primary" @click="visible = false">关闭</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { DeviceOperation } from '@/api/deviceOperation'

/**
 * 命令执行详情（只读弹窗）。
 *
 * 职责：把一条命令（device-operation）的终态原样展示 —— 命令 ID、执行状态、动作与版本、
 * 设备与节点、失败原因、人工裁决、创建/完成时间。只读：不提供任何写操作，
 * 也不自己发请求（查询在页面的 goCommand 里，失败提示也由页面负责）。
 *
 * props:
 *   modelValue  显隐（v-model；页面在点击「命令 ID」时置 true）
 *   loading     查询中（显示骨架屏而不是空白弹窗）
 *   detail      执行记录；null = 尚未拿到（查询失败时页面会直接关掉弹窗）
 *
 * emits:
 *   update:modelValue  关闭弹窗
 *
 * 为什么这样切：
 *   · 它是审计入口，数据来自页面的单次查询（goCommand）—— 放这里不会带来任何
 *     数据加载职责，只是把 38 行模板从 3700 行的页面里挪出去；
 *   · 状态文案（operationStatusText）只被这个弹窗消费，跟模板一起搬，避免页面里
 *     留下一个「只有子组件用」的映射表；
 *   · 页面对它只有 v-model + 两个只读 prop，契约窄且不随列表侧继续拆分而变化。
 */
const props = defineProps<{
  modelValue: boolean
  loading: boolean
  detail: DeviceOperation | null
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', value: boolean): void
}>()

/** el-dialog 的 v-model 需要可写：读页面传入的显隐，写回 update 事件（不在子组件存副本）。 */
const visible = computed({
  get: () => props.modelValue,
  set: (value: boolean) => emit('update:modelValue', value),
})

/** 执行状态 → 中文（与后端 OperationStatus 枚举一一对应，不给未定义的兜底文案）。 */
function operationStatusText(status: string): string {
  const map: Record<string, string> = {
    QUEUED: '排队中',
    DISPATCHED: '已下发',
    DEVICE_ACCEPTED: '设备已受理',
    VERIFYING: '核验中',
    SUCCEEDED: '成功',
    FAILED: '失败',
    CANCELLED: '已取消',
    UNKNOWN: '结果未知',
  }
  return map[status] ?? status
}
</script>

<style scoped>
/* 原先取自 AutomationRules.vue 的 scoped 样式；scoped 不跨组件，随片段搬一份。 */
.mono { font-family: monospace; }
.text-muted { color: var(--el-text-color-secondary); }
</style>
