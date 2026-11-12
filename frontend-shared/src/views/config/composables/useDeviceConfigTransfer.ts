import { type Ref } from 'vue'
import { ElMessage } from 'element-plus'
import feedback from '@/utils/feedback'
import { deviceConfigApi, type DeviceConfig } from '@/api/deviceConfig'

/**
 * 模板的导入 / 导出（单条、批量）—— 从 useDeviceConfigActions 再拆出。
 *
 * 边界依据：这三个动作只做「文件选择 / Blob 下载 + 一次接口调用」，
 * 不读对话框状态、不做分发；列表刷新通过注入的 fetchConfigs 回调完成。
 * 导入（选择文件 → 校验必要字段 → 建模板）本身就是独立的一块，
 * 与「更多操作」的分发及确认逻辑分开后，两边都能单独读。
 */
export function useDeviceConfigTransfer(options: {
  fetchConfigs: () => Promise<void>
  filteredConfigs: Ref<DeviceConfig[]>
}) {
  const { fetchConfigs, filteredConfigs } = options

  // 导出单个配置
  const exportConfig = (config: DeviceConfig) => {
    const data = JSON.stringify(config, null, 2)
    const blob = new Blob([data], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = `${config.name}.json`
    link.click()
    URL.revokeObjectURL(url)

    ElMessage.success('导出成功')
  }

  // 导入
  const importConfig = () => {
    const input = document.createElement('input')
    input.type = 'file'
    input.accept = '.json'
    input.onchange = async (e) => {
      const file = (e.target as HTMLInputElement).files?.[0]
      if (!file) return

      try {
        const text = await file.text()
        const config = JSON.parse(text)

        // 验证必要字段
        if (!config.name || !config.device_type || !config.hardware_type) {
          throw new Error('配置文件格式不正确')
        }

        delete config.id
        delete config.created_at
        delete config.updated_at
        config.is_default = false

        await deviceConfigApi.create(config)
        ElMessage.success('导入成功')
        await fetchConfigs()
      } catch (error: any) {
        feedback.handleError(error, '导入失败')
      }
    }
    input.click()
  }

  // 批量导出
  const exportConfigs = () => {
    if (filteredConfigs.value.length === 0) {
      ElMessage.warning('暂无配置可导出')
      return
    }

    const data = JSON.stringify(filteredConfigs.value, null, 2)
    const blob = new Blob([data], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = `device_configs_${Date.now()}.json`
    link.click()
    URL.revokeObjectURL(url)

    ElMessage.success('导出成功')
  }

  return { exportConfig, importConfig, exportConfigs }
}
