<template>
  <el-form-item label="触发器类型" required>
    <!-- 下拉值域与后端 models.AutomationTriggerType **不同**：后端只有 sun_event
         一个类型（日出/日落由 trigger_sun_kind 区分），而设计稿 R4 与用户需求都要求
         下拉里直接看到「日出」「日落」两项。故这里绑 triggerSelection 这个
         computed 代理（读时把 sun_event 展开为 kind，写时把 sunrise/sunset 折叠回
         trigger_type='sun_event' + trigger_sun_kind），提交时由 buildPayload() 映射。
         为什么不塞一个 "sun_event" 项再另设二级下拉：那会让下拉文案与用户脑中的
         "日落前 30 分钟" 隔一层，而这句话本身就是需求原文（设计 D-2）。 -->
    <el-select
      :model-value="triggerSelection"
      data-test="field-trigger-type"
      @update:model-value="onTriggerSelectionChange"
      @change="onTriggerTypeChange"
    >
      <el-option label="传感器阈值" value="sensor_threshold" />
      <el-option label="时间窗口" value="time_window" />
      <el-option label="日出" value="sunrise" />
      <el-option label="日落" value="sunset" />
      <el-option label="事件驱动（暂不支持创建）" value="event" disabled />
    </el-select>
  </el-form-item>

  <!-- sensor_threshold 字段组 -->
  <template v-if="form.trigger_type === 'sensor_threshold'">
    <!-- 数据来源（方案 v0.3 §4.3）：①某台边缘设备（存量语义，NULL 同义）②来源组（主备/复合）。
         来源组是跨设备的"一个数据来源"对象，规则只绑 source_group_id；目标设备锚点由后端保存期双写。 -->
    <el-form-item label="数据来源">
      <el-select v-model="form.source_kind" data-test="field-source-kind" @change="onSourceKindChange">
        <el-option label="边缘设备" value="edge_device" />
        <el-option label="来源组（主备 / 复合）" value="source_group" />
      </el-select>
    </el-form-item>
    <template v-if="form.source_kind === 'source_group'">
      <el-form-item label="来源组" required>
        <el-select v-model="form.source_group_id" filterable data-test="field-source-group" @change="onSourceGroupChange">
          <el-option v-for="g in sourceGroupOptions" :key="g.id" :label="groupOptionLabel(g)" :value="g.id" />
        </el-select>
      </el-form-item>
      <el-form-item v-if="selectedSourceGroup" label="组语义">
        <span class="hint" data-test="source-group-summary">{{ groupSummary(selectedSourceGroup) }}</span>
      </el-form-item>
      <el-form-item label="冷却提示">
        <span class="hint" data-test="cooldown-hint">来源组规则的冷却时间须 ≥ 成员采集间隔（后端保存期强制）；复合组按 combine 合成，缺员处置见组配置。</span>
      </el-form-item>
    </template>
    <el-form-item v-else label="边缘设备" required>
      <el-select v-model="form.trigger_edge_device_id" filterable data-test="field-trigger-device">
        <el-option v-for="d in devices" :key="d.id" :label="d.name" :value="d.id" />
      </el-select>
    </el-form-item>
    <!-- 传感器字段：按数据来源分成两个独立分支，而不是"一个文本框加 disabled"。
         为什么必须换掉裸文本框（文档 §3.1/§4.1）：后端接受的是**字段选择器契约**
         （字面量 temperature_1 / 字段族 cell_voltage_*:max / 语义别名 @soc / 派生量），
         手打物理名拼错一个字母 = 保存成功 + 永不触发 + 界面无痕；而文本框连后两类
         形态都表达不出来。
         为什么来源组分支**不放**选择器：字段清单由组的 category 决定，选择器没有
         设备可查目录，只会显示「请先选择边缘设备」—— 对一个已经选了来源组的用户
         是纯误导（不是"不可用"，而是"问错了问题"）。 -->
    <el-form-item v-if="form.source_kind === 'source_group'" label="传感器" required>
      <el-input
        v-model="form.trigger_sensor_name"
        disabled
        placeholder="由来源组的数据类别决定"
        data-test="field-sensor-readonly"
      />
      <div class="hint-block" data-test="sensor-source-group-hint">
        来源组的字段由所选组的数据类别决定（见上方「组语义」），保存时后端按组解析；
        需要逐字段选择或字段族聚合时，请改用「边缘设备」来源。
      </div>
    </el-form-item>
    <el-form-item v-else label="传感器" required>
      <SensorFieldPicker
        v-model="form.trigger_sensor_name"
        :device-id="form.trigger_edge_device_id"
        @change-field="onSensorFieldChange"
      />
    </el-form-item>
    <el-form-item label="条件" required>
      <div class="cond-row">
        <el-select v-model="form.trigger_comparator" data-test="field-comparator" style="width: 100px">
          <el-option v-for="c in comparators" :key="c.value" :label="c.label" :value="c.value" />
        </el-select>
        <el-input-number
          v-model="form.trigger_threshold"
          :precision="thresholdPrecision"
          data-test="field-threshold"
        />
        <!-- 单位只从字段目录取（change-field 给的 SensorField.unit），未知时不渲染。
             为什么**不**复用 utils/sensor.ts 的 sensorUnitMap：那张表是第二份词表，
             与 /sensor-fields 的 meta 并行存在；后端改单位或新增字段时它不会跟着变，
             界面就会显示一个与契约不符的单位（文档 §4.1：字段目录才是权威）。
             为什么不写成"单位映射表 + 目录兜底"：两份数据源必然漂移，且漂移是静默的。 -->
        <span v-if="thresholdUnit" class="hint threshold-unit" data-test="field-threshold-unit">{{ thresholdUnit }}</span>
      </div>
      <!-- 2026-11-12 保存前试算（文档 §4.4）：用近 24h 真实数据回答"这条规则保存后会不会响"。
           为什么是按钮、不是输入即请求：后端要扫帧表（24h 窗口在生产可达数十万帧），
           每敲一位数字都打一次 = 把填表单变成压测；用户又常常先填完整再回头改，
           自动请求只会产出一串互相覆盖的结论。
           为什么只给边缘设备：来源组没有单一设备可扫（active 成员由数据源状态机决定），
           拿组里任一成员试算，回答的是另一条规则的问题，不是这条。
           为什么"不会触发"只提示、不阻止保存：试算描述的是"最近 24h 有没有数据满足"，
           不是"规则是否合法"——阈值先填、设备后接入是正常流程。保存校验（validate /
           onSave）**完全不读 previewResult**，这一行永远只是提示。 -->
      <template v-if="form.source_kind === 'edge_device'">
        <div class="preview-row">
          <el-button
            size="small"
            :loading="previewLoading"
            :disabled="previewDisabled"
            :title="previewHint"
            data-test="preview-button"
            @click="onPreview"
          >用近 24h 数据试算</el-button>
          <span class="hint">只读预演，不改动规则，也不影响保存</span>
        </div>
        <!-- 一句人话就是结论；原文 frames / trace 不往界面上堆（排障看接口）。
             tone=warning 只用于"试算不可用"，不是规则校验错误。 -->
        <div
          v-if="previewResult"
          class="preview-result"
          :class="{ 'preview-result--warning': previewResult.tone === 'warning' }"
          data-test="preview-result"
        >{{ previewResult.text }}</div>
      </template>
      <div v-else class="hint-block" data-test="preview-unavailable">
        来源组规则不支持试算（组内成员由数据源状态机决定）
      </div>
    </el-form-item>
    <el-form-item label="持续时长">
      <el-input-number v-model="form.trigger_duration_sec" :min="0" :step="10" data-test="field-duration" />
      <span class="hint">秒，0 = 立即触发</span>
    </el-form-item>
  </template>

  <!-- time_window 字段组：时间窗改用**时间选择器**（文档 §3.5 / §4.5）。
       为什么这一步能消灭一整类静默失效：后端保存期只校验"非空"，运行期用
       time.Parse("15:04") 解析，`8:00`（不补零）解析失败时只打一条 warn 就 return
       —— 规则永久失效、不落审计、不产生事件、界面没有任何痕迹。
       选择器把「格式合法」变成控件的输出约束，用户选不出 8:00 这种值。
       为什么是 el-time-picker 而不是 el-time-select：后者按固定步长生成候选
       （常见 15/30 分钟一档），08:07 这类真实需要的时刻根本选不出来。 -->
  <template v-if="form.trigger_type === 'time_window'">
    <el-form-item label="开始时间" required>
      <el-time-picker
        v-model="form.trigger_window_start"
        format="HH:mm"
        value-format="HH:mm"
        :clearable="false"
        placeholder="选择开始时间"
        data-test="field-window-start"
      />
      <!-- 存量规则被规范化时必须说出来：改写用户数据却不说，等于让他在别处
           看到一个"从没填过"的时间（见 normalizeTimeWindow）。 -->
      <div v-if="windowStartFixNote" class="hint-block" data-test="window-start-fixed">
        {{ windowStartFixNote }}
      </div>
    </el-form-item>
    <el-form-item label="结束时间" required>
      <el-time-picker
        v-model="form.trigger_window_end"
        format="HH:mm"
        value-format="HH:mm"
        :clearable="false"
        placeholder="选择结束时间"
        data-test="field-window-end"
      />
      <div v-if="windowEndFixNote" class="hint-block" data-test="window-end-fixed">
        {{ windowEndFixNote }}
      </div>
      <!-- start > end 在后端是**受支持的跨日窗口**（22:00 → 次日 06:00），不是错误。
           不明确说出来，用户既不敢这么填，也看不懂列表里 "22:00-06:00 进入" 的含义。 -->
      <div v-if="isOvernightWindow" class="hint-block" data-test="window-overnight">
        跨越零点：{{ form.trigger_window_start }} 到次日 {{ form.trigger_window_end }}
      </div>
    </el-form-item>
    <el-form-item label="触发沿" required>
      <el-select v-model="form.trigger_window_edge" data-test="field-window-edge">
        <el-option label="进入窗口" value="enter" />
        <el-option label="离开窗口" value="exit" />
        <el-option label="窗口内持续" value="inside" />
      </el-select>
    </el-form-item>
  </template>

  <!-- sun_event 字段组 (设计稿 R6 / §6 D-1 D-2) -->
  <template v-if="isSunTrigger">
    <el-form-item label="日出 / 日落" required>
      <el-select v-model="form.trigger_sun_kind" data-test="field-sun-kind">
        <el-option v-for="opt in SUN_KIND_OPTIONS" :key="opt.value" :label="opt.label" :value="opt.value" />
      </el-select>
    </el-form-item>
    <el-form-item label="偏移分钟" required>
      <div class="sun-offset-row">
        <el-input-number
          v-model="form.trigger_sun_offset_min"
          :min="SUN_OFFSET_MIN"
          :max="SUN_OFFSET_MAX"
          :step="5"
          data-test="field-sun-offset"
          aria-label="相对日出/日落的偏移分钟，负值为之前"
        />
        <!-- 实时自然语言回显（设计 D-2）：用户需求原话就是"日落前 30 分钟"，
             让界面直接说出这句话，用户不必在脑内把"方向 + 绝对值"翻译一遍。 -->
        <span class="hint" data-test="offset-sentence">{{ sunOffsetText(form.trigger_sun_kind, form.trigger_sun_offset_min) }}</span>
      </div>
      <div class="hint-block">范围 -720 ~ 720 分钟；负值 = 日出/日落之前，正值 = 之后，0 = 正点</div>
    </el-form-item>
    <!-- 坐标来源（2026-11-14 用户裁决）：只有「外网 IP 自动定位」与「手动选择设备城市」
         两种；纬度/经度/时区由定位结果回填，时区**没有输入框**（随位置自动填充）。 -->
    <el-form-item label="位置来源" required>
      <div class="sun-location" data-test="sun-location">
        <div class="sun-location__source" data-test="field-location-source">
          <el-radio-group v-model="form.location_source" @change="onLocationSourceChange">
            <el-radio-button value="ip">自动定位（外网 IP）</el-radio-button>
            <el-radio-button value="city">手动选择城市</el-radio-button>
          </el-radio-group>
        </div>
        <div class="sun-location__picker">
          <template v-if="form.location_source === 'ip'">
            <el-button size="small" :loading="ipLoading" data-test="locate-by-ip" @click="locateByIP">获取当前位置</el-button>
            <span class="hint" data-test="ip-status">{{ ipStatus }}</span>
          </template>
          <template v-else-if="form.location_source === 'city'">
            <el-select
              v-model="cityName"
              filterable
              remote
              clearable
              :remote-method="onCitySearch"
              :loading="cityLoading"
              placeholder="搜索城市（中文或拼音）"
              data-test="field-city"
              @update:model-value="onCityChange"
            >
              <el-option
                v-for="c in cityOptions"
                :key="c.country_code + '-' + c.name + '-' + c.latitude"
                :label="c.name + '（' + c.country + '）'"
                :value="c.name"
              />
            </el-select>
          </template>
        </div>
        <div v-if="hasLocation" class="sun-location__meta" data-test="location-summary">
          <span class="sun-location__label">{{ form.location_label || '已选择位置' }}</span>
          <span class="mono">{{ form.latitude }}, {{ form.longitude }}</span>
          <el-tag size="small" type="info">{{ form.timezone }}</el-tag>
        </div>
        <div class="hint-block" data-test="sun-location-hint">
          坐标与时区由位置自动填充（时区不可手动设置）。
        </div>
      </div>
    </el-form-item>
  </template>

  <!-- 附加条件（结构化行 / JSON 源码）：编辑器自成一块，见 components/ConditionEditor.vue。
       这里只做转发 —— 状态仍在页面（useRuleConditions），子组件不复制任何判定。 -->
  <ConditionEditor
    :form="form"
    :condition-rows="conditionRows"
    :conditions-raw-mode="conditionsRawMode"
    :conditions-raw-invalid="conditionsRawInvalid"
    @add-condition="emit('add-condition')"
    @remove-condition="(uid: number) => emit('remove-condition', uid)"
    @toggle-conditions-raw="emit('toggle-conditions-raw')"
  />
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import SensorFieldPicker from '@/components/sensor/SensorFieldPicker.vue'
import { geoApi, type GeoCity } from '@/api/geo'
import type { SensorField } from '@/api/sensorField'
import type { EdgeDevice } from '@/api/edgeDevice'
import type { SourceGroup } from '@/api/sourceGroup'
import type { AutomationTriggerType, SunEventKind } from '@/api/automation'
import {
  SUN_KIND_OPTIONS, SUN_OFFSET_MIN, SUN_OFFSET_MAX, sunOffsetText,
} from '../sunEvent'
import { comparators } from '../automationFormat'
import type { RuleFormModel } from '../composables/useRuleForm'
import type { ConditionRow } from '../composables/useRuleConditions'
import ConditionEditor from './ConditionEditor.vue'

