<template>
  <div class="device-detail">
    <PageHeader title="边缘设备详情" :show-back="true" @back="goBack">
      <template #extra>
        <el-tag v-if="wsConnected" type="success" size="small" style="margin-right: 8px;">
          <el-icon><Connection /></el-icon>
          实时连接
        </el-tag>
        <el-button
          :icon="Edit"
          @click="handleEdit"
        >
          编辑
        </el-button>
        <el-button
          :icon="Delete"
          type="danger"
          plain
          @click="handleDelete"
        >
          删除
        </el-button>
        <el-button
          :icon="Connection"
          @click="handleSyncToHA"
          :loading="syncingHA"
          :disabled="!device || device.status !== 'online'"
        >
          同步到HA
        </el-button>
        <el-button
          v-if="device?.status === 'online'"
          type="primary"
          :icon="Refresh"
          :loading="refreshing"
          @click="handleRefresh"
        >
          刷新数据
        </el-button>
      </template>
    </PageHeader>

    <!-- 编辑对话框 -->
    <el-dialog v-model="editDialogVisible" title="编辑边缘设备" width="500px">
      <el-form :model="editForm" label-width="80px">
        <el-form-item label="设备名称">
          <el-input v-model="editForm.name" />
        </el-form-item>
        <el-form-item label="设备类型">
          <el-input v-model="editForm.device_type" disabled />
        </el-form-item>
        <el-form-item label="通信协议">
          <el-input v-model="editForm.protocol" disabled />
        </el-form-item>
        <el-form-item label="硬件类型">
          <el-input v-model="editForm.hardware_type" disabled />
        </el-form-item>
        <el-form-item label="硬件ID">
          <el-input v-model="editForm.hardware_id" disabled />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="editDialogVisible = false">取消</el-button>
        <el-button type="primary" @click="submitEdit" :loading="editLoading">保存</el-button>
      </template>
    </el-dialog>

    <!-- 删除确认对话框 -->
    <el-dialog v-model="deleteDialogVisible" title="删除边缘设备" width="400px">
      <p style="margin: 0;">
        确定要删除边缘设备 <strong>{{ device?.name }}</strong> 吗？此操作不可恢复。
      </p>
      <template #footer>
        <el-button @click="deleteDialogVisible = false">取消</el-button>
        <el-button type="danger" @click="submitDelete" :loading="deleteLoading">删除</el-button>
      </template>
    </el-dialog>

    <el-card v-if="loading && !device">
      <el-skeleton :rows="4" animated />
    </el-card>
    <el-card v-else-if="device">
      <template #header>
        <span>基本信息</span>
      </template>

      <el-descriptions :column="2" border>
        <el-descriptions-item label="设备名称">
          {{ device.name }}
        </el-descriptions-item>
        <el-descriptions-item label="设备类型">
          {{ deviceTypeText }}
        </el-descriptions-item>
        <el-descriptions-item label="通信协议">
          {{ device.protocol?.toUpperCase() }}
        </el-descriptions-item>
        <el-descriptions-item label="硬件类型">
          {{ device.hardware_type?.toUpperCase() }}
        </el-descriptions-item>
        <el-descriptions-item label="硬件ID">
          {{ device.hardware_id }}
        </el-descriptions-item>
        <el-descriptions-item label="状态">
          <StatusBadge :status="device.status" />
        </el-descriptions-item>
        <el-descriptions-item label="最后数据时间">
          {{ formatTime(device.last_data_time) }}
        </el-descriptions-item>
        <el-descriptions-item label="采集状态" v-if="device.last_error_code">
          <el-tag :type="getErrorInfo(device.last_error_code).type" size="small">
            {{ getErrorInfo(device.last_error_code).label }}
          </el-tag>
        </el-descriptions-item>
      </el-descriptions>
    </el-card>

    <el-card style="margin-top: 20px;">
      <template #header>
        <div style="display: flex; justify-content: space-between; align-items: center;">
          <span>实时数据</span>
          <el-tag v-if="device?.status === 'online'" type="success" size="small">
            实时
          </el-tag>
        </div>
      </template>
      <RealtimeDataList
        ref="realtimeListRef"
        :max-items="200"
        :auto-scroll="true"
        :device-type="device?.device_type"
      />
    </el-card>

    <el-card style="margin-top: 20px;" v-if="hasOperations">
      <template #header>
        <span>设备操作</span>
      </template>
      <div class="operation-buttons">
        <el-button
          v-for="op in availableOperations"
          :key="op.key"
          :type="op.type"
          :icon="op.icon"
          :disabled="device?.status !== 'online'"
          @click="handleOperation(op.key, op.label)"
        >
          {{ op.label }}
        </el-button>
      </div>
    </el-card>

    <el-dialog v-model="pwmDialogVisible" title="PWM 参数设置" width="400px">
      <el-form label-width="80px">
        <el-form-item label="占空比">
          <el-input-number v-model="pwmForm.duty" :min="0" :max="100" :step="1" />
          <span style="margin-left: 8px">%</span>
        </el-form-item>
        <el-form-item label="频率">
          <el-input-number v-model="pwmForm.frequency" :min="1" :step="100" />
          <span style="margin-left: 8px">Hz</span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="pwmDialogVisible = false">取消</el-button>
        <el-button type="primary" @click="executePwmOperation">确定</el-button>
      </template>
    </el-dialog>

    <el-card style="margin-top: 20px;">
      <template #header>
        <div style="display: flex; justify-content: space-between; align-items: center;">
          <span>历史数据</span>
          <div class="time-range-selector">
            <el-radio-group v-model="timeRange" size="small" @change="handleTimeRangeChange">
              <el-radio-button value="1h">1小时</el-radio-button>
              <el-radio-button value="24h">24小时</el-radio-button>
              <el-radio-button value="7d">7天</el-radio-button>
              <el-radio-button value="custom">自定义</el-radio-button>
            </el-radio-group>
            <el-date-picker
              v-if="timeRange === 'custom'"
              v-model="customTimeRange"
              type="datetimerange"
              size="small"
              range-separator="至"
              start-placeholder="开始时间"
              end-placeholder="结束时间"
              format="YYYY-MM-DD HH:mm"
              value-format="YYYY-MM-DD HH:mm:ss"
              style="margin-left: 10px; width: 380px;"
              @change="fetchHistoryData"
            />
            <el-button
              size="small"
              type="primary"
              plain
              style="margin-left: 10px;"
              @click="handleExportCSV"
              :disabled="historyData.length === 0"
            >
              <el-icon><Download /></el-icon>
              导出CSV
            </el-button>
          </div>
        </div>
      </template>

      <el-skeleton v-if="chartLoading" :rows="8" animated />
      <template v-else>
        <div style="min-height: 400px;">
          <LineChart
            v-if="chartSeries.length > 0"
            :series="chartSeries"
            :title="`${deviceTypeText}数据趋势`"
            height="400px"
          />
          <LineChart
            v-else-if="historyData.length > 0"
            :data="historyData"
            :title="`${deviceTypeText}数据趋势`"
            height="400px"
          />
          <el-empty v-else description="暂无历史数据" />
        </div>
      </template>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Refresh, Connection, Top, Bottom, SetUp, Edit, Delete, Download } from '@element-plus/icons-vue'
