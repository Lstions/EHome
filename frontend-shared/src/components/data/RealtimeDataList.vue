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

    <div class="list-container" ref="listContainer">
      <RecycleScroller
        v-if="items.length > 0"
        class="scroller"
        :items="items"
        :item-size="80"
        key-field="id"
        :buffer="200"
      >
        <template #default="{ item }">
          <div class="data-item">
            <div class="item-header">
              <span class="timestamp">{{ formatTime((item as DataItem).timestamp) }}</span>
              <el-tag v-if="(item as DataItem).data?.error_code && (item as DataItem).data.error_code > 0" :type="getErrorInfo((item as DataItem).data.error_code).type" size="small">
                {{ getErrorInfo((item as DataItem).data.error_code).label }}
              </el-tag>
              <el-tag size="small" :type="(item as DataItem).isRealtime ? 'success' : 'info'">
                {{ (item as DataItem).isRealtime ? '实时' : '历史' }}
              </el-tag>
            </div>
            <div class="item-content" :class="{ 'hex-mode': displayMode === 'hex' }">
              {{ formatItemData(item as DataItem) }}
            </div>
          </div>
        </template>
      </RecycleScroller>

      <el-empty v-else description="暂无实时数据，等待设备上报..." />
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, nextTick, watch } from 'vue'
import { RecycleScroller } from 'vue-virtual-scroller'
import 'vue-virtual-scroller/dist/vue-virtual-scroller.css'
import { Delete } from '@element-plus/icons-vue'
import { formatTime, formatDataPlainText, dataToHexString } from '@/utils/format'
import { getErrorInfo } from '@/utils/errorCode'

import type { DataItem } from '@/types/realtime'

interface Props {
  items: DataItem[]
  maxItems?: number
  autoScroll?: boolean
  deviceType?: string
}

const props = withDefaults(defineProps<Props>(), {
  items: () => [],
  maxItems: 500,
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

// Auto-scroll to top when new items arrive
watch(() => props.items.length, () => {
  if (props.autoScroll) {
    nextTick(() => {
      if (listContainer.value) {
        const scroller = listContainer.value.querySelector('.scroller')
        if (scroller) {
          scroller.scrollTop = 0
        }
      }
    })
  }
})

// 格式化数据项
const formatItemData = (item: DataItem): string => {
  if (displayMode.value === 'hex') {
    if (item.rawData && item.rawData.length > 0) {
      return bytesToHexLocal(item.rawData)
    }
    return dataToHexString(item.data)
  }
  return formatDataPlainText(item.data, props.deviceType)
}

// 本地16进制格式化
const bytesToHexLocal = (bytes: number[]): string => {
  return bytes
    .map(byte => byte.toString(16).toUpperCase().padStart(2, '0'))
    .join(' ')
}
</script>

<style scoped>
.realtime-data-list {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 400px;
}

.list-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 12px 16px;
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

.list-container {
  flex: 1;
  overflow: hidden;
  position: relative;
}

.scroller {
  height: 100%;
}

.data-item {
  padding: 12px 16px;
  border-bottom: 1px solid var(--el-border-color-lighter);
  background: var(--el-bg-color);
  transition: background-color 0.2s;
}

.data-item:hover {
  background: var(--el-fill-color-light);
}

.item-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 8px;
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
</style>
