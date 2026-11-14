<template>
  <div class="automation-rules-page">
    <PageHeader
      title="自动化策略"
      subtitle="传感器数据、时间窗口或日出日落触发设备动作或通知"
    />

    <!-- 规则列表（桌面 7 列宽表 / 窄屏卡片）：片段已搬到 components/RuleListSection.vue。
         数据加载与动作（fetchRules / onToggle / onDelete / onTrigger）仍留在本页，
         子组件只接 props + 抛语义化事件（create / edit / delete / toggle / trigger）。 -->
    <RuleListSection
      :rules="rules"
      :loading="rulesLoading"
      :is-mobile="isMobile"
      :triggering-id="triggeringId"
      @create="openCreate"
      @edit="openEdit"
      @delete="onDelete"
      @toggle="onToggle"
      @trigger="onTrigger"
    />

    <!-- 触发历史（筛选条 / 桌面 8 列宽表 / 窄屏卡片 / 空态）：片段已搬到 components/EventHistorySection.vue。
         数据加载、筛选值、页码与 fetchEvents 仍留在本页。
         ⚠ 分页控件**故意留在页面**（经 #pagination 插槽注入到 section 内部）：
         listPageConventions 门禁按「路由级页面的模板里有 <el-pagination>」判定列表页，
         把控件搬进子组件会让本页退出该门禁的分母（门禁失去对象）。插槽位置与搬走前一致，
         最终 DOM 不变；.events-pagination 样式也因此留在本页。 -->
    <EventHistorySection
      :events="events"
      :loading="eventsLoading"
      :is-mobile="isMobile"
      :rules="rules"
      v-model:filter-rule-id="eventFilterRuleId"
      v-model:filter-result="eventFilterResult"
      @refresh="fetchEvents"
      @filter-change="fetchEvents"
      @confirm-event="onConfirmEvent"
      @go-command="goCommand"
    >
      <!-- 分页 (真分页: 表格数据来自接口当前页, 不是本地全量切片)。
           改前本表一次性渲染后端 Limit(500) 的全部行 (实测 502 行/11902 节点,
           真实事件 >1006 条被静默截断), fixed 列为每行各注入一份 inline style。 -->
      <!-- 2026-10-21 审计 C-ops P2：total=0 时分页器仍渲染（实测 321×76「共 0 条 / 20条/页 / 1」），
           空列表被两个空控件（空态 + 分页）拉长。total>0 才渲染。 -->
      <template #pagination>
        <div v-if="eventsTotal > 0" class="events-pagination">
          <el-pagination
            v-model:current-page="eventsPage"
            v-model:page-size="eventsPageSize"
            :total="eventsTotal"
            :page-sizes="[20, 50, 100]"
            layout="total, sizes, prev, pager, next, jumper"
            data-test="events-pagination"
            @current-change="() => fetchEvents()"
            @size-change="onEventsPageSizeChange"
          />
        </div>
      </template>
    </EventHistorySection>

    <!-- 创建/编辑对话框：**壳与保存流程留在这里**（第二轮表单区拆分，2026-11-12）。
         表单字段组搬进两个子组件，但继续读写本页**同一个** form 对象：
           · components/TriggerFields.vue —— 触发器类型 / 传感器阈值组 / 时间窗 / 日出日落
             （内含 components/ConditionEditor.vue：结构化条件行 ↔ JSON 源码）
           · components/ActionCards.vue —— 多动作卡片列表（风险徽标 / 目录提示 / 参数入口）
         为什么保存编排（precheck → 诊断 → 风控清单 → 提交）不搬：它是本页唯一的
         "能不能落库"判定入口，诊断状态、warnings 内容指纹与风险勾选状态必须在同一作用域，
         否则"已确认的那一份 warnings"会跨对话框/跨规则被复用（见 resetSaveDiagnostics）。 -->
    <el-dialog
      v-model="dialogVisible"
      :title="editingId ? '编辑规则' : '创建规则'"
      :width="isMobile ? '100%' : '980px'"
      :fullscreen="isMobile"
      :top="isMobile ? '0' : '5vh'"
      class="rule-wizard-dialog"
      data-test="rule-dialog"
    >
      <div class="rule-wizard" :class="{ 'is-mobile': isMobile }">
        <!-- PC：左侧步骤导航（可点击回看/跳转，不阻断；保存时统一校验全部步骤）。 -->
        <nav v-if="!isMobile" class="rule-wizard__rail" aria-label="创建规则步骤">
          <button
            v-for="step in WIZARD_STEPS"
            :key="step.id"
            type="button"
            class="rule-wizard__step"
            :class="{ 'is-active': wizardStep === step.id, 'is-done': wizardStep > step.id }"
            :aria-current="wizardStep === step.id ? 'step' : undefined"
            :data-test="`wizard-step-${step.id}`"
            @click="goStep(step.id)"
          >
            <span class="rule-wizard__num">{{ wizardStep > step.id ? '✓' : step.id }}</span>
            <span class="rule-wizard__meta">
              <b>{{ step.title }}</b>
              <small>{{ step.desc }}</small>
            </span>
          </button>
        </nav>

        <div class="rule-wizard__main">
          <!-- 移动端：顶部步骤条（全屏对话框里没有左侧栏的位置）。 -->
          <div v-if="isMobile" class="rule-wizard__mobile-head" data-test="wizard-mobile-head">
            <div class="rule-wizard__mobile-title">
              <span>步骤 {{ wizardStep }}/{{ WIZARD_STEPS.length }}</span>
              <b>{{ currentStepTitle }}</b>
            </div>
            <el-progress :percentage="wizardStep * 25" :show-text="false" :stroke-width="6" />
          </div>

          <el-form
            :model="form"
            :label-width="isMobile ? '92px' : '104px'"
            :label-position="isMobile ? 'top' : 'right'"
            class="rule-wizard__form"
          >
            <div class="rule-wizard__scroll" data-test="rule-form">
              <!-- ① 基本信息 -->
              <section v-show="wizardStep === 1" class="rule-wizard__panel" data-test="wizard-panel-basic">
                <header class="rule-wizard__panel-head">
                  <h3>基本信息</h3>
                  <p>先给规则起个名字；保存后名称会在列表与触发历史里回链。</p>
                </header>
                <el-form-item label="名称" required>
                  <el-input v-model="form.name" placeholder="如：高温自动开窗" data-test="field-name" />
                </el-form-item>
                <el-form-item label="启用">
                  <el-switch v-model="form.enabled" data-test="field-enabled" />
                  <span class="hint">关闭后规则保留配置但不会触发</span>
                </el-form-item>
              </section>

              <!-- ② 触发条件：复用既有字段组（含附加条件与试算），语义零改动 -->
              <section v-show="wizardStep === 2" class="rule-wizard__panel" data-test="wizard-panel-trigger">
                <header class="rule-wizard__panel-head">
                  <h3>触发条件</h3>
                  <p>选择什么情况下触发；日出/日落规则的坐标来自「外网 IP」或「手动城市」。</p>
                </header>
                <TriggerFields
                  v-model:trigger-selection="triggerSelection"
                  :form="form"
                  :devices="devices"
                  :source-group-options="sourceGroupOptions"
                  :selected-source-group="selectedSourceGroup"
                  :threshold-unit="thresholdUnit"
                  :threshold-precision="thresholdPrecision"
                  :window-start-fix-note="windowStartFixNote"
                  :window-end-fix-note="windowEndFixNote"
                  :is-overnight-window="isOvernightWindow"
                  :is-sun-trigger="isSunTrigger"
                  :condition-rows="conditionRows"
                  :conditions-raw-mode="conditionsRawMode"
                  :conditions-raw-invalid="conditionsRawInvalid"
                  :preview-loading="previewLoading"
                  :preview-disabled="previewDisabled"
                  :preview-hint="previewHint"
                  :preview-result="previewResult"
                  @trigger-type-change="onTriggerTypeChange"
                  @sensor-field-change="onSensorFieldChange"
                  @source-kind-change="onSourceKindChange"
                  @source-group-change="onSourceGroupChange"
                  @preview="runPreview"
                  @add-condition="addCondition"
                  @remove-condition="removeCondition"
                  @toggle-conditions-raw="toggleConditionsRaw"
                />
              </section>

              <!-- ③ 动作与运行策略 -->
              <section v-show="wizardStep === 3" class="rule-wizard__panel" data-test="wizard-panel-actions">
                <header class="rule-wizard__panel-head">
                  <h3>动作与运行策略</h3>
                  <p>按顺序添加要执行的动作，并决定触发后是直接执行还是等待人工确认。</p>
                </header>
                <ActionCards
                  :actions="form.actions"
                  :devices="devices"
                  :risky-count="riskyActions.length"
                  :helpers="cardHelpers"
                  @add="addAction"
                  @remove="removeAction"
                  @move="moveCard"
                  @type-change="onCardTypeChange"
                  @device-change="onCardDeviceChange"
                  @action-id-change="onCardActionIdChange"
                  @open-params="openParams"
                  @open-json-params="openJsonParams"
                />

                <el-form-item label="需人工确认">
                  <el-switch v-model="form.require_confirmed" data-test="field-require-confirmed" />
                  <span class="hint">
                    开启则规则触发后只生成「待确认」通知，人工确认后才下发；关闭则触发即执行
                  </span>
                </el-form-item>
                <div v-if="!form.require_confirmed && riskyActions.length" class="ack-hint" data-test="ack-hint">
                  本规则含 {{ riskyActions.length }} 个风险动作且已关闭人工确认：保存时会要求逐项勾选确认
                  （后端按规则级授权放行，授权在动作集合变化后自动失效并需重新确认）。
                </div>

                <el-form-item label="冷却时间">
                  <div class="number-field">
                    <div class="number-field__main">
                      <el-input-number v-model="form.cooldown_sec" :min="0" :max="86400" :step="60" data-test="field-cooldown" />
                      <span class="hint">秒，0-86400</span>
                    </div>
                    <div class="chip-row" data-test="cooldown-chips">
                      <button
                        v-for="chip in COOLDOWN_CHIPS"
                        :key="chip.value"
                        type="button"
                        class="chip"
                        :class="{ 'is-active': form.cooldown_sec === chip.value }"
                        :aria-pressed="form.cooldown_sec === chip.value ? 'true' : 'false'"
                        :aria-label="`冷却时间设为 ${chip.text}`"
                        :data-test="`cooldown-chip-${chip.value}`"
                        @click="form.cooldown_sec = chip.value"
                      >{{ chip.label }}</button>
                    </div>
                  </div>
                </el-form-item>
                <el-form-item label="每日上限">
                  <div class="number-field">
                    <div class="number-field__main">
                      <el-input-number v-model="form.max_daily_exec" :min="0" :max="1000" data-test="field-max-daily" />
                      <span class="hint">0 = 不限，最大 1000</span>
                    </div>
                    <div class="chip-row" data-test="daily-limit-chips">
                      <button
                        v-for="chip in DAILY_LIMIT_CHIPS"
                        :key="chip.value"
                        type="button"
                        class="chip"
                        :class="{ 'is-active': form.max_daily_exec === chip.value }"
                        :aria-pressed="form.max_daily_exec === chip.value ? 'true' : 'false'"
                        :aria-label="`每日上限设为 ${chip.text}`"
                        :data-test="`daily-limit-chip-${chip.value}`"
                        @click="form.max_daily_exec = chip.value"
                      >{{ chip.label }}</button>
                    </div>
                  </div>
                </el-form-item>
              </section>

              <!-- ④ 检查与保存：摘要 + 后端预检诊断（诊断状态仍由本页 onSave 持有） -->
              <section v-show="wizardStep === 4" class="rule-wizard__panel" data-test="wizard-panel-review">
                <header class="rule-wizard__panel-head">
                  <h3>检查与保存</h3>
                  <p>核对下面四项；保存时还会执行后端预检（选择器解析、风险动作等）。</p>
                </header>
                <dl class="rule-review" data-test="rule-review">
                  <div class="rule-review__item">
                    <dt>触发</dt>
                    <dd data-test="review-trigger">{{ reviewTrigger }}</dd>
                  </div>
                  <div v-if="form.trigger_type === 'sun_event'" class="rule-review__item">
                    <dt>位置</dt>
                    <dd data-test="review-location">{{ reviewLocation }}</dd>
                  </div>
                  <div class="rule-review__item">
                    <dt>动作</dt>
                    <dd data-test="review-actions">{{ reviewActions }}</dd>
                  </div>
                  <div class="rule-review__item">
                    <dt>策略</dt>
                    <dd data-test="review-policy">{{ reviewPolicy }}</dd>
                  </div>
                </dl>
                <SaveDiagnostics :blocking-errors="saveBlockingErrors" :warnings="saveWarnings" />
              </section>
            </div>
          </el-form>
        </div>
      </div>

      <template #footer>
        <div class="rule-wizard__footer">
          <!-- 移动端用右上角 X 关闭（全屏弹窗里再放一个"取消"会把底部按钮挤成三层）。 -->
          <el-button v-if="!isMobile" @click="dialogVisible = false">取消</el-button>
          <div class="rule-wizard__footer-right">
            <el-button
              v-if="wizardStep > 1"
              data-test="wizard-prev"
              :style="isMobile ? { minHeight: '44px' } : undefined"
              @click="goPrev"
            >上一步</el-button>
            <el-button
              v-if="wizardStep < WIZARD_STEPS.length"
              type="primary"
              data-test="wizard-next"
              :style="isMobile ? { minHeight: '44px' } : undefined"
              @click="goNext"
            >
              下一步
            </el-button>
            <!-- 保存按钮在每一步都可用（既有的保存链路/测试契约），非最后一步用次要样式，
                 主操作仍是「下一步」；最后一步变主按钮。 -->
            <el-button
              v-if="!isMobile || wizardStep === WIZARD_STEPS.length"
              :type="wizardStep === WIZARD_STEPS.length ? 'primary' : 'default'"
              :plain="wizardStep !== WIZARD_STEPS.length"
              :loading="saving"
              :style="isMobile ? { minHeight: '44px' } : undefined"
              data-test="save-rule"
              :data-acked="warningsAcknowledged ? 'true' : 'false'"
              @click="onSave"
            >
              {{ warningsAcknowledged ? '我已了解，仍然保存' : '保存' }}
            </el-button>
          </div>
        </div>
      </template>
    </el-dialog>

    <!-- R8: 动作参数编辑（JSON 文本域 = 「高级」入口）。
         为什么还要保留它：有 input_schema 的动作默认走表单，但 ① 没有 schema 的动作
         （老驱动 / 方言参数）根本没有可生成的字段，JSON 是唯一形态；② 即使有 schema，
         表单项不完整时会禁用提交，用户有时需要直接改原文。两者互转见卡片上的「手写 JSON」
         与下面的「改用表单」。 -->
    <el-dialog
      v-model="paramsDialogVisible"
      title="编辑动作参数"
      width="min(520px, 92vw)"
      data-test="edit-params-dialog"
    >
      <p class="hint-block">
        目标动作：<span class="mono">{{ editingParamsActionId || '-' }}</span>
        <!-- JSON → 表单：把当前文本里**能读出**的对象作为 prefill 带过去（不丢用户刚写的内容），
             不写回模型 —— 用户还没点「完成」，切换编辑器不该悄悄改数据。 -->
        <el-button
          v-if="paramsEditingSchema"
          link
          type="primary"
          size="small"
          :data-test="`action-params-json-link-${paramsEditingIndex}`"
          :aria-label="`第 ${paramsEditingIndex + 1} 个动作改用表单编辑参数`"
          @click="switchParamsToForm"
        >改用表单</el-button>
      </p>
      <el-input
        v-model="paramsDraft"
        type="textarea"
        :rows="5"
        placeholder='JSON 对象，如 {"pin": 12, "value": 1}'
        data-test="params-json"
        aria-label="动作参数 JSON"
        @input="onParamsDraftChange"
      />
      <p v-if="paramsDraftError" class="params-error" data-test="params-error">{{ paramsDraftError }}</p>
      <template #footer>
        <el-button @click="paramsDialogVisible = false">取消</el-button>
        <el-button type="primary" :disabled="!!paramsDraftError" data-test="params-done" @click="applyParams">完成</el-button>
      </template>
    </el-dialog>

    <!-- 参数表单（schema 驱动）：与 BMS 页 / 设备控制面板同一个组件、同一份 definition。
         prefill 传"已存的参数对象"（编辑既有动作时把值带出来）；提交回来的是一个对象，
         写回 action_params_json 仍然保持 JSON **字符串**（全仓既有的落库形态）。 -->
    <ActionForm
      :visible="paramsFormVisible"
      :definition="paramsFormDefinition"
      :prefill="paramsFormPrefill"
      @update:visible="paramsFormVisible = $event"
      @submit="applyFormParams"
    />

    <!-- R11: 保存前风险清单二次确认（用户裁决 O-2 = 逐规则显式授权 + 保存时强确认）。
         为什么逐项勾选而不是一次总确认（设计 D-5）：一次总确认在 3 个以上风险动作时会
         退化成"闭眼点确定"；逐项勾选强制用户逐条读完 动作名 + 目标设备 + 参数。
         默认焦点在**安全侧**（取消）也沿用既有 feedback.confirmDanger 的裁决
         （utils/feedback.ts: 不得诱导性确认）。 -->
    <el-dialog
      v-model="riskDialogVisible"
      title="保存前确认"
      width="min(560px, 92vw)"
      data-test="save-risk-dialog"
      class="save-risk-dialog"
    >
      <el-alert
        type="error"
        :closable="false"
        show-icon
        :title="`本规则包含 ${riskDialogActions.length} 个风险动作`"
        data-test="risk-summary"
      >
        请核对下列动作的目标设备与参数，逐项勾选确认后才可保存。本弹窗只约束「保存这条规则」。
      </el-alert>
      <ul class="risk-list" data-test="risk-list">
        <li v-for="(item, index) in riskDialogActions" :key="item.seq" class="risk-item" data-test="risk-item">
          <div class="risk-item__name">
            <span aria-hidden="true">⚠</span>
            {{ item.action_name }}
            <el-tag type="danger" size="small" data-test="risk-item-badge">
              <span aria-hidden="true">⚠</span> {{ item.risk_label }}
            </el-tag>
          </div>
          <dl class="risk-item__meta">
            <div><dt>目标设备</dt><dd>{{ item.device_name }}</dd></div>
            <div><dt>动作 ID</dt><dd class="mono">{{ item.action_id }}</dd></div>
            <div><dt>参数</dt><dd class="mono">{{ item.params_summary }}</dd></div>
            <div><dt>风险原因</dt><dd data-test="risk-item-reason">{{ item.risk_reason }}</dd></div>
          </dl>
          <label class="risk-ack">
            <input
              type="checkbox"
              :checked="ackedSeqs.includes(item.seq)"
              :data-test="`risk-ack-${index + 1}`"
              :aria-label="`我已了解「${item.action_name}」对「${item.device_name}」的影响，并确认将其加入本规则`"
              @change="toggleAck(item.seq)"
            />
            <span>我已了解「{{ item.action_name }}」对「{{ item.device_name }}」的影响，并确认将其加入本规则。</span>
          </label>
        </li>
      </ul>
      <template #footer>
        <span class="ack-progress" data-test="ack-progress" role="status">
          已确认 {{ ackedSeqs.length }} / {{ riskDialogActions.length }} 个风险动作
        </span>
        <el-button ref="riskCancelRef" data-test="risk-cancel" @click="cancelRiskSave">取消</el-button>
        <el-button
          type="danger"
          :disabled="!allRisksAcked"
          :loading="saving"
          data-test="confirm-save"
          @click="confirmRiskSave"
        >
          保存规则
        </el-button>
      </template>
    </el-dialog>

    <!-- 命令执行详情（F18：命令 ID 此前点了没反应）。
         只读：展示该次执行的终态与（若有）失败原因/人工裁决，不提供任何写操作
         —— 审计入口的职责是"看清楚"，写操作在设备页的命令面板里。
         模板已搬到 components/CommandDetailDialog.vue：本页只保留查询（goCommand）与三份状态。 -->
    <CommandDetailDialog
      v-model="commandDetailOpen"
      :loading="commandDetailLoading"
      :detail="commandDetail"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import feedback from '@/utils/feedback'
