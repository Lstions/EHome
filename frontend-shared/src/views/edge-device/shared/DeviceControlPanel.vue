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
              <el-tag v-if="riskBadge(action.definition)" :type="riskBadge(action.definition)!.type" size="small" effect="plain">{{ riskBadge(action.definition)!.label }}</el-tag>
            </div>
            <!--
              窄屏 .op-desc 被 -webkit-line-clamp:2 裁剪（受控操作有 9 项，不裁会到 1080px）。
              裁剪是设计取舍，但**必须能取到完整文本**：2026-10-21 独立复核实测
              5 条说明 ch=30 vs sh=45~90 被硬裁且无 title/tooltip ⇒ 信息永久丢失。
              提供三条路径：① title（桌面悬停原生提示）② 点击说明展开/收起（触屏可达，
              .stop 防止误触发「开始执行」）③ 展开后自然换行完整显示。
              ⚠ 不再声称 aria-label：无 role 的 div 上 aria-label 会被多数读屏忽略，
              写了等于假修复（复核员核对 DOM 时也确认 .op-desc[aria-label] 数量为 0）。
            -->
            <div
              v-if="action.definition.description"
              class="op-desc"
              :class="{ 'is-expanded': expandedDesc.has(action.definition.id) }"
              :title="action.definition.description"
              role="button"
              tabindex="0"
              @click.stop="toggleDesc(action.definition.id)"
              @keydown.enter.space.prevent.stop="toggleDesc(action.definition.id)"
            >{{ action.definition.description }}</div>
          </div>
        </div>
      </div>
    </template>
    <el-collapse v-if="unavailable.length" class="unavailable"><el-collapse-item :title="`暂不可用操作（${unavailable.length}）`" name="unavailable"><p v-for="action in unavailable" :key="action.definition.id">{{ action.definition.name }}：{{ availabilityReason(action) }}</p></el-collapse-item></el-collapse>
    <el-divider>操作历史 <span v-if="history.length" class="history-count">最近 {{ visibleHistory.length }} / {{ history.length }} 条</span></el-divider>
    <!-- 2026-10-15 布局修复：改前历史用**无上限**的 el-timeline 直铺，
         每次受控操作都追加一条。实测运行一天后 32 条 = 4508px，把 .bottom-row
         撑到 7318px；而左列「实时数据流」卡只有 688px ⇒ 左侧留下 6630px 空白，
         页面看起来"左边空了一大截"（用户截图即此形态）。
         修法：容器封顶 max-height + 内部滚动，并只渲染最近 N 条（历史默认收起）。
         为什么是"容器滚动"而不是简单 slice：截断会**丢失**旧记录，而操作历史是
         审计线索，用户需要能翻到；因此保留全部数据、只限制可视高度。 -->
    <el-empty v-if="history.length === 0" description="暂无操作记录" :image-size="64" />
    <div v-else class="history-scroll" :class="{ 'is-expanded': expandedHistory }">
    <el-timeline><el-timeline-item v-for="operation in visibleHistory" :key="operation.command_id" :type="timelineType(operation.status)" :timestamp="format(operation.manual_resolution?.resolved_at || operation.updated_at)"><div class="timeline-line"><strong>{{ operation.action_id }}</strong><span class="status">{{ statusText(operation.status) }}</span><span v-if="operation.final_reason"> · {{ operation.final_reason }}</span><span v-if="resultSummary(operation)" class="result"> · {{ resultSummary(operation) }}</span><el-button v-if="operation.status === 'UNKNOWN' && !operation.manual_resolution" text type="warning" size="small" @click="beginResolution(operation)">人工处置</el-button></div><div v-if="operation.manual_resolution" class="resolution">人工结论：{{ resolutionLabel(operation.manual_resolution.outcome) }} · {{ operation.manual_resolution.reason }}</div></el-timeline-item></el-timeline>
    </div>
    <!-- 折叠态只显示最近 HISTORY_LIMIT 条；其余通过"显示全部"展开（数据不丢）。 -->
    <div v-if="history.length > HISTORY_LIMIT" class="history-more">
      <el-button text type="primary" size="small" @click="expandedHistory = !expandedHistory">
        {{ expandedHistory ? '收起' : `显示全部 ${history.length} 条` }}
      </el-button>
    </div>
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
// 2026-10-15（布局修复）：历史条数上限。
// 后端 List 默认返回 50 条，而每条 ~61px ⇒ 3050px；实测 32 条已 4508px。
// 这里只渲染最近 HISTORY_LIMIT 条，其余仍在容器内滚动可见（数据不丢，仅限制首屏量）。
// 展开"显示全部"需要用户显式动作 —— 历史是审计信息，默认不该主宰页面高度。
const HISTORY_LIMIT = 8
const expandedHistory = ref(false)
const visibleHistory = computed(() => expandedHistory.value ? history.value : history.value.slice(0, HISTORY_LIMIT))
const resolutionVisible = ref(false); const resolving = ref(false); const resolutionOperation = ref<DeviceOperation | null>(null); const resolutionOutcome = ref<ManualResolutionOutcome>('ACKNOWLEDGED_UNKNOWN'); const resolutionReason = ref('')
const reauthenticationVisible = ref(false); const reauthenticationPassword = ref(''); const reauthenticating = ref(false)
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
// 风险判定与样式**全部**来自后端下发的 2 级值 —— 前端不再持有任何 risk 字面量。
// requiresConfirmation 直接读后端的 requires_confirmation 布尔（它是
// deviceaction.RiskLevel.RequiresConfirmation() 的对外派生），因此前后端判定不会漂移。
function requiresConfirmation(action: ActionDefinition) { return action.requires_confirmation === true }
// riskBadge 是**唯一的**风险样式映射表。后端只下发 normal/elevated 两个值，
// 这里据此给出徽标；未识别的值不渲染徽标（fail-closed：不猜风险等级）。
function riskBadge(action: ActionDefinition): { type: 'danger' | 'warning'; label: string } | null {
  switch (action.risk_level) {
    case 'elevated': return { type: 'danger', label: '风险' }
    case 'normal': return null
    default: return null
  }
}
function actionIcon(action: ActionDefinition) { return action.semantics === 'reset' ? RefreshLeft : action.semantics === 'read' ? View : action.semantics === 'set' ? EditPen : Operation }
/**
 * 窄屏说明展开态（触屏读全文的路径；桌面有 title 但触屏没有 hover）。
 * 只影响 CSS（去掉 line-clamp），不改数据、不影响执行流程。
 */
