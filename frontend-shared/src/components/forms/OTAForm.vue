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
            :label="`${fw.version} - ${fw.filename}`"
            :value="fw.id"
          >
            <div style="display: flex; justify-content: space-between; align-items: center;">
              <span>{{ fw.version }} - {{ fw.filename }}</span>
              <span style="color: var(--el-text-color-secondary); font-size: 12px;">{{ getModelName(fw.target_model) }}</span>
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
            {{ selectedFirmware.filename }}
          </el-descriptions-item>
          <el-descriptions-item label="文件大小">
            {{ formatFileSize(selectedFirmware.size_bytes) }}
          </el-descriptions-item>
          <el-descriptions-item label="MD5">
            {{ selectedFirmware.checksum }}
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
import { ref, reactive, computed, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { nodeApi } from '@/api/node'
import { firmwareApi, type Firmware } from '@/api/firmware'
import { useFirmwareStore } from '@/stores/firmware'
import { formatFileSize } from '@/utils/format'

const props = withDefaults(defineProps<{
  visible: boolean
  collectorId: string
  collectorModel?: string
  currentFirmwareVersion?: string
}>(), {
  visible: false,
  collectorId: '',
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
      if (progressTimer) { clearInterval(progressTimer); progressTimer = null }
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

// progress bar status
const progressStatus = computed(() => {
  if (upgradeStatus.value === 'failed') return 'exception'
  if (upgradeStatus.value === 'completed') return 'success'
  return ''
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
const firmwareStore = useFirmwareStore()
const fetchFirmwares = async () => {
  firmwaresLoading.value = true
  try {
    await firmwareStore.fetchList({ status: 'active' })
    firmwares.value = firmwareStore.list
    
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
const handleStart = async () => {
  if (!form.firmware_id || !selectedFirmware.value) {
    ElMessage.warning('请先选择固件版本')
    return
  }

  try {
    upgradeStatus.value = 'uploading'
    statusText.value = '正在创建OTA任务...'
    addLog('开始 OTA 升级')

    const otaRecord = await nodeApi.startOTA(props.collectorId, form.firmware_id)

    upgradeStatus.value = 'upgrading'
    statusText.value = '正在升级中...'
    addLog(`OTA 任务已创建，ID: ${otaRecord.ota_record_id || otaRecord.id}`)

    // 轮询真实进度
    const recordId = otaRecord.ota_record_id || otaRecord.id
    pollProgress(recordId)

  } catch (error: any) {
    ElMessage.error(error.message || '启动升级失败')
    upgradeStatus.value = 'failed'
    statusText.value = '升级失败'
    addLog(`错误: ${error.message || '未知错误'}`)
  }
}

// 轮询真实OTA进度
let progressTimer: ReturnType<typeof setInterval> | null = null

const pollProgress = (recordId: number) => {
  if (progressTimer) clearInterval(progressTimer)

  progressTimer = setInterval(async () => {
    try {
      const record = await nodeApi.getOTAProgress(props.collectorId, recordId)
      progress.value = record.progress || 0

      const statusMap: Record<string, string> = {
        'pending': '等待中...',
        'downloading': '正在下载固件...',
        'flashing': '正在刷写固件...',
        'installing': '正在安装固件...',
        'success': '升级完成',
        'completed': '升级完成',
        'failed': '升级失败',
      }
      statusText.value = statusMap[record.status] || record.status

      // 状态变化时添加日志
      const lastLog = upgradeLogs.value[0]
      if (!lastLog || lastLog.message !== statusText.value) {
        addLog(statusText.value)
      }

      if (record.status === 'completed' || record.status === 'success') {
        clearInterval(progressTimer!)
        progressTimer = null
        upgradeStatus.value = 'completed'
        progress.value = 100
        addLog('升级完成！请等待设备重启...')
        emit('success')
        setTimeout(() => { dialogVisible.value = false }, 3000)
      } else if (record.status === 'failed') {
        clearInterval(progressTimer!)
        progressTimer = null
        upgradeStatus.value = 'failed'
        statusText.value = '升级失败'
        addLog(`失败: ${record.error_msg || '未知错误'}`)
      }
    } catch (error: any) {
      // 轮询失败不中断，继续尝试
      console.warn('OTA progress poll failed:', error)
    }
  }, 2000)
}

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
  background: var(--el-fill-color-light);
  border-radius: 4px;
}

.status-text {
  text-align: center;
  margin-top: 10px;
  color: var(--el-text-color-regular);
}

:deep(.el-timeline-item__timestamp) {
  color: var(--el-text-color-secondary);
}
</style>