/**
 * 触发器字段组（2026-11-12 第二轮表单区拆分，纯搬移）。
 *
 * 边界：只渲染 + 抛语义事件，**不持有任何状态**。表单模型由页面持有并作为 prop 传入
 * （字段组有 20+ 个双向字段，逐字段拆成 props/emits 会把同一份字段清单复制三遍，
 * 任何一处漏写就是"界面改了、提交体没改"）；所有派生值（单位/精度、时间窗修复提示、
 * 跨零点判定、试算结论、时区词表）仍在页面/组合式函数里，本组件不重算。
 *
 * 为什么把附加条件再拆一层（ConditionEditor）：它是唯一「行 ↔ JSON 原文」双形态的
 * 编辑器，模板与样式（条件行四件套 / 窄屏堆叠）自成一块。
 */
const props = defineProps<{
  form: RuleFormModel
  devices: EdgeDevice[]
  sourceGroupOptions: SourceGroup[]
  selectedSourceGroup: SourceGroup | null | undefined
  triggerSelection: AutomationTriggerType | SunEventKind
  thresholdUnit: string
  thresholdPrecision: number
  windowStartFixNote: string
  windowEndFixNote: string
  isOvernightWindow: boolean
  isSunTrigger: boolean
  conditionRows: ConditionRow[]
  conditionsRawMode: boolean
  conditionsRawInvalid: boolean
  previewLoading: boolean
  previewDisabled: boolean
  previewHint: string
  previewResult: { text: string; tone: 'info' | 'warning' } | null
}>()

