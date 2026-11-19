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
    <el-form label-width="140px" @submit.prevent>
      <!-- 版本号 / 目标型号 / psram **不再由人手填写**：前三项由包内清单与镜像头决定
           （§4.3「清单只保留镜像说不了的两项」），人手只负责选对那个文件。 -->
      <el-form-item label="固件包 *.ehfw" required>
        <el-upload
          ref="uploadRef"
          :auto-upload="false"
          :limit="1"
          :show-file-list="false"
          accept=".ehfw,.zip"
          :on-change="(file: UploadFile) => applySelection(file.raw)"
          :on-exceed="onExceed"
        >
          <el-button :icon="Upload">选择固件包</el-button>
        </el-upload>
        <div v-if="packageFile" class="file-info" data-testid="firmware-package-file">
          已选择: {{ packageFile.name }} ({{ formatFileSize(packageFile.size) }})
        </div>
      </el-form-item>

      <!-- 拒绝原因（本地预校验与 FW_PKG_* 服务端拒绝）常驻显示：
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
      <el-button
        type="primary"
        :loading="uploading"
        :disabled="!canUpload"
        data-testid="firmware-upload-submit"
        @click="handleUpload"
      >
        上传
      </el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { Upload } from '@element-plus/icons-vue'
import type { UploadFile } from 'element-plus'
import { feedback } from '@/utils/feedback'
import { UNKNOWN } from '@/utils/format'
import { buildFirmwareUploadFormData, uploadErrorMessage } from '@/api/firmware'
import { useFirmwareStore } from '@/stores/firmware'
import { isFirmwareBinFile, isFirmwarePackageFile } from '../firmwarePage'

/**
 * 上传固件对话框（单文件包，§6）。
 *
 * 一个文件、一次选择：固件包自带清单，配对由包结构保证，不再有「.bin 与 .meta.json
 * 缺一不可」这一层。前端只做一处同构校验——**选中的是不是固件包**——好让用户在
 * 点上传之前就知道拿错了文件，而不是等一次来回才看到 `FW_PKG_REQUIRED`。
 */
const props = defineProps<{
  modelValue: boolean
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', value: boolean): void
  (e: 'uploaded'): void
}>()

const firmwareStore = useFirmwareStore()

/** 裸镜像的针对性文案：这是最常见的「拿错了」，必须直接指向 .ehfw（§6.1 第 1 条）。 */
const RAW_IMAGE_HINT = '这是裸镜像，需要打包成 .ehfw 后上传'
const NOT_PACKAGE_HINT = '不是固件包：请选择 .ehfw（或同格式的 .zip）文件'

const uploadRef = ref()
const packageFile = ref<File | null>(null)
const uploading = ref(false)
const uploadError = ref('')

const canUpload = computed(() => Boolean(packageFile.value) && !uploading.value)

const formatFileSize = (bytes: number | undefined | null) => {
  if (!bytes && bytes !== 0) return UNKNOWN
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i]
}

/**
 * 一次选择的唯一处置点（on-change 与 on-exceed 共用）。
 *
 * 先清掉上一次的选择与错误：选错一次不该让「上一个文件仍可上传」残留下来——
 * 否则用户看到文件名换成新的、上传的却是旧的。
 */
const applySelection = (raw?: File | null) => {
  packageFile.value = null
  uploadError.value = ''
  if (!raw) return
  if (isFirmwarePackageFile(raw)) {
    packageFile.value = raw
    return
  }
  uploadError.value = isFirmwareBinFile(raw) ? RAW_IMAGE_HINT : NOT_PACKAGE_HINT
}

/**
 * 重选必须生效（评审 A-8）：`:limit="1"` 下第二次选择**只**触发 on-exceed，
 * 不会触发 on-change —— 旧实现在这里只弹一句警告，等于「选错一次后无法换文件」。
 * 先清空 el-upload 的内部列表（让下一次选择重新变成「第一次」），再手动接管这个文件。
 */
const onExceed = (files: File[]) => {
  uploadRef.value?.clearFiles?.()
  applySelection(files?.[0])
}

const resetForm = () => {
  packageFile.value = null
  uploadError.value = ''
  uploadRef.value?.clearFiles?.()
}

const close = () => {
  if (uploading.value) return
  emit('update:modelValue', false)
}

const handleUpload = async () => {
  const pkg = packageFile.value
  if (!pkg || uploading.value) return

  uploading.value = true
  uploadError.value = ''
  try {
    await firmwareStore.upload(buildFirmwareUploadFormData(pkg))
    // 只有一种成功：新增一条。旧后端可能返回「补齐身份」的 info 提示，那种语义
    // 随空库与 API 字段一起删除（§9.2），这里不再分支。
    feedback.success('上传成功')
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
