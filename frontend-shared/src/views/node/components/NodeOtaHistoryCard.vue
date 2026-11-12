<template>
        <section class="card ota-card">
          <div class="card-head">
            <span class="card-title">OTA 升级历史</span>
            <button class="btn btn-plain btn-sm" :disabled="otaHistoryLoading" @click="refreshHistory"><el-icon :size="13" :class="{ spin: otaHistoryLoading }"><RefreshRight /></el-icon>{{ otaHistoryLoading ? '加载中…' : '刷新' }}</button>
          </div>
          <div v-if="otaHistoryLoading" class="card-loading"><el-skeleton :rows="3" animated /></div>
          <div v-else-if="otaHistory.length === 0" class="card-empty">暂无升级记录</div>
          <div v-else class="bus-table-wrap ota-table-wrap">
            <!-- A-nodes P2：393px 下 6 列 680px 宽表可横滑但无任何提示（右侧三列默认看不见）。
                 复用全站既有 .mobile-table-hint（theme.css：仅 ≤768px display:block），
                 桌面零变化。 -->
            <div class="mobile-table-hint" data-ota-scroll-hint>← 左右滑动查看完整表格 →</div>
            <table class="bus-table ota-table">
              <thead>
                <tr>
                  <th>升级版本</th>
                  <th>状态</th>
                  <th>进度</th>
                  <th>开始时间</th>
                  <th>完成时间</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="record in otaHistory" :key="record.id">
                  <td class="mono">{{ record.from_version ? record.from_version + ' → ' + record.to_version : record.to_version }}</td>
                  <td><span class="bus-tag" :class="otaTagClass(record.status)">{{ otaStatusText(record.status) }}</span></td>
                  <td>
                    <div class="ota-progress"><span class="ota-progress-bar" :style="{ width: otaProgressWidth(record) }"></span></div>
                    <span class="ota-progress-num">{{ otaProgressText(record) }}</span>
                  </td>
                  <td>{{ formatTime(record.created_at) }}</td>
                  <td>{{ record.completed_at ? formatTime(record.completed_at) : '—' }}</td>
                  <td>
                    <button v-if="record.status === 'pending' || record.status === 'downloading'" class="link-btn link-btn-danger" type="button" :disabled="nodeOffline" @click="cancelRecord(record)">取消</button>
                    <span v-else class="ota-na">—</span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>
</template>

<script setup lang="ts">
import { RefreshRight } from '@element-plus/icons-vue'
import type { OTARecord } from '@/api/node'
import { formatTime } from '@/utils/format'

/**
 * 「OTA 历史」页签：升级记录表 + 取消入口。
 *
 * 状态映射（otaStatusText / otaTagClass）刻意留在这里而不是抽到通用工具：
 * 它们的唯一真源是 backend/internal/ota/ota.go 的 8 个状态，
 * 而 OtaStatusTruthSource.spec 直接读**本文件源码**做双向相等校验 ——
 * 放到别处会让门禁失去定位能力（改错状态文案时还得先猜它在哪个文件）。
 */
defineProps<{
  otaHistory: OTARecord[]
  otaHistoryLoading: boolean
  nodeOffline: boolean
}>()

const emit = defineEmits<{
  refresh: []
  cancel: [record: OTARecord]
}>()

