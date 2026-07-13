<template>
  <section class="log-realtime-viewer">
    <header class="log-toolbar">
      <span class="log-summary">实时日志 {{ filteredLogs.length }} / {{ logs.length }}</span>
      <button type="button" class="pause-button" @click="emit('update:paused', !paused)">
        {{ paused ? '继续' : '暂停' }}
      </button>
      <button type="button" class="clear-button" :disabled="logs.length === 0" @click="emit('clear')">
        清屏
      </button>
      <button type="button" class="export-text-button" :disabled="filteredLogs.length === 0" @click="emit('export', 'text')">
        导出文本
      </button>
      <button type="button" class="export-csv-button" :disabled="filteredLogs.length === 0" @click="emit('export', 'csv')">
        导出 CSV
      </button>
    </header>

    <button
      v-if="unreadCount > 0"
      type="button"
      class="unread-button"
      aria-live="polite"
      @click="returnToBottom"
    >
      {{ unreadCount }} 条新日志 · 回到底部
    </button>

    <div class="terminal-shell" role="region" aria-label="实时日志终端">
      <RecycleScroller
        v-if="filteredLogs.length > 0"
        ref="scrollerRef"
        class="log-scroller"
        role="log"
        aria-label="实时日志内容"
        :items="filteredLogs"
        :item-size="ROW_HEIGHT"
        key-field="id"
        :buffer="ROW_HEIGHT * 4"
        @scroll="handleScroll"
      >
        <template #default="{ item }">
          <div class="log-line" :class="levelClass(item.level)" :data-log-id="item.id">
            <span class="log-time">{{ formatUptime(item.ts) }}</span>
            <span class="log-level">{{ levelText(item.level) }}</span>
            <span class="log-tag">{{ item.tag }}</span>
            <span class="log-message">{{ item.msg }}</span>
          </div>
        </template>
      </RecycleScroller>
      <div v-else class="log-empty">{{ searchKeyword ? '无搜索命中' : '等待日志...' }}</div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { RecycleScroller } from 'vue-virtual-scroller'
import type { RecycleScrollerExposed } from 'vue-virtual-scroller'
import 'vue-virtual-scroller/dist/vue-virtual-scroller.css'

interface RealtimeLogLine {
  id: number
  ts: number
  level: number
  tag: string
  msg: string
}

interface RealtimeSearchCountState {
  epoch: number
  baselineId: number
  baselineMatchIds: number[]
  matchedAfterBaseline: number
}

const props = withDefaults(defineProps<{
  logs: RealtimeLogLine[]
  receivedCount: number
  generation: number
  paused: boolean
  searchKeyword: string
  searchCountState: RealtimeSearchCountState
}>(), {
  logs: () => [],
  receivedCount: 0,
  generation: 0,
  paused: false,
  searchKeyword: '',
  searchCountState: () => ({
    epoch: 0,
    baselineId: 0,
    baselineMatchIds: [],
    matchedAfterBaseline: 0,
  }),
})

const emit = defineEmits<{
  (event: 'update:paused', value: boolean): void
  (event: 'clear'): void
  (event: 'export', format: 'text' | 'csv'): void
}>()

const ROW_HEIGHT = 28
const BOTTOM_THRESHOLD = ROW_HEIGHT * 2
const scrollerRef = ref<RecycleScrollerExposed<RealtimeLogLine> | null>(null)
const followingBottom = ref(true)
const lastReadId = ref(props.receivedCount)
const searchReadState = ref({
  epoch: props.searchCountState.epoch,
  baselineId: props.searchCountState.baselineId,
  baselineReadCount: props.searchCountState.baselineMatchIds.length,
  matchedAfterBaseline: props.searchCountState.matchedAfterBaseline,
})

const filteredLogs = computed(() => {
  const keyword = props.searchKeyword.trim().toLocaleLowerCase()
  if (!keyword) return props.logs
  return props.logs.filter(line =>
    String(line.tag ?? '').toLocaleLowerCase().includes(keyword)
      || String(line.msg ?? '').toLocaleLowerCase().includes(keyword)
      || levelText(line.level).toLocaleLowerCase().includes(keyword),
  )
})

const unreadCount = computed(() => {
  if (!props.searchKeyword.trim()) {
    return Math.max(0, props.receivedCount - lastReadId.value)
  }

  const metadata = props.searchCountState
  if (searchReadState.value.epoch !== metadata.epoch) return 0

  const unreadBaseline = Math.max(
    0,
    metadata.baselineMatchIds.length - searchReadState.value.baselineReadCount,
  )
  const unreadAfterBaseline = Math.max(
    0,
    metadata.matchedAfterBaseline - searchReadState.value.matchedAfterBaseline,
  )
  return unreadBaseline + unreadAfterBaseline
})

function levelText(level: number): string {
  return ['ERROR', 'WARN', 'INFO', 'DEBUG', 'VERBOSE'][level] ?? 'UNKNOWN'
}

function levelClass(level: number): string {
  return ['log-error', 'log-warn', 'log-info', 'log-debug', 'log-verbose'][level] ?? 'log-info'
}

function formatUptime(tsUs: number): string {
  const totalMs = Math.max(0, Math.floor(Number(tsUs || 0) / 1000))
  const hours = Math.floor(totalMs / 3_600_000)
  const minutes = Math.floor((totalMs % 3_600_000) / 60_000)
  const seconds = Math.floor((totalMs % 60_000) / 1000)
  const millis = totalMs % 1000
  return `${String(hours).padStart(2, '0')}:${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}.${String(millis).padStart(3, '0')}`
}

