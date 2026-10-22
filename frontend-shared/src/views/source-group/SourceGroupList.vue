<template>
  <div class="source-group-page">
    <PageHeader
      title="来源组"
      subtitle="把多台边缘设备组合成一个数据来源：主备组按 active 跟随，复合组按 combine 合成"
    >
      <template #extra>
        <el-button type="primary" :icon="Plus" data-test="sg-create" @click="openCreate">新建来源组</el-button>
      </template>
    </PageHeader>

    <el-alert
      v-if="store.error"
      type="error"
      :closable="false"
      show-icon
      class="sg-error-alert"
      data-test="sg-error"
    >
      <template #title>
        <span>加载来源组失败：{{ store.error }}</span>
        <el-button link type="primary" size="small" data-test="sg-retry" @click="loadList">重试</el-button>
      </template>
    </el-alert>

    <el-card class="filter-card" shadow="never">
      <div class="filter-bar">
        <el-select v-model="filterKind" placeholder="类型" clearable class="filter-item" data-test="sg-filter-kind" @change="loadList">
          <el-option label="主备组" value="primary_backup" />
          <el-option label="复合组" value="composite" />
        </el-select>
        <el-button data-test="sg-refresh" @click="loadList">刷新</el-button>
      </div>
    </el-card>

    <section class="card">
      <el-skeleton v-if="store.loading && store.items.length === 0" :rows="5" animated data-test="sg-skeleton" />
      <div v-else-if="store.items.length > 0" class="mobile-table-wrapper">
        <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
        <el-table :data="store.items" v-loading="store.loading" data-test="sg-table">
          <el-table-column label="名称" min-width="150" show-overflow-tooltip>
            <template #default="{ row }">
              <span data-test="sg-name">{{ row.name || `来源组 #${row.id}` }}</span>
            </template>
          </el-table-column>
          <el-table-column label="类型" width="100">
            <template #default="{ row }">
              <el-tag :type="row.kind === 'primary_backup' ? 'success' : 'warning'" size="small" data-test="sg-kind">
                {{ kindText(row.kind) }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="数据类别" min-width="130">
            <template #default="{ row }">
              <span class="mono" data-test="sg-category">{{ row.category }}</span>
            </template>
          </el-table-column>
          <el-table-column label="合成方式" min-width="150">
            <template #default="{ row }">
              <!-- composite 才显示 combine；primary_backup 语义就是 active 单值 -->
              <span v-if="row.kind === 'composite'" class="mono" data-test="sg-combine">
                {{ combineText(asGroup(row)) }}
              </span>
              <span v-else class="text-muted" data-test="sg-combine">—</span>
            </template>
          </el-table-column>
          <el-table-column label="最少成员" width="100">
            <template #default="{ row }">
              <span class="mono" data-test="sg-min-members">{{ minMembersText(asGroup(row)) }}</span>
            </template>
          </el-table-column>
          <el-table-column label="成员" min-width="110">
            <template #default="{ row }">
              <span class="mono" data-test="sg-members">{{ memberRows(asGroup(row)).length }} 个成员</span>
            </template>
          </el-table-column>
          <el-table-column label="权威成员" min-width="180">
            <template #default="{ row }">
              <!-- 主备组显示 active；复合组没有 active 概念（禁激活，契约裁决 #6） -->
              <span v-if="row.kind === 'composite'" class="text-muted" data-test="sg-no-active">复合组无 active</span>
              <span v-else-if="activeMemberText(asGroup(row))" class="mono" data-test="sg-active">{{ activeMemberText(asGroup(row)) }}</span>
              <span v-else class="text-muted" data-test="sg-active">无 active</span>
            </template>
          </el-table-column>
          <el-table-column label="版本" width="90">
            <template #default="{ row }">
              <span class="mono" data-test="sg-revision">r{{ row.revision }}</span>
            </template>
          </el-table-column>
          <el-table-column label="启用" width="90">
            <template #default="{ row }">
              <el-switch
                :model-value="row.enabled"
                size="small"
                :aria-label="'启用来源组 ' + row.name"
                data-test="sg-enabled"
                @change="(v: string | number | boolean) => onToggleEnabled(asGroup(row), v === true)"
              />
            </template>
          </el-table-column>
          <el-table-column label="操作" width="300" :fixed="isMobile ? false : 'right'">
            <template #default="{ row }">
              <el-button
                v-if="row.kind === 'primary_backup'"
                link
                type="primary"
                size="small"
                data-test="sg-activate"
                :loading="actingId === row.id"
                @click="onActivate(asGroup(row))"
              >{{ activeMemberText(asGroup(row)) ? '重新选举' : '激活' }}</el-button>
              <!-- 复合组不渲染激活按钮：不存在"哪个成员权威"的语义 -->
              <el-button link type="primary" size="small" data-test="sg-edit" @click="openEdit(asGroup(row))">编辑</el-button>
              <el-button link type="danger" size="small" data-test="sg-delete" @click="onDelete(asGroup(row))">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>
      <EmptyState
        v-else-if="!store.loading"
        kind="empty"
        title="还没有来源组"
        description="来源组把多台边缘设备组合成一个数据来源：主备组用于自动跟 active，复合组用于 1+2 合成等场景。"
        data-test="sg-empty"
      />
    </section>

    <el-dialog
      v-model="dialogVisible"
      :title="editingId ? '编辑来源组' : '新建来源组'"
      width="720px"
      data-test="sg-dialog"
      @closed="onDialogClosed"
    >
      <el-form :model="form" label-width="110px" data-test="sg-form">
        <el-form-item label="名称" required>
          <el-input v-model="form.name" placeholder="如：客厅光照主备" data-test="field-sg-name" />
        </el-form-item>
        <el-form-item label="类型" required>
          <!-- kind/category 创建后不可变（后端 PUT DTO 无此二字段，传了也会被忽略） -->
          <el-select v-model="form.kind" :disabled="!!editingId" data-test="field-sg-kind" @change="onKindChange">
            <el-option label="主备组（active 单值）" value="primary_backup" />
            <el-option label="复合组（多成员合成）" value="composite" />
          </el-select>
        </el-form-item>
        <el-form-item label="数据类别" required>
          <el-input
            v-model="form.category"
            :disabled="!!editingId"
            placeholder="物理名，如 illumination / temperature_1"
            data-test="field-sg-category"
          />
          <span v-if="editingId" class="hint" data-test="sg-immutable-hint">类型与数据类别创建后不可修改（需删除重建）</span>
        </el-form-item>

        <template v-if="form.kind === 'composite'">
          <el-form-item label="合成方式" required>
            <el-select v-model="form.combine" placeholder="必须显式选择" data-test="field-sg-combine">
              <el-option v-for="c in combines" :key="c.value" :label="c.label" :value="c.value" />
            </el-select>
          </el-form-item>
          <el-form-item label="最少成员">
            <el-input-number
              v-model="form.min_members"
              :min="1"
              :max="64"
              data-test="field-sg-min-members"
            />
            <span class="hint" data-test="sg-min-members-hint">
              少于该数量 ⇒ 组装不可用（unbound）
            </span>
          </el-form-item>
          <el-form-item label="缺员处置">
            <el-select v-model="form.on_degrade" data-test="field-sg-on-degrade">
              <el-option label="fail_closed（默认，缺员即不可用）" value="fail_closed" />
              <el-option label="use_available（允许降级，事件标记 degraded）" value="use_available" />
            </el-select>
          </el-form-item>
        </template>

        <el-form-item label="成员值上限">
          <el-input-number v-model="form.staleness_sec" :min="0" :max="3600" data-test="field-sg-staleness" />
          <span class="hint">秒，0 = 按采集间隔推导（2×Interval，30–3600s）</span>
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" data-test="field-sg-enabled" />
        </el-form-item>
        <el-form-item label="说明">
          <el-input v-model="form.description" type="textarea" :rows="2" data-test="field-sg-desc" />
        </el-form-item>

        <el-form-item label="成员" required>
          <div class="member-editor">
            <div v-for="(m, index) in form.members" :key="m.uid" class="member-row" data-test="sg-member-row">
              <span class="member-index mono">{{ index + 1 }}</span>
              <el-select
                v-model="m.edge_device_id"
                filterable
                placeholder="选择边缘设备"
                class="member-device"
                data-test="field-member-device"
              >
                <el-option v-for="d in devices" :key="d.id" :label="`${d.name}（#${d.id}）`" :value="d.id" />
              </el-select>
              <el-input-number v-model="m.priority" :min="0" :max="1000" data-test="field-member-priority" />
              <el-checkbox v-if="form.kind === 'composite'" v-model="m.required" data-test="field-member-required">
                必需
              </el-checkbox>
              <el-button link type="danger" size="small" data-test="sg-member-remove" @click="removeMember(index)">移除</el-button>
            </div>
            <el-button size="small" data-test="sg-add-member" @click="addMember">+ 添加成员</el-button>
            <p class="hint" data-test="sg-members-hint">
              优先级越大越优先；主备组同一时刻最多一个 active 成员（由数据源状态机维护）。
            </p>
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" data-test="sg-save" @click="onSave">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { Plus } from '@element-plus/icons-vue'
import PageHeader from '@/components/common/PageHeader.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import feedback from '@/utils/feedback'
import { useResponsive } from '@/composables/useResponsive'
import { useSourceGroupStore } from '@/stores/sourceGroup'
import { edgeDeviceApi, type EdgeDevice } from '@/api/edgeDevice'
import type {
  SourceGroup,
  SourceGroupCombine,
  SourceGroupKind,
  SourceGroupMemberInput,
} from '@/api/sourceGroup'

const store = useSourceGroupStore()
const { isMobile } = useResponsive()

/** el-table 作用域槽的 row 在 EP 类型里是内部 DefaultRow（未从包根导出），此处做一次命名类型的边界收窄（非 any）。 */
const asGroup = (row: unknown) => row as SourceGroup

const combines: Array<{ value: SourceGroupCombine; label: string }> = [
  { value: 'avg', label: '平均（avg）' },
  { value: 'sum', label: '求和（sum）' },
]

const filterKind = ref<SourceGroupKind | ''>('')
const actingId = ref<number | null>(null)

function kindText(kind: SourceGroupKind): string {
  return kind === 'primary_backup' ? '主备' : '复合'
}
function combineText(group: SourceGroup): string {
  return combines.find(c => c.value === group.combine)?.label ?? (group.combine || '—')
}
function minMembersText(group: SourceGroup): string {
  if (group.kind !== 'composite') return '—'
  const n = group.min_members || 1
  return `≥ ${n}`
}
function memberRows(group: SourceGroup) {
  return Array.isArray(group.members) ? group.members : []
}
/**
 * 权威成员展示文本。
 *
 * 后端**必须**返回 active_member_id（0/null = 无）—— 这是区分「主备组暂无 active」与
 * 「复合组无 active 概念」的唯一可靠字段；members.status 扫描只作老响应兼容兜底，
 * 不参与判定（Lead 2026-10-22 裁决）。
 */
function activeMemberText(group: SourceGroup): string | null {
  if (group.kind === 'composite') return null
  const id = typeof group.active_member_id === 'number' && group.active_member_id > 0
    ? group.active_member_id
    : undefined
  if (!id) return null
  const hit = memberRows(group).find(m => m.id === id)
  return hit ? `成员 #${hit.id}（设备 ${hit.edge_device_id}）` : `成员 #${id}`
}

async function loadList() {
  try {
    await store.fetchList({ page: 1, page_size: 100, kind: filterKind.value || undefined })
  } catch {
    /* store.error 已记录，页面顶部展示 + 重试 */
  }
}

// ── 成员设备下拉 ──
const devices = ref<EdgeDevice[]>([])
const devicesLoading = ref(false)
async function loadDevices() {
  devicesLoading.value = true
  try {
    const res = await edgeDeviceApi.getList({ page: 1, page_size: 200 })
    devices.value = Array.isArray(res.items) ? res.items : []
  } catch {
    // 拿不到设备目录不阻塞页面（成员编辑会退化为"只能看到已有成员"），不弹错。
    devices.value = []
  } finally {
    devicesLoading.value = false
  }
}

// ── 对话框表单 ──
interface MemberDraft {
  uid: number
  edge_device_id?: number
  priority: number
  required: boolean
}
let uidSeq = 1
function newMember(): MemberDraft {
  return { uid: uidSeq++, edge_device_id: undefined, priority: 100, required: false }
}

function emptyForm() {
  return {
    name: '',
    kind: 'primary_backup' as SourceGroupKind,
    category: '',
    combine: '' as SourceGroupCombine | '',
    min_members: 1,
    staleness_sec: 0,
    on_degrade: 'fail_closed' as 'fail_closed' | 'use_available',
    enabled: true,
    description: '',
    members: [newMember()] as MemberDraft[],
  }
}
const form = reactive(emptyForm())

const dialogVisible = ref(false)
const saving = ref(false)
const editingId = ref<number | null>(null)
const editingRevision = ref(0)

function onKindChange(kind: SourceGroupKind) {
  if (kind === 'primary_backup') {
    form.combine = ''
    form.on_degrade = 'fail_closed'
    form.members.forEach(m => { m.required = false })
  } else if (!form.combine) {
    form.combine = 'avg'
  }
}

function addMember() {
  form.members.push(newMember())
}
function removeMember(index: number) {
  if (form.members.length <= 1) {
    feedback.warning('至少保留一个成员')
    return
  }
  form.members.splice(index, 1)
}

async function openCreate() {
  editingId.value = null
  editingRevision.value = 0
  Object.assign(form, emptyForm())
  dialogVisible.value = true
}

async function openEdit(group: SourceGroup) {
  editingId.value = group.id
  editingRevision.value = group.revision
  let detail = group
  // 列表可能未内联 members：编辑前取详情，避免把成员编辑成"空组"
  if (!Array.isArray(group.members) || group.members.length === 0) {
    try {
      detail = await store.fetchGroup(group.id)
    } catch {
      feedback.handleError(new Error('读取来源组详情失败'), '读取来源组详情失败')
      return
    }
  }
  Object.assign(form, {
    name: detail.name,
    kind: detail.kind,
    category: detail.category,
    combine: (detail.combine ?? '') as SourceGroupCombine | '',
    min_members: detail.min_members || 1,
    staleness_sec: detail.staleness_sec || 0,
    on_degrade: (detail.on_degrade === 'use_available' ? 'use_available' : 'fail_closed') as 'fail_closed' | 'use_available',
    enabled: detail.enabled,
    description: detail.description ?? '',
    members: memberRows(detail).map(m => ({
      uid: uidSeq++,
      edge_device_id: m.edge_device_id,
      priority: m.priority,
      required: m.required,
    })),
  })
  if (form.members.length === 0) form.members = [newMember()]
  dialogVisible.value = true
}
function onDialogClosed() {
  saving.value = false
}

function validate(): string | null {
  if (!form.name.trim()) return '请填写来源组名称'
  if (!form.category.trim()) return '请填写数据类别'
  if (form.kind === 'composite' && !form.combine) return '复合组必须显式选择合成方式（combine）'
  const picked = form.members.filter(m => !!m.edge_device_id)
  if (picked.length === 0) return '请至少添加一个成员'
  const ids = picked.map(m => m.edge_device_id)
  if (new Set(ids).size !== ids.length) return '成员不能重复选择同一台边缘设备'
  return null
}

function buildMembers(): SourceGroupMemberInput[] {
  return form.members
    .filter(m => !!m.edge_device_id)
    .map(m => ({ edge_device_id: Number(m.edge_device_id), priority: m.priority, required: form.kind === 'composite' ? m.required : false }))
}

async function onSave() {
  const err = validate()
  if (err) {
    feedback.warning(err)
    return
  }
  saving.value = true
  try {
    // 可变更字段（创建/更新同集）；kind/category 只在创建时提交 —— 后端 PUT DTO 无此二字段。
    const mutable = {
      name: form.name.trim(),
      combine: form.kind === 'composite' ? form.combine : '',
      min_members: form.kind === 'composite' ? form.min_members : 0,
      staleness_sec: form.staleness_sec,
      on_degrade: form.kind === 'composite' ? form.on_degrade : 'fail_closed',
      enabled: form.enabled,
      description: form.description.trim(),
      members: buildMembers(),
    }
    if (editingId.value) {
      // revision 由后端本期忽略（无乐观锁）；仍随请求发送，后端拒绝时按普通错误提示，不做特殊冲突 UI。
      await store.updateGroup(editingId.value, { ...mutable, revision: editingRevision.value })
      feedback.success('来源组已更新')
    } else {
      await store.createGroup({
        ...mutable,
        kind: form.kind,
        category: form.category.trim(),
        // min_members 仅 composite 有意义；primary_backup 交 0 由后端归一
        min_members: form.kind === 'composite' ? mutable.min_members : 0,
      })
      feedback.success('来源组已创建')
    }
    dialogVisible.value = false
    await loadList()
  } catch (e) {
    feedback.handleError(e, '保存来源组失败')
  } finally {
    saving.value = false
  }
}

async function onActivate(group: SourceGroup) {
  actingId.value = group.id
  try {
    await store.activateGroup(group.id)
    feedback.success('已触发主备选举 / 激活')
    await loadList()
  } catch (e) {
    feedback.handleError(e, '激活失败')
  } finally {
    actingId.value = null
  }
}

async function onToggleEnabled(group: SourceGroup, enabled: boolean) {
  try {
    await store.setEnabled(group, enabled)
    feedback.success(enabled ? '已启用' : '已停用')
  } catch (e) {
    feedback.handleError(e, '切换启用状态失败')
    await loadList()
  }
}

async function onDelete(group: SourceGroup) {
  const ok = await feedback.confirmDanger(
    `删除来源组「${group.name}」？被规则或成员引用的组不允许删除（后端会 409）。`,
    { title: '确认删除', confirmText: '删除', cancelText: '取消' },
  )
  if (!ok) return
  actingId.value = group.id
  try {
    await store.removeGroup(group.id)
    feedback.success('已删除')
    await loadList()
  } catch (e) {
    feedback.handleError(e, '删除失败')
  } finally {
    actingId.value = null
  }
}

// 设备目录只在打开对话框时才需要；同时加载一次，避免第一次打开时下拉为空。
const memberOptionsReady = computed(() => devices.value.length > 0 || devicesLoading.value)

onMounted(() => {
  void loadList()
  void loadDevices()
  void memberOptionsReady
})
</script>

<style scoped>
.source-group-page {
  padding: 0;
}
.filter-card {
  margin-bottom: 16px;
}
.filter-bar {
  display: flex;
  gap: 12px;
  align-items: center;
  flex-wrap: wrap;
}
.mono {
  font-family: var(--el-font-family-monospace, monospace);
}
.text-muted {
  color: var(--el-text-color-secondary);
}
.hint {
  margin-left: 8px;
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.member-editor {
  width: 100%;
}
.member-row {
  display: flex;
  gap: 8px;
  align-items: center;
  margin-bottom: 8px;
}
.member-index {
  width: 20px;
  color: var(--el-text-color-secondary);
}
.member-device {
  flex: 1;
  min-width: 220px;
}
.sg-error-alert {
  margin-bottom: 16px;
}
</style>
