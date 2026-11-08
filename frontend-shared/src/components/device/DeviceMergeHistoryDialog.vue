<template>
  <el-dialog
    v-model="dialogVisible"
    title="合并历史数据"
    width="760px"
    align-center
    :close-on-click-modal="false"
    data-test="merge-dialog"
  >
    <el-alert type="warning" :closable="false" show-icon class="merge-notice">
      <template #title>
        <span>
          把<strong>同型号</strong>的已删除设备的历史数据并入当前设备。不同类型设备的字段/单位不同，
          <strong>不能合并</strong>。合并只是「旧记录已删除」时的收尾工具 ——
          设备还在请用「编辑」换节点/通道（历史天然连续）；记录被删请优先用「恢复已删除设备」。
          <br />
          <strong>同型号只是必要条件，不等于同一台设备</strong>：请确认这些历史记录确实是当前设备的前身
          （换过节点/地址也可能同型号）；合并后源记录下的历史将归当前设备。
        </span>
      </template>
    </el-alert>

    <el-form label-width="96px" class="merge-form">
      <el-form-item label="目标设备" required>
        <el-input :model-value="targetLabel" disabled data-test="merge-target" />
      </el-form-item>
      <el-form-item label="源设备 ID" required>
        <p class="merge-identity-hint" data-test="merge-identity-hint">
          请确认这些历史记录确实是<strong>当前设备的前身</strong>（换过节点/地址也可能同型号）；
          合并后源记录下的历史将归当前设备。
        </p>
        <el-select
          v-model="sourceIds"
          multiple
          filterable
          allow-create
          default-first-option
          :loading="candidatesLoading"
          placeholder="选择同型号的已删除历史设备，或直接输入其 ID"
          style="width: 100%"
          data-test="merge-sources"
        >
          <el-option
            v-for="d in candidates"
            :key="d.id"
            :label="candidateLabel(d)"
            :value="d.id"
          />
        </el-select>
        <p v-if="deletedTotal > deletedDevices.length" class="merge-candidates-more">
          同型号已删除设备共 {{ deletedTotal }} 台，下拉仅展示最近 {{ deletedDevices.length }} 台；更早的设备请直接输入 ID。
        </p>
      </el-form-item>
    </el-form>

    <el-alert
      v-if="!targetType"
      type="error"
      :closable="false"
      show-icon
      class="merge-block"
      data-test="merge-no-type"
    >
      <template #title>目标设备没有设备型号（type 为空），无法校验「同型号」合并，请先补全型号。</template>
    </el-alert>

    <el-alert
      v-if="candidatesError"
      type="warning"
      :closable="false"
      show-icon
      class="merge-block"
      data-test="merge-candidates-error"
    >
      <template #title>
        <span>同型号已删除设备列表加载失败：{{ candidatesError }}（仍可直接输入源设备 ID）</span>
        <el-button link type="primary" size="small" data-test="merge-candidates-retry" @click="loadCandidates">重试</el-button>
      </template>
    </el-alert>
    <div v-else-if="candidatesLoaded && candidates.length === 0" class="merge-empty" data-test="merge-empty">
      <p class="merge-empty-title">没有可合并的同型号历史设备（已删除的 {{ typeLabel }}）。</p>
      <ul class="merge-empty-list">
        <li>旧设备记录<strong>还在用</strong> → 请用「编辑」换节点/通道（id 不变，历史天然连续，无需合并）。</li>
        <li>旧设备<strong>已被删除、物理设备还要继续用</strong> → 在「恢复已删除设备」中找回（id 不变，历史连续）。</li>
        <li>只有<strong>旧记录已删除、不想恢复</strong>时才用「合并历史」把历史并到当前设备。</li>
      </ul>
      <p class="merge-empty-hint">下拉超过 {{ CANDIDATE_LIMIT }} 台未展示时，也可直接输入已删除的同型号设备 ID。</p>
    </div>

    <el-alert
      v-if="blockReasons.length > 0"
      type="error"
      :closable="false"
      show-icon
      class="merge-block"
      data-test="merge-blocked"
    >
      <template #title>
        <ul class="merge-block-list">
          <li v-for="(reason, index) in blockReasons" :key="index">{{ reason }}</li>
        </ul>
      </template>
    </el-alert>

    <el-alert
      v-if="invalidSourceTokens.length > 0"
      type="error"
      :closable="false"
      show-icon
      class="merge-block"
      data-test="merge-invalid-sources"
    >
      <template #title>
        以下输入不是有效的源设备 ID（必须是正整数，不会被提交）：{{ invalidSourceTokens.join('、') }}
      </template>
    </el-alert>

    <div v-if="resolvedSourceIds.length > 0" class="merge-summary" data-test="merge-summary">
      <el-skeleton v-if="summaryLoading" :rows="2" animated />
      <el-alert v-else-if="summaryError" type="error" :closable="false" show-icon>
        <template #title>
          <span>数据量估算失败：{{ summaryError }}</span>
          <el-button link type="primary" size="small" data-test="merge-summary-retry" @click="loadSummary">重试</el-button>
        </template>
      </el-alert>
      <template v-else>
        <div class="merge-summary-title">
          <span>目标设备现有 <strong data-test="merge-target-rows">{{ targetRowsText }}</strong> 行；
            预计迁移 <strong data-test="merge-source-rows">{{ sourceRowsText }}</strong> 行（仅源设备合计）</span>
          <span v-if="sourceRowsMissing" class="merge-summary-hint">部分来源估算超时，请参照下表逐项核对</span>
        </div>

        <!-- 移动端宽表：横向滚动 + 滑动提示（主题样式 .mobile-table-wrapper，
             本仓门禁 MobileTableWrapper.spec.ts 要求每张 el-table 都在这条祖先链上）。 -->
        <div class="mobile-table-wrapper">
          <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
          <el-table :data="summary.sources" size="small" data-test="merge-summary-table">
            <el-table-column label="源设备" min-width="170">
              <template #default="{ row }">
                #{{ asSummaryItem(row).source_id }}
                <span v-if="asSummaryItem(row).device_name" class="merge-source-name">{{ asSummaryItem(row).device_name }}</span>
              </template>
            </el-table-column>
            <el-table-column label="型号" width="150">
              <template #default="{ row }">{{ asSummaryItem(row).device_type || '—' }}</template>
            </el-table-column>
            <el-table-column label="行数" width="120">
              <!-- rows 可空（估算超时被省略）：显示「—」，不要渲染 null 或 0。 -->
              <template #default="{ row }">{{ formatRows(asSummaryItem(row).rows) }}</template>
            </el-table-column>
            <el-table-column label="最早">
              <template #default="{ row }">{{ formatTime(asSummaryItem(row).first_time) }}</template>
            </el-table-column>
            <el-table-column label="最新">
              <template #default="{ row }">{{ formatTime(asSummaryItem(row).last_time) }}</template>
            </el-table-column>
          </el-table>
        </div>
        <p class="merge-irreversible-hint">合并后<strong>源设备侧不再有这些行</strong>（数据是搬走不是复制；重复执行幂等）。</p>
      </template>
    </div>

    <el-alert
      v-if="result && result.warnings.length > 0"
      type="warning"
      :closable="false"
      show-icon
      class="merge-result-warnings"
      data-test="merge-warnings"
    >
      <template #title>合并已完成，但有以下告警（人工数据源冲突等，可重试补齐全）：</template>
      <ul class="merge-warning-list">
        <li v-for="(warning, index) in result.warnings" :key="index">{{ warning }}</li>
      </ul>
    </el-alert>

    <template #footer>
      <template v-if="result">
        <el-button type="primary" data-test="merge-done" @click="dialogVisible = false">完成</el-button>
      </template>
      <template v-else>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button
          type="danger"
          :loading="submitting"
          :disabled="confirmDisabled"
          data-test="merge-confirm"
          @click="onConfirm"
        >
          确认合并
        </el-button>
      </template>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
