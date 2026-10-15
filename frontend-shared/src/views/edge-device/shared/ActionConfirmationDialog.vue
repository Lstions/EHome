<template>
  <el-dialog :model-value="visible" title="确认操作" width="420px" :close-on-click-modal="false" @update:model-value="emit('update:visible', $event)">
    <p class="confirm-text">{{ definition?.name || '该操作' }} 将向设备发送指令。</p>
    <p v-if="isElevated" class="risk">该操作为风险操作，确认后立即执行。</p>
    <template #footer>
      <el-button @click="emit('update:visible', false)">取消</el-button>
      <el-button :type="isElevated ? 'danger' : 'primary'" @click="emit('confirm')">确认</el-button>
    </template>
  </el-dialog>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import type { ActionDefinition } from '@/api/deviceOperation'
const props = defineProps<{ visible: boolean; definition: ActionDefinition | null }>()
// 风险样式来自后端下发的 2 级契约（risk_level），前端不持有 risk 字面量判定。
const isElevated = computed(() => props.definition?.risk_level === 'elevated')
const emit = defineEmits<{ 'update:visible': [value: boolean]; confirm: [] }>()
</script>
<style scoped>
.confirm-text{margin:0 0 8px;color:var(--el-text-color-primary)}
.risk{margin:0;color:var(--el-color-danger)}
</style>
