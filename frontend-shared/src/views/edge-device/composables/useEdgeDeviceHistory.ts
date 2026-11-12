/**
 * 历史设备视图（已删除但数据仍保留）的状态与操作。
 *
 * 为什么从 EdgeDeviceList.vue 搬出来：这一整块（取数 / 分页 / 勾选 / 清理状态 / 彻底删除 / CSV 导出）
 * 只服务历史视图，与在役列表共享的只有「搜索词 + 类型筛选」两个 ref —— 作为参数显式传入而不是
 * 闭包捕获，取数口径仍只有一份，但状态归属变得可读。
 *
 * 模板片段在 components/EdgeDeviceHistoryPanel.vue：面板只吃 props / 抛 emit，不直接改这里的 ref，
 * 避免「子组件偷偷改父状态」这种最难查的耦合。
 *
 * 纯函数（行格式化 / 清理状态三值 / 恢复可用性）导出在模块层：面板需要它们，而它们本来就无状态，
 * 不需要穿过 composable 实例。
 */
import { computed, ref, type Ref } from 'vue'
import { ElMessage } from 'element-plus'
import { edgeDeviceApi, type DeletedEdgeDevice } from '@/api/edgeDevice'
import { feedback } from '@/utils/feedback'
import type { TagType } from '@/utils/tagType'
import { formatRelativeTime, getDeviceTypeLabel } from './useDeviceListPresentation'

/** 批量操作失败明细的通用行形状（页面持有一个弹窗，两条批量路径共用）。 */
export interface FailureRow {
  id: number
  error: string
}

/** el-table 作用域槽 row 收窄（与 asDevice 同一范式）。 */
export const asDeletedDevice = (row: unknown) => row as DeletedEdgeDevice

export const formatHistoryRows = (row: DeletedEdgeDevice): string =>
  typeof row.row_estimate === 'number' ? `约 ${row.row_estimate.toLocaleString('zh-CN')} 行` : '—'
export const formatHistoryDeletedAt = (row: DeletedEdgeDevice): string => {
  if (!row.deleted_at) return '—'
  const d = new Date(row.deleted_at)
  return Number.isNaN(d.getTime()) ? '—' : d.toLocaleString('zh-CN', { hour12: false })
}

/** 清理状态三值：可恢复 / 已提交清理 / 清理中（不可恢复）。 */
const historyStatusKind = (row: DeletedEdgeDevice): 'recoverable' | 'queued' | 'purging' => {
  if (row.purge_in_progress) return 'purging'
  if (row.data_purge_requested) return 'queued'
  return 'recoverable'
}
export const historyStatusLabel = (row: DeletedEdgeDevice): string => {
  switch (historyStatusKind(row)) {
    case 'purging': return '清理中（不可恢复）'
    case 'queued': return '已提交清理'
    default: return '可恢复'
  }
}
export const historyStatusTagType = (row: DeletedEdgeDevice): TagType => {
  switch (historyStatusKind(row)) {
    case 'purging': return 'danger'
    case 'queued': return 'warning'
    default: return 'success'
  }
}
export const canRestoreHistory = (row: DeletedEdgeDevice): boolean =>
  !row.purge_in_progress && !row.data_purge_requested
export const restoreDisabledReason = (row: DeletedEdgeDevice): string => {
  if (row.purge_in_progress) return '后台正在清理该设备的数据，无法恢复'
  if (row.data_purge_requested) return '已提交彻底删除（后台清理），恢复入口已关闭'
  return ''
}

