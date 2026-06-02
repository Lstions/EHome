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
          共 {{ dataItems.length }} 条数据
        </el-tag>
        <el-button
          size="small"
          :icon="Delete"
          @click="handleClear"
          :disabled="dataItems.length === 0"
        >
          清空
        </el-button>
      </div>
    </div>

    <div class="list-container" ref="listContainer">
      <RecycleScroller
        v-if="dataItems.length > 0"
        class="scroller"
        :items="dataItems"
        :item-size="80"
        key-field="id"
        :buffer="200"
      >
        <template #default="{ item }">
          <div class="data-item">
            <div class="item-header">
              <span class="timestamp">{{ formatTime(item.timestamp) }}</span>
              <el-tag v-if="item.data?.error_code && item.data.error_code > 0" :type="getErrorInfo(item.data.error_code).type" size="small">
                {{ getErrorInfo(item.data.error_code).label }}
              </el-tag>
              <el-tag size="small" :type="item.isRealtime ? 'success' : 'info'">
                {{ item.isRealtime ? '实时' : '历史' }}
              </el-tag>
            </div>
            <div class="item-content" :class="{ 'hex-mode': displayMode === 'hex' }">
              {{ formatItemData(item) }}
            </div>
          </div>
        </template>
      </RecycleScroller>

      <el-empty v-else description="暂无实时数据，等待设备上报..." />
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, watch, nextTick, onMounted, onUnmounted } from 'vue'
import { RecycleScroller } from 'vue-virtual-scroller'
import 'vue-virtual-scroller/dist/vue-virtual-scroller.css'
import { Delete } from '@element-plus/icons-vue'
import { formatTime, formatDataPlainText, dataToHexString } from '@/utils/format'
import { getErrorInfo } from '@/utils/errorCode'

export interface DataItem {
  id: string
  timestamp: string
  data: any
  rawData?: number[]
  isRealtime: boolean
}

interface Props {
  initialData?: DataItem[]
  maxItems?: number
  autoScroll?: boolean
  deviceType?: string  // 设备类型，用于智能格式化（如 'bmp280'）
}

const props = withDefaults(defineProps<Props>(), {
  initialData: () => [],
  maxItems: 500,
  autoScroll: true,
  deviceType: ''
})

const emit = defineEmits<{
  (e: 'clear'): void
}>()

const displayMode = ref<'text' | 'hex'>('text')
const dataItems = ref<DataItem[]>([...props.initialData])
const listContainer = ref<HTMLElement | null>(null)

// 添加新数据
const addData = (item: DataItem) => {
  dataItems.value.unshift(item)

  // 限制最大条数
  if (dataItems.value.length > props.maxItems) {
    dataItems.value = dataItems.value.slice(0, props.maxItems)
  }

  // 自动滚动到顶部
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
}

// 批量添加数据
const addDataBatch = (items: DataItem[]) => {
  dataItems.value = [...items, ...dataItems.value].slice(0, props.maxItems)
}

// 清空数据
const handleClear = () => {
  dataItems.value = []
  emit('clear')
}

// 格式化数据项
const formatItemData = (item: DataItem): string => {
  if (displayMode.value === 'hex') {
    // 优先使用原始字节数据
    if (item.rawData && item.rawData.length > 0) {
      return bytesToHexLocal(item.rawData)
    }
    return dataToHexString(item.data)
  }
  return formatDataPlainText(item.data, props.deviceType)
}

// 本地16进制格式化（避免导入问题）
const bytesToHexLocal = (bytes: number[]): string => {
  return bytes
    .map(byte => byte.toString(16).toUpperCase().padStart(2, '0'))
    .join(' ')
}

// 格式化时间戳
const formatTimeLocal = (timestamp: string): string => {
  return formatTime(timestamp)
}

// 暴露方法供父组件调用
defineExpose({
  addData,
  addDataBatch,
  clear: handleClear,
  getDataItems: () => dataItems.value
})
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