import PageHeader from '@/components/common/PageHeader.vue'
// 参数表单与设备控制面板/BMS 页是**同一个组件**：字段/类型/枚举/范围/必填全部来自后端
// definition.input_schema。本页不另写一套 —— 第二份实现必然漂移
//（设备页能提交的参数在自动化页被拦，或反之），而这正是本次要修掉的"同一系统两套录入"。
import ActionForm from '@/views/edge-device/shared/ActionForm.vue'
import {
  automationApi,
  type AutomationEvent,
  type AutomationEventResult,
  type AutomationRule,
  type CreateAutomationRuleRequest,
  type UpdateAutomationRuleRequest,
  type PrecheckResponse,
  type PrecheckRiskAction,
} from '@/api/automation'
import { edgeDeviceApi, type EdgeDevice } from '@/api/edgeDevice'
import { deviceOperationApi, type DeviceOperation } from '@/api/deviceOperation'
import { useResponsive } from '@/composables/useResponsive'
import { useWebSocketStore } from '@/stores/websocket'
import { logger } from '@/utils/logger'
// ── 列表侧拆分（2026-11-12 第一轮，纯搬移）────────────────────────────────────
//   · ./automationFormat：触发器/动作/结果/时间等**纯展示函数**，本页与两个 section 共用；
//     resultText 在本页仍有消费方（手动触发结果提示）。
//   · ./components/*：规则区、触发历史区、命令执行详情。数据加载与动作（fetchRules /
//     fetchEvents / onToggle / onDelete / onTrigger / onConfirmEvent / goCommand / WS 订阅）
//     全部留在本页，子组件只接 props + 抛语义化事件。
import { comparators, resultText } from './automationFormat'
import { sunLocationComplete, sunOffsetText } from './sunEvent'
import { paramsSummary, riskBadge } from './actionList'
// ── 表单区拆分（2026-11-12 第二轮，纯搬移）────────────────────────────────────
//   · ./composables/useRuleForm       表单模型 / 字段组状态 / 校验 / 提交体（内含
//                                     useRuleConditions 与 useRulePreview；
//                                     状态仍是**本页唯一一份**，子组件读写同一个 form）
//   · ./composables/useRuleActions    动作卡片草稿 + 参数编辑（表单 ↔ JSON）
//   · ./composables/useActionCatalog  设备动作目录（候选 / 风险徽标 / schema 判定）
//   · ./components/TriggerFields      触发字段组（含 ConditionEditor）
//   · ./components/ActionCards        多动作卡片
//   · ./components/SaveDiagnostics    保存前诊断区（只渲染）
//   保存编排（precheck → 诊断 → 风控清单 → 提交）与两个弹窗**留在本页**，
//   见 resetSaveDiagnostics / onSave 的注释。
import { useActionCatalog } from './composables/useActionCatalog'
import { useRuleActions } from './composables/useRuleActions'
import { useRuleForm } from './composables/useRuleForm'
import RuleListSection from './components/RuleListSection.vue'
import EventHistorySection from './components/EventHistorySection.vue'
import CommandDetailDialog from './components/CommandDetailDialog.vue'
import TriggerFields from './components/TriggerFields.vue'
import ActionCards from './components/ActionCards.vue'
import SaveDiagnostics from './components/SaveDiagnostics.vue'

