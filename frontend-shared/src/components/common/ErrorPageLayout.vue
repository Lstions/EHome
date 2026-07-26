<template>
  <div class="error-page">
    <div class="error-content" :style="{ maxWidth }">
      <div class="error-code" :style="{ background: gradient, '-webkit-background-clip': 'text', '-webkit-text-fill-color': 'transparent', 'background-clip': 'text' }">
        {{ code }}
      </div>
      <h1 class="error-title">{{ title }}</h1>
      <div class="error-desc"><slot name="description">{{ description }}</slot></div>
      <slot name="extra" />
      <div class="error-actions">
        <slot name="actions" />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
withDefaults(defineProps<{
  code: string
  title: string
  description?: string
  gradient?: string
  maxWidth?: string
}>(), {
  description: '',
  gradient: 'linear-gradient(135deg, var(--el-color-primary), var(--el-color-success))',
  maxWidth: '480px',
})
</script>

<style scoped>
.error-page {
  display: flex;
  justify-content: center;
  align-items: center;
  min-height: 100vh;
  background: var(--bg-color-page);
  padding: 24px;
}
.error-content {
  text-align: center;
}
.error-code {
  font-size: 120px;
  font-weight: 700;
  line-height: 1;
  margin-bottom: 16px;
}
.error-title {
  font-size: 24px;
  font-weight: 600;
  color: var(--text-color-primary);
  margin: 0 0 12px;
}
.error-desc {
  font-size: 14px;
  color: var(--text-color-secondary);
  margin: 0 0 32px;
  line-height: 1.6;
}
.error-actions {
  display: flex;
  gap: 12px;
  justify-content: center;
}
</style>
