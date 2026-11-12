import { ref } from 'vue'
import { deviceOperationApi, type ActionDefinition } from '@/api/deviceOperation'
import type { AutomationRule } from '@/api/automation'
import { riskBadge, type ActionDraft } from '../actionList'
import { ruleActions } from '../automationFormat'

/**
 * 设备动作目录（GET /edge-devices/:id/actions）的加载、索引与"能选什么"的推导。
 *
 * 为什么单独成 composable（第二轮表单区拆分，2026-11-12）：
 *   · 它有三类消费方 —— 动作卡片（下拉候选 / 风险徽标 / 空态提示）、参数编辑
 *     （definition.input_schema 决定走表单还是 JSON）、保存前预检降级路径
 *     （目录是"这条规则含哪些风险动作"的合法判定源），以及页面挂载时的预热；
 *   · 加载态是三态（loading / ready / failed），不是一个"请求过没"的 Set：
 *     卡片空态提示、切换设备后的清空判定、风险降级判定读的必须是同一份状态 ——
 *     分散在多处就会出现"下拉说没动作、徽标说未知"这类自相矛盾；
 *
 * ⚠ 搬家纪律：索引结构、请求去重、available 缺省口径与搬移前**逐字一致**（纯搬移）。
 */

/** 动作下拉的一个候选（value = 动作 ID）。 */
export interface ActionOption {
  value: string
  label: string
  available: boolean
  reason?: string
}

/**
 * 设备目录的加载态。
 *
 * 为什么必须是三态而不是一个"请求过没"的 Set：空态提示只能说"没取到/没有动作"，
 * 不能把**请求失败**或**还在加载**说成"该设备没有动作"；而"动作不在目录中"
 * （存量规则提示）只在 ready 时才成立 —— failed 时无从判断，说了就是猜。
 */
export type CatalogLoadState = 'loading' | 'ready' | 'failed'

/**
 * definition 是否声明了可生成的表单字段（空壳 schema 不算：打开一张空表单等于废掉 JSON 入口）。
 */
export function definitionHasSchema(def: ActionDefinition | null): boolean {
  const properties = def?.input_schema?.properties
  return !!properties && Object.keys(properties).length > 0
}

