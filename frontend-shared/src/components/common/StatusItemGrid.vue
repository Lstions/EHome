<template>
  <div class="status-item-grid">
    <template v-if="items.length > 0">
      <div
        v-for="item in items"
        :key="item.key"
        class="grid-item"
        :class="{ active: item.active }"
      >
        <el-icon :size="iconSize" :color="item.active ? THEME_COLORS.danger : THEME_COLORS.success">
          <component :is="item.active ? WarningFilled : CircleCheck" />
        </el-icon>
        <span class="grid-label">{{ item.label }}</span>
        <el-tag size="small" :type="item.active ? 'danger' : 'success'" effect="plain">
          {{ item.active ? activeText : inactiveText }}
        </el-tag>
      </div>
    </template>
    <slot name="summary" v-if="items.length === 0" />
    <div v-if="items.length === 0 && !$slots.summary" class="grid-empty">
      <el-empty :description="emptyText" :image-size="60" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { WarningFilled, CircleCheck } from '@element-plus/icons-vue'
import { THEME_COLORS } from '@/utils/theme'

export interface StatusItem {
  key: string
  label: string
  active: boolean
}

withDefaults(defineProps<{
  items: StatusItem[]
  activeText?: string
  inactiveText?: string
  emptyText?: string
  iconSize?: number
}>(), {
  activeText: '异常',
  inactiveText: '正常',
  emptyText: '无状态数据',
  iconSize: 18,
})
</script>

<style scoped>
.status-item-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
  gap: 12px;
}
.grid-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  background: var(--el-fill-color-lighter);
  border-radius: 6px;
  border: 1px solid var(--el-border-color-lighter);
}
.grid-item.active {
  background: var(--el-color-danger-light-9);
  border-color: var(--el-color-danger-light-5);
}
.grid-label {
  flex: 1;
  font-size: 13px;
}
.grid-empty {
  grid-column: 1 / -1;
}
</style>