import PageHeader from '@/components/common/PageHeader.vue'
import StatusBadge from '@/components/common/StatusBadge.vue'
import LineChart from '@/components/charts/LineChart.vue'
import RealtimeDataList, { type DataItem } from '@/components/data/RealtimeDataList.vue'
import { edgeDeviceApi, type EdgeDevice } from '@/api/edgeDevice'
import { getErrorInfo } from '@/utils/errorCode'
import { haApi } from '@/api/homeassistant'
import client from '@/api/client'
import { useWebSocket } from '@/composables/useWebSocket'
import { logger } from '@/utils/logger'

interface HistoryDataPoint {
  time: string
  value: number
}

interface SeriesData {
  name: string
  data: HistoryDataPoint[]
  unit?: string
}

const router = useRouter()
const route = useRoute()

const device = ref<EdgeDevice | null>(null)
const historyData = ref<HistoryDataPoint[]>([])
const chartSeries = ref<SeriesData[]>([])
const loading = ref(true)
const refreshing = ref(false)
const chartLoading = ref(true)
const syncingHA = ref(false)
const timeRange = ref('24h')
const customTimeRange = ref<[string, string] | null>(null)
const realtimeListRef = ref<InstanceType<typeof RealtimeDataList> | null>(null)

// 编辑相关
const editDialogVisible = ref(false)
const editLoading = ref(false)
const editForm = ref({
  name: '',
  device_type: '',
  protocol: '',
  hardware_type: '',
  hardware_id: ''
})

