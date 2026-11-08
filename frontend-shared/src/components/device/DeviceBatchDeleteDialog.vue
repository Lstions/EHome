<template>
  <el-dialog
    v-model="dialogVisible"
    title="批量删除确认"
    width="520px"
    align-center
    :close-on-click-modal="false"
    aria-label="批量删除边缘设备确认"
  >
    <template v-if="devices.length > 0">
      <!-- 汇总视图: 不逐台展示数据量 (批量查询成本不可控), 详情引导单删流程。
           2026-10-23 逻辑设备移除：原「其中 N 台属于逻辑设备」的统计口径已不存在，
           保留它只会让用户以为数据处置策略因设备而异。 -->
      <p class="batch-summary">
        将删除 <strong>{{ devices.length }}</strong> 台设备。
      </p>
      <p class="batch-hint">
        逐台删除可查看该设备的数据量估算，如需请在设备列表中单独删除。
      </p>

      <!-- 统一一个 radio: 全部保留 (默认) / 全部删除 -->
      <div class="data-action-section">
        <div class="data-action-title">历史数据处理</div>
        <el-radio-group v-model="dataAction" class="data-action-group">
          <el-radio value="keep" class="data-action-radio">
            全部保留历史数据
          </el-radio>
          <el-radio value="delete" class="data-action-radio">
            <span class="danger-text">全部删除历史数据（后台分批清理；清理开始后不可恢复）</span>
          </el-radio>
        </el-radio-group>
        <p class="data-action-note">
          只删设备行是<strong>软删</strong>：保留数据的删除可在「恢复已删除设备」中找回；
          勾选删除数据后，后台清理开始前仍可恢复并撤回删除请求。
        </p>
      </div>
    </template>

    <template #footer>
      <el-button @click="handleCancel">取消</el-button>
      <el-button type="danger" :loading="submitting" @click="handleConfirm">确认删除</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { EdgeDevice } from '@/api/edgeDevice'

const props = withDefaults(defineProps<{
  visible: boolean
  devices: EdgeDevice[]
  submitting?: boolean
}>(), {
  submitting: false,
})

const emit = defineEmits<{
  (e: 'update:visible', value: boolean): void
  // deleteData: 统一处置 — 全部保留 (false) / 全部删除 (true)
  (e: 'confirm', deleteData: boolean): void
}>()

const dialogVisible = computed({
  get: () => props.visible,
  set: (value: boolean) => emit('update:visible', value),
})

// 统一 radio，默认全部保留 (方案 §2.2: 不逐台查询信息, 避免成本不可控)
const dataAction = ref<'keep' | 'delete'>('keep')

watch(() => props.visible, (open) => {
  if (open) dataAction.value = 'keep'
}, { immediate: true })

const handleCancel = () => {
  dialogVisible.value = false
}

const handleConfirm = () => {
  emit('confirm', dataAction.value === 'delete')
}
</script>

<style scoped>
.batch-summary {
  margin: 0 0 8px;
  font-size: 14px;
  color: var(--el-text-color-primary, #303133);
}

.batch-hint {
  margin: 0;
  font-size: 12px;
  color: var(--el-text-color-secondary, #909399);
}

.data-action-section {
  margin-top: 16px;
}

.data-action-title {
  margin-bottom: 8px;
  font-size: 13px;
  font-weight: 600;
  color: var(--el-text-color-primary, #303133);
}

.data-action-group {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.data-action-radio {
  height: auto;
}

.data-action-hint {
  color: var(--el-text-color-secondary, #909399);
}

.danger-text {
  color: var(--el-color-danger, #f56c6c);
}

.data-action-note {
  margin: 10px 0 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--el-text-color-secondary);
}
</style>
