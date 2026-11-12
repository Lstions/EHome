import { computed, reactive, ref, watch } from 'vue'
import type { Ref } from 'vue'
import { Operation } from '@element-plus/icons-vue'
import { useDeviceOperationStore } from '@/stores/deviceOperation'
import { deviceOperationApi, type DeviceOperation, type EffectiveAction } from '@/api/deviceOperation'
import { feedback, extractErrorMessage } from '@/utils/feedback'
import { formatStamp } from '../bmsFormat'
import {
  OP_GROUP_DEFS,
  OPERATION_UI,
  readableFinalReason,
  toOp,
  verifiedSummary,
  type HistoryRow,
  type OpDef,
  type OpGroup,
  type OpPhase,
  type OpState,
  type ReauthTarget,
} from './bmsOperationModel'

/**
 * BMS「受控操作」状态域：目录分组 / 执行状态机 / 操作历史 / 参数化表单 / 重验证编排。
 *
 * 为什么单独拆出来（2026-11-12 纯搬移式拆分）：
 *   这一整段是**一个数据域**——目录来自后端 catalogs，回执来自 store.histories，
 *   两者共同驱动「受控操作卡 + 操作历史卡 + 参数表单弹窗」。页面对它只需要
 *   「什么时候刷新」与「表单弹窗绑哪几个状态」，其余是纯内部状态机。
 *
 * 与 MOS 域的唯一交叉：set_mos_policy 的回执要更新 MOS 卡片（phase/error/权威 fet_status）。
 * 这里不复制那份逻辑，而是把回执**转交**给 onMosReceipt（页面用晚绑定接上 MOS 实例），
 * 保证「谁的状态谁维护」。
 */

export interface BmsOperationsDeps {
  deviceId: Ref<number>
  isMobileViewport: Ref<boolean>
  dispatch: (actionId: string, params: Record<string, unknown>, requiresConfirmation: boolean) => Promise<boolean>
  clearError: () => void
  lastError: Ref<string>
  reauthVisible: Ref<boolean>
  reauthTarget: Ref<ReauthTarget>
  /** set_mos_policy 回执转交（MOS 域的实例在页面里后创建，故用回调而不是直接引用） */
  onMosReceipt: (op: DeviceOperation) => void
}

