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
          :disabled="!device || (device.status !== 'online' && device.status !== 'active')"
        >
          同步到HA
        </el-button>
        <el-button
          v-if="canChangeAddress"
          type="warning"
          size="small"
          @click="showAddressDialog = true"
        >
          修改地址
        </el-button>
        <el-tooltip
          v-else-if="device"
          content="该设备型号不支持地址修改"
          placement="top"
        >
          <el-button type="warning" size="small" disabled>
            修改地址
          </el-button>
        </el-tooltip>
        <el-button
          v-if="device?.status === 'online' || device?.status === 'active'"
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

    <!-- 修改地址对话框 -->
    <el-dialog v-model="showAddressDialog" title="修改设备地址" width="400px" align-center>
      <el-form label-width="80px">
        <el-form-item label="当前地址">
          <el-tag>{{ device?.hardware_id || 'N/A' }}</el-tag>
        </el-form-item>
        <el-form-item label="新地址" required>
          <el-input-number v-model="newAddress" :min="1" :max="247" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showAddressDialog = false">取消</el-button>
        <el-button type="primary" :loading="changingAddress" @click="handleChangeAddress">
          确认修改
        </el-button>
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
        <el-descriptions-item label="健康状态">
          <StatusBadge :status="device.status" effect="dark" />
        </el-descriptions-item>
        <el-descriptions-item label="最后数据时间">
          {{ formatTime(device.last_data_time) }}
        </el-descriptions-item>
        <el-descriptions-item label="错误码" v-if="device.last_error_code && device.last_error_code > 0">
          <el-tag type="danger" size="small">
            {{ device.last_error_code }} - {{ getErrorInfo(device.last_error_code).label }}
          </el-tag>
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
          <el-tag v-if="device?.status === 'online' || device?.status === 'active'" type="success" size="small">
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

    <!-- 指令频率管理 -->
    <el-card style="margin-top: 20px;" v-if="device?.device_type">
      <template #header>
        <span>指令频率</span>
      </template>
      <CommandList :device-id="deviceId" :device-type="device.device_type" />
    </el-card>

    <el-card style="margin-top: 20px;" v-if="hasConfigOperations">
      <template #header>
        <div style="display: flex; justify-content: space-between; align-items: center;">
          <span>设备操作</span>
          <el-tag v-if="isDeviceOffline" type="info" size="small">设备离线，操作已禁用</el-tag>
        </div>
      </template>
      <div class="operation-buttons">
        <template v-for="(op, opKey) in configOperations" :key="opKey">
          <!-- 无参数操作：直接按钮 -->
          <el-button
            v-if="!op.params || op.params.length === 0"
            :type="op.type === 'read' ? 'primary' : 'warning'"
            :loading="operationLoading[opKey]"
            :disabled="isDeviceOffline"
            @click="executeConfigOperation(opKey, op)"
          >
            {{ op.label }}
          </el-button>
          <!-- 有参数操作：按钮弹出参数对话框 -->
          <el-button
            v-else
            :type="op.type === 'read' ? 'primary' : 'warning'"
            :loading="operationLoading[opKey]"
            :disabled="isDeviceOffline"
            @click="openOperationDialog(opKey, op)"
          >
            {{ op.label }}
          </el-button>
        </template>
      </div>
    </el-card>

    <!-- 设备操作参数对话框 -->
    <el-dialog v-model="opDialogVisible" :title="opDialogTitle" width="420px" align-center>
      <el-form ref="opFormRef" :model="opParamValues" label-width="100px">
        <el-form-item
          v-for="param in opDialogParams"
          :key="param.name"
          :label="param.label || param.name"
          required
        >
          <el-input-number
            v-if="param.type === 'uint8' || param.type === 'uint16' || param.type === 'int8' || param.type === 'int16' || param.type === 'int32' || param.type === 'uint32' || param.type === 'float'"
            v-model="opParamValues[param.name]"
            :min="param.min ?? 0"
            :max="param.max ?? getDefaultMax(param.type)"
            :step="param.step ?? 1"
            style="width: 100%;"
          />
          <el-select
            v-else-if="param.type === 'enum'"
            v-model="opParamValues[param.name]"
            placeholder="请选择"
            style="width: 100%;"
          >
            <el-option
              v-for="opt in param.options"
              :key="opt.value"
              :label="opt.label"
              :value="opt.value"
            />
          </el-select>
          <el-input
            v-else
            v-model="opParamValues[param.name]"
            :placeholder="`请输入${param.label || param.name}`"
          />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="opDialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="opDialogLoading" @click="submitOperationDialog">确定</el-button>
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
import { ref, reactive, computed, onMounted, onUnmounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { FormInstance } from 'element-plus'
import { Refresh, Connection, Edit, Delete, Download } from '@element-plus/icons-vue'
import PageHeader from '@/components/common/PageHeader.vue'
import StatusBadge from '@/components/common/StatusBadge.vue'
import LineChart from '@/components/charts/LineChart.vue'
import RealtimeDataList, { type DataItem } from '@/components/data/RealtimeDataList.vue'
import CommandList from '@/components/device/CommandList.vue'
import { edgeDeviceApi, type EdgeDevice, type ExecuteOperationResponse } from '@/api/edgeDevice'
import { type OperationDef, type OperationParam } from '@/api/deviceConfig'
import { getErrorInfo } from '@/utils/errorCode'
import { haApi } from '@/api/homeassistant'
import client from '@/api/client'
import { useWebSocketStore, type WebSocketMessage } from '@/stores/websocket'
import { WS_EVENT } from '@/events/events'
import { logger } from '@/utils/logger'
import { sensorNameMap, sensorUnitMap, SENSOR_ORDER } from '@/utils/sensor'
import { getDeviceTypeLabel } from '@/utils/deviceType'

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

const deviceId = Number(route.params.id)

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

// 修改地址相关
const showAddressDialog = ref(false)
const newAddress = ref(1)
const changingAddress = ref(false)

// 判断是否支持地址修改
const canChangeAddress = computed(() => {
  const dc = device.value?.device_config
  if (!dc?.config) return false
  try {
    const cfg = typeof dc.config === 'string' ? JSON.parse(dc.config) : dc.config
    return !!cfg?.change_address_command
  } catch { return false }
})

async function handleChangeAddress() {
  if (!device.value) return
  changingAddress.value = true
  try {
    await edgeDeviceApi.changeAddress(device.value.id, newAddress.value)
    ElMessage.success(`地址已修改为 ${newAddress.value}`)
    showAddressDialog.value = false
    await fetchDeviceDetail()
  } catch (e: any) {
    ElMessage.error('修改失败: ' + (e.message || '未知错误'))
  } finally {
    changingAddress.value = false
  }
}

// WebSocket — use global store connection (MainLayout manages lifecycle)
const wsStore = useWebSocketStore()
const wsConnected = computed(() => wsStore.connected)

const deviceTypeText = computed(() => {
  return device.value ? getDeviceTypeLabel(device.value.device_type) : ''
})

// 设备操作定义（从 DeviceConfig.operations 动态获取）
const configOperations = computed<Record<string, OperationDef>>(() => {
  const dc = device.value?.device_config
  if (!dc) return {}
  // operations may be nested under dc.config or directly on dc
  let ops = dc.operations
  if (!ops && dc.config) {
    try {
      const cfg = typeof dc.config === 'string' ? JSON.parse(dc.config) : dc.config
      ops = cfg?.operations
    } catch {
      console.warn('device_config.config JSON parse failed')
      return {}
    }
  }
  if (!ops || typeof ops !== 'object') return {}
  return ops as Record<string, OperationDef>
})

const hasConfigOperations = computed(() => {
  return Object.keys(configOperations.value).length > 0
})

const isDeviceOffline = computed(() => {
  if (!device.value) return true
  return device.value.status !== 'online' && device.value.status !== 'active'
})

// 操作 loading 状态
const operationLoading = reactive<Record<string, boolean>>({})

// 操作参数对话框
const opDialogVisible = ref(false)
const opDialogTitle = ref('')
const opDialogParams = ref<OperationParam[]>([])
const opParamValues = ref<Record<string, number | string>>({})
const opDialogLoading = ref(false)
const currentOpKey = ref('')
const opFormRef = ref<FormInstance>()

function getDefaultMax(type: string): number {
  const map: Record<string, number> = {
    uint8: 255, uint16: 65535, int8: 127, int16: 32767, int32: 2147483647, uint32: 4294967295
  }
  return map[type] ?? 255
}

function openOperationDialog(opKey: string, op: OperationDef) {
  currentOpKey.value = opKey
  opDialogTitle.value = op.label
  opDialogParams.value = op.params || []
  // 初始化参数默认值
  const defaults: Record<string, number | string> = {}
  for (const p of op.params || []) {
    if (p.type === 'enum' && p.options && p.options.length > 0) {
      defaults[p.name] = p.options[0].value
    } else if (p.type === 'uint8' || p.type === 'uint16' || p.type === 'int8' || p.type === 'int16' || p.type === 'int32' || p.type === 'uint32' || p.type === 'float') {
      defaults[p.name] = p.min ?? (p.default !== undefined ? p.default : 0)
    } else if (p.default !== undefined) {
      defaults[p.name] = p.default
    } else {
      defaults[p.name] = ''
    }
  }
  opParamValues.value = defaults
  opDialogVisible.value = true
}

async function submitOperationDialog() {
  const opKey = currentOpKey.value
  const op = configOperations.value[opKey]
  if (!op || !device.value) return

  // M4: 表单验证
  if (opFormRef.value) {
    try {
      await opFormRef.value.validate()
    } catch {
      return // validation failed
    }
  }

  opDialogLoading.value = true
  operationLoading[opKey] = true
  try {
    const id = Number(route.params.id)
    const result = await edgeDeviceApi.executeOperation(id, opKey, opParamValues.value)
    await handleOperationResult(opKey, op, result)
    opDialogVisible.value = false
  } catch (error: any) {
    ElMessage.error(error.message || '操作执行失败')
  } finally {
    opDialogLoading.value = false
    operationLoading[opKey] = false
  }
}

async function executeConfigOperation(opKey: string, op: OperationDef) {
  if (!device.value) return

  // write 类型需要确认
  if (op.type === 'write') {
    try {
      await ElMessageBox.confirm(
        `确定要执行"${op.label}"操作吗？`,
        '确认操作',
        { confirmButtonText: '确定', cancelButtonText: '取消', type: 'warning' }
      )
    } catch {
      return // 用户取消
    }
  }

  operationLoading[opKey] = true
  try {
    const id = Number(route.params.id)
    const result = await edgeDeviceApi.executeOperation(id, opKey)
    await handleOperationResult(opKey, op, result)
  } catch (error: any) {
    ElMessage.error(error.message || '操作执行失败')
  } finally {
    operationLoading[opKey] = false
  }
}

async function handleOperationResult(opKey: string, op: OperationDef, result: ExecuteOperationResponse) {
  if (op.type === 'write') {
    ElMessage.success('命令已发送')
    // M6: 操作成功后刷新设备数据
    await fetchDeviceDetail()
  } else {
    // read 类型：显示返回值
    const value = result?.value ?? result?.data?.value
    const unit = result?.unit ?? result?.data?.unit ?? ''
    if (value !== undefined && value !== null) {
      ElMessage.success(`查询结果: ${value}${unit ? ' ' + unit : ''}`)
    } else {
      ElMessage.success('查询成功')
    }
  }
}

// WebSocket 消息处理
function handleWebSocketMessage(message: WebSocketMessage) {
  const isDataEvent = message.type === WS_EVENT.DATA_UPDATE || message.type === 'channel_data'
  if (!isDataEvent) return

  // C6 fix: route.params.id is string, WS payload device_id may be string or number.
  const deviceId: number = Number(route.params.id)
  // Payload may be nested under message.payload or flat on message
  const p = message.payload || message
  const msgDeviceId: number = Number(p.sensor_device_id || p.edge_device_id || p.device_id)
  if (isNaN(msgDeviceId) || msgDeviceId !== deviceId) return

  // Get parsed sensor data
  const sensorData = p.data || p.sensors

  if (sensorData && realtimeListRef.value) {
    const dataItem: DataItem = {
      id: `${Date.now()}-${Math.random().toString(36).substr(2, 9)}`,
      timestamp: p.collected_at || (p.timestamp ? new Date(p.timestamp * 1000).toISOString() : new Date().toISOString()),
      data: sensorData,
      rawData: p.raw_hex || p.raw_data,
      isRealtime: true
    }
    realtimeListRef.value.addData(dataItem)
    logger.debug('收到实时数据', { deviceId, sensorData })
  }

  // Update device last_data_time only when actual sensor data is received
  if (device.value && sensorData && Object.keys(sensorData).length > 0) {
    device.value.last_data_time = new Date().toISOString()
  }
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
      // latest-data API returns DeviceData with data_json (string) containing sensors
      let parsedData = response.data || {}
      if (response.data_json) {
        try {
          const dj = JSON.parse(response.data_json)
          // data_json.sensors is [{Name, Value, Unit}], convert to {name: value}
          if (dj.sensors && Array.isArray(dj.sensors)) {
            parsedData = {}
            for (const s of dj.sensors) {
              parsedData[s.Name] = s.Value
            }
          }
        } catch (e) { /* ignore parse error */ }
      }
      const dataItem: DataItem = {
        id: `latest-${Date.now()}`,
        timestamp: response.created_at || new Date().toISOString(),
        data: parsedData,
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

      // S6 fix: use shared sensorUnitMap instead of local duplicate
      chartSeries.value = categories
        .map((cat, i) => ({
          name: sensorNameMap[cat] || cat,
          unit: sensorUnitMap[cat] || '',
          data: (seriesResults[i] as any[]).map((item: any) => ({
            time: item.created_at || item.timestamp,
            value: item.value
          }))
        }))
        .filter(s => s.data.length > 0)

      historyData.value = []
    } else {
      // S6 fix: use shared sensorNameMap/sensorUnitMap instead of local duplicates
      const knownCategories = [...SENSOR_ORDER, 'illuminance', 'uv_index', 'rain_intensity', 'rain_accum', 'voltage', 'current', 'power', 'energy', 'soc', 'soh', 'frequency']

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
          name: sensorNameMap[r.cat] || r.cat,
          unit: sensorUnitMap[r.cat] || '',
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

let unsubscribe: (() => void) | null = null

onMounted(() => {
  fetchDeviceDetail()
  fetchLatestData()
  fetchHistoryData()

  // Subscribe to WS data_update events via global store
  unsubscribe = wsStore.subscribe(WS_EVENT.DATA_UPDATE, handleWebSocketMessage)
})

onUnmounted(() => {
  if (unsubscribe) unsubscribe()
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