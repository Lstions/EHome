<template>
  <el-dialog
    v-model="dialogVisible"
    title="删除边缘设备"
    width="520px"
    align-center
    :close-on-click-modal="false"
    aria-label="删除边缘设备确认"
  >
    <template v-if="device">
      <!-- 基本信息：本地数据，打开即显示，无延迟 (方案 v3.3 §2.1) -->
      <div class="delete-device-facts">
        <div class="fact-row">
          <span class="fact-label">设备名称</span>
          <span class="fact-value">{{ device.name || '-' }}</span>
        </div>
        <div class="fact-row">
          <span class="fact-label">设备类型</span>
          <span class="fact-value">{{ device.device_type || '-' }}</span>
        </div>
        <div class="fact-row">
          <span class="fact-label">所属节点</span>
          <span class="fact-value">{{ device.node?.name || ('#' + device.node_id) }}</span>
        </div>
        <div class="fact-row">
          <span class="fact-label">通道</span>
          <span class="fact-value">{{ channelLabel }}</span>
        </div>
      </div>

      <!-- 逻辑设备信息区：异步加载，loading 骨架，失败降级为不显示 (方案 v3.3 §2.1) -->
      <div v-if="infoLoading" class="logical-info-skeleton" aria-label="正在加载逻辑设备信息">
        <div class="skeleton-line skeleton-line--long" />
        <div class="skeleton-line skeleton-line--short" />
      </div>
      <div v-else-if="logicalInfo" class="logical-info">
        <div class="logical-info-title">逻辑设备信息</div>
        <div class="logical-info-items">
          <span v-if="logicalInfo.name" class="logical-info-item">
            逻辑设备：<strong>{{ logicalInfo.name }}</strong>
          </span>
          <span class="logical-info-item">
            实例数：<strong>{{ logicalInfo.instance_count }}</strong>
          </span>
          <span v-if="logicalInfo.row_estimate !== undefined" class="logical-info-item">
            数据量估算：<strong>约 {{ formatRowCount(logicalInfo.row_estimate) }} 条</strong>
          </span>
          <span v-if="logicalInfo.retention_days !== null" class="logical-info-item">
            历史数据保留：<strong>{{ logicalInfo.retention_days }} 天</strong>
          </span>
        </div>
      </div>

      <!-- 数据处置 radio (方案 v3.3 §2.1) -->
      <div class="data-action-section">
        <div class="data-action-title">历史数据处理</div>
        <el-radio-group v-model="dataAction" class="data-action-group">
          <el-radio value="keep" class="data-action-radio">
            保留历史数据<span v-if="retentionSuffix" class="data-action-hint">（{{ retentionSuffix }}）</span>
          </el-radio>
          <el-radio value="delete" class="data-action-radio">
            <span class="danger-text">同时删除历史数据（将在后台删除，不可恢复）</span>
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
import { edgeDeviceApi, type EdgeDevice, type LogicalDeviceInfo } from '@/api/edgeDevice'

const props = withDefaults(defineProps<{
  visible: boolean
  device: EdgeDevice | null
  submitting?: boolean
}>(), {
  submitting: false,
})

const emit = defineEmits<{
  (e: 'update:visible', value: boolean): void
  // deleteData: 是否同时删除历史数据 (方案 §2.3 delete_data 参数)
  (e: 'confirm', deleteData: boolean): void
}>()

const dialogVisible = computed({
  get: () => props.visible,
  set: (value: boolean) => emit('update:visible', value),
})

const channelLabel = computed(() => {
  if (!props.device) return '-'
  const parts = [props.device.hardware_type?.toUpperCase(), props.device.hardware_id].filter(Boolean)
  return parts.length > 0 ? parts.join(' ') : '-'
})

// 数据处置：默认保留历史数据
const dataAction = ref<'keep' | 'delete'>('keep')

// 逻辑设备信息异步加载 (失败降级为不显示该区域，不阻塞删除)
const infoLoading = ref(false)
const logicalInfo = ref<LogicalDeviceInfo | null>(null)
let infoRequestSeq = 0

const retentionSuffix = computed(() => {
  const days = logicalInfo.value?.retention_days
  return typeof days === 'number' ? `保留 ${days} 天` : ''
})

watch(() => props.visible, (open) => {
  if (open && props.device) {
    dataAction.value = 'keep'
    logicalInfo.value = null
    const seq = ++infoRequestSeq
    infoLoading.value = true
    edgeDeviceApi.getLogicalDeviceInfo(props.device.id)
      .then((info) => {
        if (seq !== infoRequestSeq) return
        logicalInfo.value = info
      })
      .catch(() => {
        // 降级：请求失败时不显示信息区，删除流程不受阻 (方案 §2.1)
        if (seq !== infoRequestSeq) return
        logicalInfo.value = null
      })
      .finally(() => {
        if (seq === infoRequestSeq) infoLoading.value = false
      })
  } else {
    // 关闭时使在途请求失效，防止陈旧响应污染下次打开
    infoRequestSeq++
    infoLoading.value = false
  }
}, { immediate: true })

const formatRowCount = (count: number): string => count.toLocaleString('zh-CN')

const handleCancel = () => {
  dialogVisible.value = false
}

const handleConfirm = () => {
  emit('confirm', dataAction.value === 'delete')
}
</script>

<style scoped>
.delete-device-facts {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px;
  border-radius: 8px;
  background: var(--el-fill-color-light, #f5f7fa);
}

.fact-row {
  display: flex;
  align-items: baseline;
  gap: 12px;
  font-size: 13px;
}

.fact-label {
  flex-shrink: 0;
  width: 64px;
  color: var(--el-text-color-secondary, #909399);
}

.fact-value {
  color: var(--el-text-color-primary, #303133);
  word-break: break-all;
}

.logical-info-skeleton {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-top: 12px;
  padding: 12px;
  border-radius: 8px;
  border: 1px dashed var(--el-border-color, #dcdfe6);
}

.skeleton-line {
  height: 12px;
  border-radius: 6px;
  background: linear-gradient(90deg, #ebeef5 25%, #f5f7fa 50%, #ebeef5 75%);
  background-size: 200% 100%;
  animation: skeleton-pulse 1.2s ease-in-out infinite;
}

.skeleton-line--long {
  width: 80%;
}

.skeleton-line--short {
  width: 50%;
}

@keyframes skeleton-pulse {
  0% { background-position: 200% 0; }
  100% { background-position: -200% 0; }
}

.logical-info {
  margin-top: 12px;
  padding: 12px;
  border-radius: 8px;
  border: 1px solid var(--el-border-color-lighter, #ebeef5);
}

.logical-info-title {
  margin-bottom: 8px;
  font-size: 13px;
  font-weight: 600;
  color: var(--el-text-color-primary, #303133);
}

.logical-info-items {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 16px;
  font-size: 13px;
  color: var(--el-text-color-regular, #606266);
}

.logical-info-item strong {
  color: var(--el-text-color-primary, #303133);
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
