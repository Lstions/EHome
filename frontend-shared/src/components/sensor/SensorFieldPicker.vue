<template>
  <div class="sensor-field-picker" data-test="sensor-field-picker">
    <!-- 未选设备：edge_device_id 是必填 query，提前发请求只会拿到 400/404，故一个请求都不发 -->
    <p v-if="!hasDevice" class="sf-text sf-text--muted" data-test="sensor-field-need-device">
      请先选择边缘设备
    </p>

    <template v-else>
      <!-- 类型门禁的显式开关：默认只列数值（含未知类型），切到「全部」也不会让非数值项变成可选 -->
      <div v-if="catalogState !== 'manual'" class="sf-modes" role="group" aria-label="字段子集">
        <button
          type="button"
          class="sf-mode"
          :class="{ 'is-active': mode === 'numeric' }"
          :aria-pressed="mode === 'numeric'"
          data-test="sensor-mode-numeric"
          @click="setMode('numeric')"
        >
          数值 {{ numericCount }}
        </button>
        <button
          type="button"
          class="sf-mode"
          :class="{ 'is-active': mode === 'all' }"
          :aria-pressed="mode === 'all'"
          data-test="sensor-mode-all"
          @click="setMode('all')"
        >
          全部 {{ totalCount }}
        </button>
      </div>

      <!-- 降级链第三级：字段目录与已上报类别都不可用 ⇒ 纯手输。
           不渲染一个永远选不出东西的下拉，但也不阻止建规则（手输仍要过保存前 resolve 校验）。 -->
      <el-input
        v-if="catalogState === 'manual'"
        :model-value="modelValue"
        :disabled="disabled"
        clearable
        :placeholder="placeholder || '输入物理名，如 temperature_1'"
        aria-label="传感器字段物理名"
        data-test="sensor-field-manual"
        @update:model-value="onManualInput"
      />
      <div v-else class="sf-control" data-test="sensor-field-select">
        <el-select
          :model-value="selectValue"
          :disabled="disabled"
          filterable
          :allow-create="allowCreate"
          default-first-option
          clearable
          :loading="loading"
          :placeholder="placeholder || '选择或输入字段'"
          aria-label="传感器字段选择器"
          @update:model-value="onSelect"
        >
          <!-- 外部传入的当前值必须始终可见：编辑存量规则、或值来自降级期间手输时，
               el-select 找不到匹配项会显示空白，用户就看不出这条规则绑的是哪个字段 -->
          <el-option
            v-if="currentValueOption"
            :label="currentValueOption.label"
            :value="currentValueOption.value"
            data-test="sensor-field-option"
          />
          <el-option
            v-for="opt in pickerOptions"
            :key="opt.key"
            :class="{ 'sensor-field-option--deprecated': opt.deprecated }"
            :label="opt.label"
            :value="opt.value"
            :disabled="opt.disabled"
            data-test="sensor-field-option"
          />
          <!-- 语义别名分组只在 /schema.aliases 非空时渲染：一个选不出东西的模式不如不展示 -->
          <el-option-group v-if="aliasOptions.length > 0" label="@@别名" data-test="sensor-alias-group">
            <el-option
              v-for="alias in aliasOptions"
              :key="alias.value"
              :label="alias.label"
              :value="alias.value"
              data-test="sensor-field-option"
            />
          </el-option-group>
        </el-select>
      </div>

      <p v-if="truncated" class="sf-text" data-test="sensor-truncated">
        字段过多，后端已截断列表（可用搜索或直接手输未列出的物理名）。
      </p>
      <p v-if="catalogState === 'fallback'" class="sf-text sf-text--warn" data-test="sensor-fallback-note">
        字段目录不可用，已降级为设备已上报的类别（无中文名与类型；类型未知按数值对待）。
      </p>
      <p v-if="catalogState === 'manual'" class="sf-text sf-text--warn" data-test="sensor-manual-note">
        字段目录不可用，可手工输入物理名
      </p>
      <p v-if="catalogState === 'ready' && fields.length === 0" class="sf-text" data-test="sensor-empty">
        该设备暂无可用字段，可手工输入物理名。
      </p>
      <p v-if="mode === 'all' && hasNonNumeric" class="sf-text" data-test="sensor-nonnumeric-note">
        已显示全部字段：标有「非数值，阈值比较不生效」的字段不可选（后端判 fieldNumeric=false ⇒ 规则永不触发）。
      </p>

      <!-- 族已选中 ⇒ 聚合下拉。候选只来自 /schema.aggregates，默认值只来自 aggregate_default；
           本组件没有任何聚合名字面量用于生成候选（设计文档 §8.4 禁止第二份词表） -->
      <div v-if="familySelection" class="sf-aggregate" data-test="sensor-aggregate">
        <span class="sf-aggregate__label">聚合</span>
        <el-select
          :model-value="currentAggregate"
          :disabled="disabled || !hasAggregateVocabulary"
          :placeholder="hasAggregateVocabulary ? '选择聚合' : '聚合词表不可用'"
          aria-label="字段族聚合方式"
          @update:model-value="onAggregateSelect"
        >
          <el-option v-for="choice in aggregateChoices" :key="choice.value" :label="choice.label" :value="choice.value" />
        </el-select>
        <code class="sf-aggregate__selector">{{ modelValue }}</code>
        <span class="sf-aggregate__gap">{{ aggregateGapNote(currentAggregate) }}</span>
      </div>
      <p v-if="familySelection && hasAggregateVocabulary" class="sf-text" data-test="sensor-aggregate-gap">
        缺员语义：all / avg / sum 缺员即不触发；min / max 仍按现有成员求值并标降级。
      </p>

      <!-- 保存前校验：resolved 灰字 / unbound 黄字（不阻止输入）/ 400 红字 / 解析器不可用黄字 -->
      <p
        v-if="resolveState.tone !== 'idle'"
        :class="resolveStatusClass"
        :data-status="resolveState.tone"
        data-test="sensor-resolve-status"
        role="status"
      >
        {{ resolveState.text }}
      </p>
    </template>
  </div>