export function useActionCatalog() {
  /**
   * 设备 ID:动作 ID → ActionDefinition 的索引。
   *
   * 为什么需要它：动作卡片要显示"目标设备 / 动作名 / 风险徽标"，而这些信息只存在于
   * 各设备自己的受控操作目录里（GET /edge-devices/:id/actions）。逐卡片各发一次请求
   * 会变成 N+1，故页面挂载时一次性把**用到的**设备目录取回来建索引。
   *
   * 为什么风险徽标走目录而不走预检：见 useRuleActions.riskBadgeOf 的注释（预检失败时
   * 风险动作会静默降级为普通动作）。目录失败时索引为空 ⇒ 徽标不渲染（不猜风险），
   * 保存前的预检仍会把风险动作拦下来，故不会漏确认。
   */
  const actionCatalog = ref(new Map<string, ActionDefinition>())

  /**
   * 目录条目的可用性（后端 EffectiveAction.available / reason）。
   *
   * 为什么单独一张表、而不塞进 actionCatalog 的值里：actionCatalog 是**风险徽标的输入**
   * （catalogDefinition → riskPresentation）。改它的值类型等于改风险判定的输入 —— 本次
   * 一个字都不动风险逻辑；可用性只服务下拉选项的「当前不可用」标注与空态提示文案。
   */
  const actionAvailability = ref(new Map<string, { available: boolean; reason?: string }>())

  const catalogState = ref(new Map<number, CatalogLoadState>())

  /**
   * 进行中的目录请求（按设备）。
   *
   * 为什么除了"状态"还要留 Promise：卡片切换目标设备时要判定"已选动作还在不在新目录里"，
   * 而这个判定只有等目录**落定**才有依据。若新设备的目录恰好正在请求（例如页面挂载时
   * 已经在拉），只凭状态会走"拿不到就不断言"的保守分支，清空提示就被悄悄跳过了。
   */
  const catalogInflight = new Map<number, Promise<void>>()

  /**
   * 取一批设备的受控操作目录并建索引（幂等：已请求过的设备跳过）。
   *
   * 为什么需要"编辑中新增的设备"也走这条路：徽标只在**已加载目录**的设备上渲染。
   * 若只加载既有规则用到的设备，用户在新建规则里选一台从未配过规则的设备时，
   * 风险动作会**没有徽标**（看起来像普通动作）—— 这是安全信息缺失，不是视觉问题。
   */
  async function ensureActionCatalog(deviceIds: Iterable<number>) {
    const ids = [...new Set(deviceIds)].filter(id => id > 0)
    const pending = ids.filter(id => !catalogState.value.has(id))
    // 别的调用已经发起、还没落定的请求：等它，别重复打接口，也别在"加载中"下断言
    const inflight = ids
      .map(id => catalogInflight.get(id))
      .filter((request): request is Promise<void> => request !== undefined)
    if (pending.length === 0) {
      await Promise.all(inflight)
      return
    }
    const states = new Map(catalogState.value)
    for (const id of pending) states.set(id, 'loading')
    catalogState.value = states
    const next = new Map(actionCatalog.value)
    const availability = new Map(actionAvailability.value)
    for (const id of pending) {
      const request = (async () => {
        try {
          const items = await deviceOperationApi.actions(id)
          for (const item of items) {
            const def = item.definition
            const key = `${id}:${def.id}`
            next.set(key, def)
            // available 缺省按"可用"处理（后端字段是 bool，老响应可能没有它）：
            // 没有依据时不要把动作标成不可用，否则用户会以为设备坏了。
            availability.set(key, { available: item.available !== false, reason: item.reason ?? def.availability_reason })
          }
          states.set(id, 'ready')
        } catch {
          /* 单个设备目录失败不阻塞页面：该卡片不渲染风险徽标，保存前的预检仍会拦风险动作。
             记为 failed 而不是 ready —— 否则空态提示会声称"该设备没有动作"，而事实是"没取到"。 */
          states.set(id, 'failed')
        }
      })()
      catalogInflight.set(id, request)
    }
    await Promise.all([...pending.map(id => catalogInflight.get(id)!), ...inflight])
    for (const id of pending) catalogInflight.delete(id)
    actionCatalog.value = next
    actionAvailability.value = availability
    // 重新赋值一份新 Map：Promise.all 里改的是同一个对象，不换引用不会触发依赖它的渲染
    catalogState.value = new Map(states)
  }

  /** 该卡片目标设备的目录加载态（未请求过 ⇒ unknown：按"还没准备好"处理，不下任何断言）。 */
  function cardCatalogState(action: ActionDraft): CatalogLoadState | 'unknown' {
    const deviceId = action.action_device_id
    if (!deviceId) return 'unknown'
    return catalogState.value.get(deviceId) ?? 'unknown'
  }

  /**
   * 当前卡片目标设备下的动作候选。
   *
   * 为什么按设备过滤：action_id 由**该设备的驱动**声明，一台 BMS 与一台 ESP32 的动作集合
   * 不相交（键就是 <deviceId>:<actionId>）。把全设备的动作都列出来，只是给用户换一种方式填错。
   * 目录为空/未加载时返回空数组：下拉退化成 allow-create 手输，不阻断建规则（既有降级口径）。
   */
  function actionOptions(action: ActionDraft): ActionOption[] {
    const deviceId = action.action_device_id
    if (!deviceId) return []
    const prefix = `${deviceId}:`
    const options: ActionOption[] = []
    for (const [key, def] of actionCatalog.value) {
      if (!key.startsWith(prefix)) continue
      const availability = actionAvailability.value.get(key)
      const available = availability ? availability.available : true
      const parts = [def.name || def.id, def.id]
      // 风险文字只是**选项文案**（让用户知道这个动作危险），真正判定仍是后端下发的
      // requires_confirmation（riskBadgeOf / 保存前预检）。这里绝不参与任何风险判定。
      const badge = riskBadge(def)
      if (badge) parts.push(`${badge.icon} ${badge.label}`)
      // 不可用的动作**照样列出**、只加后缀，不隐藏：动作确实存在，只是此刻下发不了
      // （电池未接入/驱动未就绪）。隐藏会让下拉看起来"这个动作不存在"，用户转而去查
      // 驱动声明；而是否可用是**运行期**状态，配置期不据此阻断（执行结果由后端判定）。
      if (!available) parts.push('当前不可用')
      options.push({ value: def.id, label: parts.join(' · '), available, reason: availability?.reason })
    }
    // 按显示名排序（数字感知："动作 2" 排在 "动作 10" 前面）：候选多时用户按名字找，不按 ID 找
    options.sort((a, b) => a.label.localeCompare(b.label, 'zh-Hans-CN', { numeric: true }))
    return options
  }

  /** 下拉 placeholder：有候选时提示可选可输，没有候选时只剩手输（不假装有目录）。 */
  function actionIdPlaceholder(action: ActionDraft): string {
    return actionOptions(action).length ? '选择或输入动作 ID' : '输入动作 ID（如 gpio_set）'
  }

  /** 空态：目录确实取回（或明确失败）而该设备一个动作都没有 —— 明说"没有"，仍允许手输。 */
  function catalogEmpty(action: ActionDraft): boolean {
    const state = cardCatalogState(action)
    return (state === 'ready' || state === 'failed') && actionOptions(action).length === 0
  }

  /**
   * 目录里**有**动作但全部 unavailable（电池未接入 / 驱动未就绪 / 设备离线）。
   *
   * 为什么单独一条提示而不是复用空态：把"有 3 个动作但都下发不了"说成"没有动作"，
   * 会让用户去查驱动声明，而实际该查的是设备连接状态。选择仍然放行（见 allow-create 注释）。
   */
  function catalogAllUnavailable(action: ActionDraft): boolean {
    const options = actionOptions(action)
    return cardCatalogState(action) === 'ready' && options.length > 0 && options.every(o => !o.available)
  }

  /**
   * 存量规则 / 设备换型：目录已取回，但里面没有当前选中的动作。
   *
   * **不清空**用户的 action_id 与参数（清空就是静默丢数据），只给一行黄字提示，
   * 保存仍走既有后端校验 —— 与"目录拿不到不阻断建规则"的口径一致。
   */
  function catalogMissing(action: ActionDraft): boolean {
    const deviceId = action.action_device_id
    if (!deviceId || cardCatalogState(action) !== 'ready') return false
    if (!action.action_id.trim()) return false
    return !actionCatalog.value.has(`${deviceId}:${action.action_id}`)
  }

  /**
   * 在已加载的设备目录里找该动作的定义（找不到返回 null，徽标不渲染：不猜风险）。
   *
   * 入参放宽到 null：参数弹窗/表单在"当前没有选中卡片"时也要能问一句"这个动作有没有 schema"，
   * 让调用方每次都先判空只会到处多一层 if（判定逻辑本身一个字没动）。
   */
  function catalogDefinition(action: ActionDraft | null | undefined) {
    if (!action || action.action_type !== 'device_action' || !action.action_device_id || !action.action_id) return null
    const entry = actionCatalog.value.get(`${action.action_device_id}:${action.action_id}`)
    return entry ?? null
  }

  /** 该卡片是否有可生成的参数表单（见 definitionHasSchema）。 */
  function hasParamSchema(action: ActionDraft): boolean {
    return definitionHasSchema(catalogDefinition(action))
  }

  /**
   * 预热：把既有规则用到的设备目录全部取回来（页面 onMounted 调用）。
   *
   * 为什么入参是规则数组而不是读本 composable 的响应式引用：目录与规则列表是两份
   * 互不依赖的状态，预热只在挂载时发生一次，显式传参比让 catalog 依赖规则源更清楚。
   */
  async function loadActionCatalog(rules: AutomationRule[]) {
    const ids: number[] = []
    for (const rule of rules) {
      for (const action of ruleActions(rule)) {
        if (action.action_type === 'device_action' && action.action_device_id) ids.push(action.action_device_id)
      }
    }
    await ensureActionCatalog(ids)
  }

  return {
    actionCatalog,
    actionAvailability,
    catalogState,
    ensureActionCatalog,
    cardCatalogState,
    actionOptions,
    actionIdPlaceholder,
    catalogEmpty,
    catalogAllUnavailable,
    catalogMissing,
    catalogDefinition,
    definitionHasSchema,
    hasParamSchema,
    loadActionCatalog,
  }
}
