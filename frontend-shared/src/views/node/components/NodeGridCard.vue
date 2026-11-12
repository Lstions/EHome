<template>
  <el-card 
    class="collector-card"
    :class="{ offline: node.status === 'offline' }"
    shadow="hover"
    role="button"
    tabindex="0"
    :aria-label="`查看节点详情：${nodeDisplayName(node)}`"
    @click="goToDetail(node.node_id)"
    @keydown.enter.prevent="goToDetail(node.node_id)"
    @keydown.space.prevent="goToDetail(node.node_id)"
  >
    <div class="card-header">
      <div class="collector-info">
        <div class="collector-icon" :class="node.status">
          <el-icon :size="24"><Cpu /></el-icon>
        </div>
        <div class="collector-meta">
          <h3>{{ nodeDisplayName(node) }}</h3>
          <span class="model">{{ node.model || '未知型号' }}</span>
        </div>
      </div>
      <div class="status-tag" :class="node.status">
        <span class="status-dot"></span>
        {{ node.status === 'online' ? '在线' : '离线' }}
      </div>
    </div>
    
    <div class="card-body">
      <div class="info-row">
        <span class="label">设备ID</span>
        <span class="value">{{ node.node_id }}</span>
      </div>
      <div class="info-row">
        <span class="label">固件版本</span>
        <el-tag size="small">{{ node.firmware_version || '未知' }}</el-tag>
      </div>
      <div class="info-row">
        <span class="label">连接质量</span>
        <div class="quality-bar" v-if="node.status === 'online'">
          <el-progress 
            :percentage="node.connection_quality || 0" 
            :stroke-width="6"
            :color="getQualityColor(node.connection_quality)"
            :show-text="false"
          />
          <span class="quality-value">{{ node.connection_quality || 0 }}%</span>
        </div>
        <span v-else class="value">-</span>
      </div>
      <div class="info-row">
        <span class="label">上线时间</span>
        <span class="value time">{{ formatRelativeTime(node.last_online_time) }}</span>
      </div>
    </div>
    
    <div class="card-footer">
      <el-button size="small" text @click.stop="handleQuickAction('config', node)">
        <el-icon><Setting /></el-icon>
        配置
      </el-button>
      <el-button size="small" text @click.stop="handleQuickAction('ota', node)">
        <el-icon><Upload /></el-icon>
        升级
      </el-button>
      <el-button size="small" text type="danger" @click.stop="handleDelete(node)">
        <el-icon><Delete /></el-icon>
        删除
      </el-button>
    </div>
  </el-card>
</template>

<script setup lang="ts">
import { Cpu, Setting, Upload, Delete } from '@element-plus/icons-vue'
import { nodeDisplayName } from '@/utils/nodeDisplay'
import { getQualityColor } from '@/utils/theme'
import { formatRelativeTime } from '../nodeListFormat'

/**
 * 节点卡片（/node 列表页 grid 视图的单卡）。
 *
 * 边界依据：卡片是自包含模板片段 —— 只读一个 node 对象、只上抛用户动作，不持有
 * 列表/筛选/分页状态（状态仍在页面 composable 里，避免出现第二套口径）。
 * 转发器刻意沿用原模板里的调用名与实参形态（goToDetail(node.node_id) 等），
 * 所以模板文本与拆分前逐字一致，可点击语义门禁（role/tabindex/aria-label/keydown）
 * 也仍然钉在同一个 <el-card> 开标签上。
 */
defineProps<{ node: any }>()

const emit = defineEmits<{
  (e: 'open', nodeId: string): void
  (e: 'quick-action', action: string, node: any): void
  (e: 'delete', node: any): void
}>()

// 纯转发：行为与拆分前一致（路由跳转/开对话框/删除确认都由页面 composable 负责）。
const goToDetail = (nodeId: string) => emit('open', nodeId)
const handleQuickAction = (action: string, node: any) => emit('quick-action', action, node)
const handleDelete = (node: any) => emit('delete', node)
</script>

<style scoped>
.collector-card {
  cursor: pointer;
  transition: all 0.3s;
  border: 1px solid var(--el-border-color);
}

.collector-card:focus-visible {
  outline: 2px solid var(--el-color-primary);
  outline-offset: 2px;
}

.collector-card:hover {
  transform: translateY(-4px);
  box-shadow: var(--shadow-lg);
}

.collector-card.offline {
  opacity: 0.8;
}

.card-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  margin-bottom: 16px;
}

.collector-info {
  display: flex;
  gap: 12px;
}

.collector-meta h3 {
  margin: 0;
  font-size: 16px;
  color: var(--el-text-color-primary);
}

.collector-meta .model {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.status-tag {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  border-radius: 20px;
  font-size: 12px;
}

.status-tag.online {
  background: var(--el-color-success-light-9);
  color: var(--el-color-success);
}

.status-tag.offline {
  background: var(--el-fill-color-light);
  color: var(--el-text-color-secondary);
}

.status-tag .status-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
}

.status-tag.online .status-dot {
  animation: pulse 2s infinite;
}

.card-body {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 16px 0;
  border-top: 1px solid var(--el-fill-color-light);
  border-bottom: 1px solid var(--el-fill-color-light);
}

.info-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.info-row .label {
  font-size: 13px;
  color: var(--el-text-color-secondary);
}

.info-row .value {
  font-size: 13px;
  color: var(--el-text-color-regular);
}

.info-row .value.time {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}

.card-footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding-top: 12px;
}

/* 动画 */
@keyframes pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.5; }
}
</style>

<style scoped src="./nodeList.shared.css"></style>
