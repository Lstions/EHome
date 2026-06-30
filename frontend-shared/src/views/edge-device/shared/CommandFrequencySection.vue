<template>
  <el-card style="margin-top: 20px;" shadow="hover" v-if="deviceType">
    <template #header><span>指令频率</span></template>
    <CommandList :device-id="deviceId" :device-type="deviceType" />
  </el-card>
</template>

<script setup lang="ts">
import CommandList from '@/components/device/CommandList.vue'

defineProps<{
  deviceId: number
  deviceType: string
}>()
</script>
