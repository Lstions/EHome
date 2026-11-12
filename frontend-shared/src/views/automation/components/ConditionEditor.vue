<template>
  <!-- 附加条件（文档 §4.3）：JSON 文本域 → 结构化条件行。
       为什么条件行必须复用**同一个** SensorFieldPicker（而不是给条件另设一个普通下拉/文本框）：
       后端 conditionsSatisfied 逐条走的也是 evalSelector（evaluator.go:1018-1033）——
       与上面的触发字段是**同一个 selector 契约**（字面量 / 字段族 cell_voltage_*:max / @别名）。
       条件行若退回手打物理名，同一张表单里就会出现两套"字段是什么"的答案，其中一套
       拼错一个字母 = 保存成功 + 条件永不满足 + 规则静默不触发（文档 §3.1 整类缺陷）。
       为什么只在 sensor_threshold 下渲染：conditionsSatisfied 只在传感器阈值路径被调用
       （evalRule / evalRuleWithSource / evalCompositeRule），time_window / sun_event 的
       conditions_json 是**惰性数据**。给它编辑入口等于向用户承诺"日出规则也有附加条件"，
       而它永远不会被求值。但存量值仍**保留**并说明（见下方 v-else-if 分支）——
       清空别人的数据比多显示一句解释糟得多。 -->
  <el-form-item v-if="form.trigger_type === 'sensor_threshold'" label="附加条件">
    <div class="conditions" data-test="conditions-editor">
      <div class="conditions__head">
        <span class="hint-block conditions__hint">全部满足才触发（后端逐条 AND 求值，任一不满足即不触发）</span>
        <!-- 源码模式默认关闭（文档 §4.3）：JSON 是迁移期/排障入口，不是日常录入方式 -->
        <el-button
          link
          type="primary"
          size="small"
          data-test="conditions-raw-toggle"
          @click="onToggleConditionsRaw"
        >
          {{ conditionsRawMode ? '切回条件行编辑' : 'JSON 源码' }}
        </el-button>
      </div>

      <!-- 非法存量数据（历史手写 / 被别的工具改坏）：原文原样保留，并说清为什么改不动它。
           为什么不是"解析失败就清空"：那是**丢用户数据**，而且丢得没有任何痕迹 ——
           用户下次打开看到的是一张干净的空表单，会以为这些条件本来就不存在。 -->
      <div
        v-if="conditionsRawMode && conditionsRawInvalid"
        class="hint-block conditions__warn"
        data-test="conditions-raw-invalid"
      >
        现有内容无法解析为条件数组，已保留原文；修正后才能切回条件行编辑
      </div>

      <el-input
        v-if="conditionsRawMode"
        v-model="form.conditions_json"
        type="textarea"
        :rows="2"
        placeholder='JSON 数组，如 [{"sensor_name":"humidity","comparator":"lt","threshold":30}]'
        data-test="field-conditions"
      />
      <template v-else>
        <ul v-if="conditionRows.length" class="condition-list" data-test="condition-list">
          <li
            v-for="(row, index) in conditionRows"
            :key="row.uid"
            class="condition-row"
            data-test="condition-row"
          >
            <span class="condition-row__index" aria-hidden="true">{{ index + 1 }}</span>
            <!-- 来源组（source_kind=source_group）没有设备字段目录可查：选择器在这种情形只会
                 显示「请先选择边缘设备」，对已选组的用户是答非所问。故改用物理名输入框，
                 并在下方说明求值口径（组内成员按同一物理名求值）。 -->
            <el-input
              v-if="form.source_kind === 'source_group'"
              v-model="row.selector"
              class="condition-row__field"
              placeholder="物理字段名，如 temperature_2"
              :aria-label="`第 ${index + 1} 个条件的字段物理名`"
              data-test="condition-selector-input"
            />
            <SensorFieldPicker
              v-else
              v-model="row.selector"
              class="condition-row__field"
              :device-id="form.trigger_edge_device_id"
            />
            <el-select
              v-model="row.comparator"
              class="condition-row__cmp"
              :aria-label="`第 ${index + 1} 个条件的比较符`"
              :data-test="`condition-comparator-${index}`"
            >
              <el-option v-for="c in comparators" :key="c.value" :label="c.label" :value="c.value" />
            </el-select>
            <el-input-number
              v-model="row.threshold"
              class="condition-row__threshold"
              :aria-label="`第 ${index + 1} 个条件的阈值`"
              :data-test="`condition-threshold-${index}`"
            />
            <el-button
              link
              type="danger"
              size="small"
              class="condition-row__remove"
              :aria-label="`删除第 ${index + 1} 个条件`"
              :data-test="`remove-condition-${index}`"
              @click="onRemoveCondition(row.uid)"
            >✕</el-button>
          </li>
        </ul>
        <p v-else class="hint-block" data-test="conditions-empty">
          暂无附加条件：规则只按上方的触发条件判定。
        </p>
        <div class="conditions__foot">
          <el-button link type="primary" size="small" data-test="add-condition" @click="onAddCondition">+ 添加条件</el-button>
          <span
            v-if="form.source_kind === 'source_group'"
            class="hint"
            data-test="conditions-source-group-hint"
          >
            来源组没有设备字段目录：组内成员按同一物理名求值。
          </span>
        </div>
      </template>
    </div>
  </el-form-item>
  <!-- 非传感器阈值触发 + 存量带条件：说明它不会被求值，但**不清空**（清空是丢数据）。
       为什么保留而不是隐藏：用户编辑这条规则时看不到任何相关控件，却在保存后
       仍带着一段他看不见的配置 —— 说一句比"看不见"安全。 -->
  <el-form-item v-else-if="form.conditions_json.trim()" label="附加条件">
    <span class="hint-block" data-test="conditions-inactive-hint">
      该规则存有附加条件，但只有「传感器阈值」触发会使用它们（当前触发类型不会求值，保存时按原值保留）。
    </span>
  </el-form-item>
