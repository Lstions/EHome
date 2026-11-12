import { computed, reactive, ref, watch } from 'vue'
import { deviceConfigApi, type DeviceConfig } from '@/api/deviceConfig'
import feedback from '@/utils/feedback'

/**
 * 模板列表的「状态 + 请求 + 筛选/统计」层（拆分自页面原脚本，逻辑与文案一字未改）。
 *
 * 边界依据：这一组 ref 只描述「后端列表 → 当前页切片 → 筛选结果 → 统计卡」，
 * 不碰页面 DOM、不碰对话框、不碰单条操作；页面只把返回值接进模板。
 * watch 仍在本 composable 内注册：typeFilter/hardwareFilter/statusFilter 的变更
 * 必须逐次触发「回到第 1 页 + 重新请求」，注册时机与拆分前相同（setup 期间）。
 * 统计口径（F14 范围词）的说明留在页面 —— 那里才有 <StatCard> 标签。
 */
export function useDeviceConfigList() {
  // 状态
  const loading = ref(false)
  const configs = ref<DeviceConfig[]>([])
  const currentPage = ref(1)
  const pageSize = ref(12)
  const total = ref(0)
  const searchKeyword = ref('')
  const typeFilter = ref('')
  const hardwareFilter = ref('')
  const statusFilter = ref('')
  const hasActiveFilters = computed(() => Boolean(searchKeyword.value || typeFilter.value || hardwareFilter.value || statusFilter.value))

  // 统计数据
  const stats = reactive({
    total: 0,
    active: 0,
    busTypes: 0,
    deviceTypes: 0
  })

  // 过滤后的配置
  const filteredConfigs = computed(() => {
    let result = configs.value

    if (searchKeyword.value) {
      const kw = searchKeyword.value.toLowerCase()
      result = result.filter(c => c.name?.toLowerCase().includes(kw))
    }

    if (typeFilter.value) {
      result = result.filter(c => c.device_type === typeFilter.value)
    }

    if (hardwareFilter.value) {
      result = result.filter(c => c.hardware_type === hardwareFilter.value)
    }

    if (statusFilter.value) {
      result = result.filter(c => c.status === statusFilter.value)
    }

    return result
  })

  // 获取配置列表
  const fetchConfigs = async () => {
    loading.value = true
    try {
      const response = await deviceConfigApi.getList({
        device_type: typeFilter.value || undefined,
        hardware_type: hardwareFilter.value || undefined,
        page: currentPage.value,
        page_size: pageSize.value
      })

      configs.value = response.items || []
      total.value = response.total || 0
      updateStats()
    } catch (error: any) {
      feedback.handleError(error, '获取配置模板列表失败')
    } finally {
      loading.value = false
    }
  }

  // 更新统计
  // total 用服务端返回的全量总数；其余指标仅有本页数据，保持本页口径（标签已注明"本页"）
  const updateStats = () => {
    stats.total = total.value
    stats.active = configs.value.filter(c => c.status === 'active').length

    const hardwareTypes = new Set(configs.value.map(c => c.hardware_type))
    stats.busTypes = hardwareTypes.size

    const deviceTypes = new Set(configs.value.map(c => c.device_type))
    stats.deviceTypes = deviceTypes.size
  }

  // 搜索
  const handleSearch = () => {
    handleFiltersChanged()
  }

  const handleFiltersChanged = () => {
    currentPage.value = 1
    fetchConfigs()
  }

  const clearFilters = () => {
    searchKeyword.value = ''
    typeFilter.value = ''
    hardwareFilter.value = ''
    statusFilter.value = ''
    currentPage.value = 1
    fetchConfigs()
  }

  watch([typeFilter, hardwareFilter, statusFilter], handleFiltersChanged)

  return {
    loading,
    configs,
    currentPage,
    pageSize,
    total,
    searchKeyword,
    typeFilter,
    hardwareFilter,
    statusFilter,
    hasActiveFilters,
    stats,
    filteredConfigs,
    fetchConfigs,
    handleSearch,
    handleFiltersChanged,
    clearFilters,
  }
}
