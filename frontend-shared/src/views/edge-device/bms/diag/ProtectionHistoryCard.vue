<!--
  保护触发历史卡（diag #4）—— 生产实现。

  ── 只渲染内容 ──────────────────────────────────────────────────────────────
  父页用统一折叠容器包裹：外壳 / 折叠头（标题 + 摘要 + caret）都由父页渲染。
  本组件**不得**再画 .card 外壳或 .card-title（否则两级同名标题）。

  ── 计数口径（本卡最重要的正确性约束）──────────────────────────────────────
  数据源 historical-batch?categories=protection_status 返回的是**位掩码状态序列**，
  不是累计计数。实测近 30 天 20520 点里非零 1239 个，但**唯一值只有 [0, 1]**
  ⇒ 非零恒为 1，说明每一位是"当前是否触发"的状态位。
  因此：
    ✅ 触发次数 = 该位 0→1 跃变次数
    ❌ 不能把 value 求和（0,0,1,1,0,1 求和=3，真实触发=2）
  纯函数实现见 ./bitTransitions.ts，单测直接锁这条口径。

  ── 空态语义要分清三件事（否则会谎报"一切正常"）─────────────────────────
    1. 请求失败          → 「保护历史加载失败」+ 重试
    2. 没有 protection_status 数据 → 「暂无保护状态历史数据」（**不能**说"无触发"）
    3. 有数据但 12 位全 0 → 「近 N 天无保护触发记录 ✓」（绿色，这是好事）
  把 2 说成 3 是最危险的谎报：设备根本没报这个字段，却说它"没触发过保护"。

  ── 布局 ────────────────────────────────────────────────────────────────────
  内容区高度目标 ≤197px（不含父页折叠头）：
    工具行 28 + 条形区 120 + 脚注 16 + 间距 12 ≈ 176px
  条形区**固定 120px**：loading / empty / error 都不改变高度，
  父页折叠容器因此不会因取数状态抖动。
-->
<template>
  <div class="diag-body">
    <!-- 工具行：范围选择右对齐（父页折叠头只放标题+摘要，控件归内容区） -->
    <div class="diag-toolbar">
      <span class="diag-total" v-if="totalCount > 0">{{ totalCount }} 次触发</span>
      <span class="diag-total is-quiet" v-else-if="hasData">近 {{ rangeDays }} 天无触发</span>
      <el-select
        v-model="rangeDays"
        size="small"
        class="range-select"
        aria-label="保护历史时间范围"
        @change="load"
      >
        <el-option :value="7" label="近 7 天" />
        <el-option :value="30" label="近 30 天" />
        <el-option :value="90" label="近 90 天" />
      </el-select>
    </div>

    <!-- 固定高度内容区：三种状态都占满，父页高度稳定 -->
    <div class="diag-plot">
      <!-- ① 离线：不请求，也不谎报"无触发" -->
      <div v-if="!online" class="state-line">
        <el-icon :size="14"><WarningFilled /></el-icon>
        <span>设备离线，保护历史暂不可用</span>
      </div>

      <!-- ② 加载中 -->
      <div v-else-if="loading && !loaded" class="state-line is-muted">
        <span>正在加载保护历史…</span>
      </div>

      <!-- ③ 请求失败：只影响本卡 -->
      <div v-else-if="errorMsg" class="state-line is-error" role="alert">
        <el-icon :size="14"><CircleCloseFilled /></el-icon>
        <span class="err-text">保护历史加载失败：{{ errorMsg }}</span>
        <el-button link size="small" type="primary" @click="load">重试</el-button>
      </div>

      <!-- ④ 没有 protection_status 数据：这是「没这个字段」，**不是**「没触发过」 -->
      <div v-else-if="!hasData" class="state-line is-muted">
        <span>暂无保护状态历史数据</span>
      </div>

      <!-- ⑤ 有数据但 12 位全 0：这是好事，用绿色结论（不是"暂无数据"） -->
      <div v-else-if="!hitBits.length" class="state-line is-ok">
        <el-icon :size="15"><CircleCheckFilled /></el-icon>
        <span>近 {{ rangeDays }} 天无保护触发记录</span>
      </div>

      <!-- ⑤ 有触发项：只画触发过的位，其余收敛到脚注 -->
      <div v-else class="bar-list" role="list" :aria-label="`近${rangeDays}天保护触发次数`">
        <div v-for="b in hitBits" :key="b.bit" class="bar-row" role="listitem">
          <span class="bar-label" :title="`bit${b.bit} ${b.label}`">{{ b.label }}</span>
          <span class="bar-track" aria-hidden="true">
            <span class="bar-fill" :style="{ width: barWidth(b.count) }"></span>
          </span>
          <!-- 精确次数文本常驻：条形只给相对量，数字给准确值 -->
          <span class="bar-count">{{ b.count }} 次</span>
        </div>
      </div>
    </div>

    <!-- 脚注：只在与主结论**不矛盾**时出现。
         曾经的 bug：加载中/无数据时脚注仍走最后的 else，写出「12 项保护均未触发」——
         一边「正在加载」一边「均未触发」= 谎报结论（2026-10-20 视觉评审实测）。 -->
    <p v-if="hasData && !loading && !errorMsg && hitBits.length" class="diag-foot">
      其余 {{ zeroCount }} 项近 {{ rangeDays }} 天未触发
    </p>
    <p v-else-if="hasData && !loading && !errorMsg && !hitBits.length && rangeDays === 7" class="diag-foot">
      近 7 天无记录，可切到 30 / 90 天查看更多
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { CircleCheckFilled, CircleCloseFilled, WarningFilled } from '@element-plus/icons-vue'
import { decodeProtectionBits, type BitTransitionStat } from './bitTransitions'
import { pointsOf, useDiagHistory } from './useDiagHistory'

