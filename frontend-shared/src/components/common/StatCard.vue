<template>
  <div
    class="stat-card"
    :class="{ clickable: !!$attrs.onClick }"
    v-bind="$attrs"
  >
    <div class="stat-icon" :style="iconColor ? { color: iconColor } : undefined">
      <slot name="icon" />
    </div>
    <div class="stat-content">
      <slot name="value">
        <span class="stat-value">{{ value }}</span>
      </slot>
      <span class="stat-label">{{ label }}</span>
    </div>
    <slot name="suffix" />
  </div>
</template>

<script setup lang="ts">
defineProps<{
  label: string
  value?: string | number
  iconColor?: string
}>()
</script>

<style scoped>
.stat-card {
  background: var(--card-bg);
  border-radius: 12px;
  padding: 20px;
  display: flex;
  align-items: center;
  gap: 16px;
  border: 1px solid var(--el-border-color);
}
.stat-card.clickable {
  cursor: pointer;
  transition: all 0.3s;
}
.stat-card.clickable:hover {
  transform: translateY(-2px);
  box-shadow: var(--shadow-md);
}
.stat-icon {
  width: 48px;
  height: 48px;
  border-radius: 12px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 24px;
  color: var(--el-text-color-secondary);
}
.stat-content {
  flex: 1;
}
.stat-value {
  display: block;
  font-size: 28px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  line-height: 1.2;
}
.stat-label {
  font-size: 13px;
  color: var(--el-text-color-secondary);
}
</style>
