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
 *
 * 2026-11-12 纯搬移拆分：组件内状态与取数（目录/schema、降级链、resolve 防抖、回填去重、
 * 模式切换）整体搬到 composables/useSensorFieldPicker.ts；本文件只保留**对外契约**
 * （props/emits 声明）、模板与样式 —— spec 的 data-test / aria-label / .sf-* 源断言
 * 全部按本文件定位，模板与样式一条未动。
 */
import type { SensorField } from '@/api/sensorField'
import { aggregateGapNote } from './sensorFieldLogic'
import { useSensorFieldPicker } from './composables/useSensorFieldPicker'

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

// 模板消费的面：状态 + 派生视图 + 交互出口。内部实现细节（请求序号 / 防抖定时器 /
// 回填指纹）留在 composable，避免模板绕开唯一入口直接改目录状态。
const {
  // 状态
  fields, catalogState, truncated, loading, mode, resolveState,
  // 派生视图
  hasDevice, numericCount, totalCount, hasNonNumeric,
  familySelection, currentAggregate, hasAggregateVocabulary, aggregateChoices,
  pickerOptions, aliasOptions, currentValueOption, selectValue, resolveStatusClass,
  // 交互出口
  setMode, onSelect, onAggregateSelect, onManualInput,
  // 取数
  reload,
} = useSensorFieldPicker(props, emit)

/**
 * 手动刷新（父组件在目录变化后调用）。
 *
 * 内部已 watch deviceId 自动重载，保留该方法是为了「同一设备但字段目录变了」的场景
 * （例如设备刚升级固件、新的声明集已落库）。实现见 composable 的 reload。
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
