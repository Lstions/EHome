import { computed, reactive, ref } from 'vue'
import type {
  AutomationComparator, AutomationRule, AutomationTriggerType, CreateAutomationRuleRequest, SunEventKind,
} from '@/api/automation'
import type { ActionDefinition } from '@/api/deviceOperation'
import type { SensorField } from '@/api/sensorField'
import { sourceGroupApi, type SourceGroup } from '@/api/sourceGroup'
import { SUN_OFFSET_MAX, SUN_OFFSET_MIN, sunLocationComplete } from '../sunEvent'
import { emptyAction, toSequencedActions, type ActionDraft } from '../actionList'
import { ruleActions } from '../automationFormat'
import { requiredMissingText } from './useRuleActions'
import { useRuleConditions } from './useRuleConditions'
import { useRulePreview } from './useRulePreview'

/**
 * 规则创建/编辑表单：模型、触发器字段组的状态、校验与提交体。
 *
 * 为什么单独成 composable（第二轮表单区拆分，2026-11-12）：
 *   · 这里的每一条规则都不是"页面排版"，而是**与后端契约绑定的语义** ——
 *     下拉代理值 sunrise/sunset ↔ trigger_type='sun_event'、HH:mm 规范化、
 *     坐标来源（IP/城市）与来源组的双写/清空、条件行与 JSON 原文的取舍、
 *     动作 seq 按下标重排。它们必须与提交体（buildPayload）在同一个作用域里，
 *     否则"界面改了、提交体没改"这类不一致在保存前完全不可见；
 *   · 页面对外仍只暴露 `form`（既有单测直接读 wrapper.vm.form）与 openCreate/openEdit，
 *     模板里的事项（字段组 / 动作卡片 / 诊断区）搬进子组件后读写的是**同一个** form。
 *
 * ⚠ 搬家纪律：函数体、文案、错误文案顺序与搬移前**逐字一致**（纯搬移）；
 * 校验顺序即"用户先看到哪条错误"的行为，逐条顺序不得调整。
 */

/**
 * 表单模型。
 *
 * 两处与改造前的结构性差异：
 *   ① `trigger_type` 是**下拉代理值**（sunrise|sunset 代表 sun_event + kind）；
 *      提交前由 buildPayload() 映射回后端契约，见该函数注释。
 *   ② 动作由单值字段组变成 `actions: ActionDraft[]`，提交前由 toSequencedActions()
 *      按下标重排 seq（后端 V1 要求 1..N 连续无重复）。
 * 以下字段改名自设计评审中的措辞，源码里保持与后端 JSON 同名，便于对照。
 */
export function emptyForm() {
  return {
    name: '',
    /** 后端契约的触发器类型（sun_event 由 trigger_sun_kind 细分；下拉代理值见 triggerSelection） */
    trigger_type: 'sensor_threshold' as AutomationTriggerType,
    trigger_sun_kind: 'sunset' as SunEventKind,
    trigger_sun_offset_min: 0,
    trigger_sensor_name: '',
    trigger_comparator: 'gt' as AutomationComparator,
    trigger_threshold: 0,
    trigger_duration_sec: 0,
    trigger_window_start: '',
    trigger_window_end: '',
    trigger_window_edge: 'enter' as const,
    // 0 会在 el-select 里回显成「0」（看起来像已选中设备 0）；undefined 才是真空态
    trigger_edge_device_id: undefined as number | undefined,
    // 数据来源（方案 v0.3 §4.3）：edge_device=存量语义；source_group=从来源组取值
    source_kind: 'edge_device' as 'edge_device' | 'source_group',
    source_group_id: undefined as number | undefined,
    // 位置（2026-11-14 用户裁决）：只有两个来源 —— ip=外网 IP 定位，city=手动城市；
    // 经纬度/时区由定位结果回填，**时区没有输入框**（随地理位置自动填充）。
    location_source: 'ip' as 'ip' | 'city' | '',
    location_label: '',
    latitude: undefined as number | undefined,
    longitude: undefined as number | undefined,
    timezone: '' as string | undefined,
    conditions_json: '',
    actions: [emptyAction(1)] as ActionDraft[],
    cooldown_sec: 300,
    max_daily_exec: 0,
    require_confirmed: false,
    enabled: true,
  }
}

/** 表单模型的类型（由 emptyForm 推导，保证与默认值同源）。 */
export type RuleFormModel = ReturnType<typeof emptyForm>