const props = defineProps<{
  deviceId: number | string
  online: boolean
}>()

/** 折叠态摘要上抛给父页的折叠头（§3.2：折叠不是隐藏信息，标题行要有一行关键结论） */
const emit = defineEmits<{ (e: 'summary', text: string): void }>()

/** 12 个保护位的中文名（jiabaida protection_status 位序，与生产页口径一致） */
const PROTECTION_BIT_LABELS = [
  '单体过压', '单体欠压', '整组过压', '整组欠压',
  '充电过温', '充电低温', '放电过温', '放电低温',
  '充电过流', '放电过流', '短路保护', '前端 IC 错误',
] as const

/** 默认 30 天（用户拍板）：7 天实测全 0，默认 7 天会让人以为功能坏了 */
const rangeDays = ref(30)
const bits = ref<BitTransitionStat[]>([])
const hasData = ref(false)
const errorMsg = ref('')

const { loading, loaded, fetchRange } = useDiagHistory({
  nameMap: { protection_status: '保护状态' },
})

/** 有触发的位，按次数降序（最该被先看到的排最上） */
const hitBits = computed(() => bits.value.filter((b) => b.count > 0).sort((a, b) => b.count - a.count))
const totalCount = computed(() => bits.value.reduce((n, b) => n + b.count, 0))
const zeroCount = computed(() => bits.value.filter((b) => b.count === 0).length)

/** 折叠头摘要：区分「没数据」「有数据但全 0」「有触发」，不能把前者说成后者 */
const summaryText = computed(() => {
  if (errorMsg.value) return ''
  // 加载中 vs 真无数据必须分开：先写「加载中…」，否则等于对用户谎报「没有数据」
  if (loading.value) return '加载中…'
  if (!hasData.value) return '暂无保护状态数据'
  return totalCount.value > 0
    ? `近 ${rangeDays.value} 天 ${totalCount.value} 次`
    : `近 ${rangeDays.value} 天无触发`
})
watch(summaryText, (t) => emit('summary', t), { immediate: true })

