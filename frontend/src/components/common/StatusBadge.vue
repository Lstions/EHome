<template>
  <el-tag :type="tagType" size="small">
    {{ statusText }}
  </el-tag>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  status: 'online' | 'offline' | 'error'
}>()

const tagType = computed(() => {
  switch (props.status) {
    case 'online':
      return 'success'
    case 'offline':
      return 'info'
    case 'error':
      return 'danger'
    default:
      return ''
  }
})

const statusText = computed(() => {
  switch (props.status) {
    case 'online':
      return '在线'
    case 'offline':
      return '离线'
    case 'error':
      return '错误'
    default:
      return props.status
  }
})
</script>
