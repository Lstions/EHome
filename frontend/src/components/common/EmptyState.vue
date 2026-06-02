<template>
  <div class="empty-state" :class="[size]">
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
  quickActions?: QuickAction[]
}>(), {
  icon: FolderOpened,
  title: '暂无数据',
  description: '',
  size: 'default'
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
  background: linear-gradient(135deg, #f5f7fa 0%, #e8eaec 100%);
  margin-bottom: 20px;
  color: #c0c4cc;
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
  color: #303133;
}

.empty-description {
  margin: 0 0 16px;
  font-size: 14px;
  color: #909399;
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
.empty-state.data .empty-illustration {
  background: linear-gradient(135deg, #ecf5ff 0%, #d9ecff 100%);
  color: #409eff;
}

.empty-state.device .empty-illustration {
  background: linear-gradient(135deg, #f0f9eb 0%, #e1f3d8 100%);
  color: #67c23a;
}

.empty-state.warning .empty-illustration {
  background: linear-gradient(135deg, #fef0f0 0%, #fde2e2 100%);
  color: #f56c6c;
}

.empty-state.network .empty-illustration {
  background: linear-gradient(135deg, #fef9f0 0%, #f5e6d3 100%);
  color: #e6a23c;
}
</style>
