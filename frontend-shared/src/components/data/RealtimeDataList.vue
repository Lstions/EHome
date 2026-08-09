<template>
  <div class="realtime-data-list">
    <div class="list-header">
      <div class="display-mode">
        <span class="label">显示模式:</span>
        <el-radio-group v-model="displayMode" size="small">
          <el-radio-button value="text">明文</el-radio-button>
          <el-radio-button value="hex">16进制</el-radio-button>
        </el-radio-group>
      </div>
      <div class="list-stats">
        <el-tag size="small" type="info">
          共 {{ items.length }} 条数据
        </el-tag>
        <el-button
          size="small"
          :icon="Delete"
          @click="handleClear"
          :disabled="items.length === 0"
        >
          清空
        </el-button>
      </div>
    </div>

    <!-- 普通滚动列表：行高随内容自然撑开（多行帧不裁剪），条数上限由数据层 composable 截断 -->
    <div v-if="items.length > 0" class="plain-list" ref="listContainer">
      <div v-for="item in items" :key="item.id" class="data-item">
        <div class="item-header">
          <span class="timestamp">{{ formatTime(item.timestamp) }}</span>
          <div class="item-tags">
            <el-tag
              v-if="item.data?.error_code && item.data.error_code > 0"
              :type="getErrorInfo(item.data.error_code).type"
              size="small"
            >
              {{ getErrorInfo(item.data.error_code).label }}
            </el-tag>
            <el-tag size="small" :type="item.isRealtime ? 'success' : 'info'">
              {{ item.isRealtime ? '实时' : '历史' }}
            </el-tag>
          </div>
        </div>
        <div class="item-content" :class="{ 'hex-mode': displayMode === 'hex' }">
          {{ formatItemData(item) }}
        </div>
      </div>
    </div>

    <EmptyState
      v-else
      kind="initial"
      size="small"
      title="暂无实时数据"
      description="等待设备上报..."
    />
  </div>
</template>

<script setup lang="ts">
import { ref, nextTick, watch } from 'vue'
import { Delete } from '@element-plus/icons-vue'
import EmptyState from '@/components/common/EmptyState.vue'
import { formatTime, formatDataPlainText, dataToHexString, bytesToHex } from '@/utils/format'
import { getErrorInfo } from '@/utils/errorCode'

import type { DataItem } from '@/types/realtime'

interface Props {
  items: DataItem[]
  autoScroll?: boolean
  deviceType?: string
}

const props = withDefaults(defineProps<Props>(), {
  items: () => [],
  autoScroll: true,
  deviceType: ''
})

const emit = defineEmits<{
  (e: 'clear'): void
}>()

// UI-only state: display mode toggle (plaintext / hex)
const displayMode = ref<'text' | 'hex'>('text')
const listContainer = ref<HTMLElement | null>(null)

// Clear: emit to parent so it can manage the source data
const handleClear = () => {
  emit('clear')
}

// 新数据到达时滚回顶部（最新在前）。
// 监听首条 id 而非 items.length：上游按 maxItems 截断后满额增删同进，
// length 恒定不变，watch length 会永不触发。
watch(() => props.items[0]?.id, () => {
  if (props.autoScroll) {
    nextTick(() => {
      if (listContainer.value) {
        listContainer.value.scrollTop = 0
      }
    })
  }
})

// 格式化数据项；结果为空时给中性占位，不出现疑似渲染失败的裸占位符（规范 3.4.5）
const formatItemData = (item: DataItem): string => {
  let text: string
  if (displayMode.value === 'hex') {
    if (item.rawData && item.rawData.length > 0) {
      text = bytesToHex(item.rawData)
    } else if (item.data && typeof item.data === 'object' && Object.keys(item.data).length === 0) {
      // 空对象走兜底：dataToHexString({}) 会产出 '7B 7D'，形似真实 2 字节帧，误导
      text = ''
    } else {
      text = dataToHexString(item.data)
    }
  } else {
    text = formatDataPlainText(item.data, props.deviceType)
  }
  return text.trim() ? text : '(无数据字段)'
}
</script>

<style scoped>
.realtime-data-list {
  display: flex;
  flex-direction: column;
  height: 100%;
}

.list-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  padding: 12px 16px;
  margin-bottom: 4px;
  border-bottom: 1px solid var(--el-border-color-lighter);
  background: var(--el-bg-color);
}

.display-mode {
  display: flex;
  align-items: center;
  gap: 12px;
}

.display-mode .label {
  font-size: 14px;
  color: var(--el-text-color-regular);
}

.list-stats {
  display: flex;
  align-items: center;
  gap: 12px;
}

/* 滚动容器：高度随内容增长，封顶 max-height；条数少时不撑空白框 */
.plain-list {
  overflow-y: auto;
  max-height: 520px;
}

.data-item {
  padding: 12px 16px;
  border-bottom: 1px solid var(--el-border-color-lighter);
  background: var(--el-bg-color);
  transition: background-color 0.2s;
  box-sizing: border-box;
}

.data-item:last-child {
  border-bottom: none;
}

.data-item:hover {
  background: var(--el-fill-color-light);
}

.item-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 8px;
}

.item-tags {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
}

.timestamp {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  font-family: monospace;
}

.item-content {
  font-size: 13px;
  color: var(--el-text-color-primary);
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-all;
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
}

.item-content.hex-mode {
  font-family: 'SF Mono', 'Consolas', 'Monaco', monospace;
  font-size: 12px;
  color: var(--el-color-primary);
  background: var(--el-fill-color-light);
  padding: 8px 12px;
  border-radius: 4px;
}

@media (max-width: 768px) {
  .plain-list {
    max-height: 360px;
  }

  .list-header {
    align-items: stretch;
    padding: 10px 0;
  }

  .display-mode,
  .list-stats {
    justify-content: space-between;
    width: 100%;
  }

  .display-mode .label {
    font-size: 12px;
  }

  .data-item {
    padding: 10px 0;
  }

  .item-content {
    font-size: 12px;
  }
}
</style>
