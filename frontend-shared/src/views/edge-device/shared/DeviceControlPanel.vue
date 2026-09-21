<template>
  <el-card class="panel" shadow="hover" data-testid="operation-buttons">
    <template #header><div class="header"><span>受控操作</span><el-tag v-if="!ws.isConnected" type="info" size="small">实时连接断开</el-tag></div></template>
    <!-- P0-b：不可用必须**可行动**。
         - 每个 reason 都要说清「为什么不能做」与「你能做什么」；
         - 只有真正能靠一次 QueryResources 恢复的才给「立即恢复」按钮，
           冻结类（协议未冻结 / 仅限实机取证）必须明说「无需操作」，
           否则用户会一直白点一个永远不会成功的按钮。 -->
    <el-alert
      v-if="unavailableGuidance.length"
      class="stale-guidance"
      type="warning"
      :closable="false"
      show-icon
      :title="guidanceTitle"
    >
      <ul class="guidance-list">
        <li v-for="entry in unavailableGuidance" :key="entry.key">
          <strong>{{ entry.count }} 项：</strong>{{ entry.summary }}
          <span class="guidance-action">{{ entry.action }}</span>
          <span v-if="entry.raw" class="guidance-raw">服务端原文：{{ entry.raw }}</span>
        </li>
      </ul>
      <div v-if="selfHealableCount" class="stale-actions">
        <el-button type="primary" size="small" :loading="refreshing" @click="retryCapabilityRefresh">
          {{ refreshing ? '正在请求节点上报…' : '立即恢复' }}
        </el-button>
        <span class="stale-hint">服务端会主动要求节点立刻重报一次能力快照，无需等待周期心跳。</span>
      </div>
    </el-alert>
    <el-alert v-if="available.length === 0 && unavailableGuidance.length === 0" type="info" :closable="false" title="当前没有可执行的受控操作" />
    <template v-if="available.length > 0">
      <div class="ops-sub">当前设备可执行的受控操作</div>
      <div class="op-list">
        <div v-for="action in available" :key="action.definition.id" class="op-item" :class="{ busy: submitting === action.definition.id }" role="button" tabindex="0" @click="begin(action.definition)" @keydown.enter.space.prevent="begin(action.definition)">
          <el-icon :size="18" class="op-icon"><component :is="actionIcon(action.definition)" /></el-icon>
          <div class="op-text">
            <div class="op-name">
              <span>{{ action.definition.name }}</span>
              <el-tag v-if="action.definition.risk === 'critical'" type="danger" size="small" effect="plain">严重风险</el-tag>
              <el-tag v-else-if="action.definition.risk === 'high'" type="warning" size="small" effect="plain">高风险</el-tag>
            </div>
            <div v-if="action.definition.description" class="op-desc">{{ action.definition.description }}</div>
          </div>
        </div>
      </div>
    </template>
    <el-collapse v-if="unavailable.length" class="unavailable"><el-collapse-item :title="`暂不可用操作（${unavailable.length}）`" name="unavailable"><p v-for="action in unavailable" :key="action.definition.id">{{ action.definition.name }}：{{ availabilityReason(action) }}</p></el-collapse-item></el-collapse>
    <el-divider>操作历史</el-divider>
    <el-empty v-if="history.length === 0" description="暂无操作记录" :image-size="64" />
    <el-timeline v-else><el-timeline-item v-for="operation in history" :key="operation.command_id" :type="timelineType(operation.status)" :timestamp="format(operation.manual_resolution?.resolved_at || operation.updated_at)"><div class="timeline-line"><strong>{{ operation.action_id }}</strong><span class="status">{{ operation.status }}</span><span v-if="operation.final_reason"> · {{ operation.final_reason }}</span><span v-if="resultSummary(operation)" class="result"> · {{ resultSummary(operation) }}</span><el-button v-if="operation.status === 'UNKNOWN' && !operation.manual_resolution" text type="warning" size="small" @click="beginResolution(operation)">人工处置</el-button></div><div v-if="operation.manual_resolution" class="resolution">人工结论：{{ resolutionLabel(operation.manual_resolution.outcome) }} · {{ operation.manual_resolution.reason }}</div></el-timeline-item></el-timeline>
  </el-card>
  <ActionForm v-model:visible="formVisible" :definition="selectedAction" @submit="submitSelected" />
  <ActionConfirmationDialog v-model:visible="confirmationVisible" :definition="selectedAction" @confirm="confirmSelected" />
  <el-dialog v-model="reauthenticationVisible" title="验证当前身份" width="440px" :close-on-click-modal="false" destroy-on-close>
    <el-alert type="warning" :closable="false" title="该高风险操作要求最近 10 分钟内验证密码" />
    <el-form label-position="top" class="reauthentication-form"><el-form-item label="当前账户密码" required><el-input v-model="reauthenticationPassword" type="password" show-password autocomplete="current-password" @keyup.enter="reauthenticateAndRetry" /></el-form-item></el-form>
    <template #footer><el-button @click="cancelReauthentication">取消</el-button><el-button type="danger" :loading="reauthenticating" :disabled="!reauthenticationPassword" @click="reauthenticateAndRetry">验证并继续</el-button></template>
  </el-dialog>
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
import { feedback } from '@/utils/feedback'
import { ElMessage } from 'element-plus'
import { EditPen, RefreshLeft, Operation, View } from '@element-plus/icons-vue'
import { authApi } from '@/api/auth'
import { ApiError, isApiErrorCode } from '@/api/client'
import { useDeviceOperationStore, type ResourceRefreshAttempt } from '@/stores/deviceOperation'
import { classifyUnavailable } from '@/utils/actionAvailability'
import { useWebSocketStore } from '@/stores/websocket'
import { newIdempotencyKey, type ActionDefinition, type DeviceOperation, type EffectiveAction, type ManualResolutionOutcome, type OperationStatus } from '@/api/deviceOperation'
import ActionForm from './ActionForm.vue'
import ActionConfirmationDialog from './ActionConfirmationDialog.vue'
const props = defineProps<{ deviceId: number }>()
const store = useDeviceOperationStore(); const ws = useWebSocketStore(); const submitting = ref(''); const selectedAction = ref<ActionDefinition | null>(null); const selectedParams = ref<Record<string, unknown>>({}); const formVisible = ref(false); const confirmationVisible = ref(false)
const catalog = computed(() => store.catalogs.get(props.deviceId) ?? []); const history = computed(() => store.histories.get(props.deviceId) ?? []); const selectedKey = ref('')
const resolutionVisible = ref(false); const resolving = ref(false); const resolutionOperation = ref<DeviceOperation | null>(null); const resolutionOutcome = ref<ManualResolutionOutcome>('ACKNOWLEDGED_UNKNOWN'); const resolutionReason = ref('')
const reauthenticationVisible = ref(false); const reauthenticationPassword = ref(''); const reauthenticating = ref(false); const pendingConfirmationReason = ref('')
const available = computed(() => catalog.value.filter(item => !availabilityReason(item))); const unavailable = computed(() => catalog.value.filter(item => !!availabilityReason(item)))

