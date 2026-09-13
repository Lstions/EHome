<template>
  <div class="alert-rules-page">
    <PageHeader title="告警规则" description="为传感器数据配置阈值规则，越限自动产生告警通知" />

    <!-- 规则表格 -->
    <section class="card rules-card">
      <div class="card-head">
        <span class="card-title">规则<el-tag v-if="store.rules.length" size="small" class="count-tag">{{ store.rules.length }}</el-tag></span>
        <el-button type="primary" :icon="Plus" data-test="create-rule" @click="openCreate">创建规则</el-button>
      </div>
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
        <el-table-column label="操作" width="130" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" size="small" @click="openEdit(asRule(row))">编辑</el-button>
            <el-button link type="danger" size="small" @click="onDelete(asRule(row))">删除</el-button>
          </template>
        </el-table-column>
        <template #empty>暂无规则，点击右上角创建</template>
      </el-table>
    </section>

    <!-- 事件时间线 -->
    <section class="card events-card">
      <div class="card-head">
        <span class="card-title">告警事件<el-tag v-if="store.firingCount" type="danger" size="small" class="count-tag">{{ store.firingCount }} firing</el-tag></span>
        <el-button link size="small" data-test="mark-read" @click="onMarkAllRead">全部标记已读</el-button>
      </div>
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
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus } from '@element-plus/icons-vue'
import PageHeader from '@/components/common/PageHeader.vue'
import { useAlertStore } from '@/stores/alert'
import { edgeDeviceApi, type EdgeDevice } from '@/api/edgeDevice'
import type { AlertRule, AlertComparator, AlertLevel } from '@/api/alert'

/** el-table 作用域槽的 row 在 EP 类型里是内部 DefaultRow（未从包根导出），此处做一次命名类型的边界收窄（非 any）。 */
const asRule = (row: unknown) => row as AlertRule

const store = useAlertStore()
const devices = ref<EdgeDevice[]>([])

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
  try {
    await ElMessageBox.confirm(`删除规则「${rule.name}」？`, '确认删除', { type: 'warning' })
    await store.deleteRule(rule.id)
    ElMessage.success('已删除')
  } catch {
    /* 取消或失败静默 */
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

onMounted(async () => {
  await Promise.all([store.fetchRules(), store.fetchEvents()])
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
</style>