/** 窄屏（<768px，与 theme.css 的 @media (max-width:768px) 同断点）用于取消粘性操作列（F26）。 */
const { isMobile } = useResponsive()

const devices = ref<EdgeDevice[]>([])

// ── 规则 ──
const rules = ref<AutomationRule[]>([])
const rulesLoading = ref(false)

async function fetchRules() {
  rulesLoading.value = true
  try {
    rules.value = await automationApi.listRules()
  } catch {
    feedback.error('加载规则失败')
  } finally {
    rulesLoading.value = false
  }
}

// ── 事件 ──
const events = ref<AutomationEvent[]>([])
const eventsLoading = ref(false)
const eventsTotal = ref(0)
const eventsPage = ref(1)
const eventsPageSize = ref(20)
/** 筛选 (rule_id/result) 的初值, 用于判断筛选是否真的变化而重置页码。 */
const EVENT_FILTER_INITIAL = { ruleId: undefined as number | undefined, result: undefined as AutomationEventResult | undefined }
const lastEventFilter = ref({ ...EVENT_FILTER_INITIAL })
const eventFilterRuleId = ref<number | undefined>(EVENT_FILTER_INITIAL.ruleId)
const eventFilterResult = ref<AutomationEventResult | undefined>(EVENT_FILTER_INITIAL.result)

async function fetchEvents() {
  // §3.2.6 MUST: 会改变查询范围的输入 (rule_id/result 筛选) 变化时必须重置分页派生状态。
  // 本函数同时是分页控件与「刷新」按钮的入口 —— 后两者的页码变化属用户**主动翻页**,
  // 不能重置 (否则永远停在第一页)。故只在筛选值真的变了时才把 eventsPage 归 1。
  if (eventFilterRuleId.value !== lastEventFilter.value.ruleId
    || eventFilterResult.value !== lastEventFilter.value.result) {
    eventsPage.value = 1
    lastEventFilter.value = { ruleId: eventFilterRuleId.value, result: eventFilterResult.value }
  }
  eventsLoading.value = true
  try {
    const params: {
      rule_id?: number
      result?: AutomationEventResult
      page: number
      page_size: number
    } = { page: eventsPage.value, page_size: eventsPageSize.value }
    if (eventFilterRuleId.value) params.rule_id = eventFilterRuleId.value
    if (eventFilterResult.value) params.result = eventFilterResult.value
    const res = await automationApi.listEvents(params)
    events.value = res.items
    eventsTotal.value = res.total
  } catch {
    feedback.error('加载事件失败')
  } finally {
    eventsLoading.value = false
  }
}

/** 每页条数变化: 页码必须回到第 1 页 (原第 3 页在新页长下可能已越界)。 */
function onEventsPageSizeChange() {
  eventsPage.value = 1
  void fetchEvents()
}

// ── 保存前诊断（§3.3：后端 precheck 已经产出 warnings，前端此前全页 0 引用）──
//
// 诊断区**默认不渲染**，只在本轮 onSave 拿到后端判定后出现；位置紧跟 footer 上方
// （用户刚点完保存，视线就在那里），不塞进表单中部 —— 那里会让用户误以为是某字段的报错。
//
// 为什么 blocking/warnings 要逐条列全而不是只弹一条 toast（改前行为）：
//   toast 3 秒消失、只显示 blocking[0]，用户改一条、再保存、再看第二条，往返 N 次；
//   而警告类（warnings）改前根本没显示 —— 那正是"选择器写错一个字母、保存成功、
//   规则永不触发、没有任何报错"的静默失效路径。
//
// 为什么不随表单搬进子组件：诊断清单与"已确认的 warnings 指纹"是**保存流程**的状态，
// 与 onSave 的放行判定同生共死；模板在 components/SaveDiagnostics.vue，状态留在这里。

