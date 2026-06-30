<template>
  <div class="page-header">
    <div class="page-header-left">
      <el-button v-if="showBack" :icon="ArrowLeft" circle @click="goBack" />
      <h2>{{ title }}</h2>
    </div>
    <div class="page-header-right">
      <slot name="extra"></slot>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ArrowLeft } from '@element-plus/icons-vue'

const props = defineProps<{
  title: string
  showBack?: boolean
}>()

const emit = defineEmits<{
  (e: 'back'): void
}>()

const goBack = () => {
  emit('back')
}
</script>

<style scoped>
.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 16px 24px;
  background: var(--el-bg-color);
  border-bottom: 1px solid var(--el-border-color);
  margin-bottom: 16px;
}

.page-header-left {
  display: flex;
  align-items: center;
  gap: 12px;
}

.page-header-left h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 500;
  color: var(--el-text-color-primary);
}

.page-header-right {
  display: flex;
  align-items: center;
  gap: 8px;
}
</style>
