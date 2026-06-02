<template>
  <el-dialog
    v-model="dialogVisible"
    title="OTA 固件升级"
    width="600px"
    :close-on-click-modal="false"
  >
    <el-form :model="form" :rules="rules" label-width="120px" ref="formRef">
      <el-form-item label="固件版本" prop="firmware_id">
        <el-select
          v-model="form.firmware_id"
          placeholder="请选择固件版本"
          style="width: 100%;"
          :loading="firmwaresLoading"
        >
          <el-option
            v-for="fw in firmwares"
            :key="fw.id"
            :label="`${fw.version} - ${fw.name}`"
            :value="fw.id"
          >
            <div style="display: flex; justify-content: space-between; align-items: center;">
              <span>{{ fw.version }} - {{ fw.name }}</span>
              <span style="color: #909399; font-size: 12px;">{{ getModelName(fw.model) }}</span>
            </div>
          </el-option>
        </el-select>
      </el-form-item>

      <el-form-item label="当前版本">
        <el-input :value="currentVersion" disabled />
      </el-form-item>

      <el-form-item label="固件信息" v-if="selectedFirmware">
        <el-descriptions :column="2" border size="small">
          <el-descriptions-item label="文件名">
            {{ selectedFirmware.name }}
          </el-descriptions-item>
          <el-descriptions-item label="文件大小">
            {{ formatFileSize(selectedFirmware.file_size) }}
          </el-descriptions-item>
          <el-descriptions-item label="MD5">
            {{ selectedFirmware.file_md5 }}
          </el-descriptions-item>
          <el-descriptions-item label="更新日志">
            <div style="max-height: 100px; overflow-y: auto;">
              {{ selectedFirmware.changelog }}
            </div>
          </el-descriptions-item>
        </el-descriptions>
      </el-form-item>

      <el-divider v-if="upgradeStatus !== 'idle'" content-position="left">
        升级状态
      </el-divider>

      <!-- 升级进度 -->
      <div v-if="upgradeStatus !== 'idle'" class="upgrade-progress">
        <el-progress
          :percentage="progress"
          :status="progressStatus"
          :stroke-width="18"
        >
          <template #default="{ percentage }">
            <span v-if="percentage < 100">{{ percentage }}%</span>
            <span v-else>{{ percentage }}% - 完成</span>
          </template>
        </el-progress>

        <p class="status-text">
          {{ statusText }}
        </p>

        <el-timeline style="margin-top: 20px;">
          <el-timeline-item
            v-for="(log, index) in upgradeLogs"
            :key="index"
            :timestamp="log.time"
            placement="top"
            size="small"
          >
            <div>{{ log.message }}</div>
          </el-timeline-item>
        </el-timeline>
      </div>
    </el-form>

    <template #footer>
      <el-button @click="dialogVisible = false" :disabled="upgradeStatus === 'uploading' || upgradeStatus === 'upgrading'">
        取消
      </el-button>
      <el-button
        type="primary"
        :loading="upgradeStatus === 'uploading' || upgradeStatus === 'upgrading'"
        :disabled="!form.firmware_id || upgradeStatus === 'completed'"
        @click="handleStart"
      >
        {{ upgradeStatus === 'completed' ? '已完成' : '开始升级' }}
      </el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { ref, reactive, computed, watch, onUnmounted } from 'vue'
import { ElMessage } from 'element-plus'
import { collectorApi } from '@/api/collector'
import { firmwareApi, type Firmware } from '@/api/firmware'
import { formatFileSize } from '@/utils/format'
import { useWebSocketStore } from '@/stores/websocket'

const props = withDefaults(defineProps<{
  visible: boolean
  collectorId: number
  collectorModel?: string
  currentFirmwareVersion?: string
}>(), {
  visible: false,
  collectorId: 0,
  collectorModel: 'ESP32-S3',
  currentFirmwareVersion: ''
})

const emit = defineEmits<{
  (e: 'success'): void
  (e: 'update:visible', val: boolean): void
}>()

const formRef = ref()
const dialogVisible = computed({
  get: () => props.visible,
  set: (val) => {
    emit('update:visible', val)
    if (!val) {
      // 重置状态
      form.firmware_id = null
      progress.value = 0
      upgradeStatus.value = 'idle'
      upgradeLogs.value = []
    }
  }
})

const form = reactive({
  firmware_id: null as number | null
})

const firmwares = ref<Firmware[]>([])
const firmwaresLoading = ref(false)
const selectedFirmware = ref<Firmware | null>(null)
const currentVersion = computed(() => props.currentFirmwareVersion || '-')

const progress = ref(0)
const upgradeStatus = ref<'idle' | 'uploading' | 'upgrading' | 'completed' | 'failed'>('idle')
const statusText = ref('')
const upgradeLogs = ref<Array<{time: string, message: string}>>([])
const progressStatus = computed<'success' | 'exception' | undefined>(() => {
  if (upgradeStatus.value === 'completed') return 'success'
  if (upgradeStatus.value === 'failed') return 'exception'
  return undefined
})