</template>

<script setup lang="ts">
/**
 * 传感器字段选择器（自动化/告警规则的 `trigger_sensor_name` 输入控件）。
 *
 * 为什么不是"一个普通下拉就够了"：普通下拉只能产出字面量，等于把「字段族极值 / 语义别名」
 * 这两个已经实现、且在 BMS/逆变器场景最常用的能力永久藏起来（文档 §3.1 的表就是差距本身）。
 * 而裸文本框的问题是反方向的：写错一个字母保存成功、永不触发、没有任何报错。
 *
 * 三条硬约束（都有单测钉住）：
 *   1. **类型门禁**：默认只列 value_type ∈ {number, 未知}；「全部」模式下非数值项可见但不可选
 *      —— 后端 fieldNumeric=false ⇒ 规则永不触发（生产 25.4% 字段是这种，文档 §3.2）。
 *   2. **聚合词表只能来自 /schema**：aggregates / aggregate_default 一个都不许硬编码，
 *      否则后端扩展聚合时前端静默漂移（设计文档 §8.4）。
 *   3. **三级降级链**：/sensor-fields → /unified-data/categories → 纯手输，任何一层失败都
 *      不抛、不阻塞建规则，但都要 logger.warn 留痕（静默降级等于把故障藏起来）。
 *
 * a11y / 测试约定：el-select 带 aria-label；data-test 见模板（根、选项、聚合、状态行）。
 */
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { sensorFieldApi, type SensorField, type SensorFieldSchema } from '@/api/sensorField'
import { logger } from '@/utils/logger'
import {
  FAMILY_SELECT_PREFIX,
  aggregateBrief,
  aggregateGapNote,
  buildFamilyOptionTitle,
  buildFamilySelector,
  buildFieldOptionLabel,
  fallbackFieldFromCategory,
  groupFieldFamilies,
  isNumericGate,
  parseFamilySelector,
  toSelectorErrorInfo,
  translateSelectorError,
} from './sensorFieldLogic'

interface Props {
  /** 选择器原文，提交后端的唯一真相（字面量 / 字段族+聚合 / @别名） */
  modelValue: string
  /** 边缘设备 id；未选设备时不发任何请求 */
  deviceId?: number
  disabled?: boolean
  /** 默认文案；空串时用组件内置文案 */
  placeholder?: string
  /** 是否允许手输/自建选项（默认 true：设备尚未上报该字段时不能硬阻塞建规则） */
  allowCreate?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  deviceId: undefined,
  disabled: false,
  placeholder: '',
  allowCreate: true,
})