export function useEdgeDeviceHistory(options: {
  /** 与在役视图共用搜索词（已防抖）与类型筛选：服务端过滤，绝不本地筛当前页。 */
  debouncedKeyword: Ref<string>
  typeFilter: Ref<string>
  /** 清空在役 + 历史两边的勾选（表格内部勾选状态只能由页面统一收口）。 */
  onClearSelection: () => void
  /** 失败明细统一出口：弹窗状态在页面（两条批量路径共用一个弹窗）。 */
  reportFailure: (title: string, rows: FailureRow[]) => void
}) {
  const { debouncedKeyword, typeFilter, onClearSelection, reportFailure } = options

  const historyItems = ref<DeletedEdgeDevice[]>([])
  const historyTotal = ref(0)
  const historyPage = ref(1)
  const historyPageSize = ref(20)
  const historyLoading = ref(false)
  const historyLoaded = ref(false)
  const historyError = ref('')
  const historySelection = ref<DeletedEdgeDevice[]>([])
  const historyDeleting = ref(false)

  let historyRequestSequence = 0

  const historyParams = () => {
    const params: { page: number; page_size: number; search?: string; device_type?: string } = {
      page: historyPage.value,
      page_size: historyPageSize.value,
    }
    // 与在役视图共用搜索框与类型筛选（服务端过滤，绝不本地筛当前页）
    if (debouncedKeyword.value.trim()) params.search = debouncedKeyword.value.trim()
    if (typeFilter.value) params.device_type = typeFilter.value
    return params
  }

  const fetchHistory = async () => {
    const sequence = ++historyRequestSequence
    historyLoading.value = true
    historyError.value = ''
    try {
      const res = await edgeDeviceApi.getDeleted(historyParams())
      if (sequence !== historyRequestSequence) return
      historyItems.value = res.items
      historyTotal.value = res.total
      historyPage.value = res.page
      historyPageSize.value = res.page_size || historyPageSize.value
      historyLoaded.value = true
    } catch (e) {
      if (sequence !== historyRequestSequence) return
      historyError.value = e instanceof Error ? e.message : String(e)
      historyItems.value = []
      historyTotal.value = 0
    } finally {
      if (sequence === historyRequestSequence) historyLoading.value = false
    }
  }

  const handleHistorySelectionChange = (rows: DeletedEdgeDevice[]) => {
    historySelection.value = rows
  }
  const handleHistoryPageChange = (next: number) => {
    if (next === historyPage.value) return
    historyPage.value = next
    void fetchHistory()
  }

  /** 本页已提交/清理中的历史设备数（全量计数接口未提供，文案明确限定「本页」）。 */
  const historyPurgingCount = computed(() =>
    historyItems.value.filter(row => row.data_purge_requested || row.purge_in_progress).length)

  // ── 历史设备彻底删除（记录 + 全部数据，后台分批，不可恢复） ──
  const confirmDeleteHistory = async (rows: DeletedEdgeDevice[]) => {
    const ids = rows.map(r => r.id)
    if (ids.length === 0 || historyDeleting.value) return
    const message = ids.length === 1
      ? `彻底删除历史设备「${rows[0].name || '#' + ids[0]}」？\n记录与全部历史数据将被后台删除，开始后不可恢复。`
      : `彻底删除选中的 ${ids.length} 台历史设备？\n记录与全部历史数据将被后台删除，开始后不可恢复。`
    const confirmed = await feedback.confirmDanger(message, {
      title: '彻底删除历史设备',
      confirmText: '彻底删除',
      cancelText: '取消',
    })
    if (!confirmed) return
    historyDeleting.value = true
    try {
      const res = await edgeDeviceApi.batchDeleteHistory(ids)
      // 2026-10-25 运行时复验（task-10）：彻底删除是「入队 + 立即唤醒 purger」，
      // 小设备可能 <1s 就被硬删 —— 刷新后该行消失属于正常，不能因此报错。
      // 重复提交已清理的 id：HTTP 200 但该条 success=false / error="设备不存在"，
      // 语义是「已完成」，不是失败（不计入失败明细）。
      const isAlreadyGone = (r: { success: boolean; error?: string }): boolean =>
        !r.success && /设备不存在|不存在/.test(r.error || '')
      const alreadyGone = res.results.filter(isAlreadyGone)
      const realFailed = res.results.filter(r => !r.success && !isAlreadyGone(r))
      if (res.succeeded > 0) ElMessage.success(`已提交后台清理 ${res.succeeded} 台历史设备`)
      if (alreadyGone.length > 0) ElMessage.info(`另有 ${alreadyGone.length} 台设备已完成清理（记录已不存在）`)
      if (realFailed.length > 0) {
        reportFailure('彻底删除失败明细', realFailed.map(r => ({ id: r.id, error: r.error || '未知原因' })))
        if (res.succeeded === 0) {
          feedback.error(`彻底删除全部失败（${realFailed.length} 台），详见失败明细`)
        }
      }
      // 无论结果如何都刷新：行可能已消失，这是预期现象。
      await fetchHistory()
      onClearSelection()
    } catch (e) {
      feedback.handleError(e, '彻底删除历史设备失败')
    } finally {
      historyDeleting.value = false
    }
  }
  const deleteOneHistory = (row: DeletedEdgeDevice) => { void confirmDeleteHistory([row]) }

  // ── CSV 导出（前端本地生成，不请求后端） ──
  const downloadCSV = (headers: string[], rows: string[][], filenamePrefix: string) => {
    const csvContent = [headers, ...rows]
      .map(row => row.map(cell => `"${String(cell).replace(/"/g, '""')}"`).join(','))
      .join('\n')
    const blob = new Blob(['\ufeff' + csvContent], { type: 'text/csv;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = `${filenamePrefix}_${new Date().toISOString().slice(0, 10)}.csv`
    link.click()
    URL.revokeObjectURL(url)
    ElMessage.success('导出成功')
  }

  const handleHistoryExport = () => {
    if (historySelection.value.length === 0) return
    downloadCSV(
      ['ID', '名称', '型号', '原节点', '原通道', '数据量估算', '最后采集', '删除时间', '清理状态'],
      historySelection.value.map(row => [
        String(row.id),
        row.name || '',
        getDeviceTypeLabel(row.device_type),
        String(row.node_id ?? ''),
        String(row.channel_id ?? ''),
        typeof row.row_estimate === 'number' ? `约 ${row.row_estimate} 行` : '—',
        formatRelativeTime(row.last_data_at),
        row.deleted_at ? new Date(row.deleted_at).toLocaleString('zh-CN', { hour12: false }) : '—',
        historyStatusLabel(row),
      ]),
      'history_devices_export',
    )
  }


  return {
    historyItems,
    historyTotal,
    historyPage,
    historyPageSize,
    historyLoading,
    historyLoaded,
    historyError,
    historySelection,
    historyDeleting,
    historyPurgingCount,
    fetchHistory,
    handleHistorySelectionChange,
    handleHistoryPageChange,
    handleHistoryExport,
    confirmDeleteHistory,
    deleteOneHistory,
  }
}
