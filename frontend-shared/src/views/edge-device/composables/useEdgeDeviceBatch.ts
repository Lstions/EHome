/**
 * 在役视图的批量管理（批量启用/停用/设置采集间隔 + 卡片批量勾选）。
 *
 * 为什么从 EdgeDeviceList.vue 搬出来：这是一组自洽的「临时状态 + 批量 API 调用」，
 * 与页面其它部分只有三个交点 —— 选中的设备（useDeviceDelete 持有）、取数（fetchDevices）、
 * 清空选择（要同时覆盖两个视图与两张表格，只有页面持有）。三个交点都用参数显式声明，
 * 因此它既没有偷偷读页面状态，页面也不必再背 180 行批量逻辑。
 *
 * 刻意**不**搬 selectAllPage / clearSelection / handleBatchExport，也不持有失败明细弹窗的可见性：
 *   · 前两者要按当前视图分别操作「在役表格 / 历史表格 / 卡片」三处勾选状态，属于页面级协调；
 *   · handleBatchExport 里的 getDeviceBusLabel(device) 被 EdgeDeviceListBusLabel.spec.ts
 *     按页面源文件数着 3 个调用点，搬走会让门禁指向空壳；
 *   · 失败明细弹窗是「批量启用/停用/间隔」与「历史彻底删除」共用的一个弹窗，只能归页面。
 */
import { ref, type Ref } from 'vue'
import { ElMessage } from 'element-plus'
import { edgeDeviceApi, type EdgeDevice } from '@/api/edgeDevice'
import { feedback } from '@/utils/feedback'
import type { FailureRow } from './useEdgeDeviceHistory'


export function useEdgeDeviceBatch(options: {
  /** 当前勾选的设备（与 useDeviceDelete 共用同一个 ref —— 两个入口必须看到同一份选择）。 */
  selectedDevices: Ref<EdgeDevice[]>
  fetchDevices: (force?: boolean, throwOnError?: boolean) => Promise<void>
  /** 清空在役 + 历史两边的勾选（表格内部勾选状态只能由页面统一收口）。 */
  onClearSelection: () => void
  /** 失败明细统一出口：弹窗状态在页面（两条批量路径共用一个弹窗）。 */
  reportFailure: (title: string, rows: FailureRow[]) => void
}) {
  const { selectedDevices, fetchDevices, onClearSelection, reportFailure } = options

  // ── 在役批量管理 ──
  /** 卡片视图批量选择模式（仅卡片视图显示开关；关闭时清空选择）。 */
  const batchMode = ref(false)
  const batchSubmitting = ref(false)
  const batchIntervalDialogVisible = ref(false)
  const batchIntervalMs = ref(5000)
  const batchIntervalError = ref('')

  const selectedIds = (): number[] => selectedDevices.value.map(d => d.id)

  /** 批量操作结果统一处理：成功 toast + 失败明细 + 刷新 + 清空选择。 */
  const handleBatchResult = async (
    label: string,
    res: { succeeded: number; failed: number; results: Array<{ id: number; success: boolean; error?: string }> },
    refresh: () => Promise<void>,
  ) => {
    const failedRows = res.results
      .filter(r => !r.success)
      .map(r => ({ id: r.id, error: r.error || '未知原因' }))
    if (res.succeeded > 0) ElMessage.success(`已${label} ${res.succeeded} 台设备`)
    if (failedRows.length > 0) {
      reportFailure(`批量${label}失败明细`, failedRows)
    }
    if (res.succeeded === 0 && failedRows.length > 0) {
      feedback.error(`批量${label}全部失败（${failedRows.length} 台），详见失败明细`)
    }
    await refresh()
    onClearSelection()
  }

  const runBatchUpdate = async (payload: { enabled?: boolean; interval_ms?: number }, label: string) => {
    const ids = selectedIds()
    if (ids.length === 0 || batchSubmitting.value) return
    batchSubmitting.value = true
    try {
      const res = await edgeDeviceApi.batchUpdate(ids, payload)
      await handleBatchResult(label, res, () => fetchDevices(true))
    } catch (e) {
      feedback.handleError(e, `批量${label}失败`)
    } finally {
      batchSubmitting.value = false
    }
  }

  const batchEnable = () => { void runBatchUpdate({ enabled: true }, '启用') }
  const batchDisable = () => { void runBatchUpdate({ enabled: false }, '停用') }

  const openBatchIntervalDialog = () => {
    const values = selectedDevices.value
      .map(d => Number(d.config?.interval_ms))
      .filter(n => Number.isFinite(n))
    batchIntervalMs.value = values.length > 0 && values.every(v => v === values[0]) ? values[0] : 5000
    batchIntervalError.value = ''
    batchIntervalDialogVisible.value = true
  }
  const confirmBatchInterval = async () => {
    const value = Number(batchIntervalMs.value)
    if (!Number.isInteger(value) || value < 0 || value > 3600000) {
      batchIntervalError.value = '采集间隔需为 0–3600000 的整数（0 = 不调度）'
      return
    }
    batchIntervalDialogVisible.value = false
    await runBatchUpdate({ interval_ms: value }, '设置采集间隔')
  }

  // ── 卡片批量模式 ──
  const isCardSelected = (device: EdgeDevice): boolean => selectedDevices.value.some(d => d.id === device.id)
  const toggleCardSelection = (device: EdgeDevice) => {
    if (!batchMode.value) return
    selectedDevices.value = isCardSelected(device)
      ? selectedDevices.value.filter(d => d.id !== device.id)
      : [...selectedDevices.value, device]
  }
  /** 点击卡片切换选中；但点内部按钮/输入等交互元素时不切换（否则点「详情」会顺带选中）。 */
  const onCardClick = (device: EdgeDevice, event?: MouseEvent) => {
    if (!batchMode.value) return
    const target = event?.target as HTMLElement | null
    if (target?.closest('button, a, input, label, .el-checkbox, .el-switch')) return
    toggleCardSelection(device)
  }
  const onCardKeydown = (device: EdgeDevice, event?: KeyboardEvent) => {
    if (!batchMode.value) return
    event?.preventDefault?.()
    toggleCardSelection(device)
  }


  return {
    batchMode,
    batchSubmitting,
    batchIntervalDialogVisible,
    batchIntervalMs,
    batchIntervalError,
    batchEnable,
    batchDisable,
    openBatchIntervalDialog,
    confirmBatchInterval,
    isCardSelected,
    toggleCardSelection,
    onCardClick,
    onCardKeydown,
  }
}
