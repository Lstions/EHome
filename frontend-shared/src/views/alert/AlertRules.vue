<template>
  <div class="alert-rules-page">
    <PageHeader title="告警规则" subtitle="为传感器数据配置阈值规则，越限自动产生告警通知" />

    <!-- 规则表格 -->
    <section class="card rules-card">
      <div class="card-head">
        <span class="card-title">规则<el-tag v-if="store.rules.length" size="small" class="count-tag">{{ store.rules.length }}</el-tag></span>
        <el-button type="primary" :icon="Plus" data-test="create-rule" @click="openCreate">创建规则</el-button>
      </div>
      <!-- 移动端宽表：横向滚动 + 滑动提示（theme.css .mobile-table-wrapper）。
           本表 8 列合计 900px（名称 140 / 目标 120 / 传感器 120 / 阈值 140 /
           持续 80 / 级别 90 / 启用 80 / 操作 130），360px 视口下表格盒 280px。
           窄屏下「操作」列已取消 fixed（见该列上方注释），改为随表横滚，
           否则 130px 固定列（占 46.4%）会盖住行内 el-switch。 -->
      <div class="mobile-table-wrapper">
        <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
      <el-table :data="store.rules" v-loading="store.rulesLoading" data-test="rules-table">
        <el-table-column prop="name" label="名称" min-width="140" show-overflow-tooltip />
        <el-table-column label="目标" min-width="120">
          <template #default="{ row }">
            <span class="mono">{{ row.target_type === 'edge_device' ? '边缘设备' : '逻辑设备' }} #{{ row.target_id }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="sensor_name" label="传感器" min-width="120" show-overflow-tooltip />
        <el-table-column label="阈值条件" min-width="140">
          <template #default="{ row }">
            <span class="mono">{{ comparatorText(row.comparator) }} {{ row.threshold }}</span>
          </template>
        </el-table-column>
        <el-table-column label="持续" width="80">
          <template #default="{ row }">{{ row.duration_sec > 0 ? `${row.duration_sec}s` : '立即' }}</template>
        </el-table-column>
        <el-table-column label="级别" width="90">
          <template #default="{ row }">
            <el-tag :type="levelTag(row.level)" size="small">{{ levelText(row.level) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="启用" width="80">
          <template #default="{ row }">
            <!-- 可访问名带**规则名**：表格里每行都有一个一模一样的开关，
                 没有名字时屏幕阅读器只读「开关」，用户无法分辨改的是哪条规则。 -->
            <el-switch
              :model-value="row.enabled"
              :aria-label="`${asRule(row).name} 启用`"
              data-test="rule-enabled"
              @change="(v: string | number | boolean) => onToggle(asRule(row), v === true)"
            />
          </template>
        </el-table-column>
        <!-- F29 裁决：窄屏取消固定列（原 width="130" fixed="right"）。
             实测（360px，表格盒 280px，48 点 elementFromPoint 网格）：固定列 130px
             占 46.4%，行内「启用」列 el-switch 命中自身 40/48、被固定列内 DIV.cell
             吃掉 8/48。根因是粘性列的绘制顺序恒在普通列之上（见 theme.css「固定列」小节），
             与列宽无关；F26 已用真实产物实测否决收窄列宽路线（热区被折行、
             桌面行高 40px→183px）。
             改法：宽度与内容不变，只在窄屏（<768px，与 useResponsive 的 BREAKPOINTS.md
             同源）把 fixed 置 false。EP 的 table-column 对 fixed 注册了 watch 并触发
             scheduleLayout（element-plus/es/components/table/src/table-column/watcher-helper.mjs），
             运行期翻转会重算固定列集合；组件挂载时 isMobile 已是正确值，
             桌面渲染路径与改前逐字节等价（与 AutomationRules.vue:62 / DataSourceList.vue:153 同范式）。 -->
        <el-table-column label="操作" width="130" :fixed="isMobile ? false : 'right'">
          <template #default="{ row }">
            <el-button link type="primary" size="small" @click="openEdit(asRule(row))">编辑</el-button>
            <el-button link type="danger" size="small" @click="onDelete(asRule(row))">删除</el-button>
          </template>
        </el-table-column>
        <template #empty>暂无规则，点击右上角创建</template>
      </el-table>
      </div>
    </section>

    <!-- 事件时间线 -->
    <section class="card events-card">
      <div class="card-head">
        <span class="card-title">
          告警事件
          <!-- 口径: 真分页后 store.events 只装当前页, 故此处标注「本页」, 不让页内计数冒充全量 firing 数。 -->
          <el-tag v-if="store.firingCount" type="danger" size="small" class="count-tag">本页 {{ store.firingCount }} firing</el-tag>
        </span>
        <el-button link size="small" data-test="mark-read" @click="onMarkAllRead">全部标记已读</el-button>
      </div>
      <!-- 事件筛选 (G1): 后端 GET /alert-events 已支持 rule_id/state/start_time/end_time,
           改前前端 0 使用 —— 用户无法把时间线收敛到某条规则/某个状态/某个时间段。
           规则下拉用 rule.id 作 value (与后端 rule_id 同型), 时间范围传 ISO 字符串。 -->
      <div class="event-filters">
        <el-select
          v-model="filterRuleId"
          placeholder="按规则筛选"
          clearable
          size="small"
          style="width: 160px"
          data-test="event-filter-rule"
          @change="onFilterChange"
        >
          <el-option v-for="r in store.rules" :key="r.id" :label="r.name" :value="r.id" />
        </el-select>
        <el-select
          v-model="filterState"
          placeholder="按状态筛选"
          clearable
          size="small"
          style="width: 140px"
          data-test="event-filter-state"
          @change="onFilterChange"
        >
          <el-option label="触发中" value="firing" />
          <el-option label="已恢复" value="resolved" />
        </el-select>
        <el-date-picker
          v-model="filterRange"
          type="datetimerange"
          size="small"
          clearable
          range-separator="至"
          start-placeholder="开始时间"
          end-placeholder="结束时间"
          value-format="YYYY-MM-DDTHH:mm:ss"
          data-test="event-filter-range"
          @change="onFilterChange"
        />
      </div>
      <!-- 移动端宽表：横向滚动 + 滑动提示（theme.css .mobile-table-wrapper）。
           本表 5 列合计 650px（状态 100 / 规则 120 / 值 110 / 触发时间 160 /
           恢复时间 160），390px 视口下表格盒 310px ⇒ 「恢复时间」整列落在盒外。 -->
      <div class="mobile-table-wrapper">
        <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
      <el-table :data="store.events" v-loading="store.eventsLoading" data-test="events-table">
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag :type="row.state === 'firing' ? 'danger' : 'success'" size="small">{{ row.state === 'firing' ? '触发中' : '已恢复' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="规则" width="120">
          <template #default="{ row }">
            <span class="mono" data-test="event-rule-name">{{ ruleName(row.rule_id) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="值" width="110">
          <template #default="{ row }"><span class="mono">{{ formatValue(row.value) }}</span></template>
        </el-table-column>
        <el-table-column label="触发时间" min-width="160">
          <template #default="{ row }">{{ formatTime(row.fired_at) }}</template>
        </el-table-column>
        <el-table-column label="恢复时间" min-width="160">
          <template #default="{ row }">{{ formatTime(row.resolved_at) }}</template>
        </el-table-column>
        <template #empty>暂无告警事件</template>
      </el-table>
      </div>
      <!-- 分页 (真分页: 表格数据来自接口当前页, 不是本地全量切片)。
           改前本表绑 store 的全量数组 —— 后端 Limit(500) 静默截断, 前端
           el-pagination 数量为 0, 用户既看不到 total 也没有翻页入口。 -->
      <div class="events-pagination">
        <el-pagination
          v-model:current-page="eventsPage"
          v-model:page-size="eventsPageSize"
          :total="store.eventsTotal"
          :page-sizes="[20, 50, 100]"
          layout="total, sizes, prev, pager, next"
          data-test="events-pagination"
          @current-change="onEventsPageChange"
          @size-change="onEventsPageSizeChange"
        />
      </div>
    </section>

    <!-- 创建/编辑对话框 -->
    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑规则' : '创建规则'" width="520px" data-test="rule-dialog">
      <el-form :model="form" label-width="90px" data-test="rule-form">
        <el-form-item label="名称" required>
          <el-input v-model="form.name" placeholder="如：电池过压告警" data-test="field-name" />
        </el-form-item>
        <el-form-item label="目标类型" required>
          <el-select v-model="form.target_type" data-test="field-target-type">
            <el-option label="边缘设备" value="edge_device" />
            <el-option label="逻辑设备" value="logical_device" />
          </el-select>
        </el-form-item>
        <el-form-item label="目标设备" required>
          <el-select v-model="form.target_id" filterable data-test="field-target-id">
            <el-option v-for="d in devices" :key="d.id" :label="d.name" :value="d.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="传感器" required>
          <!-- G2：候选项来自目标设备已上报的类别；allow-create 保留手输（未上报时不阻塞建规则）。 -->
          <el-select
            v-model="form.sensor_name"
            filterable
            allow-create
            default-first-option
            clearable
            :loading="sensorCategoriesLoading"
            :placeholder="form.target_id ? '选择或输入传感器名' : '请先选择目标设备'"
            data-test="field-sensor"
          >
            <el-option
              v-for="c in sensorCategories"
              :key="c.code"
              :label="c.unit ? `${c.code}（${c.unit}）` : c.code"
              :value="c.code"
            />
          </el-select>
        </el-form-item>
        <el-form-item label="条件" required>
          <div class="cond-row">
            <el-select v-model="form.comparator" data-test="field-comparator">
              <el-option v-for="c in comparators" :key="c.value" :label="c.label" :value="c.value" />
            </el-select>
            <el-input-number v-model="form.threshold" :precision="3" data-test="field-threshold" />
          </div>
        </el-form-item>
        <el-form-item label="持续时长">
          <el-input-number v-model="form.duration_sec" :min="0" :step="10" data-test="field-duration" />
          <span class="hint">秒，0 = 立即触发</span>
        </el-form-item>
        <el-form-item label="静默窗口">
          <el-input-number v-model="form.silence_sec" :min="0" :step="60" data-test="field-silence" />
          <span class="hint">秒，恢复后通知静默期</span>
        </el-form-item>
        <el-form-item label="级别" required>
          <el-select v-model="form.level" data-test="field-level">
            <el-option label="信息" value="info" />
            <el-option label="警告" value="warning" />
            <el-option label="严重" value="critical" />
          </el-select>
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
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { Plus } from '@element-plus/icons-vue'
import PageHeader from '@/components/common/PageHeader.vue'
import { useAlertStore } from '@/stores/alert'
import { useResponsive } from '@/composables/useResponsive'
import { feedback } from '@/utils/feedback'
import { UNKNOWN } from '@/utils/format'
import { edgeDeviceApi, type EdgeDevice } from '@/api/edgeDevice'
import client from '@/api/client'
import { logger } from '@/utils/logger'
import type { AlertRule, AlertComparator, AlertLevel } from '@/api/alert'

/** 后端已上报的测量类别（`GET /api/v1/unified-data/categories`）。 */
interface MeasurementCategory {
  code: string
  unit: string
}

/** 与 DataPanel.vue 同一解包口径（后端可能回裸数组或 envelope.data）。 */
function unwrapList<T>(res: unknown): T[] {
  if (Array.isArray(res)) return res as T[]
  if (res && typeof res === 'object' && Array.isArray((res as { data?: unknown }).data)) {
    return (res as { data: T[] }).data
  }
  return []
}

/** el-table 作用域槽的 row 在 EP 类型里是内部 DefaultRow（未从包根导出），此处做一次命名类型的边界收窄（非 any）。 */
const asRule = (row: unknown) => row as AlertRule

const store = useAlertStore()
const devices = ref<EdgeDevice[]>([])

// ── G2：传感器名下拉（改前是手输 el-input） ──
//
// 改前 placeholder 写着「如：cell_voltage_1」，用户拼错时规则**永不触发**且没有任何反馈
// —— 这是最典型的一类静默失效：规则列表显示"已启用"，但条件永远不成立。
// 现改为按目标设备拉取后端**已上报**的类别作为候选项，同时保留 allow-create
// （设备尚未上报该类别时仍可手填，不阻塞建规则）。
//
// 端点是 `GET /api/v1/unified-data/categories?device_pk=<id>`，与 DataPanel.vue:577 同一
// 生产用法（**带 `/api/v1` 前缀**）。注意不要改用 `api/unifiedData.ts:85` 的
// `client.get('/unified-data/categories')` —— 那处缺 `/api/v1` 前缀是已登记缺陷，
// 跟着用会必然 404。
const sensorCategories = ref<MeasurementCategory[]>([])
const sensorCategoriesLoading = ref(false)
/** 已拉取过类别的设备 id，避免同一设备反复请求。 */
let loadedCategoryDeviceId: number | null = null

async function fetchSensorCategories(deviceId: number) {
  if (!deviceId || deviceId === loadedCategoryDeviceId) return
  sensorCategoriesLoading.value = true
  try {
    const response = await client.get<unknown, MeasurementCategory[]>('/api/v1/unified-data/categories', {
      params: { device_pk: deviceId },
    })
    sensorCategories.value = unwrapList<MeasurementCategory>(response)
    loadedCategoryDeviceId = deviceId
  } catch (err: unknown) {
    // 有意不弹错、不阻塞表单：拿不到候选就退化为「只能手输」（allow-create 仍在），
    // 若在此弹错误会打断用户填写。但也不空吞——留一条 warn 便于排查。
    sensorCategories.value = []
    loadedCategoryDeviceId = null
    logger.warn('告警规则：传感器候选类别加载失败，退化为手工输入', { error: String(err), deviceId })
  } finally {
    sensorCategoriesLoading.value = false
  }
}

/** 目标设备变化 ⇒ 换一份候选类别；清掉不再适用的旧值避免"设备 A 的传感器 + 设备 B"错配。 */
function watchTargetDeviceForCategories() {
  watch(() => form.target_id, (id) => {
    sensorCategories.value = []
    loadedCategoryDeviceId = null
    if (id) void fetchSensorCategories(Number(id))
  })
}

// isMobile（<768px）用于窄屏取消「操作」列的固定（F29），断点与 theme.css 一致。
// 事件表 5 列无 fixed 列，不涉及；只有规则表的「操作」列需要翻转。
const { isMobile } = useResponsive()

const comparators: Array<{ value: AlertComparator; label: string }> = [
  { value: 'gt', label: '>' },
  { value: 'gte', label: '≥' },
  { value: 'lt', label: '<' },
  { value: 'lte', label: '≤' },
  { value: 'eq', label: '=' },
  { value: 'neq', label: '≠' },
]

/** 事件只持久化 rule_id：回链规则名展示，规则已被删除时回退 #id（不留空白）。 */
function ruleName(ruleId: number): string {
  const hit = store.rules.find(r => r.id === ruleId)
  return hit ? hit.name : `#${ruleId}`
}
function comparatorText(c: AlertComparator): string {
  return comparators.find(x => x.value === c)?.label ?? c
}
function levelText(l: AlertLevel): string {
  return l === 'critical' ? '严重' : l === 'warning' ? '警告' : '信息'
}
function levelTag(l: AlertLevel): 'danger' | 'warning' | 'info' {
  return l === 'critical' ? 'danger' : l === 'warning' ? 'warning' : 'info'
}
function formatValue(v: number): string {
  return Number.isFinite(v) ? String(Math.round(v * 1000) / 1000) : UNKNOWN
}
function formatTime(t?: string | null): string {
  if (!t) return UNKNOWN
  return new Date(t).toLocaleString()
}

// ── 对话框 ──
const dialogVisible = ref(false)
const saving = ref(false)
const editingId = ref<number | null>(null)
const emptyForm = () => ({
  name: '',
  target_type: 'edge_device' as const,
  target_id: 0,
  sensor_name: '',
  comparator: 'gt' as AlertComparator,
  threshold: 0,
  duration_sec: 0,
  silence_sec: 300,
  level: 'warning' as AlertLevel,
  enabled: true,
})
const form = reactive(emptyForm())

// G2：必须在 `form` 声明**之后**注册 —— 否则 watch 的 getter 会命中 TDZ
// （实测：放在 form 之前会在挂载时抛 "Cannot access 'form' before initialization"）。
watchTargetDeviceForCategories()

function openCreate() {
  editingId.value = null
  Object.assign(form, emptyForm())
  // G2：清掉上一轮候选（watch 只在 target_id **变化**时触发，新建时 id 可能恰好同值）。
  sensorCategories.value = []
  loadedCategoryDeviceId = null
  dialogVisible.value = true
}
function openEdit(rule: AlertRule) {
  editingId.value = rule.id
  Object.assign(form, {
    name: rule.name,
    target_type: rule.target_type,
    target_id: rule.target_id,
    sensor_name: rule.sensor_name,
    comparator: rule.comparator,
    threshold: rule.threshold,
    duration_sec: rule.duration_sec,
    silence_sec: rule.silence_sec,
    level: rule.level,
    enabled: rule.enabled,
  })
  // G2：编辑已有规则时也要有候选（watch 不会触发：target_id 赋值前已是同值或被 Object.assign 一次性写入）。
  sensorCategories.value = []
  loadedCategoryDeviceId = null
  if (rule.target_id) void fetchSensorCategories(Number(rule.target_id))
  dialogVisible.value = true
}

function validate(): string | null {
  if (!form.name.trim()) return '请填写规则名称'
  if (!form.target_id) return '请选择目标设备'
  if (!form.sensor_name.trim()) return '请填写传感器名称'
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
    const req = {
      name: form.name.trim(),
      target_type: form.target_type,
      target_id: form.target_id,
      sensor_name: form.sensor_name.trim(),
      comparator: form.comparator,
      threshold: form.threshold,
      duration_sec: form.duration_sec,
      silence_sec: form.silence_sec,
      level: form.level,
      enabled: form.enabled,
    }
    if (editingId.value) {
      await store.updateRule(editingId.value, req)
      ElMessage.success('规则已更新')
    } else {
      await store.createRule(req)
      ElMessage.success('规则已创建')
    }
    dialogVisible.value = false
    // 规则增删只影响事件回链的规则名, 不改筛选条件 —— 刷新时带上当前筛选与页码。
    void loadEvents(store.eventsPage, store.eventsPageSize)
  } catch {
    feedback.error('保存失败')
  } finally {
    saving.value = false
  }
}

async function onToggle(rule: AlertRule, enabled: boolean) {
  try {
    await store.setRuleEnabled(rule.id, enabled)
  } catch {
    feedback.error('切换失败')
  }
}

async function onDelete(rule: AlertRule) {
  // 破坏性确认必须走 feedback.confirmDanger (§3.4.3/§4.3.4):
  // 它给确认键 confirmButtonType='danger' 且 autofocus=false —— 改前直接调
  // ElMessageBox.confirm 时按钮是 primary 样式, 且默认焦点就落在「确定」上,
  // 构成"回车即删除"的诱导性确认。取消时 confirmDanger 返回 false, 必须直接
  // 返回, 不得继续执行删除。
  const ok = await feedback.confirmDanger(`删除规则「${rule.name}」？`, {
    title: '确认删除',
    confirmText: '删除',
    cancelText: '取消',
  })
  if (!ok) return
  try {
    await store.deleteRule(rule.id)
    ElMessage.success('已删除')
  } catch {
    feedback.error('删除失败')
  }
}

async function onMarkAllRead() {
  try {
    // U9: 必须走 markAllEventsRead (发 {all:true})。改前调 markEventsRead([])
    // 发的是空 ids, 后端在 !all && len(ids)==0 时判 400 —— 该按钮必然失败。
    await store.markAllEventsRead()
    ElMessage.success('已全部标记')
    // 刷新时保留当前筛选与页码 (与列表取数走同一入口), 否则"全部已读"后筛选会静默失效。
    void loadEvents(store.eventsPage, store.eventsPageSize)
  } catch {
    feedback.error('操作失败')
  }
}

// ── 事件筛选 (G1) ──
/** 规则筛选值 (后端 rule_id)。 */
const filterRuleId = ref<number | undefined>(undefined)
/** 状态筛选值 (后端 state)。 */
const filterState = ref<'firing' | 'resolved' | undefined>(undefined)
/** 时间范围筛选值 (el-date-picker datetimerange, value-format 直接给 ISO 字符串)。 */
const filterRange = ref<[string, string] | null>(null)

/**
 * 当前筛选值打包成 store.fetchEvents 的参数。
 * 清空 (clearable) 后对应字段为 undefined ⇒ 不携带该参数, 后端按"不过滤"处理。
 */
function currentFilterParams() {
  return {
    ...(filterRuleId.value ? { rule_id: filterRuleId.value } : {}),
    ...(filterState.value ? { state: filterState.value } : {}),
    ...(filterRange.value?.[0] ? { start_time: filterRange.value[0] } : {}),
    ...(filterRange.value?.[1] ? { end_time: filterRange.value[1] } : {}),
  }
}

/**
 * 统一的取数入口: 一次性下发「页码 + 页长 + 当前筛选」。
 *
 * 为什么视图层统一走这里而不是直接 store.setEventsPage(page):
 * 本页筛选值由视图持有, 而 setEventsPage 只带 page/page_size —— 翻页/改页长/刷新
 * 都会把筛选悄悄丢掉, 用户看到的是"筛选突然失效"。这里把筛选与分页一起下发,
 * 且**只发一个请求** (store.fetchEvents 会用后端回显反写 eventsPage/eventsPageSize)。
 */
function loadEvents(page: number, pageSize: number) {
  return store.fetchEvents({ page, page_size: pageSize, ...currentFilterParams() })
}

/**
 * 筛选变化 (§3.2.6 MUST): 会改变查询范围的输入变化时必须重置页码到第 1 页。
 * 不重置的话, 用户停在第 3 页时筛选后可能已越界, 页面显示"空列表"而不是筛选结果。
 */
function onFilterChange() {
  void loadEvents(1, store.eventsPageSize)
}

// ── 事件分页 (§3.2.6 MUST: 分页状态由 store 持有, 视图只驱动) ──
const eventsPage = computed({
  get: () => store.eventsPage,
  set: v => { store.eventsPage = v },
})
const eventsPageSize = computed({
  get: () => store.eventsPageSize,
  set: v => { store.eventsPageSize = v },
})
/** 翻页: 页码变化即重查 (数据源是服务端当前页, 不是本地切片)。 */
function onEventsPageChange(page: number) {
  void loadEvents(page, store.eventsPageSize)
}
/** 每页条数变化: 页码必须回到第 1 页 (原第 3 页在新页长下可能已越界)。 */
function onEventsPageSizeChange(size: number) {
  void loadEvents(1, size)
}

// 规则增删会改变事件回链的规则名, 但不应重置用户所在页码 —— 只在事件总数
// 变化到当前页已越界时才回退 (由 store 的 total 驱动, 见下)。
watch(() => store.eventsTotal, total => {
  const maxPage = Math.max(1, Math.ceil(total / store.eventsPageSize))
  // 回退页码时也必须带上筛选, 否则"越界回退"会顺手把筛选清掉。
  if (store.eventsPage > maxPage) void loadEvents(maxPage, store.eventsPageSize)
})

onMounted(async () => {
  await Promise.all([store.fetchRules(), store.fetchEvents({ page: 1 })])
  try {
    const list = await edgeDeviceApi.getList()
    devices.value = list.items
  } catch {
    /* 设备下拉加载失败不阻塞页面 */
  }
})
</script>

<style scoped>
.alert-rules-page { display: flex; flex-direction: column; gap: 16px; }
.card { background: var(--el-bg-color); border-radius: 8px; padding: 16px; }
.card-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 12px; }
.card-title { font-size: 15px; font-weight: 600; }
.count-tag { margin-left: 8px; }
.mono { font-family: monospace; }
.cond-row { display: flex; gap: 8px; align-items: center; }
.hint { margin-left: 8px; color: var(--el-text-color-secondary); font-size: 12px; }
/* 事件筛选条 (G1): 窄容器下允许换行 (与 AutomationRules.vue 的 .event-filters 同范式)。 */
.event-filters { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; margin-bottom: 12px; }
/* 分页器与表格留出间距; 窄容器下允许换行 (与 AutomationRules.vue 同范式)。 */
.events-pagination { display: flex; justify-content: flex-end; margin-top: 12px; }
.events-pagination :deep(.el-pagination) { flex-wrap: wrap; }
</style>
