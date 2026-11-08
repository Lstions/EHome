<template>
  <div class="maintenance-page" data-testid="maintenance-page">
    <div class="maintenance-brand">
      <img src="/favicon.svg" alt="EHomeSystem" class="maintenance-brand__logo" />
      <span class="maintenance-brand__name">EHomeSystem</span>
    </div>

    <el-card class="maintenance-card" shadow="never">
      <div class="maintenance-head">
        <el-icon class="maintenance-head__icon" :class="{ 'is-spin': spinning }">
          <Loading />
        </el-icon>
        <h1 class="maintenance-title">{{ title }}</h1>
        <p class="maintenance-subtitle">{{ subtitle }}</p>
      </div>

      <!-- 连不上后端（进程未监听 / 正在重启）：必须显式告知，并且**不停轮询**，
           后端恢复后由门禁自动进入正常页面。 -->
      <el-alert
        v-if="connectionFailed"
        class="maintenance-alert"
        type="warning"
        :closable="false"
        show-icon
        title="无法连接服务器，正在重试…"
      >
        后端可能正在启动或重启，本页会自动重试，无需手动刷新。
      </el-alert>

      <!-- 启动失败（Fatal 阶段退出）：给出原因 + 手工重载入口。 -->
      <el-alert
        v-if="failed"
        class="maintenance-alert"
        type="error"
        :closable="false"
        show-icon
        title="系统启动失败"
      >
        {{ failedSummary }}
      </el-alert>

      <div class="maintenance-overall">
        <div class="maintenance-overall__row">
          <span class="maintenance-overall__label">{{ overallLabel }}</span>
          <span class="maintenance-overall__percent" data-testid="maintenance-percent">{{ percentText }}</span>
        </div>
        <el-progress
          class="maintenance-overall__bar"
          :percentage="clampPercent(snapshot ? snapshot.percent : 0)"
          :status="progressStatus"
          :stroke-width="10"
          :show-text="false"
        />
        <div class="maintenance-meta">
          <span class="maintenance-meta__item" data-testid="maintenance-elapsed">
            已耗时 <strong>{{ elapsedText }}</strong>
          </span>
          <span v-if="versionText" class="maintenance-meta__item" data-testid="maintenance-version">
            {{ versionText }}
          </span>
        </div>
      </div>

      <ul v-if="steps.length" class="maintenance-steps" data-testid="maintenance-steps">
        <li
          v-for="step in steps"
          :key="step.id"
          class="maintenance-step"
          :class="'is-' + step.state"
          :data-state="step.state"
          :data-step-id="step.id"
        >
          <span class="maintenance-step__icon">
            <el-icon v-if="step.state === 'done'"><CircleCheck /></el-icon>
            <el-icon v-else-if="step.state === 'failed'"><CircleClose /></el-icon>
            <el-icon v-else-if="step.state === 'skipped'"><Minus /></el-icon>
            <el-icon v-else-if="step.state === 'running'" class="is-spin"><Loading /></el-icon>
            <el-icon v-else><Clock /></el-icon>
          </span>
          <div class="maintenance-step__main">
            <div class="maintenance-step__head">
              <span class="maintenance-step__label">{{ stepDisplayLabel(step) }}</span>
              <span class="maintenance-step__state">{{ stepStateText(step.state) }}</span>
            </div>
            <div v-if="step.detail" class="maintenance-step__detail">{{ step.detail }}</div>
            <div v-if="step.error" class="maintenance-step__error">{{ step.error }}</div>
            <el-progress
              v-if="step.state === 'running' && step.progress > 0"
              class="maintenance-step__bar"
              :percentage="clampPercent(step.progress)"
              :stroke-width="4"
              :show-text="false"
            />
          </div>
        </li>
      </ul>

      <div class="maintenance-actions">
        <el-button :icon="Refresh" :type="failed ? 'primary' : 'default'" @click="reload">
          {{ actionText }}
        </el-button>
        <span class="maintenance-actions__hint">{{ actionHint }}</span>
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { CircleCheck, CircleClose, Clock, Loading, Minus, Refresh } from '@element-plus/icons-vue'
import type { MaintenanceSnapshot } from '@/types/system'
import { clampPercent, currentStageText, formatElapsed, stepDisplayLabel, stepStateText } from './progress'

/**
 * 启动期维护页（契约 §4）。
 *
 * 纯展示组件：数据由 App.vue 的门禁（useStartupGate）通过 props 注入，
 * 自身只负责 ① 渲染总体进度 + 阶段列表；② 本地推进「已耗时」；③ 给出重试入口。
 *
 * 为什么「已耗时」要前端自己走秒：后端只在被请求时算 elapsed_ms，轮询间隔 2~5s，
 * 直接展示会**一跳一跳**且跨度不齐。这里以服务端值为基准、每秒本地 +1s
 * （只增不减，防时钟回拨导致数字倒退）。
 *
 * 为什么样式全部走 token：维护页要在**未登录、深色模式、窄屏手机**三种情况下都可读。
 * token 在 theme.css 的亮/暗两块都有定义，不写死颜色就不会在暗色下塌对比度。
 */
