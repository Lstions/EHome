<template>
  <div class="mobile-stat-card" :class="variant" @click="emit('click')">
    <div class="mobile-stat-main">
      <div class="mobile-stat-icon">
        <el-icon :size="iconSize">
          <component :is="icon" />
        </el-icon>
      </div>
      <div class="mobile-stat-body">
        <span class="mobile-stat-value">{{ displayValue }}</span>
        <span class="mobile-stat-label">{{ label }}</span>
      </div>
    </div>
    <div v-if="trend !== undefined || badge !== undefined" class="mobile-stat-extra">
      <span v-if="badge !== undefined" class="mobile-stat-badge">{{ badge }}</span>
      <span v-else-if="trend !== undefined" class="mobile-stat-trend" :class="{ up: trend > 0, down: trend < 0 }">
        <el-icon v-if="trend > 0"><ArrowUp /></el-icon>
        <el-icon v-else-if="trend < 0"><ArrowDown /></el-icon>
        {{ Math.abs(trend) }}%
      </span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { ArrowUp, ArrowDown } from '@element-plus/icons-vue'
import type { Component } from 'vue'

const props = withDefaults(defineProps<{
  value: number | string
  label: string
  icon: Component
  variant?: 'primary' | 'success' | 'danger' | 'warning' | 'info'
  trend?: number
  badge?: string | number
  iconSize?: number
}>(), {
  variant: 'primary',
  iconSize: 22
})

const emit = defineEmits<{
  click: []
}>()

const displayValue = computed(() => {
  const num = Number(props.value)
  if (Number.isNaN(num)) return props.value
  if (num >= 10000) return `${(num / 1000).toFixed(1)}k`
  return String(num)
})
</script>

<style scoped>
.mobile-stat-card {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 10px 12px;
  border-radius: 10px;
  background: var(--card-bg);
  border: 1px solid var(--el-border-color);
  cursor: pointer;
  transition: transform 0.2s, box-shadow 0.2s;
  min-width: 0;
}

.mobile-stat-card:hover {
  transform: translateY(-2px);
  box-shadow: var(--shadow-sm);
}

.mobile-stat-main {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  flex: 1;
}

.mobile-stat-icon {
  width: 34px;
  height: 34px;
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  flex-shrink: 0;
  background: linear-gradient(135deg, var(--el-color-primary) 0%, var(--el-color-success) 100%);
}

.mobile-stat-card.success .mobile-stat-icon { background: var(--el-color-success); }
.mobile-stat-card.danger .mobile-stat-icon { background: var(--el-color-danger); }
.mobile-stat-card.warning .mobile-stat-icon { background: var(--el-color-warning); }
.mobile-stat-card.info .mobile-stat-icon { background: var(--el-text-color-secondary); }

.mobile-stat-body {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
}

.mobile-stat-value {
  font-size: 20px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  line-height: 1.2;
}

.mobile-stat-label {
  font-size: 13px;
  color: var(--el-text-color-secondary);
  line-height: 1.3;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.mobile-stat-extra {
  display: flex;
  align-items: center;
  flex-shrink: 0;
}

.mobile-stat-badge {
  font-size: 12px;
  padding: 2px 6px;
  border-radius: 10px;
  background: var(--el-color-success-light-9);
  color: var(--el-color-success);
  font-weight: 500;
}

.mobile-stat-trend {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  font-size: 12px;
  font-weight: 500;
  color: var(--el-text-color-secondary);
}

.mobile-stat-trend.up {
  color: var(--el-color-success);
}

.mobile-stat-trend.down {
  color: var(--el-color-danger);
}

@media (max-width: 360px) {
  .mobile-stat-card {
    padding: 8px 10px;
  }
  .mobile-stat-icon {
    width: 30px;
    height: 30px;
  }
  .mobile-stat-value {
    font-size: 18px;
  }
  .mobile-stat-label {
    font-size: 12px;
  }
}
</style>