/**
 * 合并历史数据（产品设计重做 2026-10-25，见
 * docs/分析/合并历史-产品设计重做-2026-10-25.md）。
 *
 * 这个入口**不是**通用数据搬运工具，而是三条路径里的收尾：
 *   ① 同一条物理设备换节点/通道 → 「编辑」（id 不变，历史天然连续，主路径）
 *   ② 设备行被删、物理设备还要用 → 「恢复已删除设备」（id 不变）
 *   ③ 旧记录已删除且不想/不能恢复、只想要回历史 → 本弹窗
 *
 * 准入不变量（前后端一致）：
 *   · 目标必须存活；源必须是 edge_devices 行存在、**已软删**、且 type **与目标完全相同**；
 *   · 不同型号的驱动/字段集合/单位语义不同，跨型号合并会把数据挂错语义（400）；
 *   · 在役设备不能当源（409：搬走历史会变成空壳、历史永久分裂）；
 *   · 无设备行的孤儿 id 不能合并（无法验证型号，400）。
 * 因此本弹窗：
 *   · 候选只来自 GET /deleted?device_type=<目标型号>（服务端过滤，分页下不漏数据），
 *     并过滤掉 row_estimate === 0（已合并过/无数据）；
 *   · **完全不展示现役设备**（旧版拿"当前页存活设备"当候选是产品级错误）；
 *   · 手输 id 允许（防分页遗漏），但用 data-summary 的 device_type/state 在提交前校验：
 *     跨型号 / 无设备行 / 使用中 → 红字阻断，不发请求；
 *   · 结果展示 rows_migrated + warnings（不再有「停用源设备」概念）。
 */
