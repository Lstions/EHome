<template>
  <div class="empty-state" :class="[size, kind]">
    <div class="empty-illustration">
      <slot name="illustration">
        <el-icon :size="iconSize"><component :is="icon" /></el-icon>
      </slot>
    </div>
    
    <div class="empty-content">
      <p class="empty-title">{{ title }}</p>
      <p v-if="description" class="empty-description">{{ description }}</p>
      
      <div v-if="$slots.action" class="empty-action">
        <slot name="action" />
      </div>
      
      <!-- 快捷操作 -->
      <div v-if="quickActions?.length" class="empty-quick-actions">
        <el-button
          v-for="action in quickActions"
          :key="action.label"
          :type="action.type"
          size="small"
          @click="action.handler"
        >
          <el-icon v-if="action.icon"><component :is="action.icon" /></el-icon>
          {{ action.label }}
        </el-button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { FolderOpened, Document, User, Warning } from '@element-plus/icons-vue'

interface QuickAction {
  label: string
  icon?: any
  type?: 'primary' | 'default' | 'danger'
  handler: () => void
}

const props = withDefaults(defineProps<{
  icon?: any
  title?: string
  description?: string
  size?: 'default' | 'small' | 'large'
  kind?: 'empty' | 'initial' | 'filtered' | 'error' | 'permission'
  quickActions?: QuickAction[]
}>(), {
  icon: FolderOpened,
  title: '暂无数据',
  description: '',
  size: 'default',
  kind: 'empty'
})

const iconSize = computed(() => {
  switch (props.size) {
    case 'small': return 32
    case 'large': return 64
    default: return 48
  }
})
</script>

<style scoped>
.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 40px 20px;
  text-align: center;
}

.empty-state.small {
  padding: 20px;
}

.empty-state.large {
  padding: 60px 20px;
}

.empty-illustration {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 100px;
  height: 100px;
  border-radius: 50%;
  background: linear-gradient(135deg, var(--el-fill-color) 0%, var(--el-fill-color-darker) 100%);
  margin-bottom: 20px;
  color: var(--el-text-color-secondary);
}

.empty-state.small .empty-illustration {
  width: 60px;
  height: 60px;
}

.empty-state.large .empty-illustration {
  width: 120px;
  height: 120px;
}

.empty-content {
  max-width: 400px;
}

.empty-title {
  margin: 0 0 8px;
  font-size: 16px;
  font-weight: 500;
  color: var(--el-text-color-primary);
}

.empty-description {
  margin: 0 0 16px;
  font-size: 14px;
  color: var(--el-text-color-secondary);
  line-height: 1.6;
}

.empty-action {
  margin-bottom: 16px;
}

.empty-quick-actions {
  display: flex;
  gap: 8px;
  justify-content: center;
  flex-wrap: wrap;
}

/* 不同场景的插图颜色 */
.empty-state.initial .empty-illustration {
  background: var(--el-color-primary-light-9);
  color: var(--el-color-primary);
}

.empty-state.filtered .empty-illustration {
  background: var(--el-fill-color-dark);
  color: var(--el-text-color-secondary);
}

.empty-state.permission .empty-illustration {
  background: var(--el-color-warning-light-9);
  color: var(--el-color-warning);
}

.empty-state.error .empty-illustration {
  background: var(--el-color-danger-light-9);
  color: var(--el-color-danger);
}

.empty-state.device .empty-illustration {
  background: var(--el-color-success-light-9);
  color: var(--el-color-success);
}

.empty-state.warning .empty-illustration {
  background: var(--el-color-danger-light-9);
  color: var(--el-color-danger);
}

.empty-state.network .empty-illustration {
  background: var(--el-color-warning-light-9);
  color: var(--el-color-warning);
}
</style>