const emit = defineEmits<{
  (e: 'update:triggerSelection', value: AutomationTriggerType | SunEventKind): void
  (e: 'trigger-type-change'): void
  (e: 'sensor-field-change', field: SensorField | null): void
  (e: 'source-kind-change', kind: 'edge_device' | 'source_group'): void
  (e: 'source-group-change'): void
  (e: 'preview'): void
  (e: 'add-condition'): void
  (e: 'remove-condition', uid: number): void
  (e: 'toggle-conditions-raw'): void
}>()

// 值域原样转发（含 el-select 清空时可能出现的非字符串）：代理值的折叠规则在页面
// 的 triggerSelection setter 里，本组件一个字都不解释，避免出现第二份映射。
function onTriggerSelectionChange(value: unknown) {
  emit('update:triggerSelection', value as AutomationTriggerType | SunEventKind)
}
function onTriggerTypeChange() {
  emit('trigger-type-change')
}
// 字段对象只用于页面渲染单位/小数位（不参与提交）：原样上报。
function onSensorFieldChange(field: SensorField | null) {
  emit('sensor-field-change', field)
}
function onSourceKindChange(kind: 'edge_device' | 'source_group') {
  emit('source-kind-change', kind)
}
function onSourceGroupChange() {
  emit('source-group-change')
}
function onPreview() {
  emit('preview')
}

