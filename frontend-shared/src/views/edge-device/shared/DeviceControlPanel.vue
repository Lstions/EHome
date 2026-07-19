<template>
  <el-card class="panel" shadow="hover" data-testid="operation-buttons">
    <template #header><div class="header"><span>受控操作</span><el-tag v-if="!ws.isConnected" type="info" size="small">实时连接断开</el-tag></div></template>
    <el-alert v-if="available.length === 0" type="info" :closable="false" title="当前没有可执行的受控操作" />
    <div v-else class="actions"><el-button v-for="action in available" :key="action.definition.id" type="primary" :loading="submitting === action.definition.id" @click="begin(action.definition)">{{ action.definition.name }}</el-button></div>
    <el-collapse v-if="unavailable.length" class="unavailable"><el-collapse-item title="不可用操作" name="unavailable"><p v-for="action in unavailable" :key="action.definition.id">{{ action.definition.name }}：{{ availabilityReason(action) }}</p></el-collapse-item></el-collapse>
    <el-divider>操作历史</el-divider>
    <el-empty v-if="history.length === 0" description="暂无操作记录" :image-size="64" />
    <el-timeline v-else><el-timeline-item v-for="operation in history" :key="operation.command_id" :type="timelineType(operation.status)" :timestamp="format(operation.updated_at)"><strong>{{ operation.action_id }}</strong><span class="status">{{ operation.status }}</span><span v-if="operation.final_reason"> · {{ operation.final_reason }}</span><span v-if="resultSummary(operation)" class="result"> · {{ resultSummary(operation) }}</span></el-timeline-item></el-timeline>
  </el-card>
  <ActionForm v-model:visible="formVisible" :definition="selectedAction" @submit="submitSelected" />
  <ActionConfirmationDialog v-model:visible="confirmationVisible" :definition="selectedAction" @confirm="confirmSelected" />
</template>
<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { useDeviceOperationStore } from '@/stores/deviceOperation'
import { useWebSocketStore } from '@/stores/websocket'
import type { ActionDefinition, DeviceOperation, EffectiveAction, OperationStatus } from '@/api/deviceOperation'
import ActionForm from './ActionForm.vue'
import ActionConfirmationDialog from './ActionConfirmationDialog.vue'
const props = defineProps<{ deviceId: number }>()
const store = useDeviceOperationStore(); const ws = useWebSocketStore(); const submitting = ref(''); const selectedAction = ref<ActionDefinition | null>(null); const selectedParams = ref<Record<string, unknown>>({}); const formVisible = ref(false); const confirmationVisible = ref(false)
const catalog = computed(() => store.catalogs.get(props.deviceId) ?? []); const history = computed(() => store.histories.get(props.deviceId) ?? [])
const available = computed(() => catalog.value.filter(item => !availabilityReason(item))); const unavailable = computed(() => catalog.value.filter(item => !!availabilityReason(item)))
async function load() { if (props.deviceId > 0) await store.refresh(props.deviceId) }
function availabilityReason(action: EffectiveAction) { if (!action.available) return action.reason || '当前不可用'; const fields = Object.values(action.definition.input_schema?.properties ?? {}); if (fields.some(field => !['string', 'boolean', 'integer', 'number'].includes(field.type) || field.enum && field.type !== 'string')) return '客户端不支持该参数 Schema'; return '' }
function requiresConfirmation(action: ActionDefinition) { return action.risk === 'medium' || action.risk === 'high' || action.risk === 'critical' }
function begin(action: ActionDefinition) { selectedAction.value = action; const fields = Object.keys(action.input_schema?.properties ?? {}); if (fields.length === 0) { prepare({}); return }; formVisible.value = true }
async function submitSelected(params: Record<string, unknown>) { formVisible.value = false; prepare(params) }
function prepare(params: Record<string, unknown>) { if (!selectedAction.value) return; selectedParams.value = params; if (requiresConfirmation(selectedAction.value)) { confirmationVisible.value = true; return }; void submit(selectedAction.value.id, params) }
async function confirmSelected(reason: string) { if (!selectedAction.value) return; const action = selectedAction.value; confirmationVisible.value = false; submitting.value = action.id; try { const grant = await store.confirm(props.deviceId, action.id, selectedParams.value, reason); await store.create(props.deviceId, action.id, selectedParams.value, grant.token, reason); ElMessage.info('操作已排队') } catch (error: any) { ElMessage.error(error?.message || '确认或创建操作失败') } finally { submitting.value = '' } }
async function submit(actionId: string, params: Record<string, unknown>) { submitting.value = actionId; try { await store.create(props.deviceId, actionId, params); ElMessage.info('操作已排队') } catch (error: any) { ElMessage.error(error?.message || '创建操作失败') } finally { submitting.value = '' } }
function timelineType(status: OperationStatus) { return status === 'SUCCEEDED' ? 'success' : status === 'FAILED' || status === 'UNKNOWN' ? 'danger' : status === 'CANCELLED' ? 'info' : 'primary' }
function format(value: string) { return value ? new Date(value).toLocaleString() : '' }
function resultSummary(operation: DeviceOperation) { return operation.verified_result?.map(value => `${value.name}=${value.string_value || value.value}${value.unit || ''}`).join(', ') || '' }
const offEvent = ws.subscribe('device_operation_update', message => { const operation = (message.payload ?? message.data) as DeviceOperation | undefined; if (operation?.edge_device_id === props.deviceId) store.apply(operation) })
const offConnected = typeof ws.onConnected === 'function' ? ws.onConnected(load) : () => {}
watch(() => props.deviceId, load); onMounted(load); onUnmounted(() => { offEvent(); offConnected() })
</script>
<style scoped>.panel{margin-top:20px}.header{display:flex;justify-content:space-between;align-items:center}.actions{display:flex;gap:8px;flex-wrap:wrap}.unavailable{margin-top:12px}.unavailable p{margin:6px 0;color:var(--el-text-color-secondary)}.status{margin-left:8px}.result{color:var(--el-color-success-dark-2)}</style>