const emit = defineEmits<{
  'update:modelValue': [value: string]
  /** 选中具体字段给对象（父组件用它换算单位/小数位）；族 / 手输给 null */
  'change-field': [field: SensorField | null]
}>()

type PickerMode = 'numeric' | 'all'
/** 目录状态：idle=未请求 ready=字段目录 fallback=已上报类别 manual=纯手输 */
type CatalogState = 'idle' | 'ready' | 'fallback' | 'manual'
type ResolveTone = 'idle' | 'checking' | 'resolved' | 'unbound' | 'error' | 'unavailable'

interface ResolveState {
  tone: ResolveTone
  text: string
}

/** 与文档 §4.1「防抖 400ms 调 resolve」一致 */
const RESOLVE_DEBOUNCE_MS = 400

const fields = ref<SensorField[]>([])
const schema = ref<SensorFieldSchema | null>(null)
const catalogState = ref<CatalogState>('idle')
const truncated = ref(false)
const loading = ref(false)
const mode = ref<PickerMode>('numeric')
const resolveState = ref<ResolveState>({ tone: 'idle', text: '' })

/** 目录/类别请求序号：切设备或再次 reload 时让在途响应失效，避免慢响应覆盖新设备的字段 */
let loadSeq = 0
/** 上一次回填 change-field 的 (设备, 字段名) 指纹，见 refillFieldFromCatalog */
let lastRefill: { deviceId: number | undefined; name: string } | null = null
/** resolve 请求序号 + 防抖定时器（与目录请求分开计数，互不干扰） */
let resolveSeq = 0
let resolveTimer: ReturnType<typeof setTimeout> | null = null

const hasDevice = computed(() => typeof props.deviceId === 'number' && props.deviceId > 0)
const numericCount = computed(() => fields.value.filter(field => isNumericGate(field.value_type)).length)
const totalCount = computed(() => fields.value.length)
const hasNonNumeric = computed(() => fields.value.some(field => !isNumericGate(field.value_type)))

/**
 * 聚合候选：**只**取 /schema.aggregates。
 * 这里刻意没有 'any'/'max'/... 的兜底数组 —— 词表缺失时宁可让族条目不可选，
 * 也不要在前端另存一份（后端加聚合/改默认值时，第二份词表就是漂移源）。
 */
const aggregateOptions = computed<string[]>(() => {
  const list = schema.value?.aggregates
  return Array.isArray(list) ? list.filter(item => typeof item === 'string' && item.length > 0) : []
})
const hasAggregateVocabulary = computed(() => aggregateOptions.value.length > 0)

/**
 * 默认聚合：来自 /schema.aggregate_default。
 * 若后端给的默认值不在词表内（契约漂移），退回词表首项 —— 仍然不猜一个写死的 'any'。
 */
const defaultAggregate = computed<string>(() => {
  const configured = (schema.value?.aggregate_default ?? '').trim()
  if (configured && aggregateOptions.value.includes(configured)) return configured
  return aggregateOptions.value[0] ?? ''
})

/** 当前 modelValue 是否是「族 + 聚合」形态（编辑存量规则时也要还原出聚合下拉） */
const familySelection = computed(() => parseFamilySelector(props.modelValue))
const currentAggregate = computed(() => familySelection.value?.aggregate ?? '')

interface AggregateChoice {
  value: string
  label: string
}

const aggregateChoices = computed<AggregateChoice[]>(() => {
  const choices: AggregateChoice[] = aggregateOptions.value.map(aggregate => ({
    value: aggregate,
    // 人话是展示层，未知聚合只显示原名（不编造语义）
    label: aggregateBrief(aggregate) ? aggregate + ' · ' + aggregateBrief(aggregate) : aggregate,
  }))
  const current = currentAggregate.value
  // 存量规则可能是词表外的聚合（后端收窄过词表 / 人工改库）：原样显示当前值并标注，
  // 绝不悄悄改写 —— 改写会在用户没动它的情况下改变规则行为。
  if (current && !choices.some(choice => choice.value === current)) {
    choices.unshift({ value: current, label: current + '（当前值，不在词表内）' })
  }
  return choices
})

interface PickerOption {
  key: string
  value: string
  label: string
  disabled: boolean
  deprecated: boolean
}