// ── 来源组下拉文案（只在这里使用，不参与组语义判定 —— 语义由后端 sourceref 负责）──
const COMBINE_LABELS: Record<string, string> = {
  avg: '平均',
  sum: '求和',
}
function groupOptionLabel(g: SourceGroup): string {
  return `${g.name}（${g.kind === 'primary_backup' ? '主备' : '复合'}）`
}
function groupSummary(g: SourceGroup): string {
  if (g.kind === 'primary_backup') return `主备组 · 类别 ${g.category} · active 成员由数据源状态机决定`
  const n = g.min_members || 1
  const degrade = g.on_degrade === 'use_available' ? '可降级（事件标记 degraded）' : 'fail_closed'
  return `复合组 · ${COMBINE_LABELS[g.combine || ''] ?? g.combine ?? '—'} · 最少 ${n} 个成员 · 缺员 ${degrade}`
}

// ── 坐标来源：外网 IP 自动定位 / 手动选择设备城市（2026-11-14）──
//
// 时区**不在这里选**：它随 IP/城市结果一起回来，直接写进 form.timezone。
// 组件持有的是"取数过程"的临时状态（loading / 搜索候选 / 提示），位置结果本身
// 仍在页面持有的 form 上 —— 与字段组的既有边界一致。

const ipLoading = ref(false)
const ipStatus = ref('点击获取部署机出口位置')
const cityLoading = ref(false)
const cityName = ref('')
const cityOptions = ref<GeoCity[]>([])
/** 只用于丢弃过期响应：用户快速输入时，先发的请求可能后到。 */
let citySearchSeq = 0
let citySearchTimer: ReturnType<typeof setTimeout> | undefined