function scrollToBottom(): void {
  const scroller = scrollerRef.value
  if (!scroller) return

  // Template refs expose nested refs through Vue's public-instance proxy, so `el`
  // is an HTMLElement at runtime even though the package type declares a Ref.
  const exposedElement = scroller.el as unknown as HTMLElement | { value?: HTMLElement }
  const element = exposedElement instanceof HTMLElement ? exposedElement : exposedElement.value
  if (!element) return

  const bottomPosition = Math.max(0, filteredLogs.value.length * ROW_HEIGHT - element.clientHeight)
  scroller.scrollToPosition(bottomPosition)
}

function acknowledgeCurrent(): void {
  lastReadId.value = props.receivedCount
  searchReadState.value = {
    epoch: props.searchCountState.epoch,
    baselineId: props.searchCountState.baselineId,
    baselineReadCount: props.searchCountState.baselineMatchIds.length,
    matchedAfterBaseline: props.searchCountState.matchedAfterBaseline,
  }
}

function handleScroll(event: Event): void {
  const target = event.currentTarget as HTMLElement | null
  if (!target) return

  const contentHeight = filteredLogs.value.length * ROW_HEIGHT
  followingBottom.value = contentHeight - target.scrollTop - target.clientHeight <= BOTTOM_THRESHOLD
  if (followingBottom.value && !props.paused) acknowledgeCurrent()
}

async function returnToBottom(): Promise<void> {
  followingBottom.value = true
  acknowledgeCurrent()
  await nextTick()
  scrollToBottom()
}

onMounted(async () => {
  await nextTick()
  scrollToBottom()
})

watch(
  () => [props.generation, props.receivedCount] as const,
  ([generation, receivedCount], [previousGeneration, previousReceivedCount]) => {
    const generationChanged = generation !== previousGeneration
    if (generationChanged) {
      lastReadId.value = previousReceivedCount
      searchReadState.value = {
        epoch: props.searchCountState.epoch,
        baselineId: props.searchCountState.baselineId,
        baselineReadCount: props.searchCountState.baselineMatchIds.reduce(
          (count, id) => count + (id <= previousReceivedCount ? 1 : 0),
          0,
        ),
        matchedAfterBaseline: props.searchCountState.baselineId < previousReceivedCount
          ? props.searchCountState.matchedAfterBaseline
          : 0,
      }
    }
    if (!generationChanged && receivedCount === previousReceivedCount) return

    if (props.paused || !followingBottom.value) return

    acknowledgeCurrent()
    scrollToBottom()
  },
  { flush: 'post' },
)

watch(
  () => props.paused,
  (paused, wasPaused) => {
    if (!paused && wasPaused && followingBottom.value) {
      acknowledgeCurrent()
      scrollToBottom()
    }
  },
  { flush: 'post' },
)

watch(
  () => props.searchCountState.epoch,
  (epoch, previousEpoch) => {
    if (epoch === previousEpoch) return

    searchReadState.value = {
      epoch,
      baselineId: props.searchCountState.baselineId,
      baselineReadCount: props.searchCountState.baselineMatchIds.reduce(
        (count, id) => count + (id <= lastReadId.value ? 1 : 0),
        0,
      ),
      matchedAfterBaseline: 0,
    }
  },
  { flush: 'sync' },
)

watch(
  () => props.searchKeyword,
  async () => {
    if (!props.paused && followingBottom.value) {
      acknowledgeCurrent()
      await nextTick()
      scrollToBottom()
    }
  },
  { flush: 'post' },
)
</script>

<style scoped>
.log-realtime-viewer {
  min-width: 0;
}

.log-toolbar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 8px;
}

.log-summary {
  margin-right: auto;
  color: var(--el-text-color-regular);
  font-size: 13px;
}

.terminal-shell {
  height: clamp(420px, 58vh, 720px);
  min-width: 0;
  overflow: hidden;
  border-radius: var(--radius-md);
  background: var(--terminal-bg);
  color: var(--terminal-text);
  font-family: 'JetBrains Mono', 'Cascadia Code', 'Courier New', Consolas, monospace;
  font-size: 12px;
}

.log-scroller {
  width: 100%;
  min-width: 100%;
  height: 100%;
  overflow: auto;
}

.log-scroller :deep(.vue-recycle-scroller__item-wrapper) {
  min-width: 100%;
  overflow: visible;
}

.log-scroller :deep(.vue-recycle-scroller__item-view) {
  width: max-content;
  min-width: 100%;
}

.log-line {
  box-sizing: border-box;
  display: grid;
  grid-template-columns: 96px 64px 128px max-content;
  gap: 8px;
  align-items: center;
  width: max-content;
  min-width: 100%;
  height: 28px;
  padding: 0 12px;
  white-space: nowrap;
}

.log-time { color: var(--terminal-muted); }
.log-level { font-weight: 600; }
.log-tag { color: var(--terminal-accent); }
.log-message { color: var(--terminal-text); }
.log-error .log-level, .log-error .log-message { color: var(--terminal-danger); }
.log-warn .log-level, .log-warn .log-message { color: var(--terminal-warning); }
.log-info .log-level { color: var(--terminal-success); }
.log-debug .log-level, .log-debug .log-message { color: var(--terminal-muted); }
.log-verbose .log-level, .log-verbose .log-message { color: var(--terminal-subtle); }

.log-empty {
  display: grid;
  place-items: center;
  height: 100%;
  color: var(--terminal-subtle);
}

@media (max-width: 768px) {
  .terminal-shell {
    height: clamp(320px, 58vh, 560px);
  }
}
</style>
