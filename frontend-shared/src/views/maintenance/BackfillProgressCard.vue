<template>
  <!--
    历史数据回填进度卡（契约 §4.6）。

    为什么是**独立组件**而不是塞进 Monitor.vue：维护页与监控页都要展示它，
    两处若各写一份，口径（「0 = 未知」「差额不是丢失」）必然漂移。
    这里把展示与取数一并封装，两页各自 <BackfillProgressCard /> 即可。

    为什么放在业务页而不是维护页：App.vue 的启动门禁在 ready=true 时就放行了，
    而回填**恰好在 ready 之后才真正开跑**（main.go：先 startupTracker.Done 再
    SetEngine，回填 goroutine 在那之前已启动但不阻塞就绪）。生产实测 20~40 分钟，
    维护页在这个窗口里早就消失了 —— 用户真正能看到进度的位置只有业务页。
  -->
  <el-card
    v-if="visible"
    class="backfill-card"
    :class="{ 'is-failed': isFailed, 'is-suppressed': isSuppressed }"
    shadow="hover"
    data-test="backfill-card"
  >
    <template #header>
      <div class="card-header">
        <span><el-icon><Timer /></el-icon> 历史数据回填</span>
        <el-tag :type="tagType" size="small" data-test="backfill-state">{{ stateText }}</el-tag>
      </div>
    </template>

    <!-- 失败态：必须显式说明「历史数据还没迁完」，否则用户会以为数据已经齐了。 -->
    <el-alert
      v-if="isFailed"
      class="backfill-alert"
      type="error"
      :closable="false"
      show-icon
      title="历史数据回填中断"
      data-test="backfill-failed"
    >
      <p class="backfill-alert__lead">
        历史数据尚未全部迁移完成。已迁移的部分不受影响，服务重启后会自动从断点继续。
      </p>
      <p v-if="lastError" class="backfill-alert__detail">{{ lastError }}</p>
    </el-alert>

    <!--
      已关闭（EHOME_BACKFILL_ENABLED=false）：**不隐藏、不报错**。
      关掉回填意味着老 unified_data 永远不会迁到帧表 —— 这是用户必须知道的事实，
      静默隐藏会让"数据没迁"这件事永不可见。故用 info 而非 error：
      这是运维的**有意决策**，不是故障。
    -->
    <el-alert
      v-else-if="isSuppressed"
      class="backfill-alert"
      type="info"
      :closable="false"
      show-icon
      title="历史数据回填已关闭"
      data-test="backfill-disabled"
    >
      <p class="backfill-alert__lead">
        回填功能当前被运维关闭（EHOME_BACKFILL_ENABLED=false），历史数据不会迁移到帧表。
        实时数据采集、告警与新的历史曲线均不受影响。
      </p>
    </el-alert>

    <template v-else>
      <el-progress
        class="backfill-bar"
        :percentage="percent"
        :stroke-width="10"
        :show-text="false"
        data-test="backfill-bar"
      />
      <div class="backfill-meta">
        <span class="backfill-meta__item" data-test="backfill-percent">
          进度 <strong>{{ percent }}%</strong>
        </span>
        <span class="backfill-meta__item" data-test="backfill-frames">
          已回填 <strong>{{ framesText }}</strong>
        </span>
        <span class="backfill-meta__item" data-test="backfill-eta">
          剩余 <strong>{{ etaText }}</strong>
        </span>
        <span v-if="partitionText" class="backfill-meta__item" data-test="backfill-partition">
          当前分区 <strong>{{ partitionText }}</strong>
        </span>
      </div>
    </template>

    <!--
      两句解释的取舍：都只在**需要**的时候出现，避免卡片变成一段说明书。
      · 永久保留：运维看到「回填」容易以为老数据会被删。实际回填引擎**从不**删除
        源数据（TestBackfillNeverDeletesSourceData 源码门禁守着），但保留期配置
        为 0 天时这一点的含义更强 —— 值得明说。
      · 后台进行：回填期间查询历史曲线会变慢（批量编码在抢 IO）。提前说明比
        让用户自己发现「页面卡了」要好。
    -->
    <p v-if="retentionPermanent" class="backfill-hint" data-test="backfill-retention">
      当前保留期为「永久保留」：回填只新增帧表数据，不会删除任何历史记录。
    </p>
    <p v-if="!isFailed && !isSuppressed" class="backfill-hint">
      回填在后台执行，不影响实时数据采集与告警；期间查询较久远的历史曲线可能稍慢。
    </p>
  </el-card>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted } from 'vue'