/** 条形相对量：以最大次数为满刻度；最小 6% 保证"1 次"也是一条可见的条 */
function barWidth(count: number): string {
  const max = Math.max(...hitBits.value.map((b) => b.count), 1)
  return Math.max(6, (count / max) * 100) + '%'
}

async function load() {
  errorMsg.value = ''
  if (!props.online) {
    bits.value = []
    hasData.value = false
    return
  }
  const now = new Date()
  const start = new Date(now.getTime() - rangeDays.value * 86400000)
  const { series, error } = await fetchRange(props.deviceId, ['protection_status'], start, now)
  if (error) {
    errorMsg.value = error
    bits.value = []
    hasData.value = false
    return
  }
  const points = pointsOf(series, 'protection_status')
  // hasData 区分"没有该字段"与"字段有值但全 0" —— 两者语义完全不同
  hasData.value = points.length > 0
  bits.value = points.length ? decodeProtectionBits(points, PROTECTION_BIT_LABELS) : []
}

onMounted(load)
// 设备切换 / 上下线时重取（离线时不清空已加载数据会串台到另一台设备）
watch(() => [props.deviceId, props.online], load)
</script>

<style scoped>
/* 内容区容器：不要卡片外壳、不要内距（父页卡片已给） */
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
}
.diag-total {
  font-size: 12px;
  color: var(--el-text-color-regular);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.diag-total.is-quiet { color: var(--el-text-color-secondary); }
/* 右对齐：折叠头不放控件，范围选择归内容区右上角 */
.range-select {
  margin-left: auto;
  width: 104px;
  flex: 0 0 auto;
}

/* 固定高度绘图区：120px —— loading/empty/error 都不改变父页折叠容器高度 */
.diag-plot {
  height: 120px;
  min-width: 0;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

/* 状态行：垂直居中，占满固定高度 */
.state-line {
  display: flex;
  align-items: center;
  gap: 6px;
  flex: 1;
  min-width: 0;
  font-size: 12px;
  line-height: 1.5;
  color: var(--el-text-color-regular);
}
.state-line.is-muted { color: var(--el-text-color-secondary); }
.state-line.is-ok { color: var(--el-color-success); font-weight: 500; }
.state-line.is-error { color: var(--el-color-danger); align-items: flex-start; padding-top: 4px; }
.err-text {
  flex: 1;
  min-width: 0;
  /* 失败原因允许换行（禁止 ellipsis 截断关键信息） */
  overflow-wrap: anywhere;
}

/* 条形列表：固定高度内可滚，触发项很多时也不撑高父页 */
.bar-list {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  /* 行数少时不拉伸，行距稳定 */
  justify-content: flex-start;
}
.bar-row {
  display: flex;
  align-items: center;
  gap: 6px;
  min-height: 20px;
  min-width: 0;
}
.bar-label {
  flex: 0 0 62px;
  font-size: 11px;
  color: var(--el-text-color-regular);
  /* 中文不逐字竖排；62px 够放「前端 IC 错误」以外的所有名称（该名称允许换行不截断） */
  word-break: keep-all;
  line-height: 1.25;
}
.bar-track {
  flex: 1 1 auto;
  min-width: 24px;
  height: 7px;
  border-radius: 4px;
  background: var(--el-fill-color);
  overflow: hidden;
}
.bar-fill {
  display: block;
  height: 100%;
  border-radius: 4px;
  /* 触发用 warning（需要关注），不用 danger（不是正在告警） */
  background: var(--el-color-warning);
}
.bar-count {
  flex: 0 0 auto;
  min-width: 34px;
  text-align: right;
  font-size: 11px;
  color: var(--el-text-color-regular);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

.diag-foot {
  margin: 0;
  font-size: 11px;
  line-height: 1.4;
  color: var(--el-text-color-secondary);
  /* 文案允许换行，不省略 */
  overflow-wrap: anywhere;
}
</style>
