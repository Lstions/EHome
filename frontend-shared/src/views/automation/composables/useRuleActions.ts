import { computed, ref, type Ref } from 'vue'
import { ElMessage } from 'element-plus'
import type { EdgeDevice } from '@/api/edgeDevice'
import type { ActionDefinition, ActionPreset } from '@/api/deviceOperation'
import {
  emptyAction, moveAction, riskBadge, riskPresentation, uidSeed, type ActionDraft,
} from '../actionList'
import { useActionCatalog, definitionHasSchema } from './useActionCatalog'

/**
 * 动作卡片草稿（增删/排序/类型切换/显示名/风险呈现）与动作参数编辑（表单 + JSON）。
 *
 * 为什么单独成 composable（第二轮表单区拆分，2026-11-12）：
 *   · 动作卡片的**判定**全部来自设备动作目录（风险等级、是否有 schema、候选动作），
 *     而目录是独立的一份状态（useActionCatalog）—— 卡片只是它的渲染面；
 *   · 参数编辑有两条互转通路（表单 ↔ JSON）与一条"写回仍是 JSON 字符串"的落库口径，
 *     这条口径同时被提交体（buildPayload）、卡片摘要、预检参数摘要三处消费，
 *     必须只有一处实现；
 *   · 页面保留"动作表单项"的编排（保存流程、风险清单），卡片只负责渲染与抛事件。
 *
 * ⚠ 搬家纪律：函数体、文案、判定源与搬移前**逐字一致**（纯搬移）。
 * 风险等级**只读后端下发的 requires_confirmation / risk_level**，本模块不产生新判定。
 */

/** 本 composable 需要宿主表单提供的**最小字段面**。 */
export interface ActionsHostForm {
  actions: ActionDraft[]
}

/** 动作卡片渲染所需的全部"判定 → 展示"映射（子组件只渲染，判定回本模块）。 */
export interface ActionCardHelpers {
  title: (action: ActionDraft) => string
  badge: (action: ActionDraft) => ReturnType<typeof riskBadge>
  riskState: (action: ActionDraft) => ReturnType<typeof riskPresentation>
  reason: (action: ActionDraft) => string
  options: ReturnType<typeof useActionCatalog>['actionOptions']
  idPlaceholder: ReturnType<typeof useActionCatalog>['actionIdPlaceholder']
  isEmpty: ReturnType<typeof useActionCatalog>['catalogEmpty']
  allUnavailable: ReturnType<typeof useActionCatalog>['catalogAllUnavailable']
  missing: ReturnType<typeof useActionCatalog>['catalogMissing']
  hasSchema: ReturnType<typeof useActionCatalog>['hasParamSchema']
  runtimeUnavailable: ReturnType<typeof useActionCatalog>['catalogRuntimeUnavailable']
  runtimeReason: ReturnType<typeof useActionCatalog>['catalogRuntimeReason']
  presets: (action: ActionDraft) => ActionPreset[]
}

/**
 * 缺了哪些**必填**参数（空数组 = 齐了）。
 *
 * 判定口径与 ActionForm 的 complete 完全对齐（undefined 与 '' 都算没填），否则会出现
 * "表单说齐了、保存又说不齐"两套答案。null 不算缺（ActionForm 也放行），因为方言参数里
 * null 可能是有意义的"清空该字段"。
 *
 * ⚠ 被 save（validate）与参数 JSON 弹窗共用，故是模块级纯函数（不持有状态）。
 */
export function requiredMissingText(params: Record<string, unknown>, def: ActionDefinition | null): string {
  const required = def?.input_schema?.required ?? []
  const missing = required.filter(name => params[name] === undefined || params[name] === '')
  return missing.length ? `缺少参数 ${missing.join('、')}` : ''
}

/** 参数 JSON 文本 → 对象（非法 JSON / 非对象 ⇒ null：调用方按"没有可回填的值"处理）。 */
export function parsedParams(text: string): Record<string, unknown> | null {
  if (!text.trim()) return null
  try {
    const parsed = JSON.parse(text) as unknown
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return null
    return parsed as Record<string, unknown>
  } catch {
    return null
  }
}

