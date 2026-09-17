<template>
  <el-dialog
    v-model="dialogVisible"
    title="OTA 固件升级"
    width="600px"
    :close-on-click-modal="false"
    :close-on-press-escape="!otaBusy"
    :show-close="!otaBusy"
    :before-close="handleDialogClose"
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

      <!--
        固件信息区必须是单列：MD5 是 64 字符且无自然断点的长串，两列布局时它会把
        内容列的最小宽度顶到 ~425px；600px 弹层减去外层 120px 标签宽后只剩 ~448px，
        于是 auto 表格反压另一列 —— 实测 label 列被压到 27px、内容列 57px，
        「文件大小」被拆成「1.35」/「MB」两行、表头逐字竖排、表格整体溢出弹层右侧。
        单列让长值独占整行，配合长串强制断行 + 短值禁止折行即可根治。
      -->
      <el-form-item label="固件信息" v-if="selectedFirmware">
        <el-descriptions :column="1" border size="small" class="firmware-descriptions">
          <el-descriptions-item label="文件名">
            {{ selectedFirmware.filename }}
          </el-descriptions-item>
          <el-descriptions-item label="文件大小">
            <span class="firmware-size">{{ formatFileSize(selectedFirmware.size_bytes) }}</span>
          </el-descriptions-item>
          <el-descriptions-item label="MD5">
            <span class="firmware-md5">{{ selectedFirmware.checksum }}</span>
          </el-descriptions-item>
          <el-descriptions-item label="更新日志">
            <div class="firmware-changelog">
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
import { feedback } from '@/utils/feedback'
import { ElMessage } from 'element-plus'
import { nodeApi } from '@/api/node'
import { type Firmware } from '@/api/firmware'
import { useFirmwareStore } from '@/stores/firmware'
import { UNKNOWN, formatFileSize } from '@/utils/format'

/**
 * OTA 任务状态 → 中文文案。
 *
 * 必须覆盖后端 backend/internal/ota/ota.go:24-31 的**全部 8 个**状态：
 *   pending, downloading, verifying, installing, success, failed, timeout, needs_retry
 *
 * 历史缺陷（2026-09-17 生产实测）：本表只覆盖了 7 项，漏掉 verifying / timeout /
 * needs_retry，而渲染是 `TABLE[status] || status` ⇒ 这三态在中文界面**原样显示英文**。
 * 后端注释明确 ESP32 未来会显式上报 verifying、版本不匹配会置 needs_retry，
 * 所以这不是理论问题，是"已定义但未接线"。
 *
 * 注：'flashing'/'completed' 不在后端枚举里，但历史数据/其它入口可能出现，保留兼容。
 */
const OTA_STATUS_TEXT: Record<string, string> = {
  pending: '等待中...',
  downloading: '正在下载固件...',
  verifying: '正在校验固件...',
  flashing: '正在刷写固件...',
  installing: '正在安装固件...',
  success: '升级完成',
  completed: '升级完成',
  failed: '升级失败',
  timeout: '升级超时',
  needs_retry: '需要重试',
}

/** 成功终态：停止轮询并提示"等待设备重启"。 */
const OTA_TERMINAL_SUCCESS = new Set(['success', 'completed'])

/**
 * 不成功终态：同样必须停止轮询。
 *
 * timeout / needs_retry 若不在此集合内，轮询会一直跑、进度条永远停在半途
 * ——UI 表现成"永远在升级中"，比直接报错更难排查（后端已不会再推进该任务）。
 */
