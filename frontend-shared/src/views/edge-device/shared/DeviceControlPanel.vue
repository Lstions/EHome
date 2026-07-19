<template>
  <el-card class="panel" shadow="hover" data-testid="operation-buttons">
    <template #header><div class="header"><span>受控操作</span><el-tag v-if="!ws.isConnected" type="info" size="small">实时连接断开</el-tag></div></template>
    <el-alert v-if="available.length === 0" type="info" :closable="false" title="当前没有可执行的受控操作" />
    <div v-else class="actions"><el-button v-for="action in available" :key="action.definition.id" type="primary" :loading="submitting === action.definition.id" @click="begin(action.definition)">{{ action.definition.name }}</el-button></div>
    <el-collapse v-if="unavailable.length" class="unavailable"><el-collapse-item title="不可用操作" name="unavailable"><p v-for="action in unavailable" :key="action.definition.id">{{ action.definition.name }}：{{ availabilityReason(action) }}</p></el-collapse-item></el-collapse>
    <el-divider>操作历史</el-divider>
    <el-empty v-if="history.length === 0" description="暂无操作记录" :image-size="64" />
    <el-timeline v-else><el-timeline-item v-for="operation in history" :key="operation.command_id" :type="timelineType(operation.status)" :timestamp="format(operation.manual_resolution?.resolved_at || operation.updated_at)"><div class="timeline-line"><strong>{{ operation.action_id }}</strong><span class="status">{{ operation.status }}</span><span v-if="operation.final_reason"> · {{ operation.final_reason }}</span><span v-if="resultSummary(operation)" class="result"> · {{ resultSummary(operation) }}</span><el-button v-if="operation.status === 'UNKNOWN' && !operation.manual_resolution" text type="warning" size="small" @click="beginResolution(operation)">人工处置</el-button></div><div v-if="operation.manual_resolution" class="resolution">人工结论：{{ resolutionLabel(operation.manual_resolution.outcome) }} · {{ operation.manual_resolution.reason }}</div></el-timeline-item></el-timeline>
  </el-card>
  <ActionForm v-model:visible="formVisible" :definition="selectedAction" @submit="submitSelected" />
  <ActionConfirmationDialog v-model:visible="confirmationVisible" :definition="selectedAction" @confirm="confirmSelected" />
  <el-dialog v-model="resolutionVisible" title="处置未知操作" width="480px" destroy-on-close>
    <el-form label-position="top">
      <el-form-item label="人工结论"><el-select v-model="resolutionOutcome" class="resolution-select"><el-option label="独立证据确认成功" value="CONFIRMED_SUCCEEDED" /><el-option label="独立证据确认失败" value="CONFIRMED_FAILED" /><el-option label="已复核，仍无法确认" value="ACKNOWLEDGED_UNKNOWN" /></el-select></el-form-item>
      <el-form-item label="处置理由"><el-input v-model="resolutionReason" type="textarea" :rows="3" maxlength="512" show-word-limit /></el-form-item>
    </el-form>
    <template #footer><el-button @click="resolutionVisible = false">取消</el-button><el-button type="primary" :loading="resolving" :disabled="!resolutionReason.trim()" @click="submitResolution">确认处置</el-button></template>
  </el-dialog>
