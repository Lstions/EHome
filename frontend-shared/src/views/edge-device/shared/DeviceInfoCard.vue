<template>
  <el-card shadow="hover">
    <template #header><span>基本信息</span></template>
    <el-descriptions :column="2" border>
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
import type { EdgeDevice } from '@/api/edgeDevice'

const props = defineProps<{ device: EdgeDevice }>()

const deviceTypeText = computed(() => getDeviceTypeLabel(props.device.device_type))

function formatTime(time: string | null | undefined) {
  if (!time || time === '0001-01-01T00:00:00Z' || time === '1970-01-01T00:00:00Z') return '-'
  const date = new Date(time)
  if (isNaN(date.getTime()) || date.getFullYear() <= 1970) return '-'
  return date.toLocaleString('zh-CN')
}
</script>