// 设备型号映射
const MODEL_NAMES: Record<string, string> = {
  'ESP32-S3': 'ESP32-S3',
  'ESP32-S2': 'ESP32-S2',
  'ESP32-C3': 'ESP32-C3'
}

const rules = {
  firmware_id: [{ required: true, message: '请选择固件版本', trigger: 'change' }]
}

// 获取型号名称
const getModelName = (model: string): string => {
  return MODEL_NAMES[model] || model
}

// 获取固件列表
const fetchFirmwares = async () => {
  firmwaresLoading.value = true
  try {
    // 获取所有活跃固件，不按 model 过滤
    const response = await firmwareApi.getList({
      status: 'active'
    })
    firmwares.value = Array.isArray(response) ? response : []
    
    if (firmwares.value.length === 0) {
      ElMessage.warning('暂无可用固件，请先上传固件')
    }
  } catch (error: any) {
    ElMessage.error('获取固件列表失败: ' + (error.message || '未知错误'))
  } finally {
    firmwaresLoading.value = false
  }
}

// 监听选中的固件
watch(() => form.firmware_id, (newVal) => {
  if (newVal) {
    const fw = firmwares.value.find(f => f.id === newVal)
    if (fw) {
      selectedFirmware.value = fw
    }
  } else {
    selectedFirmware.value = null
  }
})

// 开始升级
const wsStore = useWebSocketStore()
let unsubscribeOTA: (() => void) | null = null
let otaTaskId: string | null = null

const handleOTAProgress = (message: any) => {
  const p = message.payload
  if (!p) return
  // 匹配我们的 ota_id
  if (otaTaskId && p.ota_id && p.ota_id !== otaTaskId) return
  // 匹配我们的 device_id
  const deviceId = String(props.collectorId)
  if (p.device_id && p.device_id !== deviceId) return

  const pct = p.progress ?? 0
  progress.value = pct
  const status = p.status

  if (status === 'downloading') {
    statusText.value = '正在下载固件...'
    addLog(`下载进度 ${pct}%`)
  } else if (status === 'verifying') {
    statusText.value = '正在校验固件...'
    addLog('SHA256 校验中...')
  } else if (status === 'installing') {
    statusText.value = '正在安装固件...'
    addLog(`安装进度 ${pct}%`)
  } else if (status === 'success' || pct >= 100) {
    upgradeStatus.value = 'completed'
    progress.value = 100
    statusText.value = '升级完成'
    addLog('升级完成！请等待设备重启...')
    if (unsubscribeOTA) {
      unsubscribeOTA()
      unsubscribeOTA = null
    }
    emit('success')
    setTimeout(() => { dialogVisible.value = false }, 3000)
  } else if (status === 'failed' || status === 'error') {
    upgradeStatus.value = 'failed'
    statusText.value = '升级失败'
    addLog(`错误: OTA 任务失败`)
    if (unsubscribeOTA) {
      unsubscribeOTA()
      unsubscribeOTA = null
    }
  } else if (pct < 50) {
    statusText.value = '正在准备升级...'
  } else if (pct < 90) {
    statusText.value = '正在升级中...'
  }
}

const handleStart = async () => {
  if (!form.firmware_id || !selectedFirmware.value) {
    ElMessage.warning('请先选择固件版本')
    return
  }

  try {
    upgradeStatus.value = 'uploading'
    statusText.value = '正在创建 OTA 任务...'
    progress.value = 0
    upgradeLogs.value = []
    addLog('开始 OTA 升级')

    const otaRecord = await collectorApi.startOTA(props.collectorId, form.firmware_id)
    otaTaskId = otaRecord.ota_id
    addLog(`OTA 任务已创建: ${otaTaskId}`)

    // 订阅 WebSocket ota_progress 事件
    if (!wsStore.connected) {
      wsStore.connect()
    }
    unsubscribeOTA = wsStore.subscribe('ota_progress', handleOTAProgress)

    upgradeStatus.value = 'upgrading'
    statusText.value = '正在等待设备开始升级...'
  } catch (error: any) {
    ElMessage.error(error.message || '启动升级失败')
    upgradeStatus.value = 'failed'
    statusText.value = '升级失败'
    addLog(`错误: ${error.message || '未知错误'}`)
  }
}

onUnmounted(() => {
  if (unsubscribeOTA) {
    unsubscribeOTA()
    unsubscribeOTA = null
  }
})

// 添加日志
const addLog = (message: string) => {
  const time = new Date().toLocaleTimeString('zh-CN')
  upgradeLogs.value.unshift({ time, message })
}

watch(() => props.visible, (newVal) => {
  if (newVal) {
    fetchFirmwares()
  }
})
</script>

<style scoped>
.upgrade-progress {
  margin-top: 20px;
  padding: 20px;
  background: #f5f7fa;
  border-radius: 4px;
}

.status-text {
  text-align: center;
  margin-top: 10px;
  color: #606266;
}

:deep(.el-timeline-item__timestamp) {
  color: #909399;
}
</style>