import { computed, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { feedback } from '@/utils/feedback'
import { getDeviceTypeLabel } from '@/utils/deviceType'
import {
  edgeDeviceApi,
  type DeletedEdgeDevice,
  type EdgeDevice,
  type EdgeDeviceDataSummary,
  type EdgeDeviceDataSummaryItem,
  type MergeHistoryResult,
} from '@/api/edgeDevice'

const CANDIDATE_LIMIT = 200

const props = defineProps<{
  visible: boolean
  target: EdgeDevice | null
}>()
const emit = defineEmits<{
  (e: 'update:visible', value: boolean): void
  (e: 'merged', payload: { targetId: number; rowsMigrated: number; warnings: string[] }): void
}>()

const dialogVisible = computed({
  get: () => props.visible,
  set: (value: boolean) => emit('update:visible', value),
})

const targetId = computed(() => props.target?.id ?? 0)
const targetType = computed(() => props.target?.device_type ?? '')
/** 型号中文名（复用全局 deviceType 单一事实源；未知型号回退 type 原文）。 */
const typeLabel = computed(() => (targetType.value ? getDeviceTypeLabel(targetType.value) : '未知型号'))
const targetLabel = computed(() =>
  props.target
    ? `#${props.target.id} ${props.target.name || '未命名'} · 型号 ${typeLabel.value}（${targetType.value || '—'}）`
    : '—')

// ── 源候选：只来自「同型号 + 已删除」的服务端过滤列表 ──
const deletedDevices = ref<DeletedEdgeDevice[]>([])
const deletedTotal = ref(0)
const candidatesLoading = ref(false)
const candidatesLoaded = ref(false)
const candidatesError = ref('')

const candidates = computed(() =>
  deletedDevices.value
    .filter(d => d.id !== targetId.value)
    // row_estimate===0 表示已合并过/无数据：展示它们只会让用户白点（null/undefined=估不出，保留）
    .filter(d => d.row_estimate !== 0))

const formatShortTime = (t: string | null | undefined): string => {
  if (!t) return '—'
  const d = new Date(t)
  if (Number.isNaN(d.getTime())) return '—'
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

const formatRowsText = (rows: number | null | undefined): string =>
  typeof rows === 'number' ? `约 ${rows.toLocaleString('zh-CN')} 行` : '行数未知'

const candidateLabel = (d: DeletedEdgeDevice): string =>
  `#${d.id} ${d.name || '未命名'} · ${formatRowsText(d.row_estimate)} · 最后数据 ${formatShortTime(d.last_data_at)}`

const loadCandidates = async () => {
  candidatesLoading.value = true
  candidatesLoaded.value = false
  candidatesError.value = ''
  try {
    if (!targetId.value || !targetType.value) {
      deletedDevices.value = []
      deletedTotal.value = 0
      candidatesLoaded.value = true
      return
    }
    // 服务端精确型号过滤：只取与目标同型号的已删除设备（分页下本地过滤会漏数据）
    const page = await edgeDeviceApi.getDeleted({
      device_type: targetType.value,
      page: 1,
      page_size: CANDIDATE_LIMIT,
    })
    deletedDevices.value = page.items
    deletedTotal.value = page.total
    candidatesLoaded.value = true
  } catch (e) {
    candidatesError.value = e instanceof Error ? e.message : String(e)
    deletedDevices.value = []
    deletedTotal.value = 0
  } finally {
    candidatesLoading.value = false
  }
}

// ── 源 id：el-select allow-create 产出 string，必须统一收敛 ──
const sourceIds = ref<(number | string)[]>([])
const resolvedSourceIds = computed<number[]>(() => {
  const out: number[] = []
  const seen = new Set<number>()
  for (const raw of sourceIds.value) {
    const n = Number(raw)
    if (!Number.isInteger(n) || n <= 0) continue
    if (n === targetId.value) continue // 自我合并无意义，后端也会 400
    if (seen.has(n)) continue
    seen.add(n)
    out.push(n)
  }
  return out
})
/** 无法收敛的输入：明确警告并拒绝提交，不能让它变成后端 400。 */
const invalidSourceTokens = computed(() =>
  sourceIds.value.filter((raw) => {
    const n = Number(raw)
    return !Number.isInteger(n) || n <= 0
  }).map(String))
/** 手输/候选里混进了目标设备自身：前端先拦（后端语义也是 400）。 */
const targetSelfTokens = computed(() =>
  sourceIds.value.filter((raw) => Number(raw) === targetId.value).map(String))
const tooManySources = computed(() => resolvedSourceIds.value.length > 100)

// ── 数据量预览（target/sources 分组；同型号准入校验） ──
const summary = ref<EdgeDeviceDataSummary>({ target: null, sources: [] })
const summaryLoading = ref(false)
const summaryError = ref('')
const asSummaryItem = (row: unknown) => row as EdgeDeviceDataSummaryItem

const targetRowsText = computed(() => {
  const n = summary.value.target_rows ?? summary.value.target?.rows
  return typeof n === 'number' ? n.toLocaleString('zh-CN') : '—'
})
const sourceRowsMissing = computed(() => typeof summary.value.source_rows !== 'number')
const sourceRowsText = computed(() =>
  typeof summary.value.source_rows === 'number' ? summary.value.source_rows.toLocaleString('zh-CN') : '—')

/**
 * 提交前准入校验（基于 data-summary 回传的 device_type/state）：
 *   · 跨型号 → 阻断；
 *   · 无设备行（state=missing，无 device_type）→ 阻断；
 *   · 使用中（state=live）→ 阻断（在役设备不能当源）；
 *   · 拿不到 device_type/state（旧后端/异常）→ 阻断（无法验证同型号，不冒险）。
 */
// 各种阻断原因互斥（避免同一源同时报"跨型号"和"使用中"两条重复提示）
const mismatchSources = computed(() =>
  summary.value.sources.filter(s =>
    s.state === 'deleted' && !!s.device_type && !!targetType.value && s.device_type !== targetType.value))
const liveSources = computed(() => summary.value.sources.filter(s => s.state === 'live'))
const missingSources = computed(() => summary.value.sources.filter(s => s.state === 'missing'))
const unverifiableSources = computed(() =>
  summary.value.sources.filter(s => !s.state || (s.state === 'deleted' && !s.device_type)))

const blockReasons = computed<string[]>(() => {
  const reasons: string[] = []
  for (const s of mismatchSources.value) {
    reasons.push(
      `源设备 #${s.source_id} 是 ${getDeviceTypeLabel(s.device_type || '')}（${s.device_type}），与目标设备 #${targetId.value} 的型号 ${typeLabel.value}（${targetType.value}）不一致，只能合并同型号设备。`,
    )
  }
  for (const s of liveSources.value) {
    reasons.push(
      `源设备 #${s.source_id} 仍在使用中（未删除），不能合并：在役设备的历史被搬走会变成空壳。只是想换位置请用「编辑」；确实要收尾请先删除该设备（保留数据）再合并。`,
    )
  }
  for (const s of missingSources.value) {
    reasons.push(`源设备 #${s.source_id} 没有设备记录（无设备行的孤儿 id），无法验证型号，不能合并。`)
  }
  for (const s of unverifiableSources.value) {
    reasons.push(`源设备 #${s.source_id} 无法校验型号（数据量接口未返回型号/状态），为安全起见不能合并。`)
  }
  return reasons
})

const confirmDisabled = computed(() =>
  submitting.value
  || !!result.value
  || !targetType.value
  || resolvedSourceIds.value.length === 0
  || invalidSourceTokens.value.length > 0
  || targetSelfTokens.value.length > 0
  || tooManySources.value
  || summaryLoading.value
  || blockReasons.value.length > 0)

let summarySeq = 0
const loadSummary = async () => {
  if (!targetId.value || resolvedSourceIds.value.length === 0) {
    summary.value = { target: null, sources: [] }
    return
  }
  const seq = ++summarySeq
  summaryLoading.value = true
  summaryError.value = ''
  try {
    const res = await edgeDeviceApi.getDataSummary(targetId.value, resolvedSourceIds.value)
    if (seq !== summarySeq) return
    summary.value = res
  } catch (e) {
    if (seq !== summarySeq) return
    summaryError.value = e instanceof Error ? e.message : String(e)
    summary.value = { target: null, sources: [] }
  } finally {
    if (seq === summarySeq) summaryLoading.value = false
  }
}

// 源集合变化 → 重新估算/校验。
watch(resolvedSourceIds, () => { void loadSummary() }, { deep: true })

const formatRows = (rows: number | null | undefined): string =>
  typeof rows === 'number' ? rows.toLocaleString('zh-CN') : '—'

const formatTime = (t: string | null | undefined): string => {
  if (!t) return '—'
  const d = new Date(t)
  return Number.isNaN(d.getTime()) ? '—' : d.toLocaleString('zh-CN', { hour12: false })
}

// ── 提交 ──
const submitting = ref(false)
const result = ref<{ rowsMigrated: number; warnings: string[] } | null>(null)

const onConfirm = async () => {
  if (!targetId.value || resolvedSourceIds.value.length === 0) return
  if (targetSelfTokens.value.length > 0) {
    ElMessage.warning(`源设备不能是目标设备自身（#${targetId.value}）`)
    return
  }
  if (invalidSourceTokens.value.length > 0) {
    ElMessage.warning(`源设备 ID 必须是正整数：${invalidSourceTokens.value.join('、')}`)
    return
  }
  if (tooManySources.value) {
    ElMessage.warning('一次最多合并 100 个源设备，请分批提交')
    return
  }
  if (blockReasons.value.length > 0) {
    ElMessage.warning('源设备不满足「同型号已删除」条件，请先按上方提示修正')
    return
  }
  submitting.value = true
  try {
    const res: MergeHistoryResult = await edgeDeviceApi.mergeHistory(targetId.value, resolvedSourceIds.value)
    result.value = { rowsMigrated: res.rows_migrated, warnings: res.warnings }
    emit('merged', {
      targetId: targetId.value,
      rowsMigrated: res.rows_migrated,
      warnings: res.warnings,
    })
    if (res.rows_migrated === 0) {
      // 幂等：源下已无时序行。如实告知「没有可迁移的数据」，不谎报成功。
      ElMessage.info('没有需要迁移的数据（这些源设备名下已无时序数据，可能此前已合并过）')
    } else {
      ElMessage.success(`已迁移 ${res.rows_migrated.toLocaleString('zh-CN')} 行历史数据`)
    }
    if (res.warnings.length === 0) dialogVisible.value = false
  } catch (e) {
    feedback.handleError(e, '合并历史数据失败')
  } finally {
    submitting.value = false
  }
}

// 打开即加载同型号已删候选并重置全部状态；关闭时使在途估算失效。
watch(() => props.visible, (open) => {
  if (!open) {
    summarySeq++
    return
  }
  sourceIds.value = []
  summary.value = { target: null, sources: [] }
  summaryError.value = ''
  result.value = null
  void loadCandidates()
}, { immediate: true })
</script>

<style scoped>
.merge-notice {
  margin-bottom: 16px;
}
.merge-form {
  margin-bottom: 8px;
}
.merge-identity-hint {
  margin: 0 0 8px;
  width: 100%;
  font-size: 12px;
  line-height: 1.6;
  color: var(--el-color-warning);
}
.merge-candidates-more {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.merge-block {
  margin-bottom: 8px;
}
.merge-block-list {
  margin: 0;
  padding-left: 18px;
  line-height: 1.6;
}
.merge-empty {
  border: 1px dashed var(--el-border-color);
  border-radius: 8px;
  padding: 12px 16px;
  background: var(--el-fill-color-lighter);
  margin-bottom: 8px;
}
.merge-empty-title {
  margin: 0 0 8px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}
.merge-empty-list {
  margin: 0;
  padding-left: 18px;
  font-size: 13px;
  line-height: 1.8;
  color: var(--el-text-color-regular);
}
.merge-empty-hint {
  margin: 8px 0 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.merge-summary {
  border-top: 1px solid var(--el-border-color-lighter, #ebeef5);
  padding-top: 12px;
}
.merge-summary-title {
  margin-bottom: 8px;
  font-size: 13px;
  color: var(--el-text-color-primary, #303133);
}
.merge-summary-hint {
  margin-left: 8px;
  color: var(--el-color-warning);
}
.merge-source-name {
  color: var(--el-text-color-secondary);
  margin-left: 4px;
}
.merge-irreversible-hint {
  margin: 8px 0 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.merge-result-warnings {
  margin-top: 12px;
}
.merge-warning-list {
  margin: 4px 0 0;
  padding-left: 18px;
  line-height: 1.6;
}
</style>