// 删除相关
const deleteDialogVisible = ref(false)
const deleteLoading = ref(false)

// WebSocket 连接
const wsUrl = `${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}/api/ws`
const { connected: wsConnected, connect: wsConnect, disconnect: wsDisconnect, send: wsSend } = useWebSocket(wsUrl, {
  onMessage: handleWebSocketMessage,
  onConnected: handleWebSocketConnected,
  onDisconnected: handleWebSocketDisconnected,
  onError: () => {
    logger.error('WebSocket 连接错误')
  }
})

const deviceTypeMap: Record<string, string> = {
  wind_speed: '风速传感器',
  wind_direction: '风向传感器',
  rain: '雨量传感器',
  light: '光照传感器',
  temp_humidity: '温湿度传感器',
  battery: '电池保护板',
  inverter: '光伏逆变器',
  bmp280: 'BMP280温压传感器',
  sht40: 'SHT40温湿度传感器'
}

const deviceTypeText = computed(() => {
  return device.value ? deviceTypeMap[device.value.device_type] || device.value.device_type : ''
})

// 设备操作定义
const operations: Record<string, Array<{key: string, label: string, type: string, icon: any}>> = {
  rain: [
    { key: 'reset_rain', label: '重置雨量', type: 'warning', icon: Refresh }
  ],
  battery: [
    { key: 'restart_battery', label: '重启保护板', type: 'danger', icon: Refresh }
  ],
  'gpio.digital': [
    { key: 'gpio_set_high', label: '输出高电平', type: 'success', icon: Top },
    { key: 'gpio_set_low', label: '输出低电平', type: 'danger', icon: Bottom },
    { key: 'gpio_toggle', label: '翻转', type: 'warning', icon: Refresh },
  ],
  'gpio.pwm': [
    { key: 'pwm_set', label: '设置PWM', type: 'primary', icon: SetUp },
  ]
}

const availableOperations = computed(() => {
  if (!device.value) return []
  return operations[device.value.device_type] || []
})

const hasOperations = computed(() => {
  return availableOperations.value.length > 0
})

// WebSocket 消息处理
function handleWebSocketMessage(message: any) {
  if (message.type === 'data' && message.payload) {
    const payload = message.payload
    const deviceId = Number(route.params.id)

    if (payload.device_id === deviceId) {
      const data = payload.data
      if (data && realtimeListRef.value) {
        const dataItem: DataItem = {
          id: `${Date.now()}-${Math.random().toString(36).substr(2, 9)}`,
          timestamp: data.created_at || new Date().toISOString(),
          data: data.data || data,
          rawData: data.raw_data,
          isRealtime: true
        }
        realtimeListRef.value.addData(dataItem)
        logger.debug('收到实时数据', { deviceId, data: data.data })
      }
    }
  }
}

function handleWebSocketConnected() {
  logger.info('WebSocket 已连接')
  const deviceId = Number(route.params.id)
  wsSend({
    action: 'subscribe',
    room: 'data',
    device_ids: [deviceId]
  })
  logger.debug('已订阅设备数据', { deviceId })
}

function handleWebSocketDisconnected() {
  logger.info('WebSocket 已断开')
}

