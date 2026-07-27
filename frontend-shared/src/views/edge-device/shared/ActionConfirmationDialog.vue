<template>
  <el-dialog :model-value="visible" title="确认受控操作" width="440px" :close-on-click-modal="false" @update:model-value="emit('update:visible', $event)">
    <el-alert type="warning" :closable="false" :title="`${definition?.name || '该操作'} 将向设备发送参数变更`" />
    <p class="risk">风险等级：{{ definition?.risk || 'unknown' }}。确认后会创建可审计操作，成功前仅显示排队状态。</p>
    <el-form label-position="top"><el-form-item label="操作理由" required><el-input v-model="reason" type="textarea" :rows="3" maxlength="512" show-word-limit placeholder="说明本次变更的目的与观察安排" /></el-form-item></el-form>
    <template #footer><el-button @click="emit('update:visible', false)">取消</el-button><el-button type="danger" :disabled="!reason.trim()" @click="confirm">确认并排队</el-button></template>
  </el-dialog>
</template>
<script setup lang="ts">
import { ref, watch } from 'vue'
import type { ActionDefinition } from '@/api/deviceOperation'
const props = defineProps<{ visible: boolean; definition: ActionDefinition | null }>()
const emit = defineEmits<{ 'update:visible': [value: boolean]; confirm: [reason: string] }>()
const reason = ref('')
function confirm() { if (reason.value.trim()) emit('confirm', reason.value.trim()) }
watch(() => [props.visible, props.definition?.id], () => { reason.value = '' }, { immediate: true })
</script>
<style scoped>.risk{margin:12px 0;color:var(--el-text-color-secondary)}</style>