const expandedDesc = ref<Set<string>>(new Set())
function toggleDesc(id: string) {
  const next = new Set(expandedDesc.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expandedDesc.value = next
}
function begin(action: ActionDefinition) { selectedAction.value = action; const fields = Object.keys(action.input_schema?.properties ?? {}); if (fields.length === 0) { prepare({}); return }; formVisible.value = true }
async function submitSelected(params: Record<string, unknown>) { formVisible.value = false; prepare(params) }
function prepare(params: Record<string, unknown>) { if (!selectedAction.value) return; selectedParams.value = params; selectedKey.value = newIdempotencyKey(); if (requiresConfirmation(selectedAction.value)) { confirmationVisible.value = true; return }; void submit(selectedAction.value.id, params) }
async function queueConfirmed(action: ActionDefinition) { const grant = await store.confirm(props.deviceId, action.id, selectedParams.value); await store.create(props.deviceId, action.id, selectedParams.value, grant.token, '', selectedKey.value); ElMessage.info('指令已发送') }
async function handleOperationError(error: unknown) { if (error instanceof ApiError && error.response) { feedback.handleError(error, '操作未能提交'); if (error.status === 409) await load(); return }; await load(); ElMessage.warning('操作结果未确认，已刷新状态；请勿重复提交') }
async function confirmSelected() { if (!selectedAction.value) return; const action = selectedAction.value; confirmationVisible.value = false; submitting.value = action.id; try { await queueConfirmed(action) } catch (error: unknown) { if (isApiErrorCode(error, 'recent_auth_required')) { reauthenticationPassword.value = ''; reauthenticationVisible.value = true; ElMessage.warning('请验证当前账户密码后继续'); return }; await handleOperationError(error) } finally { submitting.value = '' } }
function cancelReauthentication() { reauthenticationVisible.value = false; reauthenticationPassword.value = '' }
async function reauthenticateAndRetry() { const action = selectedAction.value; const password = reauthenticationPassword.value; if (!action || !password || reauthenticating.value) return; reauthenticating.value = true; try { await authApi.reauthenticate(password); reauthenticationVisible.value = false; reauthenticationPassword.value = ''; submitting.value = action.id; await queueConfirmed(action) } catch (error: unknown) { if (isApiErrorCode(error, 'invalid_reauthentication_credentials') || isApiErrorCode(error, 'reauthentication_rate_limited')) { feedback.handleError(error, '身份验证失败'); return }; if (isApiErrorCode(error, 'recent_auth_required')) { reauthenticationVisible.value = true; feedback.error('近期身份验证未生效，请重试'); return }; await handleOperationError(error) } finally { reauthenticating.value = false; submitting.value = '' } }
async function submit(actionId: string, params: Record<string, unknown>) { submitting.value = actionId; try { await store.create(props.deviceId, actionId, params, '', '', selectedKey.value); ElMessage.info('指令已发送') } catch (error: unknown) { await handleOperationError(error) } finally { submitting.value = '' } }
function beginResolution(operation: DeviceOperation) { resolutionOperation.value = operation; resolutionOutcome.value = 'ACKNOWLEDGED_UNKNOWN'; resolutionReason.value = ''; resolutionVisible.value = true }
async function submitResolution() { if (!resolutionOperation.value || !resolutionReason.value.trim()) return; resolving.value = true; try { await store.resolve(resolutionOperation.value.command_id, resolutionOutcome.value, resolutionReason.value.trim()); resolutionVisible.value = false; ElMessage.success('人工处置已记录') } catch { await load(); ElMessage.warning('处置结果未确认，已刷新状态') } finally { resolving.value = false } }
// 执行状态 → 中文（与后端 OperationStatus 枚举一一对应）。用户只看中文，
// 不暴露 QUEUED/DISPATCHED 这类内部状态字面量。
function statusText(status: OperationStatus) {
  const map: Record<OperationStatus, string> = {
    QUEUED: '已发送', DISPATCHED: '已下发', DEVICE_ACCEPTED: '设备已收到',
    VERIFYING: '核验中', SUCCEEDED: '成功', FAILED: '失败',
    CANCELLED: '已取消', UNKNOWN: '结果未知',
  }
  return map[status] ?? status
}
function timelineType(status: OperationStatus) { return status === 'SUCCEEDED' ? 'success' : status === 'FAILED' || status === 'UNKNOWN' ? 'danger' : status === 'CANCELLED' ? 'info' : 'primary' }
function format(value: string) { return value ? new Date(value).toLocaleString() : '' }
function resultSummary(operation: DeviceOperation) { return operation.verified_result?.map(value => `${value.name}=${value.string_value || value.value}${value.unit || ''}`).join(', ') || '' }
function resolutionLabel(outcome: ManualResolutionOutcome) { return outcome === 'CONFIRMED_SUCCEEDED' ? '确认成功' : outcome === 'CONFIRMED_FAILED' ? '确认失败' : '仍无法确认' }
const offEvent = ws.subscribe('device_operation_update', message => { const operation = (message.payload ?? message.data) as DeviceOperation | undefined; if (operation?.edge_device_id === props.deviceId) store.apply(operation) })
const offConnected = typeof ws.onConnected === 'function' ? ws.onConnected(load) : () => {}
// 切换设备时重置"显示全部"，避免上一个设备的展开态带到下一个。
watch(() => props.deviceId, () => { expandedHistory.value = false; load() }); onMounted(load); onUnmounted(() => { offEvent(); offConnected() })
</script>
/* 2026-10-15 布局修复：历史容器封顶 + 内部滚动。
   改前无上限直铺，实测 32 条 = 4508px，把 .bottom-row 撑到 7318px，
   而左列实时数据卡仅 688px ⇒ 左侧 6630px 空白。
   封顶 420px（约 6~7 条）让左右两列高度接近；超出部分在容器内滚动，
   历史数据一条不丢（审计线索不能截断，见模板注释）。 */
<style scoped>.panel{margin-top:20px}.header{display:flex;justify-content:space-between;align-items:center}.stale-guidance{margin-bottom:12px}.stale-detail{margin:4px 0;font-size:12px;line-height:1.6}.guidance-list{margin:6px 0 0;padding-left:18px;font-size:12px;line-height:1.7}.guidance-list li{margin-bottom:4px}.guidance-action{color:var(--el-text-color-secondary)}.guidance-raw{display:block;color:var(--el-text-color-placeholder);font-size:11px;overflow-wrap:anywhere}.stale-actions{display:flex;align-items:center;gap:10px;flex-wrap:wrap}.stale-hint{font-size:12px;color:var(--el-text-color-secondary)}.ops-sub{margin:0 0 10px;font-size:12px;color:var(--el-text-color-secondary)}.op-list{display:grid;grid-template-columns:repeat(auto-fill,minmax(220px,1fr));gap:10px}.op-item{display:flex;align-items:flex-start;gap:10px;padding:12px;border:1px solid var(--el-border-color-lighter);border-radius:8px;cursor:pointer;transition:border-color .15s,box-shadow .15s}.op-item:hover{border-color:var(--el-color-primary-light-5);box-shadow:var(--el-box-shadow-light)}.op-item:focus-visible{outline:2px solid var(--el-color-primary);outline-offset:1px}.op-item.busy{opacity:.6;pointer-events:none}.op-icon{flex-shrink:0;margin-top:2px;color:var(--el-color-primary)}.op-text{min-width:0}.op-name{display:flex;align-items:center;gap:6px;font-size:13px;font-weight:500;color:var(--el-text-color-primary)}.op-desc{margin-top:4px;font-size:12px;color:var(--el-text-color-secondary);overflow-wrap:anywhere;cursor:pointer}.op-desc.is-expanded{color:var(--el-text-color-primary)}.unavailable{margin-top:12px}.unavailable p{margin:6px 0;color:var(--el-text-color-secondary)}.timeline-line{display:flex;align-items:center;gap:6px;flex-wrap:wrap}.status{margin-left:2px}.result{color:var(--el-color-success-dark-2)}.resolution{margin-top:4px;color:var(--el-text-color-secondary);overflow-wrap:anywhere}.resolution-select{width:100%}.reauthentication-form{margin-top:16px}
/* 2026-10-15 布局修复：历史容器封顶 + 内部滚动。
   改前无上限直铺，实测 32 条 = 4508px，把 .bottom-row 撑到 7318px，
   而左列实时数据卡仅 688px ⇒ 左侧 6630px 空白。
   封顶 420px（约 6~7 条）让左右两列高度接近；超出部分在容器内滚动，
   历史数据一条不丢（审计线索不能截断，见模板注释）。 */
.history-scroll{max-height:420px;overflow-y:auto;padding-right:4px}
.history-scroll.is-expanded{max-height:70vh}
.history-more{display:flex;justify-content:center;margin-top:4px}
.history-count{margin-left:6px;font-size:12px;font-weight:400;color:var(--el-text-color-secondary)}
@media (max-width:768px){
  /* 受控操作列表：改前窄屏 1 列，每个 op-item 是「图标 + 名称/标签 + 描述」纵向约 120px，
     9 项 ≈ 1080px。改 2 列紧凑卡：名称+标签一行、描述最多 2 行，每格约 84px。
     描述被截到 2 行属可接受（下钻有完整说明的弹窗），名称与风险标签**绝不截断**。 */
  .op-list{grid-template-columns:repeat(2,minmax(0,1fr));gap:8px}
  .op-item{flex-direction:column;align-items:flex-start;gap:4px;padding:8px 10px;min-height:64px}
  .op-icon{display:none}
  .op-name{font-size:12px;flex-wrap:wrap;gap:4px;word-break:keep-all}
  .op-desc{margin-top:0;font-size:11px;line-height:15px;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden}
  /* 点一下说明展开全文（触屏没有 hover，title 不可达）：去掉行数限制。 */
  .op-desc.is-expanded{-webkit-line-clamp:unset;display:block;overflow:visible}
  .ops-sub{margin-bottom:6px;font-size:11px}
  .history-scroll{max-height:320px}
}
</style>