// 获取时间范围
const getTimeRange = (): [Date, Date] => {
  const now = new Date()
  let startTime: Date

  switch (timeRange.value) {
    case '1h':
      startTime = new Date(now.getTime() - 60 * 60 * 1000)
      break
    case '24h':
      startTime = new Date(now.getTime() - 24 * 60 * 60 * 1000)
      break
    case '7d':
      startTime = new Date(now.getTime() - 7 * 24 * 60 * 60 * 1000)
      break
    case 'custom':
      if (customTimeRange.value) {
        return [new Date(customTimeRange.value[0]), new Date(customTimeRange.value[1])]
      }
      startTime = new Date(now.getTime() - 24 * 60 * 60 * 1000)
      break
    default:
      startTime = new Date(now.getTime() - 24 * 60 * 60 * 1000)
  }

  return [startTime, now]
}

const fetchDeviceDetail = async () => {
  const id = Number(route.params.id)
  if (!id) {
    ElMessage.error('无效的设备ID')
    goBack()
    return
  }

  loading.value = true
  try {
    device.value = await edgeDeviceApi.getDetail(id)
  } catch (error: any) {
    ElMessage.error('获取边缘设备详情失败')
  } finally {
    loading.value = false
  }
}

const fetchLatestData = async () => {
  const id = Number(route.params.id)
  if (!id) return

  try {
    const response = await edgeDeviceApi.getLatestData(id)
    if (response && realtimeListRef.value) {
      const dataItem: DataItem = {
        id: `latest-${Date.now()}`,
        timestamp: response.created_at || new Date().toISOString(),
        data: response.data || {},
        rawData: response.raw_data,
        isRealtime: false
      }
      realtimeListRef.value.addData(dataItem)
    }
  } catch (error: any) {
    logger.error('获取最新数据失败', { error: String(error) })
  }
}

const fetchHistoryData = async () => {
  const id = Number(route.params.id)
  if (!id) return

  const [startTime, endTime] = getTimeRange()
  const deviceType = device.value?.device_type

  chartLoading.value = true
  try {
    // BMP280 / SHT40 等统一数据设备：从 unified_data 表获取多 series
    if (['bmp280', 'sht40', 'temp_humidity'].includes(deviceType)) {
      const categories = deviceType === 'bmp280'
        ? ['temperature', 'pressure']
        : ['temperature', 'humidity']

      const seriesResults = await Promise.all(
        categories.map(cat =>
          client.get<unknown, any>('/api/v1/unified-data/historical', {
            params: { device_pk: id, category: cat, start_time: startTime.toISOString(), end_time: endTime.toISOString() }
          }).then(res => res.data || [])
        )
      )

      const unitMap: Record<string, string> = {
        temperature: '°C',
        pressure: 'hPa',
        humidity: '%'
      }

      chartSeries.value = categories
        .map((cat, i) => ({
          name: cat === 'temperature' ? '温度' : cat === 'pressure' ? '气压' : '湿度',
          unit: unitMap[cat] || '',
          data: (seriesResults[i] as any[]).map((item: any) => ({
            time: item.created_at || item.timestamp,
            value: item.value
          }))
        }))
        .filter(s => s.data.length > 0)

      historyData.value = []
    } else {
      // 其他设备：先尝试从 unified_data 获取
      const knownCategories = ['temperature', 'pressure', 'humidity', 'wind_speed', 'wind_direction', 'illuminance', 'uv_index', 'rain_intensity', 'rain_accum', 'voltage', 'current', 'power', 'energy', 'soc', 'soh', 'frequency']
      const unitMap: Record<string, string> = {
        temperature: '°C', pressure: 'hPa', humidity: '%',
        wind_speed: 'm/s', wind_direction: '°', illuminance: 'lux',
        voltage: 'V', current: 'A', power: 'W', energy: 'kWh',
        soc: '%', soh: '%', frequency: 'Hz'
      }
      const nameMap: Record<string, string> = {
        temperature: '温度', pressure: '气压', humidity: '湿度',
        wind_speed: '风速', wind_direction: '风向', illuminance: '光照',
        voltage: '电压', current: '电流', power: '功率', energy: '电量',
        soc: 'SOC', soh: 'SOH', frequency: '频率'
      }

      const seriesResults = await Promise.all(
        knownCategories.map(cat =>
          client.get<unknown, any>('/api/v1/unified-data/historical', {
            params: { device_pk: id, category: cat, start_time: startTime.toISOString(), end_time: endTime.toISOString() }
          }).then(res => ({ cat, data: res.data || [] }))
            .catch(() => ({ cat, data: [] }))
        )
      )

      const series = seriesResults
        .filter(r => r.data && r.data.length > 0)
        .map(r => ({
          name: nameMap[r.cat] || r.cat,
          unit: unitMap[r.cat] || '',
          data: r.data.map((item: any) => ({
            time: item.created_at || item.timestamp,
            value: item.value
          }))
        }))

      if (series.length > 0) {
        chartSeries.value = series
        historyData.value = []
        logger.debug('图表数据加载成功', { seriesCount: series.length })
      } else {
        // Fallback: 从 device_data 表获取
        const response = await edgeDeviceApi.getHistoryData(id, {
          start_time: startTime.toISOString(),
          end_time: endTime.toISOString(),
          page: 1,
          page_size: 1000
        })

        chartSeries.value = []
        if (response.items && response.items.length > 0) {
          const firstItem = response.items[0]
          const data = firstItem.data || {}
          const numericKeys = Object.keys(data).filter(key => key !== 'raw_data' && typeof data[key] === 'number')

          if (numericKeys.length > 1) {
            chartSeries.value = numericKeys.map(key => ({
              name: key,
              unit: '',
              data: response.items.map((item: any) => ({
                time: item.created_at || item.collected_at,
                value: item.data[key] ?? 0
              }))
            }))
            historyData.value = []
          } else {
            const valueKey = numericKeys[0] || Object.keys(data).find(key => typeof data[key] === 'number')
            if (valueKey) {
              historyData.value = response.items.map((item: any) => ({
                time: item.created_at || item.collected_at,
                value: item.data[valueKey]
              }))
            } else {
              historyData.value = []
            }
          }
        } else {
          historyData.value = []
        }
      }
    }
  } catch (error: any) {
    logger.error('获取历史数据失败', { error: String(error) })
    ElMessage.error('获取历史数据失败')
  } finally {
    chartLoading.value = false
  }
}