/**
 * 空值 = 无参数（合法）；非空必须是 JSON **对象**（数组/标量过不了后端 CanonicalizeParams）。
 *
 * 有 schema 时**额外**校验 required：ActionForm 会禁用不完整表单，但 JSON 路径（本弹窗 /
 * 存量规则）可以整段删掉必填字段 —— 缺字段后端必 400，必须在保存前就说清缺什么。
 */
export function validateParamsJson(text: string, def: ActionDefinition | null): string {
  if (!text.trim()) return requiredMissingText({}, def)
  try {
    const parsed = JSON.parse(text) as unknown
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return '动作参数必须是 JSON 对象'
    return requiredMissingText(parsed as Record<string, unknown>, def)
  } catch {
    return '动作参数 JSON 格式错误'
  }
}

/** 表单提交的参数对象 → 落库的 JSON 串（空对象写空串，与"无参数"的既有表示一致）。 */
export function paramsToJson(params: Record<string, unknown>): string {
  const clean: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(params)) {
    // ActionForm 不会给 undefined，但这里是数据边界：undefined 序列化后会变成 null
    if (value !== undefined) clean[key] = value
  }
  return Object.keys(clean).length ? JSON.stringify(clean) : ''
}

export function useRuleActions(options: {
  form: ActionsHostForm
  devices: Ref<EdgeDevice[]>
  catalog: ReturnType<typeof useActionCatalog>
}) {
  const { form, devices, catalog } = options
  const {
    catalogDefinition, actionOptions, actionIdPlaceholder,
    catalogEmpty, catalogAllUnavailable, catalogMissing, hasParamSchema,
    catalogRuntimeUnavailable, catalogRuntimeReason,
    ensureActionCatalog, catalogState, actionCatalog,
  } = catalog

  // ── 多动作卡片 (R7) ─────────────────────────────────────────────────────

  function addAction() {
    form.actions.push(emptyAction(uidSeed()))
  }

  function removeAction(uid: number) {
    // 至少保留一个动作：空动作集合后端会 400（"actions 不能为空"），
    // 让用户删到 0 再报错不如当场拦住。
    if (form.actions.length <= 1) {
      ElMessage.warning('规则至少需要一个动作')
      return
    }
    form.actions = form.actions.filter(a => a.uid !== uid)
  }

  function moveCard(uid: number, delta: number) {
    form.actions = moveAction(form.actions, uid, delta)
  }

  function onCardTypeChange(action: ActionDraft) {
    // 与既有单动作版的 onActionTypeChange 同口径：切到通知即清掉设备动作专属字段。
    if (action.action_type === 'notification') {
      // undefined = 未选择（el-select 显示「请选择」）。不能写 0：0 会被当成有效值
      // 回显成「0」，看起来像选中了编号 0 的设备。
      action.action_device_id = undefined
      action.action_id = ''
      action.action_params_json = ''
    } else {
      action.action_level = 'warning'
    }
  }

  /**
   * 卡片标题：目录里的**动作名**优先，目录里没有才回落动作 ID。
   *
   * 为什么改：用户认的是「设置充放电 MOS 软件策略」，不是 set_mos_policy —— 目录本来就
   * 加载在内存里（风险徽标一直在用它），标题却还拿裸 ID 显示，等于让用户对照 ID 读界面。
   * 列表摘要 actionSummary 仍显示 action_id：摘要列宽有限，且要能一眼对上后端日志/事件记录，
   * 把长中文名塞进去会把"动作是什么"与"动作叫什么"搅成两套说法（见 actionSummary 注释）。
   */
  function actionTitle(action: ActionDraft): string {
    if (action.action_type !== 'device_action') return '通知'
    const def = catalogDefinition(action)
    if (def?.name) return def.name
    return action.action_id.trim() || '未命名动作'
  }

  /** 已选目标设备的展示名（找不到时回退 #id，不留空白）。 */
  function deviceName(deviceId: number): string {
    const hit = devices.value.find(d => d.id === deviceId)
    return hit ? hit.name : `#${deviceId}`
  }

  /**
   * 卡片风险徽标。
   *
   * 判定源 = 后端下发的 `requires_confirmation`（`deviceaction.RiskLevel.RequiresConfirmation()`
   * 的对外派生，唯一判定点）。**不读 4 级 risk 字面量** —— 那是驱动出厂方言，
   * 前端自己比较它就会出现第二份判定（R2 防漂移门禁）。
   *
   * ⚠️ 为什么这里不用预检结果：卡片是"编辑中"的状态，动作可能刚被改动、尚未预检；
   * 预检失败（后端未实现/网络失败）时若靠它渲染徽标，风险动作会**静默变成普通动作**。
   * 故卡片徽标从已加载的设备目录（/edge-devices/:id/actions）推导，预检只用于
   * **保存前**（弹窗与放行判定），二者分工明确。
   */
  function riskBadgeOf(action: ActionDraft) {
    return riskBadge(catalogDefinition(action))
  }

  /** 卡片的风险呈现态：risk / normal / unknown（未知时只显示"等级未知"，不猜）。 */
  function cardRiskState(action: ActionDraft) {
    return riskPresentation(catalogDefinition(action))
  }

  /**
   * 风险原因文案。
   *
   * 后端 Catalog 目前只下发 risk_level / requires_confirmation，**没有面向用户的
   * "为什么危险"字段**（设计稿 §9 O-2 记录：前端硬编码映射表会漂移，且驱动层的
   * description 是操作说明不是危险说明）。故此处如实说明"原因文案尚未下发"，
   * **不编造危险说明** —— 编造的安全文案比没有更危险（用户会据此做判断）。
   * 后端补上 risk_reason 后，这里改为直接消费（弹窗已按该字段设计）。
   */
  function riskReasonOf(action: ActionDraft): string {
    const def = catalogDefinition(action)
    if (def?.risk_reason) return def.risk_reason
    if (def?.description) {
      return `后端未下发风险原因，以下是该动作的操作说明（非危险说明）：${def.description}`
    }
    return '后端未下发风险原因文案，请自行确认该动作对目标设备的影响'
  }

  /** 该动作的一键预设（如 BMS 充电/放电 MOS）。无预设返回空数组。 */
  function presetOptionsOf(action: ActionDraft): ActionPreset[] {
    return catalogDefinition(action)?.presets ?? []
  }

  /** 应用一键预设：直接写回 action_params_json（仍是 JSON 字符串，落库形态不变）。 */
  function applyActionPreset(action: ActionDraft, preset: ActionPreset) {
    action.action_params_json = JSON.stringify(preset.params)
  }

  /**
   * 编辑中的风险动作卡片。
   *
   * ⚠️ 这是**界面提示**用的计数（"其中 N 个风险动作（保存前需逐项确认）"），
   * 它读的是已加载的设备目录。真正决定"要不要弹风险清单、能不能保存"的判定
   * 走**后端预检端点**（页面 onSave 里），因为风险等级的唯一事实源在后端。
   * 目录尚未加载完时此值为 0 —— 提示文案会少说一句，但不会漏掉二次确认。
   */
  const riskyActions = computed(() => form.actions.filter(a => riskBadgeOf(a)))

  /**
   * 动作的人类可读名。
   *
   * 预检响应里只有 `action_device_name`（**设备**名），没有动作名（方案 §4.1.2 的字段表）。
   * 故动作名只能从已加载的设备动作目录里取；目录里也没有时回落到 action_id ——
   * 宁可显示 ID 也不能把设备名当成动作名（那会让确认错东西，是安全文案错误）。
   */
  function actionNameOf(actionId: string, deviceId: number): string {
    const def = catalogDefinition({ uid: 0, action_type: 'device_action', action_device_id: deviceId, action_id: actionId, action_params_json: '', action_level: 'warning' })
    return def?.name || actionId
  }

  /**
   * 卡片切换目标设备。
   *
   * 目录按设备隔离：旧动作在新设备上可能根本不存在（action_id 是设备驱动声明的），
   * 继续带着它保存 = 一条到死都执行不了、用户还看不出原因的规则。故**目录就绪之后**
   * 再判定：不在新目录里就清空并说明一次（用户看得见变化，而不是被静默换掉）。
   * 目录没取到（设备离线 / 请求失败）时**不清空**：拿不到目录不等于动作不存在。
   */
  async function onCardDeviceChange(action: ActionDraft) {
    const deviceId = action.action_device_id
    if (!deviceId) return
    await ensureActionCatalog([deviceId])
    // 等待期间用户可能又换了设备/动作：只在"仍然是这台设备"时判定
    if (action.action_device_id !== deviceId) return
    if (catalogState.value.get(deviceId) !== 'ready') return
    if (!action.action_id.trim()) return
    if (actionCatalog.value.has(`${deviceId}:${action.action_id}`)) return
    action.action_id = ''
    action.action_params_json = ''
    ElMessage.info('该动作不在新设备的动作目录中，已清空动作 ID 与参数，请重新选择')
  }

  /**
   * 动作 ID 变化（下拉选中 / allow-create 手输）。
   *
   * 为什么"选中就弹表单"：后端 definition.input_schema 已经声明了字段/类型/枚举/范围，
   * 却让用户去别处背 JSON，是同一系统里两套录入方式。有 schema 就当场进表单，用户不必
   * 先记住"这个动作有参数"再去找入口。
   * 为什么手输（目录里没有该 ID）时什么都不做：目录不可用是**已知降级**（设备离线/驱动
   * 未声明），此时没有 schema 可驱动，保留用户原样输入与既有参数，交给保存期后端校验。
   */
  function onCardActionIdChange(action: ActionDraft) {
    const def = catalogDefinition(action)
    if (!def) return
    // 有预设的动作（如 BMS 充放电 MOS）：预设按钮就是主交互，不能再弹参数表单
    // 逼用户面对 protocol priority。先清空旧动作参数，用户点预设后一次性写入。
    if ((def.presets?.length ?? 0) > 0) {
      action.action_params_json = ''
      return
    }
    if (definitionHasSchema(def)) {
      // 不把旧参数带进来：它们属于上一个动作，字段名对不上时只会变成干扰（ActionForm
      // 只认自己 schema 里的字段，提交时也会把它们丢掉）。取消则原样保留旧参数。
      openParamsForm(action, null)
      return
    }
    // 无参数动作：schema 明说没有可填字段 ⇒ 清掉上一个动作留下的参数。
    // 不清的话后端会拿旧键去解析新动作（轻则忽略，重则 400），而界面上"参数"还显示着旧值。
    action.action_params_json = ''
  }

  // ── 动作参数编辑 (R8)：schema 表单为主，JSON 文本域为「高级」 ──
  const paramsDialogVisible = ref(false)
  const paramsFormVisible = ref(false)
  const paramsEditingUid = ref<number | null>(null)
  const paramsDraft = ref('')
  const paramsDraftError = ref('')
  /** 表单初值：已存的参数对象（读不出/非法 JSON ⇒ null，表单从空白开始）。 */
  const paramsFormPrefill = ref<Record<string, unknown> | null>(null)
  const editingParamsAction = computed(
    () => form.actions.find(a => a.uid === paramsEditingUid.value) ?? null,
  )
  const editingParamsActionId = computed(() => editingParamsAction.value?.action_id ?? '')
  /** 当前编辑的是第几张卡片（1 起用于文案；-1 = 动作已被删除，此时不出任何入口）。 */
  const paramsEditingIndex = computed(
    () => form.actions.findIndex(a => a.uid === paramsEditingUid.value),
  )
  const paramsFormDefinition = computed(() => catalogDefinition(editingParamsAction.value))
  /** JSON 弹窗里能否切回表单：只有目录真的给了字段（properties 非空）才有表单可开。 */
  const paramsEditingSchema = computed(() => definitionHasSchema(paramsFormDefinition.value))

  /**
   * 「编辑参数」按钮的唯一入口：按**有无 schema** 分流。
   *
   * 为什么默认走表单：后端 definition.input_schema 已经声明了字段/类型/枚举/范围，
   * 同一个系统里设备页能"选动作→填表单"，自动化页却要"背 JSON" —— 参数填错要等到保存
   * 甚至执行时才发现。JSON 只在没有 schema 时才是唯一形态（有 schema 时降级为「高级」）。
   */
  function openParams(uid: number) {
    const action = form.actions.find(a => a.uid === uid)
    if (!action) return
    const def = catalogDefinition(action)
    if (definitionHasSchema(def)) openParamsForm(action, parsedParams(action.action_params_json))
    else openJsonParams(action)
  }

  /** 打开 JSON 文本域（无 schema 时的唯一入口 / 有 schema 时的高级入口）。 */
  function openJsonParams(action: ActionDraft) {
    paramsEditingUid.value = action.uid
    paramsDraft.value = action.action_params_json
    // 当场就校验一遍：存量参数可能已经缺必填字段（别的工具写坏 / schema 后来加了字段）
    paramsDraftError.value = validateParamsJson(paramsDraft.value, catalogDefinition(action))
    paramsDialogVisible.value = true
  }

  /** 打开 schema 表单（prefill = 已存参数对象）。 */
  function openParamsForm(action: ActionDraft, prefill: Record<string, unknown> | null) {
    paramsEditingUid.value = action.uid
    paramsFormPrefill.value = prefill
    paramsFormVisible.value = true
  }

  function onParamsDraftChange() {
    paramsDraftError.value = validateParamsJson(paramsDraft.value, paramsFormDefinition.value)
  }

  function applyParams() {
    paramsDraftError.value = validateParamsJson(paramsDraft.value, paramsFormDefinition.value)
    if (paramsDraftError.value) return
    const action = editingParamsAction.value
    if (action) action.action_params_json = paramsDraft.value.trim()
    paramsDialogVisible.value = false
  }

  /**
   * 表单提交 → 写回 JSON 串。
   *
   * 为什么写回字符串而不是把模型改成对象：action_params_json 是全仓既有的落库形态
   * （后端 CanonicalizeParams 校验的也是"一个 JSON 对象串"）；改成对象会波及提交体、
   * 列表摘要 paramsSummary、预检参数摘要三处，属于本次改造之外的扩散。
   */
  function applyFormParams(params: Record<string, unknown>) {
    const action = editingParamsAction.value
    if (action) action.action_params_json = paramsToJson(params)
    paramsFormVisible.value = false
  }

  /** JSON 弹窗 → 表单（「改用表单」）：把文本里能读出的对象作为 prefill 带过去。 */
  function switchParamsToForm() {
    const action = editingParamsAction.value
    if (!action) return
    const prefill = parsedParams(paramsDraft.value)
    paramsDialogVisible.value = false
    openParamsForm(action, prefill)
  }

  return {
    // 卡片
    addAction,
    removeAction,
    moveCard,
    onCardTypeChange,
    onCardDeviceChange,
    onCardActionIdChange,
    actionTitle,
    deviceName,
    riskBadgeOf,
    cardRiskState,
    riskReasonOf,
    riskyActions,
    actionNameOf,
    applyActionPreset,
    /** 卡片渲染用的判定映射（单一对象：子组件不重写任何判定）。 */
    cardHelpers: {
      title: actionTitle,
      badge: riskBadgeOf,
      riskState: cardRiskState,
      reason: riskReasonOf,
      options: actionOptions,
      idPlaceholder: actionIdPlaceholder,
      isEmpty: catalogEmpty,
      allUnavailable: catalogAllUnavailable,
      missing: catalogMissing,
      hasSchema: hasParamSchema,
      runtimeUnavailable: catalogRuntimeUnavailable,
      runtimeReason: catalogRuntimeReason,
      presets: presetOptionsOf,
    } as ActionCardHelpers,
    // 参数编辑
    paramsDialogVisible,
    paramsFormVisible,
    paramsDraft,
    paramsDraftError,
    paramsFormPrefill,
    editingParamsActionId,
    paramsEditingIndex,
    paramsFormDefinition,
    paramsEditingSchema,
    openParams,
    openJsonParams,
    onParamsDraftChange,
    applyParams,
    applyFormParams,
    switchParamsToForm,
  }
}