/** 硬校验清单（blocking_errors）。非空 = 本次保存被拒，诊断区逐条列出。 */
const saveBlockingErrors = ref<string[]>([])

/** 风险提示（warnings）。非空 = 保存后**可能**永不触发，需要用户二次确认。 */
const saveWarnings = ref<string[]>([])

/**
 * 已向用户展示并确认过的 warnings 内容指纹（warnings.join('|')）。null = 尚未确认。
 *
 * 为什么用**内容指纹**而不是一个 boolean 开关：
 * 确认的对象是"这一份警告"，不是"这个页面从此免检"。两次点击之间用户可能改了字段名/
 * 设备/条件，warnings 随之变化；若用 boolean，第一次确认后随便怎么改都会被静默放行，
 * 改出一个新的解析不到的选择器也直接保存成功 —— 那恰好是 warnings 要防的那类静默失效。
 *
 * 保存成功 / 重新打开对话框 / 切换规则时必须清空（见 resetSaveDiagnostics），
 * 否则上一份规则的确认会被下一份规则复用。
 */
const ackedWarningFingerprint = ref<string | null>(null)

/** 当前展示的 warnings 指纹（与 ackedWarningFingerprint 同源、同算法）。 */
const saveWarningFingerprint = computed(() => saveWarnings.value.join('|'))

/**
 * 主按钮是否处于"已确认过这一份 warnings"的语义（文案改为「我已了解，仍然保存」）。
 *
 * 判据 = 诊断区正展示 warnings，且该指纹已由**拦截分支**记过确认。诊断区在保存成功 /
 * 重开对话框时清空，所以它精确对应"用户已看到提示、还没点第二次"这一个状态。
 *
 * ⚠ 名字是"已确认"（warningsAcknowledged），不是"需要确认"：为真表示**已经**确认过，
 * 所以按钮文案才是「我已了解，仍然保存」（再点一次就落库）。旧名 saveWarningsNeedAck
 * 读起来恰好相反，后维护者照名字写"if (needAck) return"会把逻辑写反。
 *
 * 注意：阻断与 warnings 可能同时存在，那一轮走的是 blocking 分支（不写确认），
 * 因此本值为 false，按钮保持普通「保存」—— 硬校验没修正前，不应诱导用户去点"仍然保存"。
 */
const warningsAcknowledged = computed(() =>
  saveWarnings.value.length > 0
  && ackedWarningFingerprint.value === saveWarningFingerprint.value,
)

/** 清空诊断与确认指纹（保存成功 / 重新打开对话框 / 切换规则时调用）。 */
function resetSaveDiagnostics() {
  saveBlockingErrors.value = []
  saveWarnings.value = []
  ackedWarningFingerprint.value = null
}

// ── 表单区（第二轮拆分）──────────────────────────────────────────────────────
// 三个组合式函数共享**同一份**响应式状态：form / 目录索引 / 动作草稿都在这里创建，
// 子组件拿到的都是这些对象本身（不是拷贝），所以 `wrapper.vm.form` 与模板读写同源。
const catalog = useActionCatalog()
const {
  form,
  dialogVisible,
  editingId,
  thresholdUnit,
  thresholdPrecision,
  onSensorFieldChange,
  windowStartFixNote,
  windowEndFixNote,
  isOvernightWindow,
  triggerSelection,
  isSunTrigger,
  sourceGroupOptions,
  selectedSourceGroup,
  loadSourceGroups,
  onSourceKindChange,
  onSourceGroupChange,
  openCreate,
  openEdit,
  onTriggerTypeChange,
  validate,
  validateStep,
  firstInvalidStep,
  buildPayload,
  conditionRows,
  conditionsRawMode,
  conditionsRawInvalid,
  addCondition,
  removeCondition,
  toggleConditionsRaw,
  previewResult,
  previewLoading,
  previewDisabled,
  previewHint,
  runPreview,
} = useRuleForm({ catalogDefinition: catalog.catalogDefinition, resetSaveDiagnostics })

const {
  addAction,
  removeAction,
  moveCard,
  onCardTypeChange,
  onCardDeviceChange,
  onCardActionIdChange,
  actionNameOf,
  deviceName,
  riskReasonOf,
  riskyActions,
  cardHelpers,
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
} = useRuleActions({ form, devices, catalog })

// ── 创建规则向导（2026-11-14 重设计）────────────────────────────────────────
//
// 为什么改成 4 步：原对话框把 20+ 个控件塞进一个 640px 的滚动区，PC 上要滚 2~3 屏，
// 移动端弹窗只有半个屏幕高；用户看不到"还差什么"，保存失败时也不知道该往上还是往下找。
// 分步后每一步只有一个问题域，保存失败可以把用户直接带回出错的步骤。
//
// 关键取舍：**所有步骤的 DOM 都保留**（v-show，而非 v-if）。原因有两个：
//   1. 组件内已有大量按 data-test 直接操作的测试；v-if 会让非当前步骤的控件消失，
//      把"业务校验"测试变成"点不到控件"的假失败；
//   2. 用户从第 3 步点左侧步骤回第 1 步时，表单状态与滚动位置都在，不需要重新挂载。
const WIZARD_STEPS = [
  { id: 1, title: '基本信息', desc: '名称与启用状态' },
  { id: 2, title: '触发条件', desc: '传感器 / 时间窗 / 日出日落' },
  { id: 3, title: '动作与策略', desc: '执行动作、冷却与人工确认' },
  { id: 4, title: '检查与保存', desc: '核对摘要并提交' },
] as const

const wizardStep = ref(1)
const currentStepTitle = computed(() => WIZARD_STEPS[wizardStep.value - 1]?.title ?? '')

function goStep(step: number) {
  // 允许回看/跳到任意步骤：向导不是线性门禁，真正的校验在「下一步」与「保存」上。
  wizardStep.value = Math.min(WIZARD_STEPS.length, Math.max(1, step))
}
function goPrev() {
  if (wizardStep.value > 1) wizardStep.value--
}
function goNext() {
  const msg = validateStep(wizardStep.value)
  if (msg) {
    ElMessage.warning(msg)
    return
  }
  if (wizardStep.value < WIZARD_STEPS.length) wizardStep.value++
}

// 打开对话框（新建/编辑）必须回到第 1 步：否则会沿用上一条规则停留的步骤，
// 用户看到的是"编辑对话框打开却停在检查页"。
watch(dialogVisible, (visible) => {
  if (visible) wizardStep.value = 1
})

/** 触发摘要用的设备名（规则可能只存了 id；设备列表尚未加载时退回 #id）。 */
function reviewDeviceName(): string {
  const id = form.trigger_edge_device_id
  if (!id) return '未选择设备'
  return devices.value.find(d => d.id === id)?.name ?? `设备 #${id}`
}

const reviewTrigger = computed(() => {
  if (form.trigger_type === 'sensor_threshold') {
    const source = form.source_kind === 'source_group'
      ? (selectedSourceGroup.value?.name ?? '未选择来源组')
      : reviewDeviceName()
    const cmp = comparators.find(c => c.value === form.trigger_comparator)?.label ?? form.trigger_comparator
    const unit = thresholdUnit.value ? ` ${thresholdUnit.value}` : ''
    const hold = form.trigger_duration_sec > 0 ? `，持续 ${form.trigger_duration_sec}s` : ''
    return `${source} · ${form.trigger_sensor_name || '未选择字段'} ${cmp} ${form.trigger_threshold}${unit}${hold}`
  }
  if (form.trigger_type === 'time_window') {
    const edge = form.trigger_window_edge === 'enter'
      ? '进入窗口时'
      : form.trigger_window_edge === 'exit' ? '离开窗口时' : '窗口内持续'
    return `每天 ${form.trigger_window_start || '--:--'} ~ ${form.trigger_window_end || '--:--'}（${edge}）`
  }
  if (form.trigger_type === 'sun_event') {
    return sunOffsetText(form.trigger_sun_kind, form.trigger_sun_offset_min)
  }
  return '事件驱动（占位）'
})

const reviewLocation = computed(() => {
  if (!sunLocationComplete(form)) return '尚未选择位置'
  return `${form.location_label || '已选择位置'} · ${form.latitude}, ${form.longitude} · ${form.timezone}`
})

const reviewActions = computed(() => {
  if (form.actions.length === 0) return '尚未添加动作'
  const items = form.actions.map((action, index) => {
    const label = action.action_type === 'device_action'
      ? (action.action_id || '未选择动作')
      : `通知（${action.action_level || '未选级别'}）`
    return `${index + 1}. ${label}`
  })
  return `共 ${form.actions.length} 个：${items.join('；')}`
})

const reviewPolicy = computed(() => {
  const parts = [form.require_confirmed ? '触发后需人工确认' : '触发即执行']
  parts.push(form.cooldown_sec > 0 ? `冷却 ${form.cooldown_sec}s` : '不冷却')
  parts.push(form.max_daily_exec > 0 ? `每日最多 ${form.max_daily_exec} 次` : '每日不限')
  parts.push(form.enabled ? '已启用' : '未启用')
  return parts.join(' · ')
})