// --- P0-b：不可用的可行动指引 ---------------------------------------------------
// 「操作不可用但不知为何」是本轮要根治的形态。分类逻辑全部在
// utils/actionAvailability.ts（含每个 reason 的「为什么 / 怎么办」与是否可自愈），
// 这里只负责：把同类的不可用聚合成一条，并按分类决定要不要给自愈按钮。
const refreshing = computed(() => store.refreshing.get(props.deviceId) === true)
const lastAttempt = computed<ResourceRefreshAttempt | undefined>(() => store.lastRefresh.get(props.deviceId))

const guidanceEntries = computed(() => {
  const buckets = new Map<string, { key: string; count: number; summary: string; action: string; selfHealable: boolean; raw: string }>()
  for (const action of unavailable.value) {
    const info = classifyUnavailable(action)
    const existing = buckets.get(info.key)
    if (existing) existing.count++
    else buckets.set(info.key, { key: info.key, count: 1, summary: info.summary, action: info.action, selfHealable: info.selfHealable, raw: info.raw })
  }
  return [...buckets.values()].sort((a, b) => b.count - a.count)
})
const selfHealableCount = computed(() => guidanceEntries.value.filter(e => e.selfHealable).reduce((sum, e) => sum + e.count, 0))
const unavailableGuidance = computed(() => {
  // 只有「可自愈但自愈还没成功」时才需要额外解释失败原因；
  // 冻结类的原因本身就是完整解释，不必再叠一层。
  const attempt = lastAttempt.value
  return guidanceEntries.value.map(entry => {
    if (!entry.selfHealable || !attempt) return entry
    if (!attempt.attempted) {
      const why = attempt.nodeId ? (attempt.error || '未知原因') : '该设备没有关联节点，服务端无法向它请求资源上报'
      return { ...entry, action: `已尝试请求节点重报，但请求没有发出：${why}。请确认节点在线并重新绑定后再试。` }
    }
    if (attempt.error) {
      return { ...entry, action: `已向节点请求重报，但节点没有回应：${attempt.error}。请检查节点电源与网络。` }
    }
    return { ...entry, action: entry.action + '（刚才已自动尝试过一次仍未恢复，可直接点下方按钮再试。）' }
  })
})
const guidanceTitle = computed(() => {
  const total = unavailable.value.length
  const healable = selfHealableCount.value
  if (healable > 0 && healable === total) return `有 ${total} 个操作可以立即恢复`
  if (healable > 0) return `有 ${total} 个操作暂不可用（其中 ${healable} 个可立即恢复）`
  return `有 ${total} 个操作暂不可用（均无需在页面上操作）`
})

