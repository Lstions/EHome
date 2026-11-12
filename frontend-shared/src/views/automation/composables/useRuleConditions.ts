import { computed, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import type { AutomationComparator, AutomationTriggerType } from '@/api/automation'
import { comparators } from '../automationFormat'
import { uidSeed } from '../actionList'

/**
 * 附加条件：结构化条件行（文档 §4.3）的状态、解析与序列化。
 *
 * 为什么单独成 composable（第二轮表单区拆分，2026-11-12）：
 *   · 它是表单里**唯一**一块「界面形态（行）」与「提交形态（JSON 原文）」并存的状态，
 *     两种形态之间有一条严格的单向同步（行 → conditions_json）与一条**禁止**同步的
 *     例外（源码模式）。这段规则如果散在页面里，任何一次模板搬移都可能把它拆散；
 *   · 触发字段区（components/TriggerFields.vue）只负责渲染与抛事件，解析/序列化/
 *     非法存量保护仍由本模块持有，页面与子组件读到的是同一份状态。
 *
 * ⚠ 搬家纪律：函数体、文案、判定分支顺序与搬移前**逐字一致**（纯搬移）。
 */

/**
 * 序列化键与后端 models.AutomationCondition **逐字段同名**
 * （backend/internal/models/automation.go:141-145）：
 *   {"sensor_name":"temperature_2","comparator":"gt","threshold":45}
 * 后端 conditionsSatisfied 对每条走同一个 evalSelector，全部满足才触发
 *（任一不满足 / 无法判定 ⇒ 不触发，evaluator.go:1018-1033）。
 * 因此界面顺序 = 提交数组顺序 = 求值顺序，二者必须一致（见 serializeConditionRows）。
 *
 * 与动作卡片同思路：v-for 的 key 用 uid 而不是下标。下标作 key 时删掉中间一行，
 * 后面所有行的组件实例会被错位复用 —— 值"跳"到别的行、输入焦点跑掉，
 * 而这一行的值还可能被写进另一行的 uid 上。
 */

/** 条件行草稿。字段名直接沿用后端口径，不额外造一层 selector/threshold 的翻译表。 */
export interface ConditionRow {
  /** v-for key（同 ActionDraft.uid） */
  uid: number
  /** 字段选择器原文，原样落到后端 sensor_name（与触发字段同一个 selector 契约） */
  selector: string
  comparator: AutomationComparator
  threshold: number
}

/**
 * 本 composable 需要宿主表单提供的**最小字段面**（不是整张表单模型）。
 *
 * 为什么收窄到两个字段而不是直接收 RuleFormModel：条件编辑只读 trigger_type
 * （决定是否求值 / 是否同步）与 conditions_json（唯一提交形态），把整个模型类型
 * 引进来会让「条件模块 → 表单模块」形成类型环。
 */
export interface ConditionsHostForm {
  trigger_type: AutomationTriggerType
  conditions_json: string
}

const COMPARATOR_SET = new Set<string>(comparators.map(c => c.value))

/**
 * conditions_json → 条件行。返回 null = **无法安全还原**，调用方必须原样保留原文。
 *
 * 为什么校验这么严（缺键 / 未知比较符 / 字符串阈值一律判非法）：
 *   · 放宽 = 打开编辑框时静默改写用户数据的语义。例：历史手写的
 *     `{"sensor":"humidity"}`（旧 placeholder 的示例就是这种键名）会被读成
 *     sensor_name=""，用户什么都没动、保存一次，条件就从"湿度<30"变成"永不满足"；
 *   · 而"读不懂"是可逆的：切到源码模式，用户自己看得见、改得动、也能原样交回后端。
 */
export function parseConditionRows(raw: string): ConditionRow[] | null {
  const text = (raw ?? '').trim()
  if (!text) return []
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch {
    return null
  }
  if (!Array.isArray(parsed)) return null
  const rows: ConditionRow[] = []
  for (const [index, item] of parsed.entries()) {
    if (!item || typeof item !== 'object' || Array.isArray(item)) return null
    const record = item as Record<string, unknown>
    const sensorName = record.sensor_name
    const comparator = record.comparator
    const threshold = record.threshold
    if (typeof sensorName !== 'string') return null
    if (typeof comparator !== 'string' || !COMPARATOR_SET.has(comparator)) return null
    if (typeof threshold !== 'number' || !Number.isFinite(threshold)) return null
    // uid = 下标+1：还原出来的行是一次性快照，后续新增行走 uidSeed()，两者不会撞号。
    rows.push({ uid: index + 1, selector: sensorName, comparator: comparator as AutomationComparator, threshold })
  }
  return rows
}

/**
 * 条件行 → conditions_json 原文。
 *
 * 零行 ⇒ 空串：后端 ParseConditions 把 "" 与 "[]" 都当"无条件"，空串是本仓既有口径
 * （buildPayload 的 `trim() || undefined` 依赖它）。
 * selector 统一 trim：后端按字面量比对字段名，尾部多一个空格就是一个"看起来对、
 * 永远不满足"的条件 —— 而它不会报错。
 */
export function serializeConditionRows(rows: ConditionRow[]): string {
  if (rows.length === 0) return ''
  return JSON.stringify(rows.map(row => ({
    sensor_name: row.selector.trim(),
    comparator: row.comparator,
    threshold: row.threshold,
  })))
}

/**
 * 附加条件编辑器的完整状态与动作。
 *
 * @param form 宿主表单（只读两个字段：trigger_type / conditions_json）
 */
export function useRuleConditions(form: ConditionsHostForm) {
  const conditionRows = ref<ConditionRow[]>([])

  /**
   * 源码模式：默认关闭。只有两种情况打开 ——
   *   ① 用户手动点「JSON 源码」（迁移期录入 / 排障）；
   *   ② 存量 conditions_json 无法解析成条件数组（见 parseConditionRows），自动打开。
   * 打开时 textarea 绑定的是 form.conditions_json **原文**，本模块一个字符都不改写它。
   */
  const conditionsRawMode = ref(false)

  /**
   * 行 → form.conditions_json 的**唯一**出口。
   * 源码模式下不写：那时 textarea 里的原文才是唯一真相，行只是上一次的残影。
   */
  function syncConditionsJson() {
    if (conditionsRawMode.value) return
    // 只有 sensor_threshold 会求值附加条件：其它触发类型的 conditions_json 是**惰性数据**，
    // 这里连"顺手规范化空格"都不做 —— 用户看不见的配置不该在他保存别的内容时被悄悄改写。
    if (form.trigger_type !== 'sensor_threshold') return
    form.conditions_json = serializeConditionRows(conditionRows.value)
  }

  // 任何行变化（新增 / 删除 / 改字段 / 改比较符 / 改阈值）都同步回提交体。
  // 为什么用 deep watch 而不是在每个 @change 里手写同步：漏一处就是"界面改了、提交体没改"，
  // 而这种不一致在保存前完全不可见（用户以为条件已经生效，实际提交的是旧条件）。
  watch(conditionRows, syncConditionsJson, { deep: true })

  /**
   * 源码模式下的原文是否**不可还原**（实时判定，而不是只记"打开那一刻是坏的"）。
   * 为什么实时：用户在源码里改到一半（比如删了个逗号）时，界面要立刻说明"现在切不回去"，
   * 而不是等他点了按钮才弹一句、还不知道是哪里不对。
   */
  const conditionsRawInvalid = computed(() =>
    conditionsRawMode.value
    && form.conditions_json.trim() !== ''
    && parseConditionRows(form.conditions_json) === null,
  )

  /** 新增一行空条件：默认 gt / 0（与上方触发条件的默认值同口径）。空行由 validate() 拦。 */
  function addCondition() {
    conditionRows.value.push({ uid: uidSeed(), selector: '', comparator: 'gt', threshold: 0 })
  }

  function removeCondition(uid: number) {
    conditionRows.value = conditionRows.value.filter(row => row.uid !== uid)
  }

  /**
   * 「JSON 源码」开关。
   *
   * 行 → 源码：行是唯一真相，conditions_json 已由上面的 watch 持续保持同步，直接切即可。
   * 源码 → 行：重新解析；**解析失败就拒绝关闭**并提示。关闭的同时把用户刚输入的内容换掉，
   * 比"停在源码模式"严重得多 —— 前者是数据丢失，后者只是多一个步骤。
   */
  function toggleConditionsRaw() {
    if (!conditionsRawMode.value) {
      conditionsRawMode.value = true
      return
    }
    const rows = parseConditionRows(form.conditions_json)
    if (!rows) {
      ElMessage.warning('现有内容无法解析为条件数组，已保留原文；修正后才能切回条件行编辑')
      return
    }
    // 先落行、再退出源码模式：watch 回调排在本段同步代码之后，那时 conditionsRawMode
    // 已是 false，会顺手把（可能带空格/换行的）原文规范成与提交体同款的 JSON。
    conditionRows.value = rows
    conditionsRawMode.value = false
  }

  /**
   * 切换规则/重开对话框时的复位（原 openCreate / openEdit 里的三行）。
   *
   * 顺序有讲究：先定模式再落行 —— 源码模式下 syncConditionsJson 会直接返回，
   * 原文一个字符都不会被行数据覆盖。
   */
  function resetConditions(raw: string | null | undefined) {
    const parsed = parseConditionRows(raw ?? '')
    conditionsRawMode.value = parsed === null
    conditionRows.value = parsed ?? []
  }

  return {
    conditionRows,
    conditionsRawMode,
    conditionsRawInvalid,
    addCondition,
    removeCondition,
    toggleConditionsRaw,
    syncConditionsJson,
    resetConditions,
  }
}