// 状态全集以 backend/internal/ota/ota.go 的 Status* 常量为唯一真源（8 态）。
// 历史缺陷（2026-09-17）：otaStatusText / otaTagClass 只覆盖 5 态，漏掉
//   verifying / timeout / needs_retry；文案走 `texts[status] || status` ⇒ 中文界面
//   原样显示英文；颜色回退到中性灰 ⇒「升级超时」「需要重试」与「等待中」视觉无异。
//   timeout 是真实可达状态（ota.go 的 timeoutScanner 会写入），不是理论分支。
// 'cancelled' 是设计文档遗留的死条目：后端从未定义该状态（取消复用 failed +
//   error_msg）。这里不再为它保留映射，见 OtaStatusTruthSource.spec 门禁。
function otaStatusText(status: string): string {
  const texts: Record<string, string> = {
    pending: '等待中', downloading: '下载中', verifying: '校验中', installing: '安装中',
    success: '成功', failed: '失败', timeout: '超时', needs_retry: '需要重试',
  }
  return texts[status] || status
}
function otaTagClass(status: string): string {
  // 红/橙不是新增 token：.bus-tag-red 用亮暗两套都已定义的 --no-danger，
  // .bus-tag-orange 用 --no-warning-text + --no-warning-bg（同 .bus-stat-warning）。
  const classes: Record<string, string> = {
    pending: 'bus-tag-blue', downloading: 'bus-tag-blue',
    verifying: 'bus-tag-blue', installing: 'bus-tag-blue',
    success: 'bus-tag-green', failed: 'bus-tag-red',
    timeout: 'bus-tag-red', needs_retry: 'bus-tag-orange',
  }
  return classes[status] || 'bus-tag-gray'
}
function otaProgressWidth(record: OTARecord): string {
  const progress = Number(record.progress)
  if (!Number.isFinite(progress) || progress <= 0) return '0%'
  return `${Math.min(100, Math.max(0, progress))}%`
}
function otaProgressText(record: OTARecord): string {
  // 进度数字与进度条共用同一钳制值，避免"满条 + 150%"不一致
  const progress = Number(record.progress)
  if (!Number.isFinite(progress) || progress <= 0) return '0%'
  return `${Math.min(100, Math.max(0, progress))}%`
}

function refreshHistory() { emit('refresh') }
function cancelRecord(record: OTARecord) { emit('cancel', record) }
</script>

<style scoped>
/* OTA 卡的桌面规则。表头/列宽沿用共享层 .bus-table 口径（与总线资源表同一套密度），
   窄屏横滑提示与 .ota-table-wrap 的出血在 nodeOverview.shared.css 的唯一 768 块里。 */
/* OTA 问题态标签：红=失败/超时，橙=需要重试。色值直接引用页面级语义 token
   （--no-danger / --no-warning-text / --no-warning-bg 在 html.dark 块里已另给暗色档），
   因此亮暗两套自动跟随主题，无需再定义新 token。 */
.bus-tag-red { color: var(--no-danger); background: color-mix(in srgb, var(--no-danger) 12%, transparent); }
.bus-tag-orange { color: var(--no-warning-text); background: var(--no-warning-bg); }
/* OTA 历史 */
.ota-card { overflow: hidden; }
.ota-table { min-width: 680px; table-layout: auto; }
.ota-table th:nth-child(1), .ota-table td:nth-child(1) { width: 20%; }
.ota-table th:nth-child(2), .ota-table td:nth-child(2) { width: 14%; }
.ota-table th:nth-child(3), .ota-table td:nth-child(3) { width: 18%; }
.ota-table th:nth-child(4), .ota-table td:nth-child(4) { width: 18%; }
.ota-table th:nth-child(5), .ota-table td:nth-child(5) { width: 18%; }
.ota-table th:nth-child(6), .ota-table td:nth-child(6) { width: 12%; }
.ota-progress { display: inline-block; width: 72px; height: 6px; border-radius: 3px; background: var(--no-chip-off-bg); overflow: hidden; vertical-align: middle; margin-right: 6px; }
.ota-progress-bar { display: block; height: 100%; border-radius: 3px; background: var(--no-primary); }
.ota-progress-num { font-size: 12px; color: var(--no-text-secondary); font-variant-numeric: tabular-nums; }
.ota-na { color: var(--no-text-faint); }
.link-btn-danger { color: var(--no-danger); }
.link-btn-danger:hover:not(:disabled) { color: var(--no-danger); opacity: .8; }
</style>

<style scoped src="./nodeOverview.shared.css"></style>
