<template>
  <el-tag :type="tagType" size="small">
    {{ statusText }}
  </el-tag>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { DeviceStatus } from '@/api/edgeDevice'

const props = defineProps<{
  status: DeviceStatus
}>()

const tagType = computed(() => {
  switch (props.status) {
    case 'online':
    case 'active':
      return 'success'
    case 'offline':
      return 'info'
    case 'error':
      return 'danger'
    case 'pending':
    case 'initializing':
      return 'warning'
    case 'unknown':
      return 'info'
    default:
      return ''
  }
})

const statusText = computed(() => {
  switch (props.status) {
    case 'online':
    case 'active':
      return '在线'
    case 'offline':
      return '离线'
    case 'error':
      return '错误'
    case 'pending':
      return '等待中'
    case 'initializing':
      return '初始化'
    case 'unknown':
      return '未知'
    default:
      return props.status
  }
})
</script>