/**
 * 下拉候选 = 族伪条目 + 字段条目。
 *
 * 族条目排前面：它是"多字段聚合"这种最强能力的入口，藏在 16 条 `cell_voltage_N` 后面
 * 用户永远发现不了（这正是文档 §3.1 说"用户永远发现不了后三类能力"的原因）。
 */
const pickerOptions = computed<PickerOption[]>(() => {
  const now = Date.now()
  const visibleFields = mode.value === 'numeric'
    ? fields.value.filter(field => isNumericGate(field.value_type))
    : fields.value

  const familyEntries: PickerOption[] = groupFieldFamilies(visibleFields).map(family => {
    // 族可选的两个条件缺一不可：
    //   ① 成员全是数值/未知类型（含非数值成员时后端 fieldNumeric=false ⇒ 永不触发）；
    //   ② 拿到了聚合词表（前端不猜聚合名，词表缺失时选了也产不出合法选择器）。
    const allNumeric = family.members.every(member => isNumericGate(member.value_type))
    const disabled = !allNumeric || !hasAggregateVocabulary.value
    const suffix = !hasAggregateVocabulary.value
      ? '（聚合词表不可用）'
      : (allNumeric ? '' : '（含非数值字段，阈值比较不生效）')
    return {
      key: 'family:' + family.prefix,
      value: FAMILY_SELECT_PREFIX + family.prefix,
      label: buildFamilyOptionTitle(family) + ' · ' + family.prefix + '_*' + suffix,
      disabled,
      deprecated: false,
    }
  })

  const fieldEntries: PickerOption[] = visibleFields
    .slice()
    .sort((a, b) => a.name.localeCompare(b.name))
    .map(field => ({
      key: 'field:' + field.name,
      value: field.name,
      label: buildFieldOptionLabel(field, now),
      // 「全部」模式下非数值项可见但不可选：让用户看见"设备确实有这个字段"，
      // 同时用 disabled + 文案说明"选了也不会触发"，而不是让人先选中、再在运行期失败。
      disabled: !isNumericGate(field.value_type),
      deprecated: Boolean(field.deprecated),
    }))

  return familyEntries.concat(fieldEntries)
})

interface AliasOption {
  value: string
  label: string
}

/** 语义别名候选：仅当 /schema.aliases 非空时才有内容（空表时整块不渲染） */
const aliasOptions = computed<AliasOption[]>(() => {
  const list = schema.value?.aliases
  if (!Array.isArray(list)) return []
  return list
    .filter(alias => typeof alias?.semantic === 'string' && alias.semantic.length > 0)
    .map(alias => {
      const candidates = Array.isArray(alias.candidates) ? alias.candidates : []
      const unit = alias.expected_unit ? ' · ' + alias.expected_unit : ''
      const target = candidates.length > 0 ? '归一为 ' + candidates[0] : '无候选'
      return { value: '@' + alias.semantic, label: '@' + alias.semantic + ' · ' + target + unit }
    })
})

/** 外部传进来的当前值不在任何候选里时，补一条占位选项，避免下拉显示空白 */
const currentValueOption = computed<AliasOption | null>(() => {
  const value = (props.modelValue ?? '').trim()
  if (!value || familySelection.value) return null
  const known = pickerOptions.value.some(option => option.value === value)
    || aliasOptions.value.some(option => option.value === value)
  return known ? null : { value, label: value + '（当前值）' }
})

/** el-select 的显示值：族形态映射回族伪条目，其余原样（含手输值） */
const selectValue = computed(() => {
  const selection = familySelection.value
  return selection ? FAMILY_SELECT_PREFIX + selection.prefix : props.modelValue
})

const resolveStatusClass = computed(() => 'sf-status sf-status--' + resolveState.value.tone)

function emitValue(next: string): void {
  if (next === props.modelValue) return
  emit('update:modelValue', next)
}

function setMode(next: PickerMode): void {
  mode.value = next
}