export function useBmsOperations(deps: BmsOperationsDeps) {
  const { deviceId, isMobileViewport, dispatch, clearError, lastError, reauthVisible, reauthTarget, onMosReceipt } = deps

  const opStore = useDeviceOperationStore()
  const historyOps = computed<DeviceOperation[]>(() => opStore.histories.get(deviceId.value) ?? [])
  const catalog = computed<EffectiveAction[]>(() => opStore.catalogs.get(deviceId.value) ?? [])

  // ════════════ 受控操作（目录来自后端 catalogs） ════════════
  const opGroups = computed<OpGroup[]>(() => {
    if (!catalog.value.length) return []
    const byId = new Map(catalog.value.map((a) => [a.definition.id, a]))
    const claimed = new Set<string>()
    const groups: OpGroup[] = []
    for (const g of OP_GROUP_DEFS) {
      const ops = g.ids
        .map((id) => byId.get(id))
        .filter((a): a is EffectiveAction => !!a)
        .map(toOp)
      if (!ops.length) continue
      for (const o of ops) claimed.add(o.id)
      groups.push({ id: g.id, name: g.name, hint: g.hint, icon: g.icon, ops })
    }
    const rest = catalog.value.filter((a) => !claimed.has(a.definition.id)).map(toOp)
    if (rest.length) groups.push({ id: 'other', name: '其他操作', hint: '目录中未归类的操作', icon: Operation, ops: rest })
    return groups
  })
  const totalOps = computed(() => catalog.value.length)
  // 窄屏默认全部收起（桌面保持「运行控制」展开）：
  // 「运行控制」5 条指令展开就是 762px，把「始终可见」预算顶穿；
  // 而 MOS/直控开关已经上移到页首主位，不需要再从清单里翻。
  // 每条目只有在用户点开分组后才渲染（v-show），展开后条条目也已网格化（~78px/条）。
  const openGroups = reactive<Record<string, boolean>>({ runtime: !isMobileViewport.value })
  function toggleGroup(id: string) {
    openGroups[id] = !openGroups[id]
  }
  function riskCount(g: OpGroup): number {
    return g.ops.filter((o) => o.risky).length
  }
  function catalogName(actionId: string): string {
    const hit = catalog.value.find((a) => a.definition.id === actionId)
    return hit?.definition.name || actionId
  }

  const opStates = reactive<Record<string, OpState>>({})
  // 预初始化：render 期间不写响应式状态（避免渲染中改 dep 触发递归更新）。
  watch(catalog, (list) => {
    for (const a of list) {
      if (!opStates[a.definition.id]) opStates[a.definition.id] = { phase: 'idle', text: '', reason: '' }
    }
  }, { immediate: true })
  function opState(id: string): OpState {
    const existing = opStates[id]
    if (existing) return existing
    const created: OpState = { phase: 'idle', text: '', reason: '' }
    opStates[id] = created
    return created
  }
  function setPhase(id: string, phase: OpPhase, text: string) {
    const st = opState(id)
    st.phase = phase
    st.text = text
  }
  function opBusy(id: string): boolean {
    const p = opState(id).phase
    return p === 'sent' || p === 'running' || p === 'verifying'
  }

  const notified = new Set<string>()
  function notifyOnce(op: DeviceOperation, fn: () => void) {
    const key = op.command_id + ':' + op.status
    if (notified.has(key)) return
    notified.add(key)
    fn()
  }

  let activeCommandId = ''

  /** 把一条真实操作回执映射到页面状态（失败原因常驻，成功用权威回读桥接）。 */
  function applyOperationToUi(op: DeviceOperation) {
    // set_mos_policy 的卡片状态由 MOS 域维护（phase / error / 权威回读），这里只转交，
    // 避免同一份回执在两个地方各写一遍。
    if (op.action_id === 'set_mos_policy') {
      onMosReceipt(op)
      return
    }
    const ui = OPERATION_UI[op.status]
    const st = opState(op.action_id)
    if (op.status === 'SUCCEEDED') {
      setPhase(op.action_id, 'success', '成功')
      st.reason = ''
    } else if (op.status === 'FAILED') {
      setPhase(op.action_id, 'failed', '失败')
      st.reason = readableFinalReason(op.final_reason) || '设备未接受或回读校验不一致'
    } else if (op.status === 'UNKNOWN') {
      setPhase(op.action_id, 'unknown', '结果未知')
      st.reason = '观察类操作无法以 ACK 判定成功；请等待设备重新上线后在设备详情页确认。'
    } else if (op.status === 'CANCELLED') {
      setPhase(op.action_id, 'cancelled', '已取消')
      st.reason = '操作已取消'
    } else {
      setPhase(op.action_id, ui.phase, ui.text)
    }
    notifyOnce(op, () => {
      const name = catalogName(op.action_id)
      if (op.status === 'SUCCEEDED') feedback.success('「' + name + '」执行成功')
      else if (op.status === 'FAILED') feedback.error('「' + name + '」失败：' + (readableFinalReason(op.final_reason) || '回读校验不一致'))
      else if (op.status === 'UNKNOWN') feedback.warning('「' + name + '」结果未知：请等待设备重新上线')
    })
  }

  /** 记录本次页面发起的命令，只跟踪它自己的回执（不把历史遗留操作当成本次结果）。 */
  function trackCommand(actionId: string) {
    const op = historyOps.value.find((o) => o.action_id === actionId)
    if (!op) return
    activeCommandId = op.command_id
    applyOperationToUi(op)
  }
  watch(historyOps, (list) => {
    if (!activeCommandId) return
    const op = list.find((o) => o.command_id === activeCommandId)
    if (op) applyOperationToUi(op)
  })

  /**
   * 参数化操作表单（2026-10-16 用户要求："参数与标定功能应该参数化显示，可修改保存"）。
   *
   * 改前带参数的操作（参数与标定全组、诊断/运行/时间组共 12 项）按钮一律置灰，
   * 只给一句"请到设备详情页填写"—— 在评审页等于不可用。
   * 现在复用生产页同一份 ActionForm：字段直接由后端 definition.input_schema 生成
   * （boolean→开关、enum→下拉、integer→带上下限的数字框、string→文本框），
   * 必填与范围也在同一处校验，不再有第二套表单导致的规则漂移。
   *
   * 提交流程：填参数 →（若为高风险动作）极简确认 → dispatch(带参数)。
   * 风险动作后端强制单次令牌，仍由 useControlAction 内部处理重验证。
   */
  const opFormVisible = ref(false)
  const opFormTarget = ref<OpDef | null>(null)

  function onOpClick(op: OpDef) {
    if (op.movedTo || !op.available) return
    if (op.needsParams) {
      opFormTarget.value = op
      // 每次打开都清掉上一次的读取结果，避免把 A 动作的值带进 B 动作的表单
      opFormPrefill.value = null
      opFormVisible.value = true
      return
    }
    void runOp(op)
  }

  function submitOpForm(params: Record<string, unknown>) {
    const op = opFormTarget.value
    opFormVisible.value = false
    if (!op) return
    void runOp(op, params)
  }

  /* ══════════ 读取现有值 → 回填表单（2026-10-16 用户要求） ══════════
     用户原话："参数应该先读取现有值，弹窗内取消最左边增加'读取'"。

     为什么必须有：写入类动作的参数表动辄 15/30/33 项（保护参数 / 系统参数 / 内阻表），
     空白手填既慢又极易填错；而这些值设备本来就有，读一次就能拿到。
     这里把「写入动作」配对到「读动作」——后端命名是一一对应的
     （write_protection_parameters ↔ read_protection_parameters 等），
     按 write_→read_ 推导并**要求该读动作在当前目录里存在且可用**，
     不存在就不显示「读取」按钮（避免给出一个点了没反应的按钮）。

     结果取回：读动作以 verified_result 返回 [{name, value}]，
     字段名与写入 schema 完全对应（实测 read_protection_parameters 回 23 项，
     cell_ov_protect / cell_uv_delay / chg_ot_protect 等与写表单同名）。 */

  const opFormReading = ref(false)
  const opFormPrefill = ref<Record<string, unknown> | null>(null)

  /** 写入动作对应的读动作 id；没有可用读动作时返回空串 ⇒ 不显示「读取」按钮 */
  function readActionFor(writeId: string): string {
    if (!writeId.startsWith('write_')) return ''
    const candidate = 'read_' + writeId.slice('write_'.length)
    const hit = catalog.value.find((a) => a.definition.id === candidate && a.available)
    return hit ? candidate : ''
  }

  const opFormCanRead = computed(() => !!opFormTarget.value && !!readActionFor(opFormTarget.value.id))

  /** 轮询该读操作的终态并取回 verified_result（有界等待，失败返回 null） */
  async function waitReadResult(readId: string): Promise<Record<string, unknown> | null> {
    for (let i = 0; i < 20; i++) {
      await new Promise((r) => setTimeout(r, 1000))
      const list = await deviceOperationApi.list(deviceId.value)
      const op = list.find((o) => o.action_id === readId)
      if (!op) continue
      if (['QUEUED', 'DISPATCHED', 'DEVICE_ACCEPTED', 'VERIFYING'].includes(op.status)) continue
      if (op.status !== 'SUCCEEDED') return null
      const out: Record<string, unknown> = {}
      for (const v of op.verified_result ?? []) {
        if (v.string_value !== undefined && v.string_value !== '') out[v.name] = v.string_value
        else out[v.name] = v.value
      }
      return Object.keys(out).length ? out : null
    }
    return null
  }

  async function readOpFormValues() {
    const op = opFormTarget.value
    if (!op || opFormReading.value) return
    const readId = readActionFor(op.id)
    if (!readId) return
    opFormReading.value = true
    opFormPrefill.value = null
    clearError()
    try {
      // 是否取确认令牌必须**按该读动作自己的 requires_confirmation**，不能写死 true：
      //   read_protection_parameters / read_sn 是 elevated ⇒ 需要令牌；
      //   read_internal_resistance 是单步 low-risk 读 ⇒ **不需要**令牌，此时若仍去
      //   申请，后端直接 409 "confirmation is not required for this action"，读取静默失败。
      // 读取不改设备，两种情况下都不额外弹本地确认框。
      const readAction = catalog.value.find((a) => a.definition.id === readId)
      const ok = await dispatch(readId, {}, readAction?.definition.requires_confirmation === true)
      if (!ok) {
        feedback.error('读取现有值失败：' + (lastError.value || '未知原因'))
        return
      }
      const values = await waitReadResult(readId)
      if (!values) {
        feedback.error('已下发读取，但未取回可用值（设备未响应或读取被拒）')
        return
      }
      opFormPrefill.value = values
      feedback.success('已读取设备现有值，共 ' + Object.keys(values).length + ' 项，可直接修改后提交')
    } catch (e) {
      feedback.error('读取现有值失败：' + String(e))
    } finally {
      opFormReading.value = false
    }
  }

  async function runOp(op: OpDef, params: Record<string, unknown> = {}) {
    // ⚠ 不再因 needsParams 直接 return —— 参数由 ActionForm 收集后传入（2026-10-16 用户要求
    // "参数与标定功能应该参数化显示，可修改保存"）。原实现把带参数的操作一律挡在门外，
    // 且 dispatch 传的是空对象 {}，即使绕过去也必然被后端 schema 拒绝。
    if (op.movedTo || !op.available) return
    const st = opState(op.id)
    st.reason = ''
    clearError()
    if (op.risky || op.requiresConfirmation) {
      // 后端对高风险动作强制单次确认令牌（useControlAction 内部走 confirm），这里再补一次极简确认。
      const ok = await feedback.confirmDanger(
        '确认执行「' + op.name + '」？该操作会影响设备运行参数，执行后自动回读校验。',
        { title: '受控操作 · ' + op.name, confirmText: '确认执行', cancelText: '取消' },
      )
      if (!ok) return
    }
    setPhase(op.id, 'sent', '已发送')
    const ok = await dispatch(op.id, params, op.requiresConfirmation)
    if (ok) {
      reauthTarget.value = null
      trackCommand(op.id)
    } else if (reauthVisible.value) {
      reauthTarget.value = { type: 'op', id: op.id }
    } else {
      setPhase(op.id, 'failed', '失败')
      st.reason = lastError.value || '操作未能提交'
      feedback.error('「' + op.name + '」未能提交：' + (lastError.value || '未知原因'))
    }
  }

  // ════════════ 操作历史（审计线索，来自 store.histories） ════════════
  const historyRows = computed<HistoryRow[]>(() => {
    // store.apply 在 REST 合并时会 unshift，数组顺序不保证「新的在前」；显式按时间倒序，
    // 保证「最近 N 条」真的是最近的（否则 slice(0,30) 会留下最老的 30 条）。
    const ordered = [...historyOps.value].sort((a, b) => {
      const ta = Date.parse(a.updated_at || a.completed_at || a.created_at || '')
      const tb = Date.parse(b.updated_at || b.completed_at || b.created_at || '')
      return (Number.isNaN(tb) ? 0 : tb) - (Number.isNaN(ta) ? 0 : ta)
    })
    return ordered.slice(0, 30).map((op) => {
      const detail = op.manual_resolution?.reason || readableFinalReason(op.final_reason) || verifiedSummary(op)
      return {
        id: op.command_id,
        time: formatStamp(op.manual_resolution?.resolved_at || op.completed_at || op.updated_at || op.created_at),
        type: op.action_id === 'set_mos_policy' ? 'MOS 直控' : '受控操作',
        content: catalogName(op.action_id) + (detail ? ' — ' + detail : ''),
        result: op.status === 'SUCCEEDED' ? 'success' : op.status === 'FAILED' ? 'failed' : op.status === 'CANCELLED' ? 'cancelled' : 'unknown',
      }
    })
  })
  /** 折叠态摘要（§3.2：折叠不是隐藏信息，标题行要留一行关键结论） */
  const historyCollapseSummary = computed(() => '最近 ' + historyRows.value.length + ' 条')

  // ════════════ 受控操作目录加载 ════════════
  const catalogReady = ref(false)
  const catalogError = ref('')
  const catalogLoading = computed(() => !catalogReady.value)
  async function loadOperations() {
    catalogReady.value = false
    catalogError.value = ''
    try {
      await opStore.refresh(deviceId.value)
    } catch (e) {
      catalogError.value = extractErrorMessage(e, '受控操作目录加载失败')
    } finally {
      catalogReady.value = true
    }
  }

  /** 重验证通过：命令已按原参数重新提交 */
  function markSent(id: string) {
    setPhase(id, 'sent', '已发送')
  }
  /** 重验证失败：进入失败态并常驻原因 */
  function markFailed(id: string, message: string) {
    const st = opState(id)
    setPhase(id, 'failed', '失败')
    st.reason = message
  }
  /** 用户取消重验证：进入已取消态并常驻原因 */
  function markCancelled(id: string, message: string) {
    const st = opState(id)
    setPhase(id, 'cancelled', '已取消')
    st.reason = message
  }

  /** 设备切换前复位（只清执行态，目录与历史由 store 按 deviceId 各自隔离） */
  function resetOperations() {
    activeCommandId = ''
    Object.keys(opStates).forEach((k) => {
      opStates[k] = { phase: 'idle', text: '', reason: '' }
    })
  }

  return {
    opGroups,
    totalOps,
    openGroups,
    toggleGroup,
    riskCount,
    opState,
    opBusy,
    trackCommand,
    historyRows,
    historyCollapseSummary,
    opFormVisible,
    opFormTarget,
    opFormCanRead,
    opFormReading,
    opFormPrefill,
    onOpClick,
    submitOpForm,
    readOpFormValues,
    runOp,
    setPhase,
    markSent,
    markFailed,
    markCancelled,
    catalogError,
    catalogLoading,
    loadOperations,
    resetOperations,
  }
}
