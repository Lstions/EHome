<template>
  <div class="automation-rules-page">
    <PageHeader title="自动化策略" description="传感器数据或时间窗口触发设备动作或通知" />

    <!-- 规则表格 -->
    <section class="card rules-card">
      <div class="card-head">
        <span class="card-title">规则<el-tag v-if="rules.length" size="small" class="count-tag">{{ rules.length }}</el-tag></span>
        <el-button type="primary" :icon="Plus" data-test="create-rule" @click="openCreate">创建规则</el-button>
      </div>
      <!-- 移动端宽表：横向滚动 + 滑动提示（theme.css .mobile-table-wrapper） -->
      <div class="mobile-table-wrapper">
        <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
      <el-table :data="rules" v-loading="rulesLoading" data-test="rules-table">
        <el-table-column prop="name" label="名称" min-width="140" show-overflow-tooltip />
        <el-table-column label="触发器" min-width="180">
          <template #default="{ row }">
            <div class="trigger-cell">
              <el-tag size="small" class="trigger-type-tag">{{ triggerTypeText(row.trigger_type) }}</el-tag>
              <span class="mono trigger-summary">{{ triggerSummary(asRule(row)) }}</span>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="动作" min-width="140">
          <template #default="{ row }">
            <div class="action-cell">
              <el-tag size="small" :type="row.action_type === 'device_action' ? 'warning' : 'info'">
                {{ row.action_type === 'device_action' ? '设备动作' : '通知' }}
              </el-tag>
              <span class="mono action-summary">{{ actionSummary(asRule(row)) }}</span>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="冷却/熔断" width="120">
          <template #default="{ row }">
            <span class="mono">{{ row.cooldown_sec }}s / {{ row.max_daily_exec === 0 ? '不限' : row.max_daily_exec }}</span>
          </template>
        </el-table-column>
        <el-table-column label="确认制" width="90">
          <template #default="{ row }">
            <el-tag v-if="row.require_confirmed" type="warning" size="small">需确认</el-tag>
            <span v-else class="text-muted">-</span>
          </template>
        </el-table-column>
        <el-table-column label="启用" width="80">
          <template #default="{ row }">
            <el-switch :model-value="row.enabled" data-test="rule-enabled" @change="(v: string | number | boolean) => onToggle(asRule(row), v === true)" />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="180" fixed="right">
          <template #default="{ row }">
            <el-button link type="warning" size="small" :loading="triggeringId === row.id" data-test="trigger-rule" @click="onTrigger(asRule(row))">触发</el-button>
            <el-button link type="primary" size="small" @click="openEdit(asRule(row))">编辑</el-button>
            <el-button link type="danger" size="small" @click="onDelete(asRule(row))">删除</el-button>
          </template>
        </el-table-column>
        <template #empty>暂无规则，点击右上角创建</template>
      </el-table>
      </div>
    </section>

    <!-- 触发历史 -->
    <section class="card events-card">
      <div class="card-head">
        <span class="card-title">触发历史<el-tag v-if="events.length" size="small" class="count-tag">{{ events.length }}</el-tag></span>
        <div class="event-filters">
          <el-select v-model="eventFilterRuleId" placeholder="按规则过滤" clearable size="small" style="width: 140px" data-test="filter-rule" @change="fetchEvents">
            <el-option v-for="r in rules" :key="r.id" :label="r.name" :value="r.id" />
          </el-select>
          <el-select v-model="eventFilterResult" placeholder="按结果过滤" clearable size="small" style="width: 140px" data-test="filter-result" @change="fetchEvents">
            <el-option v-for="opt in resultOptions" :key="opt.value" :label="opt.label" :value="opt.value" />
          </el-select>
          <el-button link size="small" data-test="refresh-events" @click="fetchEvents">刷新</el-button>
        </div>
      </div>
      <!-- 移动端宽表：横向滚动 + 滑动提示（theme.css .mobile-table-wrapper） -->
      <div class="mobile-table-wrapper">
        <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
      <el-table :data="events" v-loading="eventsLoading" data-test="events-table">
        <el-table-column label="时间" min-width="160">
          <template #default="{ row }">{{ formatTime(row.triggered_at) }}</template>
        </el-table-column>
        <el-table-column label="规则" width="120">
          <template #default="{ row }">
            <span class="mono" data-test="event-rule-name">{{ ruleName(row.rule_id) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="结果" width="110">
          <template #default="{ row }">
            <el-tag :type="resultTagType(row.result)" size="small">{{ resultText(row.result) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="触发值" width="100">
          <template #default="{ row }">
            <span v-if="row.trigger_value !== undefined" class="mono">{{ formatValue(row.trigger_value) }}</span>
            <span v-else class="text-muted">-</span>
          </template>
        </el-table-column>
        <el-table-column label="来源" width="70">
          <template #default="{ row }">
            <el-tag v-if="row.trigger_source === 'manual'" type="warning" size="small">手动</el-tag>
            <span v-else class="text-muted">自动</span>
          </template>
        </el-table-column>
        <el-table-column label="命令 ID" width="110">
          <template #default="{ row }">
            <el-button v-if="row.command_id" link type="primary" size="small" @click="goCommand(row.command_id)">
              {{ row.command_id }}
            </el-button>
            <span v-else class="text-muted">-</span>
          </template>
        </el-table-column>
        <el-table-column prop="detail" label="详情" min-width="140" show-overflow-tooltip />
        <el-table-column label="操作" width="100" fixed="right">
          <template #default="{ row }">
            <el-button
              v-if="row.result === 'pending_confirm'"
              link
              type="warning"
              size="small"
              data-test="confirm-event"
              @click="onConfirmEvent(asEvent(row))"
            >
              确认执行
            </el-button>
          </template>
        </el-table-column>
        <template #empty>暂无触发事件</template>
      </el-table>
      </div>
      <!-- 分页 (真分页: 表格数据来自接口当前页, 不是本地全量切片)。
           改前本表一次性渲染后端 Limit(500) 的全部行 (实测 502 行/11902 节点,
           真实事件 >1006 条被静默截断), fixed 列为每行各注入一份 inline style。 -->
      <div class="events-pagination">
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
    </section>

    <!-- 创建/编辑对话框 -->
    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑规则' : '创建规则'" width="640px" data-test="rule-dialog">
      <el-form :model="form" label-width="110px" data-test="rule-form">
        <el-form-item label="名称" required>
          <el-input v-model="form.name" placeholder="如：高温自动开窗" data-test="field-name" />
        </el-form-item>

        <el-form-item label="触发器类型" required>
          <el-select v-model="form.trigger_type" data-test="field-trigger-type" @change="onTriggerTypeChange">
            <el-option label="传感器阈值" value="sensor_threshold" />
            <el-option label="时间窗口" value="time_window" />
            <el-option label="事件驱动（暂不支持创建）" value="event" disabled />
          </el-select>
        </el-form-item>

        <!-- sensor_threshold 字段组 -->
        <template v-if="form.trigger_type === 'sensor_threshold'">
          <el-form-item label="边缘设备" required>
            <el-select v-model="form.trigger_edge_device_id" filterable data-test="field-trigger-device">
              <el-option v-for="d in devices" :key="d.id" :label="d.name" :value="d.id" />
            </el-select>
          </el-form-item>
          <el-form-item label="传感器" required>
            <el-input v-model="form.trigger_sensor_name" placeholder="如：temperature" data-test="field-sensor" />
          </el-form-item>
          <el-form-item label="条件" required>
            <div class="cond-row">
              <el-select v-model="form.trigger_comparator" data-test="field-comparator" style="width: 100px">
                <el-option v-for="c in comparators" :key="c.value" :label="c.label" :value="c.value" />
              </el-select>
              <el-input-number v-model="form.trigger_threshold" :precision="3" data-test="field-threshold" />
            </div>
          </el-form-item>
          <el-form-item label="持续时长">
            <el-input-number v-model="form.trigger_duration_sec" :min="0" :step="10" data-test="field-duration" />
            <span class="hint">秒，0 = 立即触发</span>
          </el-form-item>
        </template>

        <!-- time_window 字段组 -->
        <template v-if="form.trigger_type === 'time_window'">
          <el-form-item label="开始时间" required>
            <el-input v-model="form.trigger_window_start" placeholder="HH:MM，如 08:00" data-test="field-window-start" />
          </el-form-item>
          <el-form-item label="结束时间" required>
            <el-input v-model="form.trigger_window_end" placeholder="HH:MM，如 18:00" data-test="field-window-end" />
          </el-form-item>
          <el-form-item label="触发沿" required>
            <el-select v-model="form.trigger_window_edge" data-test="field-window-edge">
              <el-option label="进入窗口" value="enter" />
              <el-option label="离开窗口" value="exit" />
              <el-option label="窗口内持续" value="inside" />
            </el-select>
          </el-form-item>
        </template>

        <el-form-item label="附加条件">
          <el-input
            v-model="form.conditions_json"
            type="textarea"
            :rows="2"
            placeholder='JSON 数组，如 [{"sensor":"humidity","comparator":"lt","threshold":30}]'
            data-test="field-conditions"
          />
        </el-form-item>

        <el-form-item label="动作类型" required>
          <el-select v-model="form.action_type" data-test="field-action-type" @change="onActionTypeChange">
            <el-option label="设备动作" value="device_action" />
            <el-option label="通知" value="notification" />
          </el-select>
        </el-form-item>

        <!-- device_action 字段组 -->
        <template v-if="form.action_type === 'device_action'">
          <el-form-item label="目标设备" required>
            <el-select v-model="form.action_device_id" filterable data-test="field-action-device">
              <el-option v-for="d in devices" :key="d.id" :label="d.name" :value="d.id" />
            </el-select>
          </el-form-item>
          <el-form-item label="动作 ID" required>
            <el-input v-model="form.action_id" placeholder="如：gpio_set / pwm_set_duty" data-test="field-action-id" />
          </el-form-item>
          <el-form-item label="动作参数">
            <el-input
              v-model="form.action_params_json"
              type="textarea"
              :rows="2"
              placeholder='JSON 对象，如 {"pin": 12, "value": 1}'
              data-test="field-action-params"
            />
          </el-form-item>
          <el-form-item label="需人工确认">
            <el-switch v-model="form.require_confirmed" data-test="field-require-confirmed" />
          </el-form-item>
        </template>

        <!-- notification 字段组 -->
        <template v-if="form.action_type === 'notification'">
          <el-form-item label="通知级别" required>
            <el-select v-model="form.action_level" data-test="field-action-level">
              <el-option label="信息" value="info" />
              <el-option label="警告" value="warning" />
              <el-option label="严重" value="critical" />
            </el-select>
          </el-form-item>
        </template>

        <el-form-item label="冷却时间">
          <el-input-number v-model="form.cooldown_sec" :min="0" :max="86400" :step="60" data-test="field-cooldown" />
          <span class="hint">秒，0-86400</span>
        </el-form-item>
        <el-form-item label="每日上限">
          <el-input-number v-model="form.max_daily_exec" :min="0" :max="1000" data-test="field-max-daily" />
          <span class="hint">0 = 不限，最大 1000</span>
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" data-test="field-enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" data-test="save-rule" @click="onSave">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import feedback from '@/utils/feedback'
import { Plus } from '@element-plus/icons-vue'
import PageHeader from '@/components/common/PageHeader.vue'
import { edgeDeviceApi, type EdgeDevice } from '@/api/edgeDevice'
import {
  automationApi,
  type AutomationRule,
  type AutomationEvent,
  type AutomationEventResult,
  type AutomationComparator,
  type AutomationTriggerType,
  type AutomationActionType,
  type AutomationActionLevel,
  type CreateAutomationRuleRequest,
  type UpdateAutomationRuleRequest,
} from '@/api/automation'

/** el-table 作用域槽的 row 在 EP 类型里是内部 DefaultRow（未从包根导出），此处做一次命名类型的边界收窄（非 any）。 */
const asRule = (row: unknown) => row as AutomationRule
const asEvent = (row: unknown) => row as AutomationEvent

const devices = ref<EdgeDevice[]>([])

// ── 规则 ──
const rules = ref<AutomationRule[]>([])
const rulesLoading = ref(false)

async function fetchRules() {
  rulesLoading.value = true
  try {
    rules.value = await automationApi.listRules()
  } catch {
    ElMessage.error('加载规则失败')
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

const resultOptions: Array<{ value: AutomationEventResult; label: string }> = [
  { value: 'executed', label: '已执行' },
  { value: 'pending_confirm', label: '待确认' },
  { value: 'suppressed_cooldown', label: '冷却抑制' },
  { value: 'suppressed_daily_limit', label: '达每日上限' },
  { value: 'condition_changed', label: '条件已变' },
  { value: 'failed_gate', label: '门禁失败' },
  { value: 'failed_dispatch', label: '下发失败' },
  { value: 'notification', label: '已通知' },
  { value: 'expired', label: '已过期' },
]

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
    ElMessage.error('加载事件失败')
  } finally {
    eventsLoading.value = false
  }
}

/** 每页条数变化: 页码必须回到第 1 页 (原第 3 页在新页长下可能已越界)。 */
function onEventsPageSizeChange() {
  eventsPage.value = 1
  void fetchEvents()
}

// ── 对话框 ──
const dialogVisible = ref(false)
const saving = ref(false)
const editingId = ref<number | null>(null)

const comparators: Array<{ value: AutomationComparator; label: string }> = [
  { value: 'gt', label: '>' },
  { value: 'gte', label: '≥' },
  { value: 'lt', label: '<' },
  { value: 'lte', label: '≤' },
  { value: 'eq', label: '=' },
  { value: 'neq', label: '≠' },
]

function emptyForm() {
  return {
    name: '',
    trigger_type: 'sensor_threshold' as AutomationTriggerType,
    trigger_sensor_name: '',
    trigger_comparator: 'gt' as AutomationComparator,
    trigger_threshold: 0,
    trigger_duration_sec: 0,
    trigger_window_start: '',
    trigger_window_end: '',
    trigger_window_edge: 'enter' as const,
    trigger_edge_device_id: 0,
    conditions_json: '',
    action_type: 'device_action' as AutomationActionType,
    action_device_id: 0,
    action_id: '',
    action_params_json: '',
    action_level: 'warning' as AutomationActionLevel,
    cooldown_sec: 300,
    max_daily_exec: 0,
    require_confirmed: false,
    enabled: true,
  }
}

const form = reactive(emptyForm())

function openCreate() {
  editingId.value = null
  Object.assign(form, emptyForm())
  dialogVisible.value = true
}

function openEdit(rule: AutomationRule) {
  editingId.value = rule.id
  Object.assign(form, {
    name: rule.name,
    trigger_type: rule.trigger_type,
    trigger_sensor_name: rule.trigger_sensor_name ?? '',
    trigger_comparator: rule.trigger_comparator ?? 'gt',
    trigger_threshold: rule.trigger_threshold ?? 0,
    trigger_duration_sec: rule.trigger_duration_sec ?? 0,
    trigger_window_start: rule.trigger_window_start ?? '',
    trigger_window_end: rule.trigger_window_end ?? '',
    trigger_window_edge: rule.trigger_window_edge ?? 'enter',
    trigger_edge_device_id: rule.trigger_edge_device_id ?? 0,
    conditions_json: rule.conditions_json ?? '',
    action_type: rule.action_type,
    action_device_id: rule.action_device_id ?? 0,
    action_id: rule.action_id ?? '',
    action_params_json: rule.action_params_json ?? '',
    action_level: rule.action_level ?? 'warning',
    cooldown_sec: rule.cooldown_sec,
    max_daily_exec: rule.max_daily_exec,
    require_confirmed: rule.require_confirmed,
    enabled: rule.enabled,
  })
  dialogVisible.value = true
}

function onTriggerTypeChange() {
  // 切换触发器类型时清空互斥字段
  if (form.trigger_type === 'sensor_threshold') {
    form.trigger_window_start = ''
    form.trigger_window_end = ''
  } else if (form.trigger_type === 'time_window') {
    form.trigger_sensor_name = ''
    form.trigger_threshold = 0
    form.trigger_duration_sec = 0
  }
}

function onActionTypeChange() {
  if (form.action_type === 'notification') {
    form.action_device_id = 0
    form.action_id = ''
    form.action_params_json = ''
    form.require_confirmed = false
  } else {
    form.action_level = 'warning'
  }
}

function validate(): string | null {
  if (!form.name.trim()) return '请填写规则名称'
  if (form.trigger_type === 'sensor_threshold') {
    if (!form.trigger_edge_device_id) return '请选择边缘设备'
    if (!form.trigger_sensor_name.trim()) return '请填写传感器名称'
  } else if (form.trigger_type === 'time_window') {
    if (!form.trigger_window_start.trim()) return '请填写开始时间'
    if (!form.trigger_window_end.trim()) return '请填写结束时间'
  }
  if (form.action_type === 'device_action') {
    if (!form.action_device_id) return '请选择目标设备'
    if (!form.action_id.trim()) return '请填写动作 ID'
  }
  // JSON 校验
  if (form.conditions_json.trim()) {
    try {
      const parsed = JSON.parse(form.conditions_json)
      if (!Array.isArray(parsed)) return '附加条件必须是 JSON 数组'
    } catch {
      return '附加条件 JSON 格式错误'
    }
  }
  if (form.action_type === 'device_action' && form.action_params_json.trim()) {
    try {
      JSON.parse(form.action_params_json)
    } catch {
      return '动作参数 JSON 格式错误'
    }
  }
  if (form.cooldown_sec < 0 || form.cooldown_sec > 86400) return '冷却时间必须在 0-86400 秒之间'
  if (form.max_daily_exec < 0 || form.max_daily_exec > 1000) return '每日上限必须在 0-1000 之间'
  return null
}

async function onSave() {
  const err = validate()
  if (err) {
    ElMessage.warning(err)
    return
  }
  saving.value = true
  try {
    const base = {
      name: form.name.trim(),
      trigger_type: form.trigger_type,
      conditions_json: form.conditions_json.trim() || undefined,
      action_type: form.action_type,
      cooldown_sec: form.cooldown_sec,
      max_daily_exec: form.max_daily_exec,
      require_confirmed: form.require_confirmed,
      enabled: form.enabled,
    }
    if (form.trigger_type === 'sensor_threshold') {
      Object.assign(base, {
        trigger_sensor_name: form.trigger_sensor_name.trim(),
        trigger_comparator: form.trigger_comparator,
        trigger_threshold: form.trigger_threshold,
        trigger_duration_sec: form.trigger_duration_sec,
        trigger_edge_device_id: form.trigger_edge_device_id,
      })
    } else if (form.trigger_type === 'time_window') {
      Object.assign(base, {
        trigger_window_start: form.trigger_window_start.trim(),
        trigger_window_end: form.trigger_window_end.trim(),
        trigger_window_edge: form.trigger_window_edge,
      })
    }
    if (form.action_type === 'device_action') {
      Object.assign(base, {
        action_device_id: form.action_device_id,
        action_id: form.action_id.trim(),
        action_params_json: form.action_params_json.trim() || undefined,
      })
    } else {
      Object.assign(base, {
        action_level: form.action_level,
      })
    }

    if (editingId.value) {
      await automationApi.updateRule(editingId.value, base as UpdateAutomationRuleRequest)
      ElMessage.success('规则已更新')
    } else {
      await automationApi.createRule(base as CreateAutomationRuleRequest)
      ElMessage.success('规则已创建')
    }
    dialogVisible.value = false
    void fetchRules()
    void fetchEvents()
  } catch {
    ElMessage.error('保存失败')
  } finally {
    saving.value = false
  }
}

async function onToggle(rule: AutomationRule, enabled: boolean) {
  try {
    await automationApi.setRuleEnabled(rule.id, enabled)
    rule.enabled = enabled
  } catch {
    ElMessage.error('切换失败')
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
  try {
    await ElMessageBox.confirm(
      `手动触发规则「${rule.name}」？将跳过条件评估与确认制直接执行动作 (cooldown/日熔断仍生效)。`,
      '手动触发',
      { type: 'warning', confirmButtonText: '触发', cancelButtonText: '取消' },
    )
  } catch {
    return // 用户取消
  }
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
    const err = e as { response?: { data?: { message?: string } } }
    ElMessage.error(err?.response?.data?.message ?? '触发失败')
  } finally {
    triggeringId.value = null
  }
}

function goCommand(commandId: string) {
  // TODO: 跳转命令详情页（当前无路由，先留占位）
  console.log('command id:', commandId)
}

// ── 显示辅助 ──
/** 事件只持久化 rule_id：回链规则名展示，规则已被删除时回退 #id（不留空白）。 */
function ruleName(ruleId: number): string {
  const hit = rules.value.find(r => r.id === ruleId)
  return hit ? hit.name : `#${ruleId}`
}
function triggerTypeText(t: AutomationTriggerType): string {
  return t === 'sensor_threshold' ? '传感器阈值' : t === 'time_window' ? '时间窗口' : '事件驱动'
}
function triggerSummary(rule: AutomationRule): string {
  if (rule.trigger_type === 'sensor_threshold') {
    const cmp = comparators.find(c => c.value === rule.trigger_comparator)?.label ?? rule.trigger_comparator
    return `${rule.trigger_sensor_name} ${cmp} ${rule.trigger_threshold}`
  }
  if (rule.trigger_type === 'time_window') {
    const edge = rule.trigger_window_edge === 'enter' ? '进入' : rule.trigger_window_edge === 'exit' ? '离开' : '持续'
    return `${rule.trigger_window_start}-${rule.trigger_window_end} ${edge}`
  }
  return '-'
}
function actionSummary(rule: AutomationRule): string {
  if (rule.action_type === 'device_action') {
    return rule.action_id ?? '-'
  }
  return rule.action_level === 'critical' ? '严重' : rule.action_level === 'warning' ? '警告' : '信息'
}
function resultText(r: AutomationEventResult): string {
  const map: Record<AutomationEventResult, string> = {
    executed: '已执行',
    pending_confirm: '待确认',
    suppressed_cooldown: '冷却抑制',
    suppressed_daily_limit: '达每日上限',
    condition_changed: '条件已变',
    failed_gate: '门禁失败',
    failed_dispatch: '下发失败',
    notification: '已通知',
    expired: '已过期',
  }
  return map[r] ?? r
}
function resultTagType(r: AutomationEventResult): 'success' | 'warning' | 'danger' | 'info' {
  if (r === 'executed' || r === 'notification') return 'success'
  if (r === 'pending_confirm') return 'warning'
  if (r.startsWith('failed_') || r === 'expired') return 'danger'
  return 'info'
}
function formatValue(v: number): string {
  return Number.isFinite(v) ? String(Math.round(v * 1000) / 1000) : '-'
}
function formatTime(t?: string | null): string {
  if (!t) return '-'
  return new Date(t).toLocaleString()
}

onMounted(async () => {
  await Promise.all([fetchRules(), fetchEvents()])
  try {
    const list = await edgeDeviceApi.getList()
    devices.value = list.items
  } catch {
    /* 设备下拉加载失败不阻塞页面 */
  }
})
</script>

<style scoped>
.automation-rules-page { display: flex; flex-direction: column; gap: 16px; }
.card { background: var(--el-bg-color); border-radius: 8px; padding: 16px; }
.card-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 12px; }
.card-title { font-size: 15px; font-weight: 600; }
.count-tag { margin-left: 8px; }
.mono { font-family: monospace; }
.cond-row { display: flex; gap: 8px; align-items: center; }
.hint { margin-left: 8px; color: var(--el-text-color-secondary); font-size: 12px; }
.text-muted { color: var(--el-text-color-secondary); }
.trigger-cell, .action-cell { display: flex; align-items: center; gap: 6px; }
.trigger-type-tag { flex-shrink: 0; }
.trigger-summary, .action-summary { font-size: 12px; }
.event-filters { display: flex; gap: 8px; align-items: center; }
.events-pagination { display: flex; justify-content: flex-end; margin-top: 12px; flex-wrap: wrap; }
/* 分页控件换行（窄屏真实裁切修复，与 LogicalDeviceList.vue 同根因）。
   .events-pagination 的 flex-wrap 只管多个 item 之间；内层 el-pagination 自身
   white-space:nowrap + display:flex 且宽 676.94px，在 768px（.events-pagination 宽 488px）
   就已把「共 N 条」推出容器左界 153px —— 实测 el-pagination x=47.06 < 容器 x=236，
   祖先链 scrollWidth === clientWidth（不可回滚）⇒ 真实裁切。
   同仓范式：firmware/FirmwareManage.vue 的 .firmware-manage :deep(.el-pagination)。 */
.events-pagination :deep(.el-pagination) { flex-wrap: wrap; }
</style>