const handleRefresh = async () => {
  refreshing.value = true
  try {
    await fetchHistoryData()
    ElMessage.success('数据已刷新')
  } catch (error) {
    ElMessage.error('刷新失败')
  } finally {
    refreshing.value = false
  }
}

const handleTimeRangeChange = () => {
  if (timeRange.value !== 'custom') {
    fetchHistoryData()
  }
}

// 编辑边缘设备
const handleEdit = () => {
  if (!device.value) return
  editForm.value = {
    name: device.value.name,
    device_type: device.value.device_type,
    protocol: device.value.protocol,
    hardware_type: device.value.hardware_type || '',
    hardware_id: device.value.hardware_id
  }
  editDialogVisible.value = true
}

const submitEdit = async () => {
  if (!device.value) return

  editLoading.value = true
  try {
    await edgeDeviceApi.update(device.value.id, { name: editForm.value.name })
    ElMessage.success('设备信息已保存')
    editDialogVisible.value = false
    await fetchDeviceDetail()
  } catch (error: any) {
    ElMessage.error(error.message || '保存失败')
  } finally {
    editLoading.value = false
  }
}

// 删除边缘设备
const handleDelete = () => {
  deleteDialogVisible.value = true
}

const submitDelete = async () => {
  if (!device.value) return

  deleteLoading.value = true
  try {
    await edgeDeviceApi.delete(device.value.id)
    ElMessage.success('设备已删除')
    deleteDialogVisible.value = false
    router.replace('/edge-device')
  } catch (error: any) {
    ElMessage.error(error.message || '删除失败')
  } finally {
    deleteLoading.value = false
  }
}