/** 当前是否已有完整位置（纬度/经度/时区都在）。 */
const hasLocation = computed(() => (
  typeof props.form.latitude === 'number' && Number.isFinite(props.form.latitude)
  && typeof props.form.longitude === 'number' && Number.isFinite(props.form.longitude)
  && typeof props.form.timezone === 'string' && props.form.timezone.trim() !== ''
))

function locationLabel(city: string, country?: string): string {
  return city && country ? `${city}（${country}）` : (city || '已定位')
}

/** 把定位结果写进表单；时区由来源给出，不接受手填。 */
function applyLocation(latitude: number, longitude: number, timezone: string, label: string) {
  props.form.latitude = Number(latitude.toFixed(4))
  props.form.longitude = Number(longitude.toFixed(4))
  props.form.timezone = timezone
  props.form.location_label = label
}

/** 切换来源必须清掉旧位置：否则会出现"上海坐标 + 北京标签"的组合。 */
function onLocationSourceChange(source: string | number | boolean | undefined) {
  props.form.latitude = undefined
  props.form.longitude = undefined
  props.form.timezone = ''
  props.form.location_label = ''
  cityName.value = ''
  ipStatus.value = '点击获取部署机出口位置'
  if (source === 'city') void loadCityOptions('')
}

async function loadCityOptions(query: string) {
  const seq = ++citySearchSeq
  cityLoading.value = true
  try {
    const items = await geoApi.searchCities(query, 20)
    if (seq !== citySearchSeq) return // 过期响应：用更新的那次
    cityOptions.value = items
  } catch {
    if (seq === citySearchSeq) cityOptions.value = []
  } finally {
    if (seq === citySearchSeq) cityLoading.value = false
  }
}

/** 远端搜索带 250ms 防抖：输入框每敲一个字都打后端既浪费也不必要。 */
function onCitySearch(query: string) {
  if (citySearchTimer) clearTimeout(citySearchTimer)
  citySearchTimer = setTimeout(() => { void loadCityOptions(query) }, 250)
}

