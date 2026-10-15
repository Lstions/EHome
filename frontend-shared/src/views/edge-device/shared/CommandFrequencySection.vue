<template>
  <el-card
    v-if="deviceId"
    class="command-frequency-card"
    :class="{ 'embedded-card': embedded }"
    :shadow="embedded ? 'never' : 'hover'"
  >
    <!-- 嵌入模式（移动端折叠面板内）：外层折叠标题已说明用途，不再重复"指令频率"卡片头 -->
    <template v-if="!embedded" #header><span>指令频率</span></template>
    <CommandList
      :device-id="deviceId"
      :embedded="embedded"
      :node-id="nodeId"
      :max-polling-commands="maxPollingCommands"
    />
  </el-card>
</template>

<script setup lang="ts">
import CommandList from '@/components/device/CommandList.vue'

defineProps<{
  deviceId: number
  embedded?: boolean
  /** 所属节点数值 id —— 透传给 CommandList，用于保存后确认节点是否已应用（2026-10-15） */
  nodeId?: number | string
  /** 后端轮询指令条数上限 —— 透传给 CommandList，用于保存前的事前容量约束（2026-10-15） */
  maxPollingCommands?: number
}>()
</script>

<style scoped>
.command-frequency-card {
  margin-top: 20px;
}

.command-frequency-card.embedded-card {
  margin-top: 0;
  border: 0;
  box-shadow: none;
}

/* 嵌入模式：无卡片头，body 去掉多余内边距，内容直接贴合折叠面板 */
.command-frequency-card.embedded-card :deep(.el-card__body) {
  padding: 4px 0 0;
}
</style>
