<template>
  <el-dialog
    :model-value="modelValue"
    title="上传固件"
    align-center
    width="560px"
    :close-on-click-modal="false"
    data-testid="firmware-upload-dialog"
    @update:model-value="emit('update:modelValue', $event)"
    @close="resetForm"
  >
    <el-form label-width="120px" @submit.prevent>
      <!-- 版本号/目标型号**不再由人手填写**（§4.2）：version 取自镜像，
           sidecar 只做交叉校验；target_model 列已随 M0 删除。 -->
      <el-alert
        type="info"
        :closable="false"
        show-icon
        class="upload-alert"
        title="必须同时选择 .bin 与同名 .meta.json"
      >
        <template #default>
          版本、芯片与容量全部从镜像与侧车读取，不再手填。存量固件可用
          <code>ehomectl firmware meta-gen --bin &lt;file&gt;</code> 补出侧车后重传。
        </template>
      </el-alert>

      <el-form-item label="固件镜像" required>
        <el-upload
          ref="binUploadRef"
          :auto-upload="false"
          :limit="1"
          accept=".bin"
          :on-change="(file: UploadFile) => onBinChange(file)"
          :on-exceed="onBinExceed"
        >
          <el-button :icon="Upload">选择 .bin</el-button>
        </el-upload>
        <div v-if="binFile" class="file-info" data-testid="firmware-bin-file">
          已选择: {{ binFile.name }} ({{ formatFileSize(binFile.size) }})
        </div>
      </el-form-item>

      <el-form-item label="侧车元数据" required>
        <el-upload
          ref="metaUploadRef"
          :auto-upload="false"
          :limit="1"
          accept=".json"
          :on-change="(file: UploadFile) => onMetaChange(file)"
          :on-exceed="onMetaExceed"
        >
          <el-button :icon="Upload">选择 .meta.json</el-button>
        </el-upload>
        <div v-if="metaFile" class="file-info" data-testid="firmware-meta-file">
          已选择: {{ metaFile.name }} ({{ formatFileSize(metaFile.size) }})
        </div>
      </el-form-item>

      <!-- 服务端拒绝原因（FW_META_REQUIRED / FW_META_CONFLICT 等）常驻显示：
           一次性 toast 会消失，用户回来再看就只剩「上传失败」四个字。 -->
      <el-alert
        v-if="uploadError"
        type="error"
        :closable="false"
        show-icon
        class="upload-error"
        data-testid="firmware-upload-error"
        :title="uploadError"
      />
    </el-form>

    <template #footer>
      <el-button :disabled="uploading" @click="close">取消</el-button>
      <el-button type="primary" :loading="uploading" data-testid="firmware-upload-submit" @click="handleUpload">
        上传
      </el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { Upload } from '@element-plus/icons-vue'
import type { UploadFile } from 'element-plus'
import { feedback } from '@/utils/feedback'
import { UNKNOWN } from '@/utils/format'
import {
  FIRMWARE_ERROR_TEXT,
  buildFirmwareUploadFormData,
  firmwareUploadOutcome,
  uploadErrorMessage,
} from '@/api/firmware'
import { useFirmwareStore } from '@/stores/firmware'
import { isFirmwareBinFile, isFirmwareMetaFile } from '../firmwarePage'

/**
 * 上传固件对话框（M1b / T0.6：表单不再接收 version / target_model）。
 *
 * 两个文件缺一不可：缺 .bin 无从解析；缺 .meta.json 后端直接 400 FW_META_REQUIRED
 * （§4.2「sidecar 必填」—— 不再有「入库但身份未知」的第三条路）。
 * 前端先做同构校验，是为了让用户在**点上传之前**就知道少了什么，
 * 而不是等一次来回才看到错误码。
 */
const props = defineProps<{
  modelValue: boolean
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', value: boolean): void
  (e: 'uploaded'): void
}>()

const firmwareStore = useFirmwareStore()

const binUploadRef = ref()
const metaUploadRef = ref()
const binFile = ref<File | null>(null)
const metaFile = ref<File | null>(null)
const uploading = ref(false)
const uploadError = ref('')

const formatFileSize = (bytes: number | undefined | null) => {
  if (!bytes && bytes !== 0) return UNKNOWN
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i]
}

const onBinExceed = () => feedback.warning('只能选择一个镜像文件')
const onMetaExceed = () => feedback.warning('只能选择一个侧车文件')

const onBinChange = (file: UploadFile) => {
  if (!file.raw) return
  if (!isFirmwareBinFile(file.raw)) {
    feedback.warning('固件镜像必须是 .bin 文件')
    return
  }
  binFile.value = file.raw
  uploadError.value = ''
}

const onMetaChange = (file: UploadFile) => {
  if (!file.raw) return
  if (!isFirmwareMetaFile(file.raw)) {
    feedback.warning('侧车文件必须是 .meta.json')
    return
  }
  metaFile.value = file.raw
  uploadError.value = ''
}

const resetForm = () => {
  binFile.value = null
  metaFile.value = null
  uploadError.value = ''
  binUploadRef.value?.clearFiles?.()
  metaUploadRef.value?.clearFiles?.()
}

const close = () => {
  if (uploading.value) return
  emit('update:modelValue', false)
}

const handleUpload = async () => {
  if (uploading.value) return
  if (!binFile.value) {
    uploadError.value = '请选择固件镜像（.bin）'
    return
  }
  if (!metaFile.value) {
    // 与服务端 FW_META_REQUIRED 同一句话：避免「前端说少文件、后端说另一个词」
    uploadError.value = FIRMWARE_ERROR_TEXT.FW_META_REQUIRED
    return
  }

  uploading.value = true
  uploadError.value = ''
  try {
    const result = await firmwareStore.upload(buildFirmwareUploadFormData(binFile.value, metaFile.value))
    // 命中 legacy 行并就地补齐时（completed_legacy=true）走 info：
    // 这不是「新增了一条」，也不是「重复上传被忽略」，是「补上了身份信息」。
    // 无论哪种成功都要 emit('uploaded') —— 补齐路径更新的是同一 id，
    // 页面必须重新拉列表才能看到 psram / 身份状态的变化。
    const outcome = firmwareUploadOutcome(result)
    if (outcome.level === 'info') feedback.info(outcome.message)
    else feedback.success(outcome.message)
    resetForm()
    emit('update:modelValue', false)
    emit('uploaded')
  } catch (error) {
    // 常驻展示（不是 toast）：错误码对应的是「该换哪个文件」，用户需要边看边改
    uploadError.value = uploadErrorMessage(error)
    feedback.error(uploadError.value)
  } finally {
    uploading.value = false
  }
}
</script>

<style scoped>
.upload-alert {
  margin-bottom: 16px;
}
.upload-error {
  margin-top: 4px;
}
.file-info {
  margin-top: 8px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  word-break: break-all;
}
</style>