function onCityChange(name: string | number | boolean | undefined) {
  const picked = cityOptions.value.find(c => c.name === String(name ?? ''))
  if (!picked) return
  applyLocation(picked.latitude, picked.longitude, picked.timezone, locationLabel(picked.name, picked.country))
}

async function locateByIP() {
  ipLoading.value = true
  ipStatus.value = '正在定位…'
  try {
    const loc = await geoApi.resolveIP()
    applyLocation(loc.latitude, loc.longitude, loc.timezone, locationLabel(loc.city ?? '', loc.country))
    props.form.location_source = 'ip'
    ipStatus.value = `已定位：${props.form.location_label}`
  } catch (e) {
    ipStatus.value = '自动定位不可用，请改用手动选择城市'
    // 保留底层原因便于排查（不弹窗：定位失败是预期分支，不是页面错误）。
    console.warn('[automation] IP 定位失败:', e)
  } finally {
    ipLoading.value = false
  }
}

// 编辑规则时来源可能是 city：立刻准备默认候选（人口最多的前 20 个），
// 用户不用先敲字才能看到选项。
watch(() => props.form.location_source, (source) => {
  if (source === 'city' && cityOptions.value.length === 0) void loadCityOptions('')
}, { immediate: true })

onBeforeUnmount(() => { if (citySearchTimer) clearTimeout(citySearchTimer) })
</script>

<style scoped>
/* 条件行（比较符 + 阈值 + 单位）：原先写在 AutomationRules.vue 的 scoped 样式里。
   scoped 样式不作用于子组件内部 DOM，片段搬走时规则必须一起搬，否则搬完就掉样式。 */
/* 2026-10-21 审计 C-ops P2：「条件」行无宽度约束 ⇒ 393 弹窗 .el-dialog__body
   scrollWidth 368 > clientWidth 361（横向溢出 7px），＋按钮贴到弹窗右内边。
   与 /alerts 同口径：比较符定宽 52px、阈值框弹性保底 176px（± 钮各 44px ⇒ 内层 input ≈88px
   ≥84px），更窄视口下允许换行而不是溢出。 */
.cond-row { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.cond-row .el-select { flex: 0 0 52px; min-width: 52px; }
.cond-row .el-input-number { flex: 1 1 auto; min-width: 176px; max-width: 100%; }
/* 阈值单位：跟在数字框右侧的**文本**，不是输入框后缀（EP 的 suffix 只接受图标/字符串,
   且会随 precision 抖动）；不参与伸缩、不换行，用户读到的是完整的 "30 °C" 一句话。 */
.cond-row .threshold-unit { flex: 0 0 auto; margin-left: 4px; white-space: nowrap; }
/* 试算按钮与结论都在「条件」行的下一行：结论是**提示**，不占输入位，也不挤阈值。 */
.preview-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; width: 100%; margin-top: 8px; }
.preview-row .hint { margin-left: 0; }
/* 结论必须独占一行：与按钮同排时会被挤成一条窄缝，长句读不全 */
.preview-result { width: 100%; margin-top: 6px; font-size: 12px; line-height: 1.6; color: var(--el-text-color-regular); }
/* "试算不可用"（413 / 服务异常）用 warning 色，但仍是提示，不是表单校验错误 */
.preview-result--warning { color: var(--el-color-warning); }
.hint { margin-left: 8px; color: var(--el-text-color-secondary); font-size: 12px; }
.hint-block { margin-top: 4px; font-size: 12px; color: var(--el-text-color-secondary); line-height: 1.6; }

/* ============================================================================
   日出/日落字段组 (设计稿 §5 R6 / §6 D-2)
   ========================================================================== */
.sun-offset-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
/* 提示行与输入框同排时 margin-left:8px（.hint 既有口径），此处不需要额外偏移 */
.sun-offset-row .hint { margin-left: 0; }
.sun-location { width: 100%; display: flex; flex-direction: column; gap: 8px; }
.sun-location__picker { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.sun-location__picker :deep(.el-select) { width: 320px; max-width: 100%; }
.sun-location__meta { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; font-size: 13px; }
.sun-location__label { font-weight: 600; }

/* ============================================================================
   窄屏：768px 主断点（与 theme.css / useResponsive 同源）
   ========================================================================== */
@media (max-width: 768px) {
  .sun-location__picker :deep(.el-select) { width: 100%; }
}
</style>