/**
 * 冷却 / 每日上限的快捷值（文档 §4.5）。
 *
 * 只是把值写回 v-model（`form.cooldown_sec = chip.value`），**不改控件类型** ——
 * 数字框仍在，0-86400 的连续值域里任何枚举都会缺档，chips 只覆盖现场最常用的几档。
 * 单位在 label 里用人类可读的写法（5s/1m/1h），写回的一律是**秒**（后端契约字段名就是 _sec）。
 *
 * text 只用于 aria-label：读屏用户听到的是"冷却时间设为 5 分钟"，不是 "5m"。
 */
const COOLDOWN_CHIPS: Array<{ value: number; label: string; text: string }> = [
  { value: 0, label: '0', text: '0 秒（不冷却）' },
  { value: 5, label: '5s', text: '5 秒' },
  { value: 30, label: '30s', text: '30 秒' },
  { value: 60, label: '1m', text: '1 分钟' },
  { value: 300, label: '5m', text: '5 分钟' },
  { value: 1800, label: '30m', text: '30 分钟' },
  { value: 3600, label: '1h', text: '1 小时' },
]

/** 每日上限快捷值：0 = 不限（后端语义），其余为"每天最多 N 次"。 */
const DAILY_LIMIT_CHIPS: Array<{ value: number; label: string; text: string }> = [
  { value: 0, label: '0', text: '0 次（不限）' },
  { value: 1, label: '1', text: '1 次' },
  { value: 3, label: '3', text: '3 次' },
  { value: 10, label: '10', text: '10 次' },
  { value: 50, label: '50', text: '50 次' },
]

/** 保存中（主按钮 loading 与"保存"按钮的二次点击拦截都读它）。 */
const saving = ref(false)

// ── 保存前风险清单二次确认 (R11 / F-4, 用户裁决 O-2) ──────────────────────

interface RiskCheckItem {
  seq: number
  action_id: string
  action_name: string
  device_name: string
  /** 后端下发的展示标签（缺失时用 2 级契约的固定文案） */
  risk_label: string
  risk_reason: string
  params_summary: string
}

const riskDialogVisible = ref(false)
const riskDialogActions = ref<RiskCheckItem[]>([])
const ackedSeqs = ref<number[]>([])
/** 保存成功后真的要执行的提交（避免把已确认的风险清单状态带进下一次保存）。 */
const pendingSubmit = ref<CreateAutomationRuleRequest | null>(null)
const riskCancelRef = ref<{ $el?: HTMLElement } | null>(null)

const allRisksAcked = computed(
  () => riskDialogActions.value.length > 0 && ackedSeqs.value.length === riskDialogActions.value.length,
)

function toggleAck(seq: number) {
  ackedSeqs.value = ackedSeqs.value.includes(seq)
    ? ackedSeqs.value.filter(s => s !== seq)
    : [...ackedSeqs.value, seq]
}

function cancelRiskSave() {
  riskDialogVisible.value = false
  ackedSeqs.value = []
  pendingSubmit.value = null
}

/** 弹窗内某个动作的风险原因：后端 risk_reason 优先，其次回落到卡片同款说明。 */
function riskItemReason(actionId: string, deviceId: number, backendReason?: string): string {
  if (backendReason && backendReason.trim()) return backendReason
  const draft = form.actions.find(a => a.action_id === actionId && a.action_device_id === deviceId)
  return draft ? riskReasonOf(draft) : '后端未下发风险原因文案，请自行确认该动作对目标设备的影响'
}

/**
 * 把**预检响应**转换成风险清单条目。
 *
 * 清单必须列全 动作名 / 目标设备 / 动作 ID / 参数 / 风险原因（需求 §1.3"列出风险操作"）。
 * 动作名只能从本地设备目录取 —— 预检响应只有 `action_device_name`（设备名），
 * 没有动作名字段，把设备名当动作名会让用户确认错对象（安全文案错误）。
 */
function buildRiskItems(risks: PrecheckRiskAction[]): RiskCheckItem[] {
  return risks.map(item => ({
    seq: item.seq,
    action_id: item.action_id,
    action_name: actionNameOf(item.action_id, item.action_device_id),
    device_name: item.action_device_name || deviceName(item.action_device_id),
    risk_label: item.risk_label || '风险',
    risk_reason: riskItemReason(item.action_id, item.action_device_id, item.risk_reason),
    // 摘要优先级: 后端给的摘要 → 后端回带的参数 JSON → 本地卡片里的参数 JSON。
    // 不能写成 `paramsSummary(item.action_params_json) || ...`：paramsSummary(undefined)
    // 返回 '无参数'（真值），会把本地兜底整段短路掉，风险清单里就看不到用户实际填的参数。
    params_summary: item.params_summary
      || (item.action_params_json ? paramsSummary(item.action_params_json) : '')
      || paramsSummary(form.actions.find(a => a.action_id === item.action_id)?.action_params_json),
  }))
}

function openRiskDialog(risks: PrecheckRiskAction[], payload: CreateAutomationRuleRequest) {
  riskDialogActions.value = buildRiskItems(risks)
  ackedSeqs.value = []
  pendingSubmit.value = payload
  riskDialogVisible.value = true
}

/**
 * 保存入口：先探后端预检端点，再按结果分派。
 *
 * ## 为什么必须有"端点不可用"的降级分支（而不是一律 fail-closed）
 *
 * 方案 §4.1.2 的 `POST /automation-rules/precheck` 是**新增端点**，B4 前端不得假定
 * 它已随本批上线：实测后端 routes 里**只有** GET/POST ""、GET/:id、PUT/:id、
 * DELETE/:id、PATCH /:id/enabled、POST /:id/trigger —— **没有 precheck**。
 * 若在端点缺失时一律 fail-closed，用户将**完全无法保存任何规则**（含普通规则），
 * 这比"风险清单拿不到"严重得多。
 *
 * ## 降级到什么？（关键：不能降级成"当作没有风险动作"）
 *
 * 后端**已经**通过规则详情下发动作集合（`actions` 数组），并已在动作目录
 * （`GET /edge-devices/:id/actions`）里下发风险等级（`risk_level` /
 * `requires_confirmation`）。二者合起来足以判定"这条规则是否含风险动作" ——
 * 判定源仍是**后端下发的字段**，前端没有第二份风险等级表（不违反 R2/F-7）。
 *
 * 因此降级语义是"用后端下发的动作集合自行拼装清单"，而**不是**"跳过确认"：
 *   · 判定源 = requires_confirmation（后端字段，与后端确认门同一判定点）；
 *   · 风险原因 = 后端 Catalog 的 description，并**明确标注它不是危险说明**
 *     （后端未实现 risk_reason，见设计稿 §9 O-2；编造危险说明比没有更危险）；
 *   · 设备名 = 本地设备列表（后端未给时的等价来源）。
 *
 * ⚠️ 唯一的真实缺口：目录**尚未加载**的设备上的动作无法判定等级。此时**不得**
 * 静默放行 —— 必须显式告知用户"无法判定风险，请重试/刷新"并阻止保存，
 * 而不是把未知当普通（那正是 F-3/F-6 要防的"猜风险"）。
 */
