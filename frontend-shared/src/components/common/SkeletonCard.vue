<template>
  <div class="skeleton-card" :class="[variant, { animated }]">
    <!-- 统计卡片变体 -->
    <template v-if="variant === 'stat'">
      <div class="skeleton-icon" :style="{ width: iconSize + 'px', height: iconSize + 'px' }"></div>
      <div class="skeleton-content">
        <div class="skeleton-value"></div>
        <div class="skeleton-label"></div>
      </div>
    </template>
    
    <!-- 列表项变体 -->
    <template v-else-if="variant === 'list-item'">
      <div class="skeleton-avatar"></div>
      <div class="skeleton-list-content">
        <div class="skeleton-title"></div>
        <div class="skeleton-desc"></div>
      </div>
    </template>
    
    <!-- 表格行变体 -->
    <template v-else-if="variant === 'table-row'">
      <div v-for="i in columns" :key="i" class="skeleton-cell"></div>
    </template>
    
    <!-- 卡片变体 -->
    <template v-else>
      <div class="skeleton-image"></div>
      <div class="skeleton-body">
        <div class="skeleton-title"></div>
        <div class="skeleton-text"></div>
        <div class="skeleton-text short"></div>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
defineProps<{
  variant?: 'default' | 'stat' | 'list-item' | 'table-row' | 'card'
  columns?: number
  animated?: boolean
  iconSize?: number
}>()
</script>

<style scoped>
.skeleton-card {
  background: var(--card-bg);
  border-radius: 8px;
  overflow: hidden;
}

.skeleton-card.animated {
  animation: skeleton-pulse 1.5s ease-in-out infinite;
}

/* 统计卡片变体 */
.skeleton-card.stat {
  display: flex;
  align-items: center;
  gap: 16px;
  padding: 20px;
}

.skeleton-icon {
  border-radius: 12px;
  background: var(--skeleton-shimmer);
  background-size: 200% 100%;
}

.skeleton-card.animated .skeleton-icon {
  animation: skeleton-shimmer 1.5s infinite;
}

.skeleton-content {
  flex: 1;
}

.skeleton-value {
  height: 28px;
  width: 60%;
  border-radius: 4px;
  background: var(--skeleton-shimmer);
  background-size: 200% 100%;
  margin-bottom: 8px;
}

.skeleton-card.animated .skeleton-value {
  animation: skeleton-shimmer 1.5s infinite;
}

.skeleton-label {
  height: 14px;
  width: 40%;
  border-radius: 4px;
  background: var(--skeleton-shimmer);
  background-size: 200% 100%;
}

.skeleton-card.animated .skeleton-label {
  animation: skeleton-shimmer 1.5s infinite;
}

/* 列表项变体 */
.skeleton-card.list-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 16px;
}

.skeleton-avatar {
  width: 40px;
  height: 40px;
  border-radius: 50%;
  background: var(--skeleton-shimmer);
  background-size: 200% 100%;
  flex-shrink: 0;
}

.skeleton-list-content {
  flex: 1;
}

.skeleton-title {
  height: 16px;
  width: 60%;
  border-radius: 4px;
  background: var(--skeleton-shimmer);
  background-size: 200% 100%;
  margin-bottom: 8px;
}

.skeleton-desc {
  height: 12px;
  width: 80%;
  border-radius: 4px;
  background: var(--skeleton-shimmer);
  background-size: 200% 100%;
}

/* 表格行变体 */
.skeleton-card.table-row {
  display: flex;
  gap: 16px;
  padding: 16px 20px;
  border-bottom: 1px solid var(--border-color-light);
}

.skeleton-cell {
  height: 16px;
  border-radius: 4px;
  background: var(--skeleton-shimmer);
  background-size: 200% 100%;
  flex: 1;
}

.skeleton-cell:nth-child(odd) {
  width: 80%;
}

.skeleton-cell:nth-child(even) {
  width: 60%;
}

/* 卡片变体 */
.skeleton-card.card {
  padding: 16px;
}

.skeleton-image {
  height: 160px;
  border-radius: 8px;
  background: var(--skeleton-shimmer);
  background-size: 200% 100%;
  margin-bottom: 16px;
}

.skeleton-body .skeleton-title {
  height: 20px;
  width: 70%;
  margin-bottom: 12px;
}

.skeleton-body .skeleton-text {
  height: 14px;
  width: 100%;
  border-radius: 4px;
  background: var(--skeleton-shimmer);
  background-size: 200% 100%;
  margin-bottom: 8px;
}

.skeleton-body .skeleton-text.short {
  width: 60%;
}

.skeleton-card.animated .skeleton-image,
.skeleton-card.animated .skeleton-title,
.skeleton-card.animated .skeleton-text {
  animation: skeleton-shimmer 1.5s infinite;
}

@keyframes skeleton-shimmer {
  0% { background-position: 200% 0; }
  100% { background-position: -200% 0; }
}

@keyframes skeleton-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.6; }
}
</style>