/** 选中主下拉的一项（含 allow-create 手输产生的项） */
function onSelect(raw: unknown): void {
  const value = typeof raw === 'string' ? raw : (raw == null ? '' : String(raw))
  if (value.startsWith(FAMILY_SELECT_PREFIX)) {
    const prefix = value.slice(FAMILY_SELECT_PREFIX.length)
    const aggregate = defaultAggregate.value
    // 词表不可用时族条目本该是 disabled；真走到这里也只产 `prefix_*`（不带 `:聚合`），
    // 让后端的 400 暴露出来，而不是前端编一个聚合名 —— 编错的聚合正是静默死规则
    emitValue(aggregate ? buildFamilySelector(prefix, aggregate) : prefix + '_*')
    emit('change-field', null)
    return
  }
  emitValue(value)
  // 选中具体字段 → 给对象（父组件要用 unit/precision 渲染阈值控件）；
  // 族 → null；手输的值若恰好等于目录里的物理名，也给出对象（语义上就是那个字段）
  emit('change-field', fields.value.find(field => field.name === value) ?? null)
}

/** 聚合下拉变化：改写选择器里的聚合段，族名保持不变 */
function onAggregateSelect(raw: unknown): void {
  const aggregate = typeof raw === 'string' ? raw : ''
  const selection = familySelection.value
  if (!selection || !aggregate) return
  emitValue(buildFamilySelector(selection.prefix, aggregate))
  emit('change-field', null)
}

/**
 * 目录就绪后回填「当前值对应的字段对象」（**每次载入一次**，不是 watch modelValue）。
 *
 * 为什么必须有这一步：编辑存量规则时用户没有任何交互 —— 父组件只是把字符串写进
 * modelValue，拿不到 unit/precision，于是阈值控件既没有单位也没有正确的小数位；
 * 用户必须先随便动一下字段再改回来才能看到单位，这显然不是契约该有的样子。
 *
 * 为什么不能改成 watch(modelValue)：那样用户手输时（onSelect / onManualInput 已经 emit）
 * 会再叠加一次 emit，父组件在同一份输入上收到两次语义不同的通知；而且手输中间值
 * （例如只敲到 "temperat"）也会被当成一次"选中"。
 *
 * 只在"目录刚就绪 / 换了设备"时调一次；族与 @别名照旧给 null（没有单一单位），
 * 与 onSelect 的语义保持一致。
 */
function refillFieldFromCatalog(): void {
  const value = (props.modelValue ?? '').trim()
  if (!value || familySelection.value) return
  const matched = fields.value.find(field => field.name === value)
  if (!matched) return
  // 同一 (设备, 字段名) 只回填一次：父组件手动 reload 目录时不该反复收到同一份通知
  // （它会据此重写阈值单位，重复通知等于每次刷新都抖一下）。
  // 换设备必须重新回填 —— 同名物理字段在不同设备上单位/精度可能不同。
  if (lastRefill && lastRefill.deviceId === props.deviceId && lastRefill.name === matched.name) return
  lastRefill = { deviceId: props.deviceId, name: matched.name }
  emit('change-field', matched)
}

/** 降级第三级的手输输入框（此时没有目录可查，一律给 null） */
function onManualInput(raw: unknown): void {
  const value = typeof raw === 'string' ? raw : (raw == null ? '' : String(raw))
  emitValue(value)
  emit('change-field', null)
}

function clearResolveTimer(): void {
  if (resolveTimer) {
    clearTimeout(resolveTimer)
    resolveTimer = null
  }
}

/**
 * 保存前校验的调度：值一变就重排 400ms 防抖。
 *
 * 为什么防抖：每敲一个字符打一次 resolve 会把后端解析器打满，而且红/黄提示会跟着字符闪烁
 * （用户根本看不清是在说哪一段）。
 * 为什么 unbound 不阻止输入：与后端保存期口径一致 —— 它只是 warning（"保存后可能永不触发"），
 * 字段可能是设备暂时未上报，硬拦会让用户无法提前配规则。
 */
function scheduleResolve(): void {
  clearResolveTimer()
  // 先让在途请求失效：字符又变了，旧结果不再代表当前值
  resolveSeq += 1
  const deviceId = props.deviceId
  const selector = (props.modelValue ?? '').trim()
  if (!deviceId || deviceId <= 0 || !selector) {
    resolveState.value = { tone: 'idle', text: '' }
    return
  }
  resolveState.value = { tone: 'checking', text: '校验中…' }
  resolveTimer = setTimeout(() => {
    resolveTimer = null
    void runResolve(deviceId, selector)
  }, RESOLVE_DEBOUNCE_MS)
}