const OTA_TERMINAL_FAILURE = new Set(['failed', 'timeout', 'needs_retry'])


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
const otaBusy = computed(() => upgradeStatus.value === 'uploading' || upgradeStatus.value === 'upgrading')
const handleDialogClose = (done: () => void) => {
  if (otaBusy.value) return
  done()
}
const dialogVisible = computed({
  get: () => props.visible,
  set: (val) => {
    emit('update:visible', val)
    if (!val) {
      otaGeneration++
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
const currentVersion = computed(() => props.currentFirmwareVersion || UNKNOWN)

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

// 获取型号名称（Firmware.target_model 可选，缺省时原样返回空）
const getModelName = (model?: string): string => {
  if (!model) return ''
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
    feedback.handleError(error, '获取固件列表失败')
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
let progressTimer: ReturnType<typeof setInterval> | null = null
let otaGeneration = 0

const handleStart = async () => {
  if (!form.firmware_id || !selectedFirmware.value) {
    ElMessage.warning('请先选择固件版本')
    return
  }

  const collectorId = props.collectorId
  const generation = ++otaGeneration
  try {
    upgradeStatus.value = 'uploading'
    statusText.value = '正在创建OTA任务...'
    addLog('开始 OTA 升级')

    const otaRecord = await nodeApi.startOTA(collectorId, form.firmware_id)
    if (generation !== otaGeneration || props.collectorId !== collectorId) return

    upgradeStatus.value = 'upgrading'
    statusText.value = '正在升级中...'
    addLog(`OTA 任务已创建，ID: ${otaRecord.ota_record_id ?? otaRecord.id}`)

    // 轮询真实进度
    const recordId = otaRecord.ota_record_id ?? otaRecord.id
    if (recordId == null) {
      throw new Error('后端未返回 OTA 记录 ID')
    }
    pollProgress(collectorId, recordId, generation)

  } catch (error: any) {
    if (generation !== otaGeneration || props.collectorId !== collectorId) return
    feedback.handleError(error, '启动升级失败')
    upgradeStatus.value = 'failed'
    statusText.value = '升级失败'
    addLog(`错误: ${error.message || '未知错误'}`)
  }
}

// 轮询真实OTA进度
const pollProgress = (collectorId: string, recordId: number, generation: number) => {
  if (progressTimer) clearInterval(progressTimer)

  progressTimer = setInterval(async () => {
    try {
      const record = await nodeApi.getOTAProgress(collectorId, recordId)
      if (generation !== otaGeneration || props.collectorId !== collectorId) return
      progress.value = record.progress || 0

      statusText.value = OTA_STATUS_TEXT[record.status] || record.status

      // 状态变化时添加日志
      const lastLog = upgradeLogs.value[0]
      if (!lastLog || lastLog.message !== statusText.value) {
        addLog(statusText.value)
      }

      if (OTA_TERMINAL_SUCCESS.has(record.status)) {
        clearInterval(progressTimer!)
        progressTimer = null
        upgradeStatus.value = 'completed'
        progress.value = 100
        addLog('升级完成！请等待设备重启...')
        emit('success')
        const closeGeneration = generation
        setTimeout(() => {
          if (closeGeneration === otaGeneration && props.collectorId === collectorId) dialogVisible.value = false
        }, 3000)
      } else if (OTA_TERMINAL_FAILURE.has(record.status)) {
        // timeout / needs_retry 与 failed 同属**不成功终态**：必须在这里停轮询，
        // 否则 UI 会永远转圈（后端不会再推进这些任务）。文案与处置分别给：
        //   failed     → 展示 error_msg 的错误详情
        //   timeout / needs_retry → 展示状态文案本身（含义比"失败"更具体）
        clearInterval(progressTimer!)
        progressTimer = null
        upgradeStatus.value = 'failed'
        if (record.status === 'failed') {
          statusText.value = '升级失败'
          addLog(`失败: ${record.error_msg || '未知错误'}`)
        } else {
          addLog(`${OTA_STATUS_TEXT[record.status]}${record.error_msg ? ': ' + record.error_msg : ''}`)
        }
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
  } else {
    otaGeneration++
    if (progressTimer) clearInterval(progressTimer)
    progressTimer = null
  }
})

watch(() => props.collectorId, () => {
  otaGeneration++
  if (progressTimer) clearInterval(progressTimer)
  progressTimer = null
  upgradeStatus.value = 'idle'
  progress.value = 0
  statusText.value = ''
  upgradeLogs.value = []
})

onUnmounted(() => {
  otaGeneration++
  if (progressTimer) clearInterval(progressTimer)
})
</script>

<style scoped>
/* ── 固件信息区（单列）───────────────────────────────────────────────
   2026-09-17 缺陷取证：600px 弹层 + 120px 表单标签宽下，两列描述表的
   label 列被 64 字符 MD5 反压到 27px（「文件大小」逐字竖排、值拆成两行）。
   下列规则与模板里的 :column="1" 共同构成防挤压契约，缺一即回归。 */
.firmware-descriptions :deep(.el-descriptions__label) {
  /* 表头/标签永不被压成竖排 */
  white-space: nowrap;
}

.firmware-size {
  /* 「1.35 MB」是原子信息，禁止拆行 */
  white-space: nowrap;
}

.firmware-md5 {
  /* 64 字符哈希：任意处可断行，独占整行后完整可见（不省略、不截断） */
  font-family: var(--el-font-family-mono, ui-monospace, SFMono-Regular, Menlo, Consolas, monospace);
  word-break: break-all;
  /* 便于整串复制粘贴核对 */
  user-select: all;
}

.firmware-changelog {
  /* 更新日志滚动区（原内联样式，语义等价迁移到类上） */
  max-height: 100px;
  overflow-y: auto;
  /* changelog 是多行文本（含 \n），默认 white-space: normal 会把换行**折叠**成空格，
     多行日志因此挤成一行（实测 lineBoxes=1）。pre-wrap 保留换行、同时仍允许长行折行。 */
  white-space: pre-wrap;
}

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
