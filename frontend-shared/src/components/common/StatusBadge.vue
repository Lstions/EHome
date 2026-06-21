<template>
  <el-tag :type="tagType" size="small" :effect="effect">
    {{ statusText }}
  </el-tag>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { DeviceStatus } from '@/api/edgeDevice'

const props = withDefaults(defineProps<{
  status: DeviceStatus
  effect?: 'dark' | 'light' | 'plain'
}>(), {
  effect: 'light'
})

const tagType = computed(() => {
  switch (props.status) {
    case 'online':
    case 'active':
      return 'success'
    case 'offline':
    case 'disabled':
      return 'info'
    case 'warning':
      return 'warning'
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
      return '在线'
    case 'active':
      return '在线'
    case 'offline':
      return '离线'
    case 'warning':
      return '警告'
    case 'error':
      return '故障'
    case 'disabled':
      return '已禁用'
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