async function onSave() {
  // 绕过护栏（F-4 反向验证的落点）：风险清单弹窗已打开时，**任何**再次触发的保存
  // 都必须被拒 —— 否则用户可以在未勾选的情况下重复点「保存」把风险确认绕过去。
  // 这条判定放在函数入口而不是只靠按钮 disabled：disabled 是 UI 属性，
  // 键盘/程序化触发/未来的快捷键都可能绕过它。
  if (riskDialogVisible.value) return
  // 诊断区在每次保存时**重新计算**：先清掉上一轮的结果，避免"用户已经改掉的那条"
  // 继续留在界面上（残留提示比没有提示更误导）。
  saveBlockingErrors.value = []
  saveWarnings.value = []
  const err = validate()
  if (err) {
    // 向导：把用户带到第一个有问题的步骤，而不是只在 toast 里说一句。
    wizardStep.value = firstInvalidStep()
    ElMessage.warning(err)
    return
  }
  // 校验通过 → 进入「检查与保存」：用户能在预检/风险弹窗旁看到完整摘要与诊断。
  wizardStep.value = WIZARD_STEPS.length
  saving.value = true
  try {
    const payload = buildPayload()
    // 普通动作**无**二次确认，直接落库。此处刻意**不带** risk_acknowledged 字段
    // （既不 true 也不 false）：带 false 会在每次保存普通规则时清掉规则级授权，而
    // "未授权的普通规则被改成含风险动作"必然触发后端 B1 的授权失效（动作集合变化即
    // 清空）并强制重新确认 —— 故 false 不带来额外安全性，只带来无谓的状态翻转；
    // 带 true 则是无依据的授权。省略 = "本次保存不改变授权语义"，与后端 *bool 可空语义一致。
    const submitPlain = async () => { await submitRule(payload) }

    // ① 后端预检端点（方案 §4.1.2，**尚未实现**）。
    const precheck = await automationApi.precheckRule(payload)
    if (precheck !== null) {
      const blocking = precheck.blocking_errors ?? []
      const warnings = precheck.warnings ?? []
      // 整体覆盖而不是累积：诊断区只反映**本轮**判定。
      saveBlockingErrors.value = blocking
      saveWarnings.value = warnings
      if (blocking.length > 0) {
        // 即时反馈仍是 toast + 首条（既有单测契约），**完整清单**交给诊断区逐条列出：
        // 改前这里是 `ElMessage.warning(blocking[0]); return`，多条错误要来回保存 N 次。
        ElMessage.warning(blocking[0])
        return
      }
      if (warnings.length > 0) {
        const fingerprint = warnings.join('|')
        if (ackedWarningFingerprint.value !== fingerprint) {
          // 第一次点击（同一份内容）：**不保存**，只把风险提示展示出来，主按钮改成
          // 「我已了解，仍然保存」；用户再点一次（同一份 warnings）才真正落库。
          //
          // 为什么 warnings 要二次确认，而不是像 blocking 那样硬拒、也不是直接保存：
          //   · 硬拒：后端把它判成 warning 就是因为**当前无法证明它错**（选择器语法合法，
          //     只是此刻解析不到，字段可能稍后才上报），拦死会让合法规则永远存不进去；
          //   · 直接放行：这是最坏的一种失效 —— 保存成功、规则永不触发、界面没有任何
          //     痕迹，用户不会有第二次机会看到这句提示。
          // 因此留一次"已看到并坚持保存"的选择，代价是一次点击，收益是把静默失效变成可见取舍。
          //
          // 确认记在**内容指纹**上（不是 boolean）：见 ackedWarningFingerprint 注释。
          ackedWarningFingerprint.value = fingerprint
          return
        }
      }
      const risks = precheck.risk_actions ?? []
      if (risks.length === 0) {
        await submitPlain()
        return
      }
      openRiskDialog(risks, payload)
      await focusRiskCancel()
      return
    }

    // ② 降级：端点不可用（未实现/网络失败）。用后端已下发的动作目录判定，
    //    **绝不**把"无法判定"当成"没有风险动作"（见 onSave 顶部注释）。
    const fallback = riskActionsFromCatalog()
    if (fallback === null) {
      feedback.error('无法判定本规则是否包含风险动作（设备动作目录尚未加载），已阻止保存：请稍后重试')
      return
    }
    if (fallback.length === 0) {
      await submitPlain()
      return
    }
    openRiskDialog(fallback, payload)
    await focusRiskCancel()
  } catch {
    feedback.error('保存失败')
  } finally {
    saving.value = false
  }
}

/**
 * 降级路径：从**后端已下发的动作目录**里挑出风险动作，拼成预检同形的清单。
 *
 * 返回 `null` 表示**无法判定**（有 device_action 的目录尚未加载）——调用方必须
 * 据此阻止保存，而不是当作"无风险动作"。
 */
function riskActionsFromCatalog(): PrecheckRiskAction[] | null {
  const risks: NonNullable<PrecheckResponse['risk_actions']> = []
  for (const [index, action] of form.actions.entries()) {
    if (action.action_type !== 'device_action' || !action.action_device_id || !action.action_id) continue
    const def = catalog.catalogDefinition(action)
    if (!def) return null // 目录未加载 ⇒ 无法判定，不得猜
    if (!riskBadge(def)) continue
    risks.push({
      seq: index + 1,
      action_id: action.action_id,
      action_device_id: action.action_device_id,
      action_device_name: deviceName(action.action_device_id),
      risk_level: def.risk_level,
      risk_label: '风险',
      // 刻意**不填** risk_reason：后端未实现该字段，而 Catalog 的 description 是
      // **操作说明**不是**危险说明**（例如 set_mos_policy 的 description 写的是
      // "一次提交充放电两个关闭位；bounded 写+读回 fet_status 对账"，直接当风险原因
      // 用户读不懂 —— 设计稿 §9 O-2 记录的正是这一点）。留空后 riskItemReason 会
      // 回落到 riskReasonOf()，那里会**显式标注"非危险说明"**并说明后端未下发原因。
      // 编造一条听起来像危险说明的文案，比如实说"未下发"更危险。
      params_summary: paramsSummary(action.action_params_json),
    })
  }
  return risks
}

/** 默认焦点落在**安全侧**（取消），沿用 utils/feedback.ts 的既有裁决（不得诱导性确认）。 */
async function focusRiskCancel() {
  await nextTick()
  const el = riskCancelRef.value?.$el
  if (el && typeof el.focus === 'function') el.focus()
}

/** 逐项勾满后才可调用（按钮 disabled 的同时在函数里再判一次：绕过 UI 也必须被拒）。 */
async function confirmRiskSave() {
  if (!allRisksAcked.value) return
  const payload = pendingSubmit.value
  if (!payload) return
  saving.value = true
  try {
    await submitRule({ ...payload, risk_acknowledged: true })
    riskDialogVisible.value = false
    ackedSeqs.value = []
    pendingSubmit.value = null
  } catch {
    feedback.error('保存失败')
  } finally {
    saving.value = false
  }
}

async function submitRule(payload: CreateAutomationRuleRequest) {
  if (editingId.value) {
    await automationApi.updateRule(editingId.value, payload as UpdateAutomationRuleRequest)
    ElMessage.success('规则已更新')
  } else {
    await automationApi.createRule(payload)
    ElMessage.success('规则已创建')
  }
  // 保存成功 = 本次流程结束：清空诊断与确认指纹。不清的话，下一份规则若恰好产出
  // 相同的 warnings 文本，会被上一份的确认静默放行（"下一份被静默放行"正是要防的）。
  resetSaveDiagnostics()
  dialogVisible.value = false
  void fetchRules()
  void fetchEvents()
}

async function onToggle(rule: AutomationRule, enabled: boolean) {
  try {
    await automationApi.setRuleEnabled(rule.id, enabled)
    rule.enabled = enabled
  } catch {
    feedback.error('切换失败')
  }
}

async function onDelete(rule: AutomationRule) {
  // 删除规则不可恢复：走统一危险确认（danger 确认键 + 焦点不落在破坏性按钮上）。
  const confirmed = await feedback.confirmDanger(`删除规则「${rule.name}」？`, {
    title: '确认删除',
    confirmText: '删除',
    cancelText: '取消',
  })
  if (!confirmed) return
  try {
    await automationApi.deleteRule(rule.id)
    ElMessage.success('已删除')
    void fetchRules()
    void fetchEvents()
  } catch {
    /* 取消或失败静默 */
  }
}

async function onConfirmEvent(event: AutomationEvent) {
  // 确认制事件的「确认执行」会放开人工确认门、直接下发设备动作（不可撤销），
  // 属破坏性操作，故走 danger 确认 + 安全侧焦点。
  const confirmed = await feedback.confirmDanger('确认执行该高风险动作？', {
    title: '确认执行',
    confirmText: '确定',
    cancelText: '取消',
  })
  if (!confirmed) return
  try {
    await automationApi.confirmEvent(event.id)
    ElMessage.success('已确认执行')
    void fetchEvents()
  } catch {
    /* 取消或失败静默 */
  }
}

// ── 手动触发 ──
const triggeringId = ref<number | null>(null)

async function onTrigger(rule: AutomationRule) {
  // 手动触发是**非破坏性**操作 ⇒ 走 feedback.confirm (普通确认)。
  // 不得改走 confirmDanger: §3.4.3 的 danger 类目只用于破坏性操作 (本仓既有结构性裁决,
  // 见 listPageConventions.spec.ts)。confirm 返回 boolean, 取消即 return。
  const ok = await feedback.confirm(
    `手动触发规则「${rule.name}」？将跳过条件评估与确认制直接执行动作 (cooldown/日熔断仍生效)。`,
    { title: '手动触发', confirmText: '触发', cancelText: '取消' },
  )
  if (!ok) return // 用户取消
  triggeringId.value = rule.id
  try {
    const ev = await automationApi.triggerRule(rule.id)
    const msg = resultText(ev.result)
    if (ev.result === 'executed' || ev.result === 'notification') {
      ElMessage.success(`已触发: ${msg}`)
    } else if (ev.result === 'suppressed_cooldown') {
      ElMessage.warning(`触发被抑制: ${msg} (${ev.detail ?? ''})`)
    } else if (ev.result === 'suppressed_daily_limit') {
      ElMessage.warning(`触发被抑制: ${msg}`)
    } else {
      ElMessage.info(`触发结果: ${msg}${ev.detail ? ' — ' + ev.detail : ''}`)
    }
    void fetchEvents()
  } catch (e: unknown) {
    feedback.handleError(e, '触发失败')
  } finally {
    triggeringId.value = null
  }
}

/**
 * 命令 ID 回链（F18：此前是 console.log 占位 —— 点了没反应，属"静默失败"）。
 *
 * 本仓**没有**"命令详情页"路由，但有真实可查的端点
 * `GET /api/v1/device-operations/:command_id`（handler_device_operation.go:113）。
 * 因此这里不再"跳转"，而是**就地查询并展示**该次执行的终态 —— 用户点这个 ID 的
 * 真实意图是"这条命令后来怎么样了"，而不是"换一个页面"。
 *
 * 为什么用只读弹窗而不是引导用户去设备详情页：审计行只带 command_id，
 * 不带 edge_device_id 之外的定位信息；把用户丢到另一个列表再自己找，等于没回链。
 */