import { Timer } from '@element-plus/icons-vue'
import { UNKNOWN } from '@/utils/format'
import { useBackfillProgress, type BackfillProgressOptions } from './useBackfillProgress'
import {
  BACKFILL_STATE_TEXT,
  clampBackfillPercent,
  formatEta,
  formatFrames,
  isBackfillSuppressed,
  shouldShowBackfill,
} from './backfill'

/**
 * 历史数据回填进度卡（契约 §4.6）。
 *
 * 四种可见状态，**都必须有确定的表现**（不能出现"卡片忽隐忽现"）：
 *   · running  —— 进度条 + 帧数 + ETA + 当前分区；
 *   · failed   —— 错误块 + 中断原因（**不隐藏**：历史数据没迁完是必须告知的事实）；
 *   · disabled —— info 块（**不隐藏**：运维关阀 ⇒ 老数据永远不会迁移，同样必须告知）；
 *   · done / idle —— 整卡不渲染（没有未完成的工作可汇报，不该占首屏）。
 *
 * options 只为单测注入：生产两处调用都不传参。
 */
const props = defineProps<{
  /** 取数/排程注入（单测用）；生产不传。 */
  options?: BackfillProgressOptions
}>()

const { progress, start, stop } = useBackfillProgress(props.options ?? {})

// 挂载即开始轮询；卸载即停（useBackfillProgress 内部也注册了 onUnmounted，
// 这里显式再调一次是为了让「谁负责停」在组件里可见 —— stop 是幂等的）。
onMounted(start)
onUnmounted(stop)

/** 只有 running / failed 需要占用户注意力，其余整卡不渲染（判据见 backfill.ts）。 */
const visible = computed(() => shouldShowBackfill(progress.value))

const isFailed = computed(() => progress.value?.state === 'failed')

/** 已关闭（运维关阀）：无进度可展示，但必须让用户看到这件事（判据见 backfill.ts）。 */
const isSuppressed = computed(() => isBackfillSuppressed(progress.value?.state))

const percent = computed(() => clampBackfillPercent(progress.value?.percent))

/** 状态文案：状态本身 + 一句人话。idle 不会走到这里（visible 已挡掉），但仍给兜底。 */
const stateText = computed(() => {
  const state = progress.value?.state
  return state ? BACKFILL_STATE_TEXT[state] : UNKNOWN
})

/** el-tag 的语义色：失败红、已关闭灰（info）、进行中主色（不传 type 即主色）。 */
const tagType = computed(() => {
  if (isFailed.value) return 'danger'
  if (isSuppressed.value) return 'info'
  return undefined
})

const framesText = computed(() =>
  formatFrames(progress.value?.frames_done, progress.value?.frames_total),
)

const etaText = computed(() => formatEta(progress.value?.eta_seconds))

/**
 * 当前分区名。
 *
 * 直接显示后端给的分区名（如 unified_data_202610）而不做「2026 年 10 月」的美化：
 * 排障时运维要拿这个名字去数据库里 `\\d+` / 查 pg_class，译成中文反而对不上。
 */
const partitionText = computed(() => progress.value?.current_partition || '')

/** 保留期是否永久：只有 true 才提示（false 时那句话会误导成"会删"）。 */
const retentionPermanent = computed(() => progress.value?.retention_permanent === true)

/** 中断原因取最近一条（后端最多保留 20 条，越靠后越新）。 */
const lastError = computed(() => {
  const errors = progress.value?.errors
  return Array.isArray(errors) && errors.length > 0 ? errors[errors.length - 1] : ''
})
</script>

<style scoped>
.backfill-card {
  margin-top: 20px;
}

/* 失败态用左边框强调，不改变卡片底色 —— 底色变化会和「控制面健康」的 stale 观感混淆。 */
.backfill-card.is-failed {
  border-left: 3px solid var(--el-color-danger);
}

/* 已关闭态用中性左边框：它是运维决策，不是故障，不该有红色告警观感。 */
.backfill-card.is-suppressed {
  border-left: 3px solid var(--el-color-info);
}

.card-header {
  display: flex;
  align-items: center;
  gap: 8px;
  font-weight: bold;
}

.backfill-bar {
  margin-bottom: 12px;
}

.backfill-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 20px;
  font-size: 13px;
  color: var(--el-text-color-regular);
}

.backfill-meta__item strong {
  color: var(--el-text-color-primary);
  font-weight: 600;
}

.backfill-hint {
  margin: 12px 0 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--el-text-color-secondary);
}

.backfill-alert {
  margin-bottom: 0;
}

.backfill-alert__lead {
  margin: 0;
}

.backfill-alert__detail {
  margin: 8px 0 0;
  font-family: var(--el-font-family-mono, monospace);
  font-size: 12px;
  word-break: break-all;
}
</style>
