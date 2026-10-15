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
    <!-- 2026-10-15 报障修复：「一直输出数据，滚动页面功能不能正常使用」。
         根因见 script 里的 watch：每条新数据都强制 scrollTop=0，高频上报时用户
         刚往下滚就被拽回顶部，历史帧根本读不了。
         现在引入"跟随模式"：只有**用户本来就在顶部**时才自动跟随；
         用户向上滚即暂停跟随，并给出「回到最新」入口。 -->
    <div
      v-if="items.length > 0"
      ref="listContainer"
      class="plain-list"
      @scroll.passive="onListScroll"
    >
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

    <!-- 暂停跟随时常驻的返回入口：用户随时能回到最新，并明确知道"已暂停自动跟随" -->
    <div v-if="items.length > 0 && !following" class="follow-paused" role="status">
      <el-button size="small" type="primary" plain @click="scrollToLatest">回到最新</el-button>
      <span class="follow-paused-text">已暂停自动跟随（向上滚动即暂停）</span>
    </div>

    <EmptyState
      v-else-if="items.length === 0"
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

/**
 * 跟随模式（2026-10-15 报障修复）。
 *
 * 缺陷：改前每条新数据都无条件 `scrollTop = 0`。列表是"最新在前"，所以
 * "跟随最新"恰好等于"停在顶部"——但当用户**主动往下滚**去读历史帧时，
 * 下一秒的新数据就把他拽回顶部。实测 BMS 约 1 帧/秒、列表上限 200 条，
 * 结果就是用户说的"一直输出数据，滚动页面功能不能正常使用"。
 *
 * 判据：只有用户**本来就在顶部**（说明他在跟随）时才自动回顶；
 * 一旦他往上滚开，就暂停自动跟随，并显示「回到最新」让他自己决定何时回去。
 * 这样既保留"实时跟随"的默认体验，又不与用户的手势抢滚动位置。
 */
const TOP_THRESHOLD_PX = 8
const following = ref(true)

/** 用户滚动：位置在顶部即视为"跟随中"，离开顶部即暂停跟随。 */
function onListScroll() {
  if (!props.autoScroll) return
  const el = listContainer.value
  if (!el) return
  following.value = el.scrollTop <= TOP_THRESHOLD_PX
}

/** 回到最新并恢复跟随。 */
function scrollToLatest() {
  following.value = true
  nextTick(() => {
    if (listContainer.value) listContainer.value.scrollTop = 0
  })
}

// 新数据到达时滚回顶部（最新在前）——**仅在跟随态**。
// 监听首条 id 而非 items.length：上游按 maxItems 截断后满额增删同进，
// length 恒定不变，watch length 会永不触发。
watch(() => props.items[0]?.id, () => {
  if (!props.autoScroll || !following.value) return
  nextTick(() => {
    if (listContainer.value) {
      listContainer.value.scrollTop = 0
    }
  })
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
  /* 让滚动容器的滚动条与侧栏同款细样式（全局是亮色滚动条，本表背面是白底，保持原样即可） */
  scrollbar-width: thin;
}
/* 暂停跟随时提示条：紧贴列表下方，常驻直到用户回到最新 */
.follow-paused {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-top: 8px;
  padding: 6px 0;
}
.follow-paused-text { font-size: 12px; color: var(--el-text-color-secondary); }

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
