import { ref } from 'vue'
import { ElMessage } from 'element-plus'
import { edgeDeviceApi, type EdgeDevice } from '@/api/edgeDevice'

/**
 * 设备删除 composable — 单删 + 批量删除
 * 封装删除确认弹窗状态、API 调用、结果处理
 */
export function useDeviceDelete(options: {
  onSuccess?: (deletedIds: number[], deleteData: boolean) => void
} = {}) {
  const { onSuccess } = options

  // 单删
  const showDeleteDialog = ref(false)
  const deletingDevice = ref<EdgeDevice | null>(null)
  const deleteSubmitting = ref(false)

  // 批量删除
  const showBatchDeleteDialog = ref(false)
  const batchDeleteSubmitting = ref(false)
  const selectedDevices = ref<EdgeDevice[]>([])

  const handleDelete = (device: EdgeDevice) => {
    deletingDevice.value = device
    showDeleteDialog.value = true
  }

  const confirmDelete = async (deleteData: boolean) => {
    const device = deletingDevice.value
    if (!device) return { success: false }

    deleteSubmitting.value = true
    let deleted = false
    try {
      await edgeDeviceApi.delete(device.id, { delete_data: deleteData })
      deleted = true
      ElMessage.success(deleteData ? '删除成功，历史数据将在后台删除' : '删除成功')
      showDeleteDialog.value = false
      onSuccess?.([device.id], deleteData)
      return { success: true, id: device.id }
    } catch {
      if (!deleted) {
        ElMessage.error('删除失败')
      }
      return { success: false }
    } finally {
      deleteSubmitting.value = false
    }
  }

  const handleBatchDelete = () => {
    if (selectedDevices.value.length === 0) return
    showBatchDeleteDialog.value = true
  }

  const confirmBatchDelete = async (deleteData: boolean) => {
    const ids = selectedDevices.value.map(d => d.id)
    if (ids.length === 0) {
      showBatchDeleteDialog.value = false
      return { success: false, succeeded: 0, failed: 0 }
    }

    batchDeleteSubmitting.value = true
    try {
      // 方案 v3.3 §2.2: 优先使用批量 API，失败时降级为逐条删除
      let succeeded = 0
      let failed = 0
      let succeededIds: number[] = []

      try {
        const batchResult = await edgeDeviceApi.batchDelete(ids, { delete_data: deleteData })
        succeeded = batchResult.succeeded
        failed = batchResult.failed
        succeededIds = batchResult.results.filter(r => r.success).map(r => r.id)
      } catch {
        // 批量 API 不可用或失败，降级为逐条删除
        const results = await Promise.allSettled(
          ids.map(id => edgeDeviceApi.delete(id, { delete_data: deleteData }))
        )
        succeeded = results.filter(r => r.status === 'fulfilled').length
        failed = results.length - succeeded
        succeededIds = ids.filter((_id, index) => results[index].status === 'fulfilled')
      }

      if (succeeded > 0) {
        ElMessage.success(`成功删除 ${succeeded} 个设备`)
        showBatchDeleteDialog.value = false
        onSuccess?.(succeededIds, deleteData)
      }
      if (failed > 0) {
        ElMessage.error(`${failed} 个设备删除失败`)
      }
      selectedDevices.value = []
      return { success: succeeded > 0, succeeded, failed, succeededIds }
    } catch {
      ElMessage.error('批量删除失败')
      return { success: false, succeeded: 0, failed: 0 }
    } finally {
      batchDeleteSubmitting.value = false
    }
  }

  const handleSelectionChange = (selection: EdgeDevice[]) => {
    selectedDevices.value = selection
  }

  return {
    // 单删
    showDeleteDialog,
    deletingDevice,
    deleteSubmitting,
    handleDelete,
    confirmDelete,
    // 批量删除
    showBatchDeleteDialog,
    batchDeleteSubmitting,
    selectedDevices,
    handleBatchDelete,
    confirmBatchDelete,
    handleSelectionChange,
  }
}
