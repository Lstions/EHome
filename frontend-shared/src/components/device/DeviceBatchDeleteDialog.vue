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
      <!-- 汇总视图 (方案 v3.3 §2.2): 不逐台展示实例数, 详情引导单删流程 -->
      <p class="batch-summary">
        将删除 <strong>{{ devices.length }}</strong> 台设备<span v-if="logicalCount > 0">，其中
        <strong>{{ logicalCount }}</strong> 台属于逻辑设备（数据默认保留）</span><span v-else>（均不属于逻辑设备）</span>。
      </p>
      <p class="batch-hint">
        逐台删除可查看每台设备的逻辑设备详情，如需请在设备列表中单独删除。
      </p>

      <!-- 统一一个 radio: 全部保留 (默认) / 全部删除 -->
      <div class="data-action-section">
        <div class="data-action-title">历史数据处理</div>
        <el-radio-group v-model="dataAction" class="data-action-group">
          <el-radio value="keep" class="data-action-radio">
            全部保留历史数据
          </el-radio>
          <el-radio value="delete" class="data-action-radio">
            <span class="danger-text">全部删除历史数据（将在后台删除，不可恢复）</span>
          </el-radio>
        </el-radio-group>
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

// 属于逻辑设备的台数 (后端 omitempty: 未建立时字段缺省)
const logicalCount = computed(
  () => props.devices.filter(d => typeof d.logical_device_id === 'number').length,
)

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
</style>
