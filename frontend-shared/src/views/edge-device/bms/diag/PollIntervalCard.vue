<!--
  轮询间隔分布卡（diag #12）—— 生产实现。

  ── 只渲染内容 ──────────────────────────────────────────────────────────────
  外壳 / 折叠头由父页渲染。内容区高度目标 ≤199px：
    汇总行 28 + 条形区 150 + 间距 6 ≈ 184px

  ── 它回答的问题 ────────────────────────────────────────────────────────────
  「轮询指令配置」列表里每条指令是一个毫秒数（1000 / 5000 / 30000…），
  用户要自己换算才知道"谁刷得快、总线压力多大"。本卡用同一条 0~60s 轴
  把各指令的间隔画成横条：一眼看出快慢，且汇总出**采样负载（帧/秒）** ——
  这是判断"是不是某条指令把总线刷爆了"的直接依据。

  ── 取数：父页优先（2026-10-20 契约 §2.6）────────────────────────────────────
  页面本身已经要取 GET /edge-devices/:id/commands 来渲染「轮询指令配置」卡；
  本卡再取一次就是根因报告 R7 实测的 commands ×2（同毫秒发出）。
  因此新增 commands prop：父页把**原始返回**下发给本卡，本卡只按 schedulable 过滤、不再发请求。
  commands 为 null 时保持旧行为（本卡自取数），保证单测 / 其它入口不变。
  parentLoading 是去重协作的小补充：父页首屏取数期间为 true，本卡不自取数、只显示加载中，
  避免「父页取一次 + 本卡再取一次」的重复（无此 prop 时默认 false ⇒ 行为与旧版一致）。

  ── 为什么不用 ECharts ──────────────────────────────────────────────────────
  数据只有 5~10 行、只需要长度编码。Canvas 图在这点信息量上反而：
    · 文字被缩放（手机上会小于 10px）
    · 空态/失败态要额外画一层
    · 多一个 dispose 生命周期
  CSS 条形天然自适应容器宽度、文字永远清晰（plan §4.6 同结论）。

  ── 离线语义 ────────────────────────────────────────────────────────────────
  轮询间隔是**后端保存的配置**（GET /edge-devices/:id/commands），
  设备离线不影响读取。因此本卡**不因 online=false 而拒绝取数**，
  避免"设备离线 → 连配置都看不到"的错误降级。
-->
<template>
  <div class="diag-body">
    <div class="diag-toolbar">
      <span class="diag-tip" v-if="enabledCount > 0">
        {{ enabledCount }} 条轮询中 · 合计 {{ loadPerSec.toFixed(1) }} 帧/秒
      </span>
      <span class="diag-tip is-quiet" v-else-if="loaded && commandRows.length">全部已禁用</span>
      <span class="diag-tip is-quiet" v-else></span>
    </div>

    <div class="diag-plot">
      <div v-if="loading && !loaded" class="state-line is-muted">正在加载轮询配置…</div>
      <div v-else-if="errorMsg" class="state-line is-error">
        <el-icon :size="14"><CircleCloseFilled /></el-icon>
        <span class="err-text">轮询配置加载失败：{{ errorMsg }}</span>
        <el-button link size="small" type="primary" @click="load">重试</el-button>
      </div>
      <div v-else-if="!commandRows.length" class="state-line is-muted">暂无轮询指令</div>

      <div v-else class="bar-list" role="list" aria-label="各轮询指令的间隔">
        <div v-for="c in commandRows" :key="c.id" class="bar-row" role="listitem">
          <span class="bar-label" :title="c.name">{{ c.name }}</span>
          <span class="bar-track" aria-hidden="true">
            <span
              class="bar-fill"
              :class="{ 'is-off': c.current_interval_ms <= 0 }"
              :style="{ width: barWidth(c.current_interval_ms) }"
            ></span>
          </span>
          <!-- 数值只在文字里出现一次，单位随数值走（"5 秒" / "已禁用"） -->
          <span class="bar-val" :class="{ 'is-off': c.current_interval_ms <= 0 }">
            {{ formatInterval(c.current_interval_ms) }}
          </span>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { CircleCloseFilled } from '@element-plus/icons-vue'
import { edgeDeviceApi, type CommandTemplateWithInterval } from '@/api/edgeDevice'

const props = withDefaults(
  defineProps<{
    deviceId: number | string
    online: boolean
    /**
     * 父页已取到的指令原始返回（契约 §2.6）。
     * null（默认）= 旧行为：本卡自取数；非 null 时不再请求，只按 schedulable 过滤后重建列表。
     */
    commands?: CommandTemplateWithInterval[] | null
    /**
     * 父页是否正在取 commands（仅 commands === null 时有意义）。
     * true 时本卡等待父页数据、不发请求（消除首屏 commands ×2）；父页取数失败（false 且仍为 null）
     * 时本卡回退自取数，保证失败态 + 重试按钮仍然可见。
     */
    parentLoading?: boolean
  }>(),
  { commands: null, parentLoading: false },
)

/** 折叠态摘要上抛给父页的折叠头（§3.2） */
const emit = defineEmits<{ (e: 'summary', text: string): void }>()

/** 间隔轴满刻度 = 后端上限 60000ms（与 el-input-number 的 max 一致） */
const FULL_SCALE_MS = 60000

/** 实际渲染的指令行（来自父页 props 或本卡请求；只保留可调度项） */
const commandRows = ref<CommandTemplateWithInterval[]>([])
const loading = ref(false)
const loaded = ref(false)
const errorMsg = ref('')

function schedulableOnly(all: CommandTemplateWithInterval[] | null | undefined): CommandTemplateWithInterval[] {
  return Array.isArray(all) ? all.filter((c) => c.schedulable) : []
}