const props = withDefaults(defineProps<{
  /** 最近一次成功拉到的快照；尚未拉到时为 null（渲染'连接中'态）。 */
  snapshot?: MaintenanceSnapshot | null
  /** 最近一次探测是否因「连不上服务器」失败。 */
  connectionFailed?: boolean
}>(), {
  snapshot: null,
  connectionFailed: false,
})

/** 启动失败（失败态优先于连接态：快照说明后端明确报了 Fatal）。 */
const failed = computed(() => Boolean(props.snapshot?.failed))

/** 还在跑（无快照或未就绪未失败）→ 图标转圈。 */
const spinning = computed(() => !props.snapshot || (!props.snapshot.ready && !props.snapshot.failed))

const title = computed(() => {
  if (failed.value) return '系统启动失败'
  if (props.connectionFailed) return '正在连接服务器'
  return '系统升级中'
})

const subtitle = computed(() => {
  if (failed.value) return '数据库优化中断，系统未能完成启动。'
  if (props.connectionFailed) return '正在等待后端服务就绪，请勿关闭页面…'
  return '正在优化数据库，请勿关闭页面…'
})

/**
 * 总体进度行的文案。
 *
 * 三种情形都要有**确定的**文案，不能出现悬空标签：
 * · 失败：明说进度已中断（percent 停在任意值，不能让人以为是还在跑）；
 * · 有当前阶段：显示阶段名；
 * · 无当前阶段（契约允许 failed/边界态下 current_label 为空）：若快照存在，
 *   用 currentStageText 的「已停止」兜底；没有快照（从未连上）才退回「总体进度」。
 */
const overallLabel = computed(() => {
  if (failed.value) return '中断时的总体进度'
  if (props.snapshot) return currentStageText(props.snapshot.current_label)
  return '总体进度'
})

const percentText = computed(() => String(clampPercent(props.snapshot ? props.snapshot.percent : 0)) + '%')

/** el-progress 的语义色：失败红、完成绿、其余主色（不传 status 即主色）。 */
const progressStatus = computed(() => {
  if (failed.value) return 'exception'
  if (props.snapshot?.ready) return 'success'
  return undefined
})

const steps = computed(() => props.snapshot?.steps ?? [])

/** 失败阶段摘要：把失败的步骤名与原因拼成一句可读文案。 */
const failedSummary = computed(() => {
  const failedSteps = steps.value.filter((step) => step.state === 'failed')
  if (!failedSteps.length) return '后端在启动过程中退出，系统未能进入可服务状态。'
  return failedSteps
    .map((step) => stepDisplayLabel(step) + (step.error ? '：' + step.error : '失败'))
    .join('；')
})

const versionText = computed(() => {
  const version = props.snapshot?.version
  if (!version) return ''
  const commit = props.snapshot?.build_commit
  const shortCommit = commit ? commit.slice(0, 7) : ''
  return shortCommit ? '版本 v' + version + '（' + shortCommit + '）' : '版本 v' + version
})

const actionText = computed(() => {
  if (failed.value) return '重新加载'
  if (props.connectionFailed) return '立即重试'
  return '重新加载'
})

const actionHint = computed(() => {
  if (failed.value) return '若反复失败，请联系管理员查看后端日志。'
  if (props.connectionFailed) return '会自动重连，也可以手动刷新。'
  return '启动完成后会自动进入系统。'
})

// ── 已耗时：服务端基准 + 本地每秒递增 ──────────────────────────────
const elapsedMs = ref(0)
let tickTimer: ReturnType<typeof setInterval> | null = null

watch(
  () => props.snapshot?.elapsed_ms,
  (value) => {
    if (typeof value === 'number' && Number.isFinite(value)) {
      // 只增不减：轮询回来的值可能因服务端计时起点抖动而略小
      elapsedMs.value = Math.max(elapsedMs.value, value)
    }
  },
  { immediate: true },
)

const elapsedText = computed(() => formatElapsed(elapsedMs.value))

/**
 * 重新加载：维护期把用户从「已知的维护页」带回正常入口。
 *
 * 独立成函数而不是内联 —— 单测要断言点击确实触发重载；内联会强制测试去
 * 替换 window.location（读写全局，容易互相污染）。
 */
function reload() {
  window.location.reload()
}

onMounted(() => {
  tickTimer = setInterval(() => {
    elapsedMs.value += 1000
  }, 1000)
})

onUnmounted(() => {
  if (tickTimer) {
    clearInterval(tickTimer)
    tickTimer = null
  }
})
</script>

<style scoped>
.maintenance-page {
  position: relative;
  display: flex;
  justify-content: center;
  align-items: center;
  box-sizing: border-box;
  min-height: 100vh;
  min-height: 100dvh;
  padding: 24px;
  background: var(--bg-color-page);
}