async function retryCapabilityRefresh() {
  try {
    await store.retryCapabilityRefresh(props.deviceId)
    const attempt = store.lastRefresh.get(props.deviceId)
    if (selfHealableCount.value === 0) ElMessage.success('已恢复可用操作')
    // 故障提示必须走 feedback（I-1 收敛门禁：禁止裸 ElMessage.error）。
    // 这里的提示带操作上下文，因为同一页面还有其它会报错的动作。
    else if (attempt?.attempted && !attempt.error) ElMessage.warning('节点尚未回传新的能力快照，请稍后重试')
    else feedback.error(`恢复可用操作失败：${attempt?.error || '节点未回应'}`)
  } catch (error) {
    feedback.handleErrorWithContext(error, '恢复可用操作失败', '请稍后重试')
  }
}

async function load() { if (props.deviceId > 0) await store.refresh(props.deviceId) }
// P1-1 的可用性文案契约：reason 由后端给出，前端只保证「未提供时不留白」。
// reason_code 只用于**分派可行动指引**（见 staleGuidance），不直接展示给用户。
function availabilityReason(action: EffectiveAction) { if (!action.available) return action.reason || '当前不可用'; const fields = Object.values(action.definition.input_schema?.properties ?? {}); if (fields.some(field => !['string', 'boolean', 'integer', 'number'].includes(field.type) || field.enum && field.type !== 'string')) return '客户端不支持该参数 Schema'; return '' }
function requiresConfirmation(action: ActionDefinition) { return action.risk === 'medium' || action.risk === 'high' || action.risk === 'critical' }
function actionIcon(action: ActionDefinition) { return action.semantics === 'reset' ? RefreshLeft : action.semantics === 'read' ? View : action.semantics === 'set' ? EditPen : Operation }
function begin(action: ActionDefinition) { selectedAction.value = action; const fields = Object.keys(action.input_schema?.properties ?? {}); if (fields.length === 0) { prepare({}); return }; formVisible.value = true }
async function submitSelected(params: Record<string, unknown>) { formVisible.value = false; prepare(params) }
function prepare(params: Record<string, unknown>) { if (!selectedAction.value) return; selectedParams.value = params; selectedKey.value = newIdempotencyKey(); if (requiresConfirmation(selectedAction.value)) { confirmationVisible.value = true; return }; void submit(selectedAction.value.id, params) }
async function queueConfirmed(action: ActionDefinition, reason: string) { const grant = await store.confirm(props.deviceId, action.id, selectedParams.value, reason); await store.create(props.deviceId, action.id, selectedParams.value, grant.token, reason, selectedKey.value); ElMessage.info('操作已排队') }
async function handleOperationError(error: unknown) { if (error instanceof ApiError && error.response) { feedback.handleError(error, '操作未能提交'); if (error.status === 409) await load(); return }; await load(); ElMessage.warning('操作结果未确认，已刷新状态；请勿重复提交') }
async function confirmSelected(reason: string) { if (!selectedAction.value) return; const action = selectedAction.value; confirmationVisible.value = false; submitting.value = action.id; try { await queueConfirmed(action, reason) } catch (error: unknown) { if (isApiErrorCode(error, 'recent_auth_required')) { pendingConfirmationReason.value = reason; reauthenticationPassword.value = ''; reauthenticationVisible.value = true; ElMessage.warning('请验证当前账户密码后继续'); return }; await handleOperationError(error) } finally { submitting.value = '' } }
function cancelReauthentication() { reauthenticationVisible.value = false; reauthenticationPassword.value = ''; pendingConfirmationReason.value = '' }
async function reauthenticateAndRetry() { const action = selectedAction.value; const password = reauthenticationPassword.value; const reason = pendingConfirmationReason.value; if (!action || !password || !reason || reauthenticating.value) return; reauthenticating.value = true; try { await authApi.reauthenticate(password); reauthenticationVisible.value = false; reauthenticationPassword.value = ''; submitting.value = action.id; await queueConfirmed(action, reason); pendingConfirmationReason.value = '' } catch (error: unknown) { if (isApiErrorCode(error, 'invalid_reauthentication_credentials') || isApiErrorCode(error, 'reauthentication_rate_limited')) { feedback.handleError(error, '身份验证失败'); return }; if (isApiErrorCode(error, 'recent_auth_required')) { reauthenticationVisible.value = true; feedback.error('近期身份验证未生效，请重试'); return }; await handleOperationError(error) } finally { reauthenticating.value = false; submitting.value = '' } }
async function submit(actionId: string, params: Record<string, unknown>) { submitting.value = actionId; try { await store.create(props.deviceId, actionId, params, '', '', selectedKey.value); ElMessage.info('操作已排队') } catch (error: unknown) { await handleOperationError(error) } finally { submitting.value = '' } }
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
<style scoped>.panel{margin-top:20px}.header{display:flex;justify-content:space-between;align-items:center}.stale-guidance{margin-bottom:12px}.stale-detail{margin:4px 0;font-size:12px;line-height:1.6}.guidance-list{margin:6px 0 0;padding-left:18px;font-size:12px;line-height:1.7}.guidance-list li{margin-bottom:4px}.guidance-action{color:var(--el-text-color-secondary)}.guidance-raw{display:block;color:var(--el-text-color-placeholder);font-size:11px;overflow-wrap:anywhere}.stale-actions{display:flex;align-items:center;gap:10px;flex-wrap:wrap}.stale-hint{font-size:12px;color:var(--el-text-color-secondary)}.ops-sub{margin:0 0 10px;font-size:12px;color:var(--el-text-color-secondary)}.op-list{display:grid;grid-template-columns:repeat(auto-fill,minmax(220px,1fr));gap:10px}.op-item{display:flex;align-items:flex-start;gap:10px;padding:12px;border:1px solid var(--el-border-color-lighter);border-radius:8px;cursor:pointer;transition:border-color .15s,box-shadow .15s}.op-item:hover{border-color:var(--el-color-primary-light-5);box-shadow:var(--el-box-shadow-light)}.op-item:focus-visible{outline:2px solid var(--el-color-primary);outline-offset:1px}.op-item.busy{opacity:.6;pointer-events:none}.op-icon{flex-shrink:0;margin-top:2px;color:var(--el-color-primary)}.op-text{min-width:0}.op-name{display:flex;align-items:center;gap:6px;font-size:13px;font-weight:500;color:var(--el-text-color-primary)}.op-desc{margin-top:4px;font-size:12px;color:var(--el-text-color-secondary);overflow-wrap:anywhere}.unavailable{margin-top:12px}.unavailable p{margin:6px 0;color:var(--el-text-color-secondary)}.timeline-line{display:flex;align-items:center;gap:6px;flex-wrap:wrap}.status{margin-left:2px}.result{color:var(--el-color-success-dark-2)}.resolution{margin-top:4px;color:var(--el-text-color-secondary);overflow-wrap:anywhere}.resolution-select{width:100%}.reauthentication-form{margin-top:16px}@media (max-width:768px){.op-list{grid-template-columns:1fr}}</style>