<template>
  <div
    class="stat-card"
    :class="{ clickable: isClickable }"
    v-bind="attrs"
    :role="isClickable ? 'button' : undefined"
    :tabindex="isClickable ? 0 : undefined"
    :aria-label="isClickable ? `查看${label}` : undefined"
    @keydown.enter.prevent="handleKeyboardActivate"
    @keydown.space.prevent="handleKeyboardActivate"
  >
    <div class="stat-icon" :style="iconColor ? { color: iconColor } : undefined">
      <slot name="icon" />
    </div>
    <div class="stat-content">
      <slot name="value">
        <span class="stat-value">{{ value }}</span>
      </slot>
      <span class="stat-label">
      <template v-if="mobileLabel">
        <span class="stat-label-desktop">{{ label }}</span>
        <span class="stat-label-mobile" aria-hidden="true">{{ mobileLabel }}</span>
      </template>
      <template v-else>{{ label }}</template>
    </span>
    </div>
    <slot name="suffix" />
  </div>
</template>

<script setup lang="ts">
import { computed, useAttrs } from 'vue'

defineOptions({ inheritAttrs: false })

defineProps<{
  label: string
  mobileLabel?: string
  value?: string | number
  iconColor?: string
}>()

const attrs = useAttrs()
const isClickable = computed(() => Boolean(attrs.onClick))

function handleKeyboardActivate(event: KeyboardEvent) {
  const onClick = attrs.onClick
  if (Array.isArray(onClick)) {
    onClick.forEach(handler => handler(event))
  } else if (typeof onClick === 'function') {
    onClick(event)
  }
}
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
.stat-card.clickable:focus-visible {
  outline: 3px solid var(--el-color-primary);
  outline-offset: 2px;
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
  min-width: 0;
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
  /* 中文标签防止逐字断行竖排 */
  word-break: keep-all;
  overflow-wrap: break-word;
}
/* 桌面端默认隐藏移动端简写标签，仅显示完整标签 */
.stat-label-mobile { display: none; }

/* 移动端：单行 4 列纵向紧凑小卡（卡宽 ~80px），压低占高让位给内容区 */
@media (max-width: 768px) {
  .stat-card {
    flex-direction: column;
    align-items: center;
    text-align: center;
    gap: 4px;
    padding: 8px 4px;
    border-radius: 10px;
  }
  .stat-icon {
    width: 22px;
    height: 22px;
    border-radius: 6px;
    font-size: 13px;
    flex-shrink: 0;
  }
  .stat-content {
    width: 100%;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 1px;
  }
  .stat-value {
    font-size: 16px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 100%;
  }
  .stat-label {
    font-size: 10px;
    line-height: 1.3;
    max-height: 2.6em;
    overflow: hidden;
    word-break: keep-all;
    overflow-wrap: break-word;
  }
  .stat-label-desktop { display: none; }
  .stat-label-mobile { display: inline; }
}
</style>