.maintenance-brand {
  position: absolute;
  top: 24px;
  left: 24px;
  display: flex;
  align-items: center;
  gap: 8px;
}
.maintenance-brand__logo {
  width: 28px;
  height: 28px;
}
.maintenance-brand__name {
  font-size: 16px;
  font-weight: 600;
  color: var(--text-color-primary);
}

.maintenance-card {
  width: min(640px, calc(100vw - 32px));
  max-height: calc(100vh - 96px);
  max-height: calc(100dvh - 96px);
  overflow-y: auto;
  border: 1px solid var(--border-color);
  border-radius: 16px;
  background: var(--card-bg);
  box-shadow: var(--card-shadow);
}

.maintenance-head {
  text-align: center;
  margin-bottom: 20px;
}
.maintenance-head__icon {
  font-size: 44px;
  color: var(--el-color-primary);
}
.maintenance-title {
  margin: 12px 0 8px;
  font-size: 22px;
  font-weight: 600;
  color: var(--text-color-primary);
}
.maintenance-subtitle {
  margin: 0;
  font-size: 14px;
  line-height: 1.6;
  color: var(--text-color-secondary);
}

.maintenance-alert {
  margin-bottom: 16px;
}

.maintenance-overall {
  margin-bottom: 20px;
}
.maintenance-overall__row {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 8px;
}
.maintenance-overall__label {
  font-size: 14px;
  color: var(--text-color-regular);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.maintenance-overall__percent {
  font-size: 20px;
  font-weight: 600;
  color: var(--el-color-primary);
  font-variant-numeric: tabular-nums;
}
.maintenance-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 16px;
  margin-top: 10px;
  font-size: 12px;
  color: var(--text-color-secondary);
}
.maintenance-meta__item strong {
  color: var(--text-color-primary);
  font-variant-numeric: tabular-nums;
}

.maintenance-steps {
  margin: 0;
  padding: 0;
  list-style: none;
  border-top: 1px solid var(--border-color);
}
.maintenance-step {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  padding: 10px 2px;
  border-bottom: 1px solid var(--border-color);
}
.maintenance-step__icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  flex-shrink: 0;
  font-size: 16px;
  color: var(--text-color-placeholder);
}
.maintenance-step__main {
  flex: 1;
  min-width: 0;
}
.maintenance-step__head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
}
.maintenance-step__label {
  font-size: 14px;
  color: var(--text-color-regular);
}
.maintenance-step__state {
  flex-shrink: 0;
  font-size: 12px;
  color: var(--text-color-secondary);
}
.maintenance-step__detail {
  margin-top: 4px;
  font-size: 12px;
  line-height: 1.5;
  color: var(--text-color-secondary);
  word-break: break-all;
}
.maintenance-step__error {
  margin-top: 4px;
  font-size: 12px;
  line-height: 1.5;
  color: var(--el-color-danger);
  word-break: break-all;
}
.maintenance-step__bar {
  margin-top: 8px;
}

/* 五态语义色：pending/skipped 灰、running 主色、done 绿、failed 红 */
.maintenance-step.is-running .maintenance-step__icon,
.maintenance-step.is-running .maintenance-step__label {
  color: var(--el-color-primary);
  font-weight: 600;
}
.maintenance-step.is-running .maintenance-step__state {
  color: var(--el-color-primary);
}
.maintenance-step.is-done .maintenance-step__icon,
.maintenance-step.is-done .maintenance-step__state {
  color: var(--el-color-success);
}
.maintenance-step.is-failed .maintenance-step__icon,
.maintenance-step.is-failed .maintenance-step__label {
  color: var(--el-color-danger);
  font-weight: 600;
}
.maintenance-step.is-failed .maintenance-step__state {
  color: var(--el-color-danger);
}
.maintenance-step.is-skipped .maintenance-step__label {
  text-decoration: line-through;
  color: var(--text-color-secondary);
}

.maintenance-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
  margin-top: 20px;
}
.maintenance-actions__hint {
  font-size: 12px;
  color: var(--text-color-secondary);
}

/* 转圈：running 态的阶段图标与头部图标共用。 */
.is-spin {
  animation: maintenance-spin 1s linear infinite;
}
@keyframes maintenance-spin {
  from {
    transform: rotate(0deg);
  }
  to {
    transform: rotate(360deg);
  }
}

@media (prefers-reduced-motion: reduce) {
  .is-spin {
    animation: none;
  }
}

@media (max-width: 480px) {
  .maintenance-brand {
    top: 16px;
    left: 16px;
  }
  .maintenance-title {
    font-size: 20px;
  }
  .maintenance-step__label {
    font-size: 13px;
  }
  /* 触控档：窄屏下按钮热区撑到 44px（规范 §4.4.5），视觉高度不变 */
  .maintenance-actions :deep(.el-button) {
    min-height: 44px;
  }
}
</style>