</template>
<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { useDeviceOperationStore } from '@/stores/deviceOperation'
import { useWebSocketStore } from '@/stores/websocket'
import { newIdempotencyKey, type ActionDefinition, type DeviceOperation, type EffectiveAction, type ManualResolutionOutcome, type OperationStatus } from '@/api/deviceOperation'
import ActionForm from './ActionForm.vue'
import ActionConfirmationDialog from './ActionConfirmationDialog.vue'
const props = defineProps<{ deviceId: number }>()
const store = useDeviceOperationStore(); const ws = useWebSocketStore(); const submitting = ref(''); const selectedAction = ref<ActionDefinition | null>(null); const selectedParams = ref<Record<string, unknown>>({}); const formVisible = ref(false); const confirmationVisible = ref(false)
const catalog = computed(() => store.catalogs.get(props.deviceId) ?? []); const history = computed(() => store.histories.get(props.deviceId) ?? []); const selectedKey = ref('')
const resolutionVisible = ref(false); const resolving = ref(false); const resolutionOperation = ref<DeviceOperation | null>(null); const resolutionOutcome = ref<ManualResolutionOutcome>('ACKNOWLEDGED_UNKNOWN'); const resolutionReason = ref('')
const available = computed(() => catalog.value.filter(item => !availabilityReason(item))); const unavailable = computed(() => catalog.value.filter(item => !!availabilityReason(item)))
async function load() { if (props.deviceId > 0) await store.refresh(props.deviceId) }
function availabilityReason(action: EffectiveAction) { if (!action.available) return action.reason || '当前不可用'; const fields = Object.values(action.definition.input_schema?.properties ?? {}); if (fields.some(field => !['string', 'boolean', 'integer', 'number'].includes(field.type) || field.enum && field.type !== 'string')) return '客户端不支持该参数 Schema'; return '' }
function requiresConfirmation(action: ActionDefinition) { return action.risk === 'medium' || action.risk === 'high' || action.risk === 'critical' }
function begin(action: ActionDefinition) { selectedAction.value = action; const fields = Object.keys(action.input_schema?.properties ?? {}); if (fields.length === 0) { prepare({}); return }; formVisible.value = true }
async function submitSelected(params: Record<string, unknown>) { formVisible.value = false; prepare(params) }
function prepare(params: Record<string, unknown>) { if (!selectedAction.value) return; selectedParams.value = params; selectedKey.value = newIdempotencyKey(); if (requiresConfirmation(selectedAction.value)) { confirmationVisible.value = true; return }; void submit(selectedAction.value.id, params) }
async function confirmSelected(reason: string) { if (!selectedAction.value) return; const action = selectedAction.value; confirmationVisible.value = false; submitting.value = action.id; try { const grant = await store.confirm(props.deviceId, action.id, selectedParams.value, reason); await store.create(props.deviceId, action.id, selectedParams.value, grant.token, reason, selectedKey.value); ElMessage.info('操作已排队') } catch (error: any) { await load(); ElMessage.warning('操作结果未确认，已刷新状态；请勿重复提交') } finally { submitting.value = '' } }
async function submit(actionId: string, params: Record<string, unknown>) { submitting.value = actionId; try { await store.create(props.deviceId, actionId, params, '', '', selectedKey.value); ElMessage.info('操作已排队') } catch (error: any) { await load(); ElMessage.warning('操作结果未确认，已刷新状态；请勿重复提交') } finally { submitting.value = '' } }
function beginResolution(operation: DeviceOperation) { resolutionOperation.value = operation; resolutionOutcome.value = 'ACKNOWLEDGED_UNKNOWN'; resolutionReason.value = ''; resolutionVisible.value = true }
async function submitResolution() { if (!resolutionOperation.value || !resolutionReason.value.trim()) return; resolving.value = true; try { await store.resolve(resolutionOperation.value.command_id, resolutionOutcome.value, resolutionReason.value.trim()); resolutionVisible.value = false; ElMessage.success('人工处置已记录') } catch { await load(); ElMessage.warning('处置结果未确认，已刷新状态') } finally { resolving.value = false } }
function timelineType(status: OperationStatus) { return status === 'SUCCEEDED' ? 'success' : status === 'FAILED' || status === 'UNKNOWN' ? 'danger' : status === 'CANCELLED' ? 'info' : 'primary' }
function format(value: string) { return value ? new Date(value).toLocaleString() : '' }
function resultSummary(operation: DeviceOperation) { return operation.verified_result?.map(value => `${value.name}=${value.string_value || value.value}${value.unit || ''}`).join(', ') || '' }
function resolutionLabel(outcome: ManualResolutionOutcome) { return outcome === 'CONFIRMED_SUCCEEDED' ? '确认成功' : outcome === 'CONFIRMED_FAILED' ? '确认失败' : '仍无法确认' }
const offEvent = ws.subscribe('device_operation_update', message => { const operation = (message.payload ?? message.data) as DeviceOperation | undefined; if (operation?.edge_device_id === props.deviceId) store.apply(operation) })
const offConnected = typeof ws.onConnected === 'function' ? ws.onConnected(load) : () => {}
watch(() => props.deviceId, load); onMounted(load); onUnmounted(() => { offEvent(); offConnected() })
</script>
<style scoped>.panel{margin-top:20px}.header{display:flex;justify-content:space-between;align-items:center}.actions{display:flex;gap:8px;flex-wrap:wrap}.unavailable{margin-top:12px}.unavailable p{margin:6px 0;color:var(--el-text-color-secondary)}.timeline-line{display:flex;align-items:center;gap:6px;flex-wrap:wrap}.status{margin-left:2px}.result{color:var(--el-color-success-dark-2)}.resolution{margin-top:4px;color:var(--el-text-color-secondary);overflow-wrap:anywhere}.resolution-select{width:100%}</style>