// 导出 CSV
const handleExportCSV = () => {
  if (!device.value || historyData.value.length === 0) return

  const deviceName = device.value.name
  const rows = [['时间', '数值']]

  if (chartSeries.value.length > 0) {
    // 多系列：按时间戳对齐，缺失值填空
    const seriesNames = chartSeries.value.map(s => s.name)
    rows[0] = ['时间', ...seriesNames]

    // 收集所有唯一时间戳并排序
    const timeSet = new Set<number>()
    const seriesMap: Map<string, Map<number, number>> = new Map()
    chartSeries.value.forEach(s => {
      const valMap = new Map<number, number>()
      s.data.forEach((d: any) => {
        const ts = new Date(d.time).getTime()
        timeSet.add(ts)
        valMap.set(ts, d.value)
      })
      seriesMap.set(s.name, valMap)
    })

    const sortedTimes = Array.from(timeSet).sort((a, b) => a - b)
    sortedTimes.forEach(ts => {
      const row = [formatTime(new Date(ts))]
      seriesNames.forEach(name => {
        const val = seriesMap.get(name)?.get(ts)
        row.push(val !== undefined ? String(val) : '')
      })
      rows.push(row)
    })
  } else {
    // 单系列
    historyData.value.forEach(item => {
      rows.push([formatTime(item.time), String(item.value)])
    })
  }

  const csvContent = rows.map(row => row.map(cell => `"${cell}"`).join(',')).join('\n')
  const blob = new Blob(['\ufeff' + csvContent], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = `${deviceName}_history.csv`
  link.click()
  URL.revokeObjectURL(url)

  ElMessage.success('导出成功')
}

const handleOperation = async (operation: string, label: string) => {
  if (operation === 'pwm_set') {
    pwmDialogVisible.value = true
    pwmForm.value = { duty: 50, frequency: 1000 }
    currentPwmOperation.value = operation
    return
  }

  try {
    await ElMessageBox.confirm(
      `确定要执行"${label}"操作吗？`,
      '确认操作',
      {
        confirmButtonText: '确定',
        cancelButtonText: '取消',
        type: 'warning'
      }
    )

    const id = Number(route.params.id)
    await edgeDeviceApi.executeOperation(id, operation)
    ElMessage.success(`${label}命令已发送`)
    await fetchDeviceDetail()
  } catch (error: any) {
    if (error !== 'cancel') {
      ElMessage.error(error.message || '操作执行失败')
    }
  }
}

// PWM 参数
const pwmDialogVisible = ref(false)
const pwmForm = ref({ duty: 50, frequency: 1000 })
const currentPwmOperation = ref('')

const executePwmOperation = async () => {
  try {
    const id = Number(route.params.id)
    await edgeDeviceApi.executeOperation(id, currentPwmOperation.value, {
      duty: pwmForm.value.duty,
      frequency: pwmForm.value.frequency,
    })
    ElMessage.success('PWM 设置命令已发送')
    pwmDialogVisible.value = false
    await fetchDeviceDetail()
  } catch (error: any) {
    ElMessage.error(error.message || '操作失败')
  }
}

const handleSyncToHA = async () => {
  const id = Number(route.params.id)
  if (!id) return

  syncingHA.value = true
  try {
    await haApi.syncDevice(id)
    ElMessage.success('设备已同步到HomeAssistant')
  } catch (error: any) {
    ElMessage.error('同步到HomeAssistant失败: ' + (error.message || '未知错误'))
  } finally {
    syncingHA.value = false
  }
}

const formatTime = (time: string | null | undefined) => {
  if (!time || time === '0001-01-01T00:00:00Z' || time === '1970-01-01T00:00:00Z') return '-'
  const date = new Date(time)
  if (isNaN(date.getTime()) || date.getFullYear() <= 1970) return '-'
  return date.toLocaleString('zh-CN')
}

const goBack = () => {
  router.back()
}

onMounted(() => {
  fetchDeviceDetail()
  fetchLatestData()
  fetchHistoryData()

  wsConnect()
})

onUnmounted(() => {
  wsDisconnect()
})
</script>

<style scoped>
.device-detail {
  padding: 0;
}

.operation-buttons {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
}

.time-range-selector {
  display: flex;
  align-items: center;
}
</style>