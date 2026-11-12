import { ref } from 'vue'
import type { DeviceConfig } from '@/api/deviceConfig'

/**
 * 模板的「预览 / 表单对话框状态 + 表单成功回调」（拆分自页面原脚本，逻辑一字未改）。
 *
 * 边界依据：这一层只管"哪条 config 正在被预览/编辑"与"表单提交成功后怎么收尾"，
 * 不做接口分发，也不持有列表状态（刷新通过注入的 fetchConfigs 回调完成）。
 * 「更多操作」的分发与危险确认**留在页面**：listPageConventions.spec.ts 的
 * 「危险操作必须二次确认」按**页面文件**扫描 deviceConfigApi.delete 与
 * feedback.confirmDanger，搬进这里会让该页退出那条规则的射程。
 */
export function useDeviceConfigActions(options: { fetchConfigs: () => Promise<void> }) {
  const { fetchConfigs } = options

  // 对话框
  const showFormDialog = ref(false)
  const previewVisible = ref(false)
  const editingConfig = ref<DeviceConfig | null>(null)
  const previewConfig = ref<DeviceConfig | null>(null)

  // 预览
  const handlePreview = (config: DeviceConfig) => {
    previewConfig.value = config
    previewVisible.value = true
  }

  // 编辑
  const handleEdit = (config: DeviceConfig) => {
    editingConfig.value = { ...config }
    showFormDialog.value = true
  }

  // 表单成功
  const handleFormSuccess = () => {
    showFormDialog.value = false
    editingConfig.value = null
    fetchConfigs()
  }

  return {
    showFormDialog,
    previewVisible,
    editingConfig,
    previewConfig,
    handlePreview,
    handleEdit,
    handleFormSuccess,
  }
}