/** 父页下发数据：只建列表，不发请求（父页是唯一取数入口） */
function applyFromParent(all: CommandTemplateWithInterval[]): void {
  commandRows.value = schedulableOnly(all)
  errorMsg.value = ''
  loading.value = false
  loaded.value = true
}

const enabledCount = computed(() => commandRows.value.filter((c) => c.current_interval_ms > 0).length)
/** 采样负载：每条启用指令的 1000/interval 之和（帧/秒）。用来判断总线压力 */
const loadPerSec = computed(() =>
  commandRows.value.reduce(
    (sum, c) => sum + (c.current_interval_ms > 0 ? 1000 / c.current_interval_ms : 0),
    0,
  ),
)

/** 折叠头摘要：「N 条轮询中」—— 一眼知道有几条在跑 */
const summaryText = computed(() => {
  if (errorMsg.value) return ''
  if (loading.value) return '加载中…'
  if (!loaded.value) return ''
  return commandRows.value.length === 0 ? '暂无轮询指令' : `${enabledCount.value} 条轮询中`
})
watch(summaryText, (t) => emit('summary', t), { immediate: true })

/** 条形长度：间隔 / 60s；最小 3% 让 1s 这类短间隔也可辨认（否则只有 1.7%） */
function barWidth(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '0%'
  return Math.max(3, Math.min((ms / FULL_SCALE_MS) * 100, 100)) + '%'
}

/** 毫秒 → 人类可读。单位只在这里出现一次 */
function formatInterval(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '已禁用'
  if (ms < 1000) return ms + ' ms'
  const s = ms / 1000
  return (Number.isInteger(s) ? s : s.toFixed(1)) + ' 秒'
}

async function load() {
  const deviceId = Number(props.deviceId)
  errorMsg.value = ''
  loading.value = true
  try {
    // 只取可调度的指令（与轮询配置卡口径一致：schedulable = 允许设置轮询间隔）
    const all = await edgeDeviceApi.getCommandIntervals(deviceId)
    // 请求期间父页数据到达 ⇒ 父页优先（避免本卡的旧响应覆盖父页新数据）
    const fromParent = props.commands
    if (fromParent !== null) {
      applyFromParent(fromParent)
      return
    }
    commandRows.value = schedulableOnly(all)
  } catch (e: unknown) {
    // 失败只影响本卡：渲染本卡失败态，不抛到父页
    errorMsg.value = e instanceof Error && e.message ? e.message : '请求失败'
    commandRows.value = []
  } finally {
    loading.value = false
    loaded.value = true
  }
}

// 父页数据到达 / 变化：重建列表（不再请求）。初始即为非 null 时（页面已取到）直接使用。
watch(
  () => props.commands,
  (all) => {
    if (all !== null) {
      applyFromParent(all)
      return
    }
    // 父页开始新一轮取数（如切换设备）：跟随进入加载态，等它下发数据
    if (props.parentLoading) {
      errorMsg.value = ''
      loading.value = true
    }
  },
  { immediate: true },
)

// 父页取数结束：仍为 null（父页失败）⇒ 回退到本卡自取数，让失败态与重试按钮可见
watch(
  () => props.parentLoading,
  (pending) => {
    if (pending) {
      errorMsg.value = ''
      loading.value = true
      return
    }
    if (props.commands === null) void load()
  },
)

onMounted(() => {
  // immediate watch 已处理两种「有人负责取数」的情形；只有无人负责时才自取（旧行为）
  if (props.commands === null && !props.parentLoading && !loading.value) void load()
})

// 设备切换：父页会重新取数并下发；父页不负责（旧用法）时本卡自己重取
watch(() => props.deviceId, () => {
  if (props.commands === null && !props.parentLoading) void load()
})
</script>

<style scoped>
.diag-body {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
}

.diag-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 28px;
  flex-wrap: wrap;
}
.diag-tip {
  font-size: 12px;
  color: var(--el-text-color-regular);
  font-variant-numeric: tabular-nums;
}
.diag-tip.is-quiet { color: var(--el-text-color-secondary); }

/* 固定 150px：loading/empty/error 同高，父页折叠容器不抖 */
.diag-plot {
  height: 150px;
  min-width: 0;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.state-line {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  flex: 1;
  font-size: 12px;
  line-height: 1.5;
  color: var(--el-text-color-regular);
  text-align: center;
}
.state-line.is-muted { color: var(--el-text-color-secondary); }
.state-line.is-error { color: var(--el-color-danger); align-items: flex-start; padding-top: 4px; }
.err-text { flex: 1; min-width: 0; overflow-wrap: anywhere; text-align: left; }

.bar-list {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
}
.bar-row {
  display: flex;
  align-items: center;
  gap: 6px;
  min-height: 22px;
  min-width: 0;
}
.bar-label {
  flex: 0 0 84px;
  font-size: 11px;
  color: var(--el-text-color-regular);
  word-break: keep-all;
  line-height: 1.25;
}
.bar-track {
  flex: 1 1 auto;
  min-width: 24px;
  height: 8px;
  border-radius: 4px;
  background: var(--el-fill-color);
  overflow: hidden;
}
.bar-fill {
  display: block;
  height: 100%;
  border-radius: 4px;
  /* 轮询中 = primary（运行态，不是告警） */
  background: var(--el-color-primary);
}
.bar-fill.is-off { background: transparent; }
.bar-val {
  flex: 0 0 auto;
  min-width: 52px;
  text-align: right;
  font-size: 11px;
  color: var(--el-text-color-regular);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.bar-val.is-off { color: var(--el-text-color-placeholder); }
</style>
