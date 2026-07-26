<template>
  <div class="page-header">
    <div class="page-header-left">
      <el-button v-if="showBack" :icon="ArrowLeft" circle aria-label="返回上一页" @click="goBack" />
      <div>
        <h2>{{ title }}</h2>
        <p v-if="subtitle" class="page-header-subtitle">{{ subtitle }}</p>
      </div>
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
  subtitle?: string
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

.page-header-subtitle {
  margin: 4px 0 0;
  font-size: 13px;
  color: var(--el-text-color-secondary);
}

.page-header-right {
  display: flex;
  align-items: center;
  gap: 8px;
}

@media (max-width: 768px) {
  .page-header {
    align-items: flex-start;
    gap: 12px;
    padding: 14px 16px;
  }

  .page-header-left h2 {
    font-size: 18px;
  }

  .page-header-right {
    flex-wrap: wrap;
    justify-content: flex-end;
  }
}
</style>