const commandDetail = ref<DeviceOperation | null>(null)
const commandDetailLoading = ref(false)
const commandDetailOpen = ref(false)

async function goCommand(commandId: string) {
  commandDetailOpen.value = true
  commandDetailLoading.value = true
  commandDetail.value = null
  try {
    commandDetail.value = await deviceOperationApi.get(commandId)
  } catch (e: unknown) {
    // 查不到（例如执行记录已被保留期清理）必须**显式告知**，不能留一个空弹窗。
    //
    // 必须用 handleErrorWithContext 而不是 handleError：后者的第二参数是**兜底**，
    // 只在拿不到 message 时才生效；而后端**总是**带 message（envelope.Error 必写 Message），
    // 实测结果是用户只看到 "operation not found"，**不知道是哪个操作失败了**
    // （见 utils/feedback.ts:114-131 的记录）。这里"查命令记录"这个上下文对用户有信息量，
    // 所以显式拼进消息。
    feedback.handleErrorWithContext(e, '未能读取该命令的执行记录（可能已被保留期清理）')
    commandDetailOpen.value = false
  } finally {
    commandDetailLoading.value = false
  }
}

onMounted(async () => {
  await Promise.all([fetchRules(), fetchEvents()])
  try {
    const list = await edgeDeviceApi.getList()
    devices.value = list.items
  } catch {
    /* 设备下拉加载失败不阻塞页面 */
  }
  await loadSourceGroups()
  await catalog.loadActionCatalog(rules.value)
})

// ── G5：人工确认事件到达时自动刷新，不要求用户手动翻页/重进 ──
//
// 改前：后端 `automation/planner.go` 在「待人工确认 / 日熔断 / 系统执行者不可用」时
// 已通过 WS 推送（`automation_pending_confirm` 等），但本页**没有任何订阅** ——
// 事件到达时页面看不到新事件，用户只能手动点「刷新」或翻页才可能发现待确认项，
// 而此时确认时限可能已经过去（策略卡在 pending_confirm）。
// MainLayout 只负责弹提示与跳转（`MainLayout.vue:495-508`），不刷新本页数据。
//
// 订阅的事件名是**后端实际推送的字面量**（`planner.go:474/525/550`），
// 它们尚未收进 `events/events.ts` 的 WS_EVENT 枚举（枚举缺失是既有债，
// 这里先用字面量并注释出处，避免为 3 个事件扩枚举引发更大范围的改动）。
//
// B4 新增两个事件名的订阅（方案 §4.3）：
//   · `automation_confirmation_required` —— C5 消除的**前端闭环**：自动路径上的风险动作
//     被确认门拦住时，前端必须给出用户可见的提示，而不是只往事件表里写一行
//     （改造前该结果落 failed_dispatch，与"传输失败"在界面上无法区分，用户看不到
//     任何可行动信号）。这里**显式提示 + 带规则名**，并把事件表刷出来。
//   · `automation_partial_failure` —— 多动作执行到第 k 个失败、剩余已中止。这是
//     危险状态（设备已被改了一半），必须显式提示"需要人工介入"，不能等用户翻事件表。
const wsStore = useWebSocketStore()
const AUTOMATION_PUSH_EVENTS = [
  'automation_pending_confirm',
  'automation_daily_limit',
  'automation_system_actor_unavailable',
  'automation_confirmation_required',
  'automation_partial_failure',
] as const
let unsubscribeAutomationPushes: Array<() => void> = []

/** WS 载荷可能嵌一层 notification 实体，detail 里才有规则名/动作名。 */
function pushDetail(message: { payload?: { detail?: Record<string, unknown> } } & Record<string, unknown>): Record<string, unknown> {
  const payload = (message.payload ?? {}) as Record<string, unknown>
  const detail = (payload.detail ?? {}) as Record<string, unknown>
  return { ...payload, ...detail }
}

function onAutomationPush(name: string, message: unknown) {
  const detail = pushDetail(message as { payload?: { detail?: Record<string, unknown> } })
  if (name === 'automation_confirmation_required') {
    const rule = detail.rule_name ?? detail.rule_id ?? ''
    const action = detail.action_id ?? ''
    ElMessage.warning(
      `规则「${rule}」的风险动作 ${action} 未获授权，已被确认门拦下：请在本页「触发历史」中对相应事件执行确认，或在编辑规则时勾选风险确认。`,
    )
  } else if (name === 'automation_partial_failure') {
    const rule = detail.rule_name ?? detail.rule_id ?? ''
    const seq = detail.failed_seq ?? ''
    const failed = detail.failed_action_id ?? ''
    // 必须走 feedback（I-1 收敛门禁：禁止裸 ElMessage.error）。这是**错误**级提示，
    // 且带上下文（哪条规则、第几个动作、哪个动作），用户据此才能去人工核查。
    feedback.error(
      `规则「${rule}」第 ${seq} 个动作 ${failed} 执行失败，剩余动作已中止 —— 设备可能处于部分变更状态，请立即人工核查。`,
    )
  }
  // 只刷事件表（确认动作就是针对事件行的）；规则表结构不受推送影响。
  // 失败不打扰用户：这是被动刷新，页面上仍有手动「刷新」按钮兜底。
  void fetchEvents().catch((err: unknown) => {
    logger.warn('自动化推送触发的刷新失败', { error: String(err), event: name })
  })
}

onMounted(() => {
  try {
    unsubscribeAutomationPushes = AUTOMATION_PUSH_EVENTS.map(name => wsStore.subscribe(name, message => onAutomationPush(name, message)))
  } catch (err: unknown) {
    // 订阅失败不得阻塞页面（无 WS 时页面仍可手动刷新）。
    logger.warn('自动化推送订阅失败', { error: String(err) })
  }
})

onUnmounted(() => {
  for (const off of unsubscribeAutomationPushes) off()
  unsubscribeAutomationPushes = []
})
</script>

<style scoped>
.automation-rules-page { display: flex; flex-direction: column; gap: 16px; }
.mono { font-family: monospace; }
.hint { margin-left: 8px; color: var(--el-text-color-secondary); font-size: 12px; }
.hint-block { margin-top: 4px; font-size: 12px; color: var(--el-text-color-secondary); line-height: 1.6; }
.events-pagination { display: flex; justify-content: flex-end; margin-top: 12px; flex-wrap: wrap; }
/* 分页控件换行（窄屏真实裁切修复，与 notification/NotificationChannels.vue 同根因；
   原参照页 LogicalDeviceList.vue 已于 2026-10-23 随逻辑设备概念移除）。
   .events-pagination 的 flex-wrap 只管多个 item 之间；内层 el-pagination 自身
   white-space:nowrap + display:flex 且宽 676.94px，在 768px（.events-pagination 宽 488px）
   就已把「共 N 条」推出容器左界 153px —— 实测 el-pagination x=47.06 < 容器 x=236，
   祖先链 scrollWidth === clientWidth（不可回滚）⇒ 真实裁切。
   同仓范式：firmware/FirmwareManage.vue 的 .firmware-manage :deep(.el-pagination)。 */
.events-pagination :deep(.el-pagination) { flex-wrap: wrap; }

/* ============================================================================
   冷却 / 每日上限快捷 chips（文档 §4.5）
   —— 视觉沿用页面既有的"浅描边 + 圆角"语言，不引入新的圆角/色板变量。
   —— 这两个字段属于规则级编排（与"需人工确认"同组），不随触发器/动作片段搬走。
   ========================================================================== */
.number-field { width: 100%; display: flex; flex-direction: column; gap: 6px; }
.number-field__main { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.chip-row { display: flex; flex-wrap: wrap; gap: 6px; }
.chip {
  display: inline-flex; align-items: center; justify-content: center;
  min-height: 26px; padding: 0 10px;
  border: 1px solid var(--el-border-color);
  border-radius: 13px;
  background: var(--el-fill-color-blank);
  color: var(--el-text-color-regular);
  font-size: 12px; line-height: 1;
  cursor: pointer;
}
.chip:hover { border-color: var(--el-color-primary); color: var(--el-color-primary); }
.chip:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 1px; }
.chip.is-active { border-color: var(--el-color-primary); color: var(--el-color-primary); background: var(--el-color-primary-light-9); }

/* ============================================================================
   规则级「风险动作 + 已关闭人工确认」提示（R9 / V7）
   —— 与动作卡片的风险样式（components/ActionCards.vue）分开：这一条是**规则级**语义。
   ========================================================================== */
.ack-hint {
  margin: -6px 0 12px 110px;
  padding: 6px 10px;
  border: 1px dashed var(--el-color-warning-light-5);
  border-radius: 4px;
  background: var(--el-color-warning-light-9);
  font-size: 12px;
  color: var(--el-text-color-regular);
  line-height: 1.6;
}
.params-error { margin: 6px 0 0; font-size: 12px; color: var(--el-color-danger); }