</template>

<script setup lang="ts">
import SensorFieldPicker from '@/components/sensor/SensorFieldPicker.vue'
import { comparators } from '../automationFormat'
import type { RuleFormModel } from '../composables/useRuleForm'
import type { ConditionRow } from '../composables/useRuleConditions'

/**
 * 附加条件编辑器（2026-11-12 第二轮表单区拆分，纯搬移）。
 *
 * 边界：**只渲染**条件行与 JSON 源码两种形态，不持有状态 —— 行数组、源码模式、
 * "原文是否可还原"都在 useRuleConditions 里（页面持有），子组件只把点按翻成语义事件。
 * 为什么把它从 TriggerFields 再拆一层：它是唯一「行 ↔ JSON 原文」双形态的编辑器，
 * 模板与样式（条件行四件套 / 定宽比较符 / 窄屏堆叠）自成一块，混在触发字段里
 * 会让那个文件越过可读上限（600 行）。
 */
defineProps<{
  form: RuleFormModel
  conditionRows: ConditionRow[]
  conditionsRawMode: boolean
  conditionsRawInvalid: boolean
}>()

const emit = defineEmits<{
  (e: 'add-condition'): void
  (e: 'remove-condition', uid: number): void
  (e: 'toggle-conditions-raw'): void
}>()

function onAddCondition() {
  emit('add-condition')
}
function onRemoveCondition(uid: number) {
  emit('remove-condition', uid)
}
function onToggleConditionsRaw() {
  emit('toggle-conditions-raw')
}
</script>

<style scoped>
.hint { margin-left: 8px; color: var(--el-text-color-secondary); font-size: 12px; }
.hint-block { margin-top: 4px; font-size: 12px; color: var(--el-text-color-secondary); line-height: 1.6; }

/* ============================================================================
   附加条件：条件行（文档 §4.3）
   —— 一行放：序号 + 字段选择器（弹性） + 比较符（定宽） + 阈值（定宽） + 删除。
   640px 弹窗里 label 占 110px、内容区约 500px：定宽三件套 104+120+32 与 3 个 8px
   间距共约 280px，选择器仍有 ~190px 可用（与 .cond-row 的比较符定宽同口径，不另立一套）。
   ========================================================================== */
.conditions { width: 100%; display: flex; flex-direction: column; gap: 8px; min-width: 0; }
.conditions__head { display: flex; align-items: center; justify-content: space-between; gap: 8px; flex-wrap: wrap; }
.conditions__hint { margin-top: 0; flex: 1 1 auto; min-width: 0; }
/* 无法解析的存量原文是**警告**而不是错误：值本身还在，只是暂时改不动（不是数据坏了要删） */
.conditions__warn { color: var(--el-color-warning); }
.condition-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; }
.condition-row { display: flex; align-items: center; gap: 8px; min-width: 0; }
.condition-row__index {
  display: inline-flex; align-items: center; justify-content: center; flex: 0 0 auto;
  width: 20px; height: 20px; border-radius: 50%;
  background: var(--el-color-primary-light-9); color: var(--el-color-primary);
  font-size: 12px; font-weight: 600;
}
/* 选择器列必须能收缩：SensorFieldPicker 根是 width:100% 的纵向 flex，
   少了 min-width:0 它会把比较符/阈值挤出弹窗右侧。 */
.condition-row__field { flex: 1 1 auto; min-width: 0; }
.condition-row__field :deep(.el-select) { width: 100%; }
.condition-row__cmp { flex: 0 0 104px; }
.condition-row__threshold { flex: 0 0 120px; }
.condition-row__remove { flex: 0 0 auto; }
.conditions__foot { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }

/* 窄屏：768px 主断点（与 theme.css / useResponsive 同源）。
   条件行：窄屏换行/纵向堆叠，不新增断点。
   行内四件套在 640px 弹窗里刚好放得下，但在 390px 视口下只剩 ~230px 内容宽，
   继续并排会把字段选择器压成一条竖线（与 .cond-row 的 flex-wrap 同口径处理）。 */
@media (max-width: 768px) {
  .condition-row { flex-wrap: wrap; align-items: stretch; }
  .condition-row__field { flex: 1 1 100%; }
  .condition-row__cmp, .condition-row__threshold { flex: 1 1 96px; }
  .condition-row__remove { min-height: 36px; }
}
</style>
