<!--
  GPIOPinCard.vue — 兼容导出（重定向到 GPIOPinRow）
  保留旧导入路径不破坏，实际逻辑见 GPIOPinRow.vue
  规格: docs/设计/GPIO_PWM_UI重设计规格.md §11
-->
<script setup lang="ts">
import GPIOPinRow from './GPIOPinRow.vue'
import type { GPIOConfig } from '@/api/periph'

defineProps<{
  config: GPIOConfig
  nodeId: string
  offline?: boolean
}>()

defineEmits<{
  (e: 'remove', pin: number): void
}>()
</script>

<template>
  <GPIOPinRow v-bind="$attrs" :config="config" :node-id="nodeId" :offline="offline" @remove="$emit('remove', $event)" />
</template>