async function runResolve(deviceId: number, selector: string): Promise<void> {
  const seq = ++resolveSeq
  try {
    const resolution = await sensorFieldApi.resolve(deviceId, selector)
    if (seq !== resolveSeq) return
    if (!resolution || typeof resolution !== 'object') {
      resolveState.value = { tone: 'unavailable', text: '校验暂不可用，保存前请确认字段名' }
      return
    }
    if (resolution.status === 'resolved') {
      const matched = Array.isArray(resolution.matched) ? resolution.matched : []
      // 别名/族命中多候选时后端只给 ambiguous + 首候选：明说"已归一"，
      // 否则用户会以为别的候选被漏掉了（文档 §4.1「已归一为 rsoc（首候选）」）
      const normalized = resolution.ambiguous && matched.length > 0 ? '已归一为 ' + matched[0] + '；' : ''
      resolveState.value = { tone: 'resolved', text: normalized + '解析到 ' + matched.length + ' 个字段' }
      return
    }
    resolveState.value = { tone: 'unbound', text: '当前解析不到，保存后可能永不触发' }
  } catch (error) {
    if (seq !== resolveSeq) return
    const info = toSelectorErrorInfo(error)
    if (info.status === 400) {
      // 400 = 语法/未知语义/未知派生：红字 + 用 /schema.error_codes 翻成人话（未知码回落 message）
      resolveState.value = {
        tone: 'error',
        text: translateSelectorError(info.errorCode, info.message, schema.value?.error_codes),
      }
      return
    }
    // 503/网络/404 = 解析器整体不可用，不能说"你的字段名写错了"
    resolveState.value = { tone: 'unavailable', text: '校验暂不可用，保存前请确认字段名' }
    logger.warn('传感器字段保存前校验不可用（非 400），已跳过硬诊断', { error: info.message, deviceId, selector })
  }
}

function resetCatalog(): void {
  fields.value = []
  truncated.value = false
  catalogState.value = 'idle'
  loading.value = false
  // 回填指纹一并作废：设备被清空后重新选中同一台设备时，单位仍要能重新回填
  lastRefill = null
  // 序号自增：让尚未返回的旧响应作废
  loadSeq += 1
}

/** schema 与设备无关：成功过就不再拉（失败过则每次 reload 重试一次，成本一次 GET） */
async function loadSchemaOnce(): Promise<void> {
  if (schema.value) return
  try {
    const loaded = await sensorFieldApi.getSchema()
    schema.value = loaded && typeof loaded === 'object' ? loaded : null
  } catch (error) {
    // schema 只影响「聚合候选 / 别名分组 / 错误码人话」，拿不到不阻塞字段选择，也不向上抛
    logger.warn('传感器字段 schema 拉取失败：聚合词表与错误码翻译不可用', { error: String(error) })
  }
}

/**
 * 拉取字段目录（三级降级链的唯一入口）。
 *
 * 1) /sensor-fields：有中文名、单位、类型、最近上报 —— 正常态；
 * 2) /unified-data/categories：只有 code+unit（类型未知按数值对待，仍然比手敲强）；
 * 3) 纯手输：两个都拿不到时也不抛、不阻塞建规则，只留一条 warn 并提示可手输物理名。
 */
async function reload(): Promise<void> {
  const deviceId = props.deviceId
  if (!deviceId || deviceId <= 0) {
    resetCatalog()
    return
  }
  const seq = ++loadSeq
  loading.value = true
  try {
    await loadSchemaOnce()
    if (seq !== loadSeq) return
    try {
      const catalog = await sensorFieldApi.listFields(deviceId)
      if (seq !== loadSeq) return
      fields.value = Array.isArray(catalog?.fields) ? catalog.fields : []
      truncated.value = Boolean(catalog?.truncated)
      catalogState.value = 'ready'
    } catch (fieldsError) {
      if (seq !== loadSeq) return
      // 降级不得抛未处理异常：目录 503/网络失败时先试第二级
      logger.warn('传感器字段目录不可用，降级到设备已上报类别', { error: String(fieldsError), deviceId })
      try {
        const categories = await sensorFieldApi.listCategories(deviceId)
        if (seq !== loadSeq) return
        const list = Array.isArray(categories) ? categories : []
        fields.value = list
          .map(item => fallbackFieldFromCategory(item?.code ?? '', item?.unit ?? ''))
          .filter(field => field.name.length > 0)
        truncated.value = false
        catalogState.value = 'fallback'
      } catch (categoriesError) {
        if (seq !== loadSeq) return
        logger.warn('字段目录与已上报类别均不可用，退化为手工输入物理名', { error: String(categoriesError), deviceId })
        fields.value = []
        truncated.value = false
        catalogState.value = 'manual'
      }
    }
  } finally {
    if (seq === loadSeq) {
      loading.value = false
      // 三级降级链的任意一级成功都可能让"当前 modelValue"从字符串变成可确认的字段对象，
      // 故统一在这里回填一次（见 refillFieldFromCatalog 注释）。
      refillFieldFromCatalog()
    }
  }
}