/* ============================================================================
   保存前风险清单弹窗 (设计稿 §5 R11 / §6 D-5)
   ========================================================================== */
.risk-list { list-style: none; margin: 12px 0 0; padding: 0; display: flex; flex-direction: column; gap: 10px; }
.risk-item {
  border: 1px solid var(--el-color-danger-light-5);
  border-radius: 8px;
  padding: 10px 12px;
  background: var(--el-bg-color);
}
.risk-item__name {
  display: flex; align-items: center; gap: 6px; flex-wrap: wrap;
  font-weight: 600; color: var(--el-text-color-primary); font-size: 14px;
}
.risk-item__meta { display: grid; grid-template-columns: 1fr; gap: 2px; margin: 8px 0 0; }
.risk-item__meta > div { display: flex; gap: 6px; font-size: 12px; min-width: 0; }
.risk-item__meta dt { flex: 0 0 60px; color: var(--el-text-color-secondary); }
.risk-item__meta dd { margin: 0; color: var(--el-text-color-regular); overflow-wrap: anywhere; }
.risk-ack { display: flex; gap: 8px; align-items: flex-start; margin-top: 10px; font-size: 13px; color: var(--el-text-color-regular); cursor: pointer; line-height: 1.6; }
.risk-ack input { width: 16px; height: 16px; margin: 3px 0 0; flex: 0 0 auto; accent-color: var(--el-color-danger); }
.risk-ack input:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 1px; }
.ack-progress { margin-right: auto; font-size: 12px; color: var(--el-text-color-secondary); }

/* ============================================================================
   窄屏：768px 主断点（与 theme.css / useResponsive 同源）
   ========================================================================== */
@media (max-width: 768px) {
  .ack-hint { margin-left: 0; }
  /* 触控目标高度：chips 在粗指针设备上同样要够 36px（theme.css 的触控档） */
  .chip { min-height: 36px; padding: 0 12px; }
}

/* ============================================================================
   窄屏：480px 加强档 (F-10；设计稿 §7)
   为什么弹窗要变底部抽屉：640px 宽的规则编辑器在 390px 视口下即使 92vw 也只有
   359px 可用宽，表单标签 + 控件会被压到逐字换行；而保存前确认弹窗内容长度不可控
   （风险动作越多越长），全屏抽屉能让内容区独立滚动且 footer 常驻可达。
   ========================================================================== */
@media (max-width: 480px) {
  /* 列表两张卡片（RuleListSection / EventHistorySection 的根节点）在窄屏收紧内边距。
     子组件根节点会继承本页的 scope id（data-v-xxx），故这条规则仍然是活的命中，
     不是孤儿选择器 —— 挪进子组件反而会丢掉"页面级密度"这层语义。 */
  .card { padding: 12px; }
  /* 风险清单 dt/dd 上下堆叠：避免"风险原因"这类长标签挤压正文。 */
  .risk-item__meta > div { flex-direction: column; gap: 0; }
  .risk-item__meta dt { flex: none; }
  /* 弹窗改为底部抽屉：满宽、贴底、顶部圆角、内容区独立滚动。
     ⚠ 2026-10-21 实测：只把 .el-dialog 的 margin 归零是**不够**的 —— 外层
     .el-overlay-dialog 计算样式是 display:block（不是 flex），于是 92vh 高的弹窗
     顶对齐：dialog y=0 / bottom=783，视口 851 时底部留下 68px 灰带、顶部圆角出现在
     屏幕最上方 —— 视觉上是个"顶抽屉"。把 overlay 容器本身改为 flex 且底部对齐，
     抽屉才真正贴底（改后 dialog bottom=851）。 */
  :deep(.el-overlay-dialog) {
    display: flex;
    align-items: flex-end;
    justify-content: center;
  }
  :deep(.el-dialog) {
    width: 100% !important;
    max-width: 100% !important;
    margin: 0 !important;
    border-radius: 12px 12px 0 0 !important;
    max-height: 92vh;
    display: flex;
    flex-direction: column;
  }
  :deep(.el-dialog__body) { overflow: auto; }
  :deep(.el-dialog__footer) { display: flex; flex-wrap: wrap; gap: 8px; }
  :deep(.el-dialog__footer .el-button) { flex: 1 1 auto; min-height: 44px; }
  .ack-progress { flex: 1 1 100%; margin: 0 0 4px; }
}

/* ============================================================================
   创建规则向导（2026-11-14 重设计）
   —— 对话框被 teleport 到 body，scoped 的 :deep 选不中，故用 :global 挂类。
   PC：左侧步骤栏 + 右侧内容；移动端：全屏 + 顶部步骤条 + 单列。
   ========================================================================== */
:global(.rule-wizard-dialog) {
  display: flex;
  flex-direction: column;
  max-height: 88vh;
  margin-bottom: 0;
}
:global(.rule-wizard-dialog .el-dialog__body) {
  padding: 0;
  flex: 1;
  min-height: 0;
  overflow: hidden;
}
:global(.rule-wizard-dialog.is-fullscreen) {
  /* theme.css 有全局 .el-dialog { max-width: 92vw }，全屏时必须显式解除，
     否则移动端会变成「左右各留 15px 的全屏」——高度对、宽度不对。 */
  width: 100%;
  max-width: 100%;
  margin: 0;
  height: 100vh;
  max-height: 100vh;
}

.rule-wizard { display: flex; height: 100%; min-height: 520px; }
.rule-wizard__rail {
  width: 232px;
  flex: none;
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 18px 12px;
  border-right: 1px solid var(--el-border-color-lighter);
  background: var(--el-fill-color-blank);
}
.rule-wizard__step {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  width: 100%;
  padding: 11px 12px;
  border: 0;
  border-radius: 10px;
  background: transparent;
  text-align: left;
  cursor: pointer;
  color: var(--el-text-color-primary);
  transition: background-color .15s ease;
}
.rule-wizard__step:hover { background: var(--el-fill-color-light); }
.rule-wizard__step.is-active { background: var(--el-color-primary-light-9); }
.rule-wizard__step.is-active .rule-wizard__num {
  background: var(--el-color-primary);
  border-color: var(--el-color-primary);
  color: var(--text-on-fill);
}
.rule-wizard__step.is-done .rule-wizard__num {
  border-color: var(--el-color-success);
  color: var(--el-color-success);
}
.rule-wizard__num {
  width: 26px;
  height: 26px;
  flex: none;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--el-border-color);
  border-radius: 50%;
  font-size: 12px;
  font-weight: 600;
  line-height: 1;
}
.rule-wizard__meta { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.rule-wizard__meta b { font-size: 14px; }
.rule-wizard__meta small { font-size: 12px; color: var(--el-text-color-secondary); line-height: 1.4; }
.rule-wizard__main { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.rule-wizard__form { flex: 1; min-height: 0; display: flex; flex-direction: column; }
.rule-wizard__scroll { flex: 1; overflow-y: auto; padding: 22px 26px; max-height: 62vh; }
.rule-wizard__panel-head { margin-bottom: 18px; }
.rule-wizard__panel-head h3 { margin: 0 0 4px; font-size: 16px; }
.rule-wizard__panel-head p { margin: 0; font-size: 13px; line-height: 1.5; color: var(--el-text-color-secondary); }
.rule-review { display: grid; gap: 10px; margin: 0 0 16px; }
.rule-review__item {
  display: grid;
  grid-template-columns: 56px 1fr;
  gap: 12px;
  padding: 12px 14px;
  border-radius: 10px;
  background: var(--el-fill-color-light);
}
.rule-review dt { font-size: 12px; color: var(--el-text-color-secondary); }
.rule-review dd { margin: 0; font-size: 13px; line-height: 1.55; word-break: break-word; }
.rule-wizard__footer { display: flex; align-items: center; justify-content: space-between; gap: 12px; width: 100%; }
.rule-wizard__footer-right { display: flex; align-items: center; gap: 8px; }
.rule-wizard__mobile-head { padding: 14px 16px 2px; }
.rule-wizard__mobile-title {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  margin-bottom: 8px;
  font-size: 13px;
  color: var(--el-text-color-secondary);
}
.rule-wizard__mobile-title b { font-size: 15px; color: var(--el-text-color-primary); }

@media (max-width: 768px) {
  .rule-wizard { min-height: 100%; }
  .rule-wizard__scroll { padding: 10px 16px 24px; max-height: none; }
  .rule-wizard__panel-head { margin-bottom: 12px; }
  .rule-wizard__footer { justify-content: flex-end; }
  .rule-wizard__footer-right { display: flex; width: 100%; gap: 8px; }
  .rule-wizard__footer-right .el-button { flex: 1; margin-left: 0; min-height: 44px; }
  /* 弹窗被 teleport 到 body，scoped 选择器在个别 Element Plus 版本下匹配不到
     子组件根节点；这里用 :global 明确提升优先级，保证 44px 触控目标真的生效。 */
  :global(.rule-wizard-dialog) .rule-wizard__footer .el-button { min-height: 44px; }
  .rule-review__item { grid-template-columns: 48px 1fr; padding: 10px 12px; }
}
</style>
