<template>
  <el-card shadow="hover">
    <template #header><span>基本信息</span></template>
    <div v-if="isMobile" class="mobile-info-list">
      <div class="mobile-info-row">
        <span class="mobile-info-label">设备名称</span>
        <span class="mobile-info-value">{{ device.name || '-' }}</span>
      </div>
      <div class="mobile-info-row">
        <span class="mobile-info-label">设备类型</span>
        <span class="mobile-info-value">{{ deviceTypeText }}</span>
      </div>
      <div class="mobile-info-row">
        <span class="mobile-info-label">通信协议</span>
        <span class="mobile-info-value">{{ device.protocol?.toUpperCase() || '-' }}</span>
      </div>
      <div class="mobile-info-row">
        <span class="mobile-info-label">硬件类型</span>
        <span class="mobile-info-value">{{ device.hardware_type?.toUpperCase() || '-' }}</span>
      </div>
      <div class="mobile-info-row">
        <span class="mobile-info-label">硬件ID</span>
        <span class="mobile-info-value">{{ device.hardware_id || '-' }}</span>
      </div>
      <div class="mobile-info-row">
        <span class="mobile-info-label">健康状态</span>
        <span class="mobile-info-value"><StatusBadge :status="device.status" effect="dark" /></span>
      </div>
      <div class="mobile-info-row">
        <span class="mobile-info-label">最后数据时间</span>
        <span class="mobile-info-value">{{ formatTime(device.last_data_time) }}</span>
      </div>
      <div v-if="device.last_error_code && device.last_error_code > 0" class="mobile-info-row">
        <span class="mobile-info-label">错误码</span>
        <span class="mobile-info-value">
          <el-tag type="danger" size="small">
            {{ device.last_error_code }} - {{ getErrorInfo(device.last_error_code).label }}
          </el-tag>
        </span>
      </div>
    </div>
    <el-descriptions v-else :column="2" border>
      <el-descriptions-item label="设备名称">{{ device.name || '-' }}</el-descriptions-item>
      <el-descriptions-item label="设备类型">{{ deviceTypeText }}</el-descriptions-item>
      <el-descriptions-item label="通信协议">{{ device.protocol ? device.protocol.toUpperCase() : '-' }}</el-descriptions-item>
      <el-descriptions-item label="硬件类型">{{ device.hardware_type ? device.hardware_type.toUpperCase() : '-' }}</el-descriptions-item>
      <el-descriptions-item label="硬件ID">{{ device.hardware_id || '-' }}</el-descriptions-item>
      <el-descriptions-item label="健康状态">
        <StatusBadge :status="device.status" effect="dark" />
      </el-descriptions-item>
      <el-descriptions-item label="最后数据时间">{{ formatTime(device.last_data_time) }}</el-descriptions-item>
      <el-descriptions-item v-if="device.last_error_code && device.last_error_code > 0" label="错误码">
        <el-tag type="danger" size="small">
          {{ device.last_error_code }} - {{ getErrorInfo(device.last_error_code).label }}
        </el-tag>
      </el-descriptions-item>
    </el-descriptions>
  </el-card>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import StatusBadge from '@/components/common/StatusBadge.vue'
import { getErrorInfo } from '@/utils/errorCode'
import { getDeviceTypeLabel } from '@/utils/deviceType'
import { useResponsive } from '@/composables/useResponsive'
import type { EdgeDevice } from '@/api/edgeDevice'

const props = defineProps<{ device: EdgeDevice }>()

const { isMobile } = useResponsive()

const deviceTypeText = computed(() => getDeviceTypeLabel(props.device.device_type))

function formatTime(time: string | null | undefined) {
  if (!time || time === '0001-01-01T00:00:00Z' || time === '1970-01-01T00:00:00Z') return '-'
  const date = new Date(time)
  if (isNaN(date.getTime()) || date.getFullYear() <= 1970) return '-'
  return date.toLocaleString('zh-CN')
}
</script>

<style scoped>
.mobile-info-list {
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 4px;
  overflow: hidden;
}

.mobile-info-row {
  display: grid;
  /* 标签列宽由最长字段推导（最后数据时间 ≈98px 含 padding），不再硬编码固定像素列宽 */
  grid-template-columns: minmax(max-content, 106px) minmax(0, 1fr);
  align-items: center;
  min-height: 44px;
  border-bottom: 1px solid var(--el-border-color-lighter);
}

.mobile-info-row:last-child {
  border-bottom: 0;
}

.mobile-info-label {
  align-self: stretch;
  display: flex;
  align-items: center;
  padding: 9px 10px;
  background: var(--el-fill-color-lighter);
  color: var(--el-text-color-regular);
  font-size: 13px;
  font-weight: 500;
  /* 标签禁折行（防「最后数据时间」拆字），超长时省略而非拆字 */
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.mobile-info-value {
  min-width: 0;
  padding: 9px 12px;
  color: var(--el-text-color-primary);
  font-size: 13px;
  line-height: 1.45;
  overflow-wrap: anywhere;
  word-break: break-word;
}
</style>