watch(() => props.deviceId, () => {
  // 换设备：默认回到「数值」门禁；同一选择器在不同设备上解析结果可能不同，必须重算
  mode.value = 'numeric'
  clearResolveTimer()
  void reload()
  scheduleResolve()
})

watch(() => props.modelValue, () => {
  scheduleResolve()
})

onMounted(() => {
  void reload()
  scheduleResolve()
})

onUnmounted(() => {
  clearResolveTimer()
  // 卸载后到达的响应不得再写状态
  resolveSeq += 1
  loadSeq += 1
})

/**
 * 手动刷新（父组件在目录变化后调用）。
 *
 * 内部已 watch deviceId 自动重载，保留该方法是为了「同一设备但字段目录变了」的场景
 * （例如设备刚升级固件、新的声明集已落库）。
 */
defineExpose({ reload })
</script>

<style scoped>
/*
  宽度：占满父容器（规则弹窗里的字段行），不自带页面级断点 —— 该组件出现在弹窗/内联表单
  两种容器里，断点由容器决定，组件内再写一套 @media 只会与容器打架。

  触控高度 ≥36px：EP 输入类控件默认 32px，粗指针设备上低于可用触控尺寸。
  这里只抬本组件的三处控件（模式切换、主下拉、聚合下拉），不动全局控件档位。
*/
.sensor-field-picker {
  display: flex;
  flex-direction: column;
  gap: 6px;
  width: 100%;
}

.sf-control {
  width: 100%;
}

.sf-control :deep(.el-select) {
  width: 100%;
}

.sf-control :deep(.el-select__wrapper) {
  min-height: 36px;
}

.sf-modes {
  display: flex;
  gap: 6px;
}

.sf-mode {
  min-height: 36px;
  padding: 0 12px;
  border: 1px solid var(--el-border-color);
  border-radius: 6px;
  background: var(--el-fill-color-blank);
  color: var(--el-text-color-regular);
  font-size: 13px;
  line-height: 1;
  cursor: pointer;
}

.sf-mode.is-active {
  border-color: var(--el-color-primary);
  color: var(--el-color-primary);
  background: var(--el-color-primary-light-9);
}

.sf-aggregate {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  width: 100%;
}

.sf-aggregate :deep(.el-select) {
  width: 200px;
  max-width: 100%;
}

.sf-aggregate :deep(.el-select__wrapper) {
  min-height: 36px;
}

.sf-aggregate__label {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.sf-aggregate__selector {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  word-break: break-all;
}

.sf-aggregate__gap {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.sf-text {
  margin: 0;
  font-size: 12px;
  line-height: 1.5;
  color: var(--el-text-color-secondary);
}

.sf-text--muted {
  color: var(--el-text-color-placeholder);
}

.sf-text--warn {
  color: var(--el-color-warning);
}

.sf-status {
  margin: 0;
  font-size: 12px;
  line-height: 1.5;
}

.sf-status--resolved {
  color: var(--el-text-color-secondary);
}

.sf-status--unbound,
.sf-status--unavailable {
  color: var(--el-color-warning);
}

.sf-status--error {
  color: var(--el-color-danger);
}

.sf-status--checking {
  color: var(--el-text-color-placeholder);
}
</style>

<style>
/*
  el-select 的下拉 popper 默认 teleport 到 body，scoped 样式够不到（`:deep` 也不行），
  所以「已弃用 → 灰显」只能用一条非 scoped 的具名类规则。
  类名足够具体（挂在 el-option 上），不会泄露到其它下拉。
*/
.sensor-field-option--deprecated {
  color: var(--el-text-color-disabled, #a8abb2);
}
</style>