/** 后端运行期 time.Parse("15:04") 要求的形态：两位补零、冒号分隔。 */
const HHMM_PATTERN = /^\d{2}:\d{2}$/

/**
 * 创建/编辑表单的完整状态与动作。
 *
 * @param options.catalogDefinition 设备动作目录查询（唯一事实源在 useActionCatalog）
 * @param options.resetSaveDiagnostics 重开对话框时清空保存前诊断（保存流程的状态在页面）
 */
export function useRuleForm(options: {
  catalogDefinition: (action: ActionDraft | null | undefined) => ActionDefinition | null
  resetSaveDiagnostics: () => void
}) {
  const { catalogDefinition, resetSaveDiagnostics } = options

  const form = reactive(emptyForm())

  // ── 对话框 ──
  /** 创建/编辑对话框可见性 + 正在编辑的规则 id（null = 新建）。 */
  const dialogVisible = ref(false)
  const editingId = ref<number | null>(null)

  // ── 传感器字段：阈值单位与小数位的唯一来源（文档 §4.1 / §4.2）──

  /**
   * 当前选中的字段对象；**只有**选择器给出「具体字面量字段」时才有值。
   *
   * 族（`cell_voltage_*:max`）、@别名、手输、来源组分支一律 null：它们没有单一单位/小数位
   * （族是多个字段的聚合，成员单位可能不同），此时阈值旁不显示单位（§4.2「未知时不显示」）。
   * 编辑存量规则时的回填由组件负责（目录就绪后 emit 一次 change-field），本模块不重复拉目录。
   */
  const sensorField = ref<SensorField | null>(null)

  /** 选择器给的字段对象变化：只用于渲染单位/小数位，不参与提交（提交体没有单位字段）。 */
  function onSensorFieldChange(field: SensorField | null) {
    sensorField.value = field
  }

  /** 阈值单位：来自字段目录，空串 = 未知 ⇒ 不渲染（不猜、不用 sensorUnitMap 兜底）。 */
  const thresholdUnit = computed(() => (sensorField.value?.unit ?? '').trim())

  /**
   * 阈值小数位：字段目录的 precision 优先，≤0 回落到 3 位。
   *
   * 生产 meta.precision 全为 0（后端尚未回填 ⇒ 0 = 未知），回落 3 与改造前写死的
   * `:precision="3"` 完全一致 —— 不会因为"这次拿不到精度"就把已有规则的值显示成另一种形态。
   */
  const thresholdPrecision = computed(() => {
    const precision = sensorField.value?.precision ?? 0
    return precision > 0 ? precision : 3
  })

  // ── 保存前试算（文档 §4.4）：状态与请求口径在 useRulePreview ──
  // 试算只读表单、不写表单，与 validate/onSave 没有耦合（"不会触发"永远只是提示）。
  const preview = useRulePreview({ form, thresholdUnit })

  // ── 时间窗：HH:mm 是运行期唯一能解析的形态（文档 §3.5）──

  /** 打开存量规则时改写过时间值留下的修复提示（空串 = 本轮无需修复）。 */
  const windowStartFixNote = ref('')
  const windowEndFixNote = ref('')

  /**
   * 把存量时间窗值规范成 HH:mm。
   *
   * 为什么必须改写而不是原样回填：`8:00` 在后端运行期解析失败后只打一条 warn 就 return，
   * 规则永久不触发且界面无痕 —— 用户打开编辑框看到一个"看起来正常"的 8:00，会以为它是好的。
   * 改写用户数据必须**可见**，所以同时返回一句提示（渲染在该字段下方）。
   *
   * 无法解析时**保留原值**：静默丢弃用户填的东西比留一个坏值更糟；提示里让他重新选择。
   */
  function normalizeTimeWindow(raw: string): { value: string; note: string } {
    const text = (raw ?? '').trim()
    if (!text) return { value: '', note: '' }
    const matched = /^(\d{1,2}):(\d{1,2})$/.exec(text)
    const hour = matched ? Number(matched[1]) : NaN
    const minute = matched ? Number(matched[2]) : NaN
    if (!matched || hour > 23 || minute > 59) {
      return {
        value: text,
        note: `原值「${text}」无法解析为合法的 HH:mm，已保留原值但运行期不会触发，请重新选择时间。`,
      }
    }
    const fixed = String(hour).padStart(2, '0') + ':' + String(minute).padStart(2, '0')
    if (fixed === text) return { value: text, note: '' }
    return {
      value: fixed,
      note: `已把原值 ${text} 规范为 ${fixed}（原格式运行期不会触发）。`,
    }
  }

  /**
   * start > end = 后端支持的**跨零点窗口**（22:00 → 次日 06:00），不是输入错误。
   *
   * 只在两端都是合法 HH:mm 时判定：字符串比较对补零的 HH:mm 与"比较时刻"等价，
   * 而非法值（例如存量 `8:00`）不该冒出一条"跨越零点"把真正的问题掩盖掉。
   */
  const isOvernightWindow = computed(() => {
    const start = (form.trigger_window_start ?? '').trim()
    const end = (form.trigger_window_end ?? '').trim()
    if (!HHMM_PATTERN.test(start) || !HHMM_PATTERN.test(end)) return false
    return start > end
  })

  // ── 来源组（方案 v0.3 §4.3）──
  // 只做为「下拉候选」加载；组语义（active 选择/combine/缺员）由后端 sourceref 负责，前端不重算。
  const sourceGroups = ref<SourceGroup[]>([])
  /** 编辑中的规则原本是否绑来源组：决定「切回边缘设备」时要不要显式清空来源列。 */
  const editingSourceWasGroup = ref(false)
  const sourceGroupOptions = computed(() => sourceGroups.value.filter(g => g.enabled !== false))
  const selectedSourceGroup = computed(() => sourceGroups.value.find(g => g.id === form.source_group_id))

  function onSourceKindChange(kind: 'edge_device' | 'source_group') {
    if (kind === 'edge_device') {
      form.source_group_id = undefined
      return
    }
    form.trigger_edge_device_id = undefined
    if (form.source_group_id) onSourceGroupChange()
  }

  /** 选组后把传感器名同步为组的数据类别（组已声明 category；用户不需要再手打物理名）。 */
  function onSourceGroupChange() {
    const g = selectedSourceGroup.value
    if (g) form.trigger_sensor_name = g.category
  }

  async function loadSourceGroups() {
    try {
      const res = await sourceGroupApi.list({ page: 1, page_size: 200 })
      sourceGroups.value = Array.isArray(res.items) ? res.items : []
    } catch {
      // 只影响下拉候选；存量边缘设备规则与页面其它功能不受影响。
      sourceGroups.value = []
    }
  }

  /** 下拉当前值：sun_event 规则展开为 sunrise/sunset 供下拉显示，其余原样。 */
  const triggerSelection = computed<AutomationTriggerType | SunEventKind>({
    get: () => {
      if (form.trigger_type === 'sun_event') return form.trigger_sun_kind
      return form.trigger_type
    },
    set: (value) => {
      if (value === 'sunrise' || value === 'sunset') {
        form.trigger_type = 'sun_event'
        form.trigger_sun_kind = value
      } else {
        form.trigger_type = value as AutomationTriggerType
      }
    },
  })

  const isSunTrigger = computed(() => form.trigger_type === 'sun_event')

  // ── 附加条件：结构化条件行的状态/解析/序列化在 useRuleConditions ──
  const conditions = useRuleConditions(form)

  function openCreate() {
    editingId.value = null
    editingSourceWasGroup.value = false
    // 重开对话框 = 新的一次保存流程：清诊断与已确认指纹，避免上一份内容的确认被复用
    resetSaveDiagnostics()
    Object.assign(form, emptyForm())
    // 单位/精度与"时间窗已修复"提示都属于**上一条规则**的状态：不清掉就会挂到新表单上
    // （用户还没选字段就看到一个 °C，或者看到一条与当前内容无关的修复说明）。
    sensorField.value = null
    // 试算结论属于**上一条规则**：即使新表单的值恰好与旧值相同（watch 不触发），
    // 也必须显式清掉，否则用户会看到上一份输入的答案。
    preview.clearPreviewResult()
    windowStartFixNote.value = ''
    windowEndFixNote.value = ''
    // 条件行与源码模式同样属于**上一条规则**的状态：不清就是新表单上挂着旧条件。
    // 先退出源码模式再清行：watch 会在本段同步代码之后跑，那时写回的是空串（零行 ⇒ ""）。
    conditions.resetConditions('')
    dialogVisible.value = true
  }

  function openEdit(rule: AutomationRule) {
    editingId.value = rule.id
    editingSourceWasGroup.value = rule.source_kind === 'source_group'
    // 时间窗存量值先规范化（见 normalizeTimeWindow）：`8:00` 这种形态在后端运行期
    // time.Parse("15:04") 失败后只打一条 warn 就 return —— 规则永久失效且界面无痕，
    // 而用户在编辑框里看到的仍是"看起来正常"的 8:00。规范化后必须留下可见的修复提示。
    const windowStart = normalizeTimeWindow(rule.trigger_window_start ?? '')
    const windowEnd = normalizeTimeWindow(rule.trigger_window_end ?? '')
    // 传感器字段对象在 openEdit 里**不主动拉目录**：
    // SensorFieldPicker 在目录就绪后会自己对当前 modelValue 回填一次 change-field
    // （组件契约，见该组件 refillFieldFromCatalog），这里只需先清空上一份的单位。
    sensorField.value = null
    // 切换规则 = 换了一份答案：试算结论不得跨规则复用（值相同也不会被 watch 清掉）。
    preview.clearPreviewResult()
    Object.assign(form, {
      name: rule.name,
      trigger_type: rule.trigger_type,
      trigger_sun_kind: rule.trigger_sun_kind ?? 'sunset',
      trigger_sun_offset_min: rule.trigger_sun_offset_min ?? 0,
      trigger_sensor_name: rule.trigger_sensor_name ?? '',
      trigger_comparator: rule.trigger_comparator ?? 'gt',
      trigger_threshold: rule.trigger_threshold ?? 0,
      trigger_duration_sec: rule.trigger_duration_sec ?? 0,
      trigger_window_start: windowStart.value,
      trigger_window_end: windowEnd.value,
      trigger_window_edge: rule.trigger_window_edge ?? 'enter',
      trigger_edge_device_id: rule.trigger_edge_device_id ?? undefined,
      source_kind: (rule.source_kind === 'source_group' ? 'source_group' : 'edge_device') as 'edge_device' | 'source_group',
      source_group_id: rule.source_group_id ?? undefined,
      // 2026-11-14 之前的规则没有来源概念（坐标手填）：降级为 city + 说明标签，
      // 保留坐标可用；用户可重新选城市/自动定位覆盖。
      location_source: (rule.location_source as 'ip' | 'city' | undefined)
        ?? (typeof rule.latitude === 'number' && typeof rule.longitude === 'number' ? 'city' : ''),
      location_label: rule.location_label
        ?? (typeof rule.latitude === 'number' && typeof rule.longitude === 'number' ? '已配置坐标（历史规则）' : ''),
      latitude: rule.latitude,
      longitude: rule.longitude,
      timezone: rule.timezone ?? '',
      conditions_json: rule.conditions_json ?? '',
      actions: ruleActions(rule).map((a, index) => ({
        uid: index + 1,
        action_type: a.action_type === 'notification' ? 'notification' : 'device_action',
        // undefined（而不是 0）= 「未选择」：0 会回显成设备 0，见 actionList.ActionDraft 注释
        action_device_id: a.action_device_id,
        action_id: a.action_id ?? '',
        action_params_json: a.action_params_json ?? '',
        action_level: (a.action_level as ActionDraft['action_level']) ?? 'warning',
      })) as ActionDraft[],
      cooldown_sec: rule.cooldown_sec,
      max_daily_exec: rule.max_daily_exec,
      require_confirmed: rule.require_confirmed,
      enabled: rule.enabled,
    })
    // 附加条件：合法数组 ⇒ 还原成条件行；**读不懂就自动进源码模式并保留原文**
    //（历史手写 / 被别的工具改坏的数据不能被"解析失败"吞掉，见 parseConditionRows）。
    // 顺序有讲究：先定模式再落行 —— 源码模式下 syncConditionsJson 会直接返回，
    // 原文一个字符都不会被行数据覆盖（见 useRuleConditions.resetConditions）。
    conditions.resetConditions(rule.conditions_json)
    // 修复提示跟着这一条规则走：用户改完字段再切回来不该还看到"已把原值…"（那时值已是 08:00）
    windowStartFixNote.value = windowStart.note
    windowEndFixNote.value = windowEnd.note
    // 切换规则 = 另一份内容：上一份规则的 warnings 确认不得带过来
    resetSaveDiagnostics()
    dialogVisible.value = true
  }

  function onTriggerTypeChange() {
    // 切换触发器类型时清空互斥字段（既有行为保留），并同步清掉 sun_event 专属值。
    if (form.trigger_type === 'sensor_threshold') {
      form.trigger_window_start = ''
      form.trigger_window_end = ''
    } else if (form.trigger_type === 'time_window') {
      form.trigger_sensor_name = ''
      form.trigger_threshold = 0
      form.trigger_duration_sec = 0
    } else if (form.trigger_type !== 'sun_event') {
      form.trigger_sun_offset_min = 0
    }
    // 单位与"已规范化"提示只对刚打开的那条规则有意义：换触发器类型后必然过时。
    sensorField.value = null
    windowStartFixNote.value = ''
    windowEndFixNote.value = ''
  }

  // ── 校验：按步骤拆开（2026-11-14 创建规则向导）──
  //
  // 为什么拆：向导的「下一步」只能校验当前步骤（否则用户在第一步就被"第 2 个动作缺动作 ID"
  // 拦住，却看不到那个动作）；而「保存」必须校验全部。两者共用同一组判据，避免第二套规则。
  //
  // validate() 的**拼接顺序保持改造前逐字一致**（名称 → 触发器 → 动作 → 条件 → 策略），
  // 因为"用户先看到哪条错误"是既有行为；分步校验只是把同一批判据重新分组。

  function validateBasic(): string | null {
    if (!form.name.trim()) return '请填写规则名称'
    return null
  }

  /** 触发器类型自身的必填项（不含附加条件）。 */
  function validateTriggerType(): string | null {
    if (form.trigger_type === 'sensor_threshold') {
      if (form.source_kind === 'source_group') {
        if (!form.source_group_id) return '请选择来源组'
      } else if (!form.trigger_edge_device_id) {
        return '请选择边缘设备'
      }
      if (!form.trigger_sensor_name.trim()) return '请填写传感器名称'
    } else if (form.trigger_type === 'time_window') {
      if (!form.trigger_window_start.trim()) return '请填写开始时间'
      if (!form.trigger_window_end.trim()) return '请填写结束时间'
      // 格式兜底（控件之外的入口仍然存在：存量规则、浏览器自动填充、将来的快捷键）。
      // 后端保存期只校验"非空"，而运行期解析失败只打一条 warn —— 所以这一道必须在**保存前**，
      // 文案与时间选择器的 value-format 同一口径（HH:mm，两位补零）。
      if (!HHMM_PATTERN.test(form.trigger_window_start.trim())) return '时间格式必须为 HH:mm'
      if (!HHMM_PATTERN.test(form.trigger_window_end.trim())) return '时间格式必须为 HH:mm'
    } else if (form.trigger_type === 'sun_event') {
      // 界面侧前置校验（2026-11-14 用户裁决）：坐标只有两个来源 —— 外网 IP 自动定位
      // 或手动选择设备城市；时区随位置自动填充，**不允许用户单独设置**。
      //
      // 为什么必须拦住"没选位置"：日出/日落规则没有坐标时后端 fail-closed 400，
      // 但更糟的是如果哪天后端校验退化，存下一条没有坐标的规则会**永不触发**，
      // 而界面上看不出来。前端在点保存时就给因果，而不是让用户撞 400。
      if (!form.trigger_sun_kind) return '请选择日出或日落'
      if (!form.location_source) return '请选择位置来源：自动定位或手动选择城市'
      if (!sunLocationComplete(form)) return '请先获取当前位置或选择设备城市（坐标与时区会自动填充）'
      if (form.trigger_sun_offset_min < SUN_OFFSET_MIN || form.trigger_sun_offset_min > SUN_OFFSET_MAX) {
        return `偏移分钟必须在 ${SUN_OFFSET_MIN} ~ ${SUN_OFFSET_MAX} 之间`
      }
    }
    return null
  }

  /** 动作集合（含参数 JSON 与风险动作必填项）。 */
  function validateActions(): string | null {
    if (form.actions.length === 0) return '请至少添加一个动作'
    for (const [index, action] of form.actions.entries()) {
      const where = `第 ${index + 1} 个动作`
      if (action.action_type === 'device_action') {
        if (!action.action_device_id) return `${where}：请选择目标设备`
        if (!action.action_id.trim()) return `${where}：请填写动作 ID`
        let paramsObject: Record<string, unknown> = {}
        if (action.action_params_json.trim()) {
          try {
            const parsed = JSON.parse(action.action_params_json) as unknown
            // 只把**对象**交给必填校验：数组/标量原本就能过这一关（校验口径不变），
            // 到后端 CanonicalizeParams 才会被拒 —— 本次不顺手改这条既有判定。
            if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
              paramsObject = parsed as Record<string, unknown>
            }
          } catch {
            return `${where}：动作参数 JSON 格式错误`
          }
        }
        // 有 input_schema 时补一道 required：表单路径由 ActionForm 的"不完整即禁提交"兜住，
        // 但**手写 JSON**（卡片上的高级入口 / 存量规则 / 别的工具写入）可以整段删掉必填字段。
        // 缺字段后端必 400，这道校验是用户保存前能看到"到底缺哪个参数"的最后机会。
        const missingParams = requiredMissingText(paramsObject, catalogDefinition(action))
        if (missingParams) return `${where}：${missingParams}`
      } else if (!action.action_level) {
        return `${where}：请选择通知级别`
      }
    }
    return null
  }

  /** 附加条件（只有 sensor_threshold 会求值）。 */
  function validateConditions(): string | null {
    // 附加条件：只有 sensor_threshold 会求值（后端 conditionsSatisfied 只在这条路径上被调用），
    // 所以也只有它需要在这里校验 —— 对一条日出规则拦下"它自己不会读的字段"，用户
    // 既不知道为什么要改、也只能先切触发器类型才能改。
    if (form.trigger_type !== 'sensor_threshold') return null
    if (!conditions.conditionsRawMode.value) {
      // 行模式：逐行校验，**空行必须报错**。
      // 为什么不静默丢弃："点了 + 没填完"与"我配了 3 个条件"在界面上一模一样（行还在），
      // 丢弃后保存成功、用户以为 3 个条件都生效，实际只有 2 个 —— 这是最难发现的配置错。
      for (const [index, row] of conditions.conditionRows.value.entries()) {
        const where = `第 ${index + 1} 个条件`
        if (!row.selector.trim()) return `${where}：请选择字段`
        // el-input-number 被清空时给 null/undefined（不是 NaN），两者都必须算"没填"。
        if (typeof row.threshold !== 'number' || !Number.isFinite(row.threshold)) {
          return `${where}：请填写阈值`
        }
      }
    } else if (form.conditions_json.trim()) {
      // 源码模式：保持改造前的校验口径（非法 JSON / 非数组在保存前拦下）。
      // 这段原文可能是用户手写的历史数据，"格式对不对"只能在这里判断。
      try {
        const parsed = JSON.parse(form.conditions_json)
        if (!Array.isArray(parsed)) return '附加条件必须是 JSON 数组'
      } catch {
        return '附加条件 JSON 格式错误'
      }
    }
    return null
  }

  /** 运行策略的边界值。 */
  function validatePolicy(): string | null {
    if (form.cooldown_sec < 0 || form.cooldown_sec > 86400) return '冷却时间必须在 0-86400 秒之间'
    if (form.max_daily_exec < 0 || form.max_daily_exec > 1000) return '每日上限必须在 0-1000 之间'
    return null
  }

  function validate(): string | null {
    return validateBasic()
      ?? validateTriggerType()
      ?? validateActions()
      ?? validateConditions()
      ?? validatePolicy()
  }

  /**
   * 向导「下一步」：只校验当前步骤。
   * 步骤 1 基础 / 2 触发（含条件）/ 3 动作与策略 / 4 检查（无本地校验）。
   */
  function validateStep(step: number): string | null {
    switch (step) {
      case 1: return validateBasic()
      case 2: return validateTriggerType() ?? validateConditions()
      case 3: return validateActions() ?? validatePolicy()
      default: return null
    }
  }

  /** 保存失败时定位到第一个有问题的步骤（4 = 非本地校验，如预检/风险确认）。 */
  function firstInvalidStep(): number {
    if (validateBasic()) return 1
    if (validateTriggerType() || validateConditions()) return 2
    if (validateActions() || validatePolicy()) return 3
    return 4
  }

  /**
   * 组装提交体。
   *
   * 三处必须显式处理的映射（都会导致后端 400，且都不是"格式小问题"）：
   *   ① 下拉代理值 sunrise/sunset → trigger_type='sun_event' + trigger_sun_kind；
   *   ② 动作卡片 → actions 数组，seq 按**下标**重排（后端 V1: 1..N 连续无重复）；
   *   ③ 坐标只在整个"全有或全无"组合下提交，否则省略（= 继承节点）。
   */
  function buildPayload(riskAcknowledged?: boolean): CreateAutomationRuleRequest {
    // 行模式下提交前再同步一次（防御）：任何绕过 deep watch 的写入都不该让提交体与界面脱节。
    // 源码模式下本函数是空操作（syncConditionsJson 自己 guard），原文原样提交。
    conditions.syncConditionsJson()
    const payload: CreateAutomationRuleRequest = {
      name: form.name.trim(),
      trigger_type: form.trigger_type,
      conditions_json: form.conditions_json.trim() || undefined,
      actions: toSequencedActions(form.actions),
      cooldown_sec: form.cooldown_sec,
      max_daily_exec: form.max_daily_exec,
      require_confirmed: form.require_confirmed,
      enabled: form.enabled,
    }
    if (riskAcknowledged !== undefined) payload.risk_acknowledged = riskAcknowledged
    if (form.trigger_type === 'sensor_threshold') {
      payload.trigger_sensor_name = form.trigger_sensor_name.trim()
      payload.trigger_comparator = form.trigger_comparator
      payload.trigger_threshold = form.trigger_threshold
      payload.trigger_duration_sec = form.trigger_duration_sec
      if (form.source_kind === 'source_group') {
        // 来源组：**不传** trigger_edge_device_id —— 锚点由后端按契约 #7 双写
        //（旧二进制降级为「只看主成员」而不是「任意设备触发」）。
        payload.source_kind = 'source_group'
        payload.source_group_id = form.source_group_id
      } else {
        payload.trigger_edge_device_id = form.trigger_edge_device_id
        // 编辑「来源组规则」后切回边缘设备：必须显式清空来源列（后端按指针区分 NULL）。
        if (editingSourceWasGroup.value) {
          payload.source_kind = 'edge_device'
          payload.source_group_id = undefined
        }
      }
    } else if (form.trigger_type === 'time_window') {
      payload.trigger_window_start = form.trigger_window_start.trim()
      payload.trigger_window_end = form.trigger_window_end.trim()
      payload.trigger_window_edge = form.trigger_window_edge
    } else if (form.trigger_type === 'sun_event') {
      // 三组坐标字段 + 来源元数据在 2026-11-14 后**无条件提交**：
      // validate() 已保证来源与坐标齐全；时区由定位结果带出（这里仍 trim 一次，
      // 避免把空白时区发给后端）。
      payload.trigger_type = 'sun_event'
      payload.trigger_sun_kind = form.trigger_sun_kind
      payload.trigger_sun_offset_min = form.trigger_sun_offset_min
      payload.latitude = form.latitude
      payload.longitude = form.longitude
      payload.timezone = form.timezone?.trim()
      payload.location_source = form.location_source || undefined
      payload.location_label = form.location_label?.trim() || undefined
    }
    return payload
  }

  return {
    // 表单与对话框
    form,
    dialogVisible,
    editingId,
    // 传感器字段元数据
    sensorField,
    onSensorFieldChange,
    thresholdUnit,
    thresholdPrecision,
    // 时间窗
    windowStartFixNote,
    windowEndFixNote,
    normalizeTimeWindow,
    isOvernightWindow,
    // 来源组
    sourceGroups,
    sourceGroupOptions,
    selectedSourceGroup,
    loadSourceGroups,
    onSourceKindChange,
    onSourceGroupChange,
    // 位置 / 触发器下拉
    triggerSelection,
    isSunTrigger,
    // 编辑流程
    openCreate,
    openEdit,
    onTriggerTypeChange,
    // 校验与提交体
    validate,
    validateStep,
    firstInvalidStep,
    buildPayload,
    // 附加条件（useRuleConditions）
    conditionRows: conditions.conditionRows,
    conditionsRawMode: conditions.conditionsRawMode,
    conditionsRawInvalid: conditions.conditionsRawInvalid,
    addCondition: conditions.addCondition,
    removeCondition: conditions.removeCondition,
    toggleConditionsRaw: conditions.toggleConditionsRaw,
    // 试算（useRulePreview）
    previewResult: preview.previewResult,
    previewLoading: preview.previewLoading,
    previewDisabled: preview.previewDisabled,
    previewHint: preview.previewHint,
    runPreview: preview.runPreview,
  }
}
