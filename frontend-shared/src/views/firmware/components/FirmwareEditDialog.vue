<template>
  <el-dialog
    :model-value="modelValue"
    title="编辑固件"
    align-center
    width="560px"
    :close-on-click-modal="false"
    data-testid="firmware-edit-dialog"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <el-form label-width="120px" @submit.prevent>
      <!-- 版本号只读展示：**不可编辑**（版本以镜像为唯一真源，§4.2 / 判断 5）。
           契约里 PUT 只允许 changelog / channel / min_from_version 三项。 -->
      <el-form-item label="版本号">
        <el-input :model-value="firmware ? formatVersion(firmware.version) : ''" disabled />
        <div class="form-hint">版本取自镜像，不可手工修改；要换版本请重新上传构建产物。</div>
      </el-form-item>
      <el-form-item label="渠道">
        <el-select v-model="form.channel" placeholder="选择渠道" style="width: 100%;" data-testid="firmware-edit-channel">
          <el-option label="稳定（stable）" value="stable" />
          <el-option label="灰度（beta）" value="beta" />
          <el-option label="内部（internal）" value="internal" />
        </el-select>
      </el-form-item>
      <el-form-item label="最低可升级版本">
        <el-input v-model="form.min_from_version" placeholder="留空表示不限制（如 2.6.0）" />
        <div class="form-hint">填写后，节点当前版本低于该值将被闸门拒绝（OTA_GATE_BELOW_MIN_VERSION）。</div>
      </el-form-item>
      <el-form-item label="更新日志">
        <el-input v-model="form.changelog" type="textarea" :rows="4" placeholder="本次固件更新的内容描述" />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button :disabled="saving" @click="emit('update:modelValue', false)">取消</el-button>
      <el-button type="primary" :loading="saving" data-testid="firmware-edit-submit" @click="handleSubmit">保存</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import feedback from '@/utils/feedback'
import { type Firmware } from '@/api/firmware'
import { useFirmwareStore } from '@/stores/firmware'

/**
 * 编辑固件（M1b / T1b.3「编辑收敛」）。
 *
 * 收敛点：**只保留后端允许修改的三项**。改前这里可以改版本号、目标型号、稳定标记，
 * 而 M0 之后 version 由镜像决定、target_model/stable 两列已从库中删除 ——
 * 表单再留着它们，用户改完会「保存成功但什么都没变」（甚至 400）。
 */
const props = defineProps<{
  modelValue: boolean
  firmware: Firmware | null
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', value: boolean): void
  (e: 'saved'): void
}>()

const firmwareStore = useFirmwareStore()
const saving = ref(false)

const form = reactive({
  channel: 'stable',
  min_from_version: '',
  changelog: '',
})

watch(
  () => props.firmware,
  (row) => {
    if (!row) return
    form.channel = row.channel || 'stable'
    form.min_from_version = row.min_from_version || ''
    form.changelog = row.changelog || ''
  },
  { immediate: true }
)

const formatVersion = (version: string) => {
  if (!version) return 'v-'
  if (version.startsWith('v') || version.startsWith('V')) return version
  return `v${version}`
}

const handleSubmit = async () => {
  if (!props.firmware || saving.value) return
  saving.value = true
  try {
    await firmwareStore.update(props.firmware.id, {
      channel: form.channel,
      min_from_version: form.min_from_version.trim(),
      changelog: form.changelog,
    })
    feedback.success('更新成功')
    emit('update:modelValue', false)
    emit('saved')
  } catch (error) {
    feedback.handleErrorWithContext(error, '更新固件信息失败')
  } finally {
    saving.value = false
  }
}
</script>

<style scoped>
.form-hint {
  margin-top: 4px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
</style>
