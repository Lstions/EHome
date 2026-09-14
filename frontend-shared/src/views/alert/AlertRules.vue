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
            <el-switch :model-value="row.enabled" data-test="rule-enabled" @change="(v: string | number | boolean) => onToggle(asRule(row), v === true)" />
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
          <el-input v-model="form.sensor_name" placeholder="如：cell_voltage_1" data-test="field-sensor" />
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
import { edgeDeviceApi, type EdgeDevice } from '@/api/edgeDevice'
import type { AlertRule, AlertComparator, AlertLevel } from '@/api/alert'

/** el-table 作用域槽的 row 在 EP 类型里是内部 DefaultRow（未从包根导出），此处做一次命名类型的边界收窄（非 any）。 */
const asRule = (row: unknown) => row as AlertRule

const store = useAlertStore()
const devices = ref<EdgeDevice[]>([])

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
  return Number.isFinite(v) ? String(Math.round(v * 1000) / 1000) : '-'
}
function formatTime(t?: string | null): string {
  if (!t) return '-'
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

function openCreate() {
  editingId.value = null
  Object.assign(form, emptyForm())
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
    void store.fetchEvents()
  } catch {
    ElMessage.error('保存失败')
  } finally {
    saving.value = false
  }
}

async function onToggle(rule: AlertRule, enabled: boolean) {
  try {
    await store.setRuleEnabled(rule.id, enabled)
  } catch {
    ElMessage.error('切换失败')
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
    ElMessage.error('删除失败')
  }
}

async function onMarkAllRead() {
  try {
    await store.markEventsRead([])
    ElMessage.success('已全部标记')
    void store.fetchEvents()
  } catch {
    ElMessage.error('操作失败')
  }
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
  void store.setEventsPage(page)
}
/** 每页条数变化: 页码必须回到第 1 页 (原第 3 页在新页长下可能已越界)。 */
function onEventsPageSizeChange(size: number) {
  void store.setEventsPage(1, size)
}

// 规则增删会改变事件回链的规则名, 但不应重置用户所在页码 —— 只在事件总数
// 变化到当前页已越界时才回退 (由 store 的 total 驱动, 见下)。
watch(() => store.eventsTotal, total => {
  const maxPage = Math.max(1, Math.ceil(total / store.eventsPageSize))
  if (store.eventsPage > maxPage) void store.setEventsPage(maxPage)
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
/* 分页器与表格留出间距; 窄容器下允许换行 (与 AutomationRules.vue 同范式)。 */
.events-pagination { display: flex; justify-content: flex-end; margin-top: 12px; }
.events-pagination :deep(.el-pagination) { flex-wrap: wrap; }
</style>
