<!--
  顶栏通知下拉的**内容面板**（通知中心 / 全部已读 / 列表 / 空态）。

  为什么切成子组件：这一段是自包含的模板片段 —— 只消费「通知列表」一个只读值，
  只产出两个动作（点某条通知、全部已读）。铃铛按钮、el-badge 与 el-popover 的
  引用槽仍留在页面：它们由页面的 scoped 规则直接命中（`.main-header .el-button.is-circle`
  的 44px 触控盒见 TouchTargets.spec；`.notification-badge :deep(.el-badge__content)`
  的背景色），搬进子组件会失去父组件的 data-v 作用域 ⇒ 视觉与触控尺寸都会变。

  数据面（REST 拉取 / WS 订阅 / 已读同步）在 composables/useNotificationCenter.ts，
  本组件是纯展示层：不改 props、不算状态、不做请求。
-->
<template>
  <div class="notification-panel">
    <div class="notification-header">
      <span>通知中心</span>
      <el-button link type="primary" size="small" @click="emit('read-all')">全部已读</el-button>
    </div>
    <el-scrollbar max-height="300px">
      <div v-if="items.length === 0" class="notification-empty">
        <el-icon :size="32"><Bell /></el-icon>
        <p>暂无新通知</p>
      </div>
      <div v-else class="notification-list">
        <button
          v-for="item in items"
          :key="item.id"
          type="button"
          class="notification-item"
          :class="{ unread: !item.read }"
          :aria-label="`通知：${item.title}`"
          @click="emit('select', item)"
        >
          <div class="notification-icon" :class="item.type">
            <el-icon><WarningFilled v-if="item.type === 'warning'" /><SuccessFilled v-else-if="item.type === 'success'" /><InfoFilled v-else /></el-icon>
          </div>
          <div class="notification-content">
            <p class="notification-title">{{ item.title }}</p>
            <p class="notification-desc">{{ item.description }}</p>
            <span class="notification-time">{{ item.created_at }}</span>
          </div>
        </button>
      </div>
    </el-scrollbar>
  </div>
</template>

<script setup lang="ts">
import { Bell, InfoFilled, SuccessFilled, WarningFilled } from '@element-plus/icons-vue'
import type { Notification as ApiNotification } from '@/api/notification'

defineProps<{ items: ApiNotification[] }>()

const emit = defineEmits<{
  (e: 'select', item: ApiNotification): void
  (e: 'read-all'): void
}>()
</script>

<style scoped>
.notification-panel {
  margin: -12px;
}

.notification-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 12px 16px;
  border-bottom: 1px solid var(--divider-color);
  font-weight: 500;
}

.notification-empty {
  padding: 40px 20px;
  text-align: center;
  color: var(--el-text-color-secondary);
}

.notification-empty p {
  margin-top: 8px;
}

/* 通知条目：<button> 承载（自带焦点与 Enter/Space 激活），视觉与原 <div> 一致 */
.notification-item {
  display: flex;
  width: 100%;
  gap: 12px;
  padding: 12px 16px;
  border: 0;
  background: transparent;
  text-align: left;
  font: inherit;
  cursor: pointer;
  transition: background 0.2s;
}

.notification-item:hover {
  background: var(--el-fill-color-light);
}

.notification-item:focus-visible {
  outline: 2px solid var(--el-color-primary);
  outline-offset: -2px;
}

.notification-item.unread {
  background: var(--el-color-primary-light-9);
}

.notification-icon {
  width: 32px;
  height: 32px;
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}

.notification-icon.info {
  background: var(--el-color-primary-light-9);
  color: var(--el-color-primary);
}

.notification-icon.warning {
  background: var(--el-color-danger-light-9);
  color: var(--el-color-warning);
}

.notification-icon.success {
  background: var(--el-color-success-light-9);
  color: var(--el-color-success);
}

.notification-content {
  flex: 1;
  min-width: 0;
}

.notification-title {
  margin: 0;
  font-size: 13px;
  font-weight: 500;
  color: var(--text-color-primary);
}

.notification-desc {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.notification-time {
  font-size: 11px;
  color: var(--el-text-color-placeholder);
}
</style>
