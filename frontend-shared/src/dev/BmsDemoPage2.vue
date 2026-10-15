<template>
  <!--
    BmsDemoPage2.vue — BMS 设备详情页「重设计」评审 demo（2026-10-15）

    · 全部数据为本地 mock，不调用任何后端 API。
    · 仅开发环境路由可见（/dev/bms-demo2），带 DEV 水印。
    · 不改动既有 BmsDemoPage.vue / 生产页面 / 共享组件。
    · 本轮四条用户诉求的落点见各区段标题注释。
  -->
  <div class="bms-demo2">
    <div class="dev-watermark">DEV DEMO · BMS 详情页重设计评审（全部为本地 mock 数据，非生产页面）</div>

    <!-- 页头：设备身份 + 演示态切换 -->
    <header class="page-head">
      <div class="crumb">
        <el-icon :size="14"><HomeFilled /></el-icon>
        <span class="sep">/</span>
        <span class="crumb-link">边缘设备管理</span>
        <span class="sep">/</span>
        <span class="crumb-current">BMS 设备详情</span>
      </div>
      <div class="head-main">
        <div class="dev-ident">
          <div class="dev-name-line">
            <h1 class="dev-name">BMS-Pack-01</h1>
            <el-tag type="success" size="small" effect="plain">在线</el-tag>
            <el-tag type="info" size="small" effect="plain">jiabaida · 16S</el-tag>
          </div>
          <div class="dev-sub">
            节点 工厂A / 产线1 · 通道1 · 设备ID BMS3000A1B2C3 · 固件 {{ softwareVersion }}
            · 轮询已启用 <b>{{ enabledCount }}/{{ MAX_POLL }}</b> 条
          </div>
        </div>
        <div class="head-actions">
          <div class="mode-switch" role="group" aria-label="演示数据模式">
            <span class="mode-label">演示数据</span>
            <el-radio-group v-model="mode" size="small">
              <el-radio-button value="normal">正常态</el-radio-button>
              <el-radio-button value="abnormal">异常态</el-radio-button>
            </el-radio-group>
          </div>
          <el-button size="small" :icon="Refresh" @click="resetDemo">重置演示</el-button>
        </div>
      </div>
    </header>

    <main class="page-body">
      <!-- 指标卡 -->
      <section class="row metrics">
        <MetricStatCard label="SOC 剩余电量" :value="soc.toFixed(1)" unit="%" tone="success" :progress="Math.round(soc)" sub-text="健康区间 20% – 90%">
          <template #icon><el-icon :size="22"><PieChart /></el-icon></template>
        </MetricStatCard>
        <MetricStatCard label="剩余容量" :value="remainingCapacity.toFixed(2)" unit="Ah" tone="primary" :sub-text="'/ ' + nominalCapacity + ' Ah 标称'">
          <template #icon><el-icon :size="22"><Odometer /></el-icon></template>
        </MetricStatCard>
        <MetricStatCard label="总电压" :value="totalVoltage.toFixed(2)" unit="V" tone="primary" :sub-text="'16 串 · 平均 ' + avgCell.toFixed(3) + 'V'">
          <template #icon><el-icon :size="22"><DataLine /></el-icon></template>
        </MetricStatCard>
        <MetricStatCard label="电流" :value="currentDisplay.toFixed(2)" unit="A" tone="primary" :sub-text="currentHint">
          <template #icon><el-icon :size="22"><Lightning /></el-icon></template>
        </MetricStatCard>
      </section>

      <!-- 诉求①：MOS 充电/放电直接开关 + 诉求④：保护状态 -->
      <section class="row mos-protect">
        <!-- 诉求① 落点：MOS 直控卡（页首主位，不再埋进 26 项受控操作） -->
        <div class="card mos-card">
          <div class="card-head">
            <span class="card-title">MOS 直控</span>
            <span class="card-hint">像电灯开关一样直接操作</span>
            <span class="head-right mono-badge">fet_status = 0x{{ fetHex }}</span>
          </div>
          <p class="card-note">
            充电 / 放电回路通断的软件策略 <code>set_mos_policy</code>。开关即时下发，切换前弹
            <b>一次极简确认</b>（只有 确认 / 取消，不填理由）；后端回读 fet_status 校验，不一致即判失败并常驻原因。
          </p>

          <div v-for="m in mosRows" :key="m.key" class="mos-item">
            <div class="mos-row" :class="{ 'is-applying': !!m.phase }">
              <span class="mos-icon" :class="m.on ? 'is-on' : 'is-off'">
                <el-icon :size="18"><component :is="m.on ? Open : Lock" /></el-icon>
              </span>
              <div class="mos-main">
                <div class="mos-name-line">
                  <span class="mos-name">{{ m.name }}</span>
                  <el-tag :type="m.on ? 'success' : 'info'" size="small" effect="plain">{{ m.on ? '导通' : '关断' }}</el-tag>
                  <el-tag v-if="m.phase" type="primary" size="small">{{ m.phase }}…</el-tag>
                  <span v-if="m.hwOff" class="hw-off-note"><el-icon :size="12"><WarningFilled /></el-icon>被硬件保护关断</span>
                </div>
                <div class="mos-desc">{{ m.desc }}</div>
              </div>
              <el-switch
                class="mos-switch"
                :model-value="m.on"
                :loading="!!m.phase"
                :disabled="!!m.phase"
                :aria-label="m.name + '开关'"
                @change="(v: string | number | boolean) => requestMosChange(m.key, !!v)"
              />
            </div>
            <el-alert v-if="m.error" type="error" :closable="false" show-icon class="inline-alert">
              <template #title>{{ m.name }}操作失败（回读校验不一致）</template>
              <div class="alert-detail">{{ m.error }}</div>
            </el-alert>
          </div>

          <p class="card-footnote">
            着色规则：<b>导通</b>用成功色、<b>关断</b>用中性色 —— 关断是正常工况，不是故障；只有「保护已触发」才用告警色。
          </p>
        </div>

        <!-- 诉求④ 落点：保护状态（正常态紧凑一行 / 异常态展开 12 项明细） -->
        <div class="card protect-card">
          <div class="card-head">
            <span class="card-title">保护状态</span>
            <el-tag v-if="activeProtections.length" type="danger" size="small">
              {{ activeProtections.length }} 项保护已触发
            </el-tag>
            <el-tag v-else-if="mode === 'abnormal'" type="warning" size="small">无保护位命中</el-tag>
            <el-tag v-else type="success" size="small" effect="plain">
              <el-icon :size="12"><CircleCheckFilled /></el-icon> 全部正常
            </el-tag>
            <span class="head-right mono-badge">protection_status = 0x{{ protHex }}</span>
          </div>

          <!-- 正常态：紧凑一行 -->
          <div v-if="!activeProtections.length" class="protect-compact" role="status">
            <el-icon :size="18" color="var(--el-color-success)"><CircleCheckFilled /></el-icon>
            <span class="protect-compact-text">12 项保护均未触发（bit0 – bit11 全 0）</span>
            <button type="button" class="link-btn" @click="protectExpanded = !protectExpanded">
              {{ protectExpanded ? '收起明细' : '展开明细' }}
            </button>
          </div>

          <!-- 异常态：结论 + 明细 -->
          <template v-else>
            <el-alert type="error" :closable="false" show-icon class="inline-alert">
              <template #title>{{ activeProtections.map(p => p.label).join('、') }} 已触发</template>
              <div class="alert-detail">
                protection_status = 0x{{ protHex }}。硬件保护可能已覆盖对应 MOS：请先排除故障，再到左上方尝试开关。
              </div>
            </el-alert>
          </template>

          <div v-if="activeProtections.length || protectExpanded" class="protect-grid">
            <div v-for="p in protectionBits" :key="p.bit" class="protect-item" :class="{ active: p.active }">
              <span class="protect-name">{{ p.label }}</span>
              <el-tag :type="p.active ? 'danger' : 'success'" size="small" :effect="p.active ? 'dark' : 'plain'">
                {{ p.active ? '已触发' : '正常' }}
              </el-tag>
              <span class="protect-bit">bit{{ p.bit }}</span>
            </div>
          </div>
        </div>
      </section>

      <!-- 电芯电压分布 + 温度探头 -->
      <section class="row cell-temp">
        <div class="card cell-card">
          <div class="card-head">
            <span class="card-title">电芯电压分布</span>
            <span class="card-hint">16 串 · 单位 V</span>
            <span class="head-right cell-summary">
              最高 <b class="txt-warning">#{{ maxCell.index }} {{ maxCell.voltage.toFixed(3) }}V</b>
              · 最低 <b class="txt-secondary">#{{ minCell.index }} {{ minCell.voltage.toFixed(3) }}V</b>
              · 压差 <b :class="diff > 0.1 ? 'txt-warning' : 'txt-secondary'">{{ diff.toFixed(3) }}V</b>
            </span>
          </div>
          <div class="cell-chart-scroll">
            <div class="cell-chart">
              <div v-for="c in cells" :key="c.index" class="cell-col" :class="cellClass(c)">
                <span class="cell-value" :class="{ show: isExtreme(c) }">{{ c.voltage.toFixed(3) }}</span>
                <div class="cell-bar-track">
                  <div class="cell-bar" :style="{ height: cellBarHeight(c.voltage) }"></div>
                </div>
                <span class="cell-idx">{{ c.index }}</span>
              </div>
            </div>
          </div>
          <div class="cell-legend">
            <span class="lg"><i class="swatch swatch-normal"></i>正常</span>
            <span class="lg"><i class="swatch swatch-high"></i>组内相对最高</span>
            <span class="lg"><i class="swatch swatch-low"></i>组内相对最低</span>
            <span class="lg"><i class="swatch swatch-danger"></i>越限</span>
            <span class="cell-thresholds">单体过压 3.650V · 单体欠压 2.500V</span>
          </div>
          <p class="card-footnote">橙色 / 青色只表示组内相对高低，<b>不等于故障</b>；红色才是越限。</p>
        </div>

        <div class="card temp-card">
          <div class="card-head">
            <span class="card-title">温度探头</span>
            <span class="card-hint">4 路</span>
          </div>
          <div class="temp-list">
            <div v-for="t in temps" :key="t.index" class="temp-item">
              <span class="temp-label">探头{{ t.index }}</span>
              <span class="temp-value" :class="'is-' + t.level">{{ t.value.toFixed(1) }} ℃</span>
              <el-tag :type="t.level === 'success' ? 'success' : t.level === 'warning' ? 'warning' : 'danger'" size="small" effect="plain">
                {{ t.text }}
              </el-tag>
            </div>
          </div>
          <p class="card-footnote">判定：&gt;45℃ 偏高（告警）· &gt;60℃ 过高（危险）。正常温区用成功色，不用危险色。</p>
        </div>
      </section>

      <!-- 诉求②：实时数据流不抢滚动 + 诉求③：轮询 3 条上限 -->
      <section class="row stream-poll">
        <!-- 诉求② 落点：跟随模式 + 上滚即暂停 + 回到最新 -->
        <div class="card stream-card">
          <div class="card-head">
            <span class="card-title">实时数据流</span>
            <el-tag :type="following ? 'success' : 'warning'" size="small" :effect="following ? 'plain' : 'dark'">
              {{ following ? '跟随最新' : '已暂停跟随' }}
            </el-tag>
            <el-tag v-if="!following && pendingCount" type="primary" size="small">{{ pendingCount }} 条新数据</el-tag>
            <span class="card-hint">上滚即暂停，不抢滚动</span>
            <div class="head-right stream-actions">
              <el-button size="small" :type="injecting ? 'warning' : 'default'" :icon="MagicStick" @click="toggleInject">
                {{ injecting ? '停止注入' : '高频注入（1 条/秒）' }}
              </el-button>
              <el-button size="small" @click="clearStream">清空</el-button>
            </div>
          </div>

          <div class="stream-box">
            <div ref="streamEl" class="stream-scroll" @scroll="onStreamScroll">
              <div v-for="row in streamRows" :key="row.id" class="stream-row">
                <span class="s-time">{{ row.time }}</span>
                <span class="s-item">{{ row.item }}</span>
                <span class="s-value">{{ row.value }}</span>
                <span class="s-unit">{{ row.unit }}</span>
                <span class="s-status" :class="'is-' + row.status">{{ row.statusText }}</span>
              </div>
              <div v-if="!streamRows.length" class="stream-empty">暂无数据，点击「高频注入」开始滚动压测</div>
            </div>
            <transition name="fade">
              <button v-if="!following" type="button" class="back-latest" @click="scrollToLatest">
                <el-icon :size="14"><Bottom /></el-icon>
                回到最新<span v-if="pendingCount">（{{ pendingCount }} 条）</span>
              </button>
            </transition>
          </div>

          <p class="card-footnote">
            验证方式：点「高频注入」让列表每秒新增 1 条 → 用鼠标/触控<b>向上滚</b>，列表应停在原处、
            新数据只累积计数而不把视口拉回；点「回到最新」或滚回底部即恢复跟随。
          </p>
        </div>

        <!-- 诉求③ 落点：轮询指令配置（3 条上限可见、超限点名要禁用哪几条） -->
        <div class="card poll-card">
          <div class="card-head">
            <span class="card-title">轮询指令配置</span>
            <el-tag :type="pollBadgeType" size="small">已启用 {{ enabledCount }}/{{ MAX_POLL }}</el-tag>
          </div>
          <p class="card-note">
            每条指令可单独设间隔，<b>0 = 禁用</b>。固件静态上限为同时最多 <b>{{ MAX_POLL }} 条</b>；
            超过会被后端整单拒绝（HTTP 400）且不落库 —— 所以这里在保存前就把约束摆出来。
          </p>

          <el-alert v-if="overLimit" type="warning" :closable="false" show-icon class="inline-alert">
            <template #title>已启用 {{ enabledCount }} 条，超过上限 {{ MAX_POLL }} 条，保存会被拒绝</template>
            <div class="alert-detail">请先禁用 {{ enabledCount - MAX_POLL }} 条：<b>{{ excessNames }}</b>。</div>
          </el-alert>
          <el-alert v-else-if="enabledCount === MAX_POLL" type="info" :closable="false" class="inline-alert">
            <template #title>已占满 {{ MAX_POLL }}/{{ MAX_POLL }} 条轮询额度</template>
            <div class="alert-detail">再启用任何一条都会超限；如需调整，请先禁用一条。</div>
          </el-alert>

          <div class="poll-list">
            <div v-for="c in pollCommands" :key="c.id" class="poll-row" :class="{ 'is-over': isExcess(c) }">
              <div class="poll-main">
                <div class="poll-name-line">
                  <span class="poll-name">{{ c.name }}</span>
                  <el-tag v-if="isExcess(c)" type="warning" size="small" effect="dark">超限</el-tag>
                  <el-tag v-else-if="!c.enabled" type="info" size="small" effect="plain">已禁用</el-tag>
                </div>
                <div class="poll-desc">{{ c.desc }}</div>
              </div>
              <div class="poll-controls">
                <el-input-number
                  v-model="c.interval"
                  :min="0"
                  :max="60000"
                  :step="1000"
                  size="small"
                  controls-position="right"
                  :disabled="!c.enabled"
                  class="poll-interval"
                  :aria-label="c.name + '轮询间隔（毫秒，0 = 禁用）'"
                  @change="() => onIntervalChange(c)"
                />
                <span class="poll-unit">ms</span>
                <el-switch
                  :model-value="c.enabled"
                  size="small"
                  :aria-label="c.name + '启用轮询'"
                  @change="(v: string | number | boolean) => togglePoll(c, !!v)"
                />
                <el-button v-if="isExcess(c)" size="small" @click="togglePoll(c, false)">禁用</el-button>
              </div>
            </div>
          </div>

          <div class="poll-foot">
            <el-button type="primary" size="small" :disabled="overLimit || !pollDirty || pollBusy" @click="savePoll">保存</el-button>
            <span class="poll-sync" :class="pollSaveState === 'synced' ? 'is-ok' : overLimit ? 'is-warn' : ''">{{ pollHint }}</span>
          </div>
        </div>
      </section>

      <!-- 受控操作（分组，不平铺 26 项） + 操作历史 -->
      <section class="row ops-history">
        <div class="card ops-card">
          <div class="card-head">
            <span class="card-title">受控操作</span>
            <span class="card-hint">共 {{ totalOps }} 项 · 按用途分组，默认收起</span>
            <span class="head-right state-legend">
              <el-icon :size="13"><InfoFilled /></el-icon>
              状态文案：已发送 → 执行中 → 校验中 → 成功 / 失败 / 结果未知 / 已取消
            </span>
          </div>

          <div class="op-groups">
            <div v-for="g in opGroups" :key="g.id" class="op-group">
              <button type="button" class="op-group-head" @click="toggleGroup(g.id)">
                <el-icon :size="14" class="op-caret" :class="{ open: openGroups[g.id] }"><ArrowRight /></el-icon>
                <el-icon :size="15" class="op-group-icon"><component :is="g.icon" /></el-icon>
                <span class="op-group-name">{{ g.name }}</span>
                <span class="op-group-count">{{ g.ops.length }} 项</span>
                <el-tag v-if="riskCount(g)" type="danger" size="small" effect="plain">{{ riskCount(g) }} 项风险</el-tag>
                <span class="op-group-hint">{{ g.hint }}</span>
              </button>

              <div v-show="openGroups[g.id]" class="op-list">
                <div v-for="op in g.ops" :key="op.id" class="op-item">
                  <div class="op-info">
                    <div class="op-name-line">
                      <span class="op-name">{{ op.name }}</span>
                      <el-tag :type="op.risk ? 'danger' : 'info'" size="small" effect="plain">{{ op.risk ? '风险' : '普通' }}</el-tag>
                      <el-tag v-if="op.movedTo" type="primary" size="small">已移至上方直控</el-tag>
                    </div>
                    <div v-if="op.note" class="op-desc">{{ op.note }}</div>
                  </div>
                  <div class="op-right">
                    <span v-if="opState(op.id).phase !== 'idle'" class="op-state">
                      <i class="dot" :class="stateDot(opState(op.id).phase)"></i>{{ opState(op.id).text }}
                    </span>
                    <el-button v-if="op.movedTo" size="small" @click="scrollToMos">去上方开关</el-button>
                    <el-button
                      v-else
                      size="small"
                      :type="op.risk ? 'warning' : 'primary'"
                      :plain="op.risk"
                      :loading="opBusy(op.id)"
                      :disabled="opBusy(op.id)"
                      @click="runOp(op)"
                    >
                      {{ op.risk ? '确认后执行' : '执行' }}
                    </el-button>
                  </div>
                  <el-alert v-if="opState(op.id).reason" type="error" :closable="false" class="op-reason" :title="opState(op.id).reason" />
                </div>
              </div>
            </div>
          </div>
        </div>

        <div class="card history-card">
          <div class="card-head">
            <span class="card-title">操作历史</span>
            <span class="card-hint">审计线索 · 最近 {{ history.length }} 条</span>
          </div>
          <div class="history-list">
            <div v-for="h in history" :key="h.id" class="history-item">
              <div class="h-top">
                <span class="h-type">{{ h.type }}</span>
                <el-tag :type="resultTag(h.result)" size="small" effect="plain">{{ resultText(h.result) }}</el-tag>
              </div>
              <div class="h-content">{{ h.content }}</div>
              <div class="h-meta">{{ h.time }} · admin</div>
            </div>
          </div>
        </div>
      </section>
    </main>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, watch, onMounted, onUnmounted, nextTick } from 'vue'
import type { Component } from 'vue'
import {
  HomeFilled, PieChart, Odometer, DataLine, Lightning, Open, Lock,
  CircleCheckFilled, WarningFilled, Bottom, ArrowRight, InfoFilled,
  Timer, Operation, SetUp, Tools, Document, MagicStick, Refresh,
} from '@element-plus/icons-vue'
import MetricStatCard from '@/components/common/MetricStatCard.vue'
import { feedback } from '@/utils/feedback'

// ════════════ 通用 ════════════
const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms))
const pad2 = (n: number) => String(n).padStart(2, '0')
function nowTime(): string {
  const t = new Date()
  return pad2(t.getHours()) + ':' + pad2(t.getMinutes()) + ':' + pad2(t.getSeconds())
}
function nowStamp(): string {
  const t = new Date()
  return t.getFullYear() + '-' + pad2(t.getMonth() + 1) + '-' + pad2(t.getDate()) + ' '
    + pad2(t.getHours()) + ':' + pad2(t.getMinutes()) + ':' + pad2(t.getSeconds())
}

// ════════════ 演示态：正常 / 异常（本地 mock） ════════════
const mode = ref<'normal' | 'abnormal'>('normal')
const isAbnormal = computed(() => mode.value === 'abnormal')
const softwareVersion = 'v2.14'
const nominalCapacity = 106
const CYCLE_COUNT = 10

const NORMAL_CELLS = [3.323, 3.318, 3.331, 3.309, 3.336, 3.327, 3.314, 3.330, 3.341, 3.325, 3.312, 3.319, 3.338, 3.329, 3.321, 3.316]
const ABNORMAL_CELLS = [3.323, 3.318, 3.331, 3.309, 3.681, 3.327, 3.314, 3.330, 3.341, 3.325, 3.312, 3.319, 3.338, 3.329, 3.321, 3.316]
const NORMAL_TEMPS = [25.4, 26.1, 24.8, 27.3]
const ABNORMAL_TEMPS = [30.2, 31.7, 47.5, 34.9]

interface CellVoltage { index: number; voltage: number }
interface TempProbe { index: number; value: number; level: 'success' | 'warning' | 'danger'; text: string }

const cellValues = computed(() => (isAbnormal.value ? ABNORMAL_CELLS : NORMAL_CELLS))
const tempValues = computed(() => (isAbnormal.value ? ABNORMAL_TEMPS : NORMAL_TEMPS))
const cells = computed<CellVoltage[]>(() => cellValues.value.map((v, i) => ({ index: i + 1, voltage: v })))
const totalVoltage = computed(() => cellValues.value.reduce((a, b) => a + b, 0))
const avgCell = computed(() => totalVoltage.value / cellValues.value.length)
const maxCell = computed(() => cells.value.reduce((a, b) => (b.voltage > a.voltage ? b : a), cells.value[0]))
const minCell = computed(() => cells.value.reduce((a, b) => (b.voltage < a.voltage ? b : a), cells.value[0]))
const diff = computed(() => maxCell.value.voltage - minCell.value.voltage)
const soc = computed(() => (isAbnormal.value ? 76.0 : 78.0))
const remainingCapacity = computed(() => (soc.value / 100) * nominalCapacity)

function tempLevel(v: number): 'success' | 'warning' | 'danger' {
  return v > 60 ? 'danger' : v > 45 ? 'warning' : 'success'
}
function tempText(v: number): string {
  return v > 60 ? '过高' : v > 45 ? '偏高' : '正常'
}
const temps = computed<TempProbe[]>(() =>
  tempValues.value.map((v, i) => ({ index: i + 1, value: v, level: tempLevel(v), text: tempText(v) })),
)

// ── 电芯柱状图（相对高度；只标最高/最低） ──
const cellDomain = computed(() => {
  const vs = cellValues.value
  const lo = Math.min(...vs)
  const hi = Math.max(...vs)
  const pad = Math.max(0.04, (hi - lo) * 0.6)
  return { lo: lo - pad, hi: hi + pad }
})
function cellBarHeight(v: number): string {
  const { lo, hi } = cellDomain.value
  const pct = ((v - lo) / (hi - lo)) * 100
  const clamped = Math.max(8, Math.min(100, pct))
  return clamped + '%'
}
function cellClass(c: CellVoltage): string {
  if (c.voltage > 3.6 || c.voltage < 2.5) return 'is-danger'
  if (c.index === maxCell.value.index) return 'is-high'
  if (c.index === minCell.value.index) return 'is-low'
  return ''
}
function isExtreme(c: CellVoltage): boolean {
  return c.index === maxCell.value.index || c.index === minCell.value.index
}

// ════════════ 诉求①：MOS 直控 ════════════
const mos = reactive<{ charge: boolean; discharge: boolean }>({ charge: true, discharge: true })
const mosPhase = reactive<Record<'charge' | 'discharge', string>>({ charge: '', discharge: '' })
const mosError = reactive<Record<'charge' | 'discharge', string>>({ charge: '', discharge: '' })

const fetStatus = computed(() => (mos.charge ? 1 : 0) | (mos.discharge ? 2 : 0))
const fetHex = computed(() => fetStatus.value.toString(16).toUpperCase().padStart(2, '0'))
const fetText = computed(() => {
  if (mos.charge && mos.discharge) return '充放导通'
  if (mos.charge) return '仅充电导通'
  if (mos.discharge) return '仅放电导通'
  return '全部关断'
})
const currentDisplay = computed(() => (mos.discharge ? (isAbnormal.value ? -4.85 : -3.42) : 0))
const currentHint = computed(() => {
  if (!mos.discharge) return '已停止输出（放电 MOS 关断）'
  return currentDisplay.value < 0 ? '放电中（电流为负）' : '充电中（电流为正）'
})

const mosRows = computed(() => [
  {
    key: 'charge' as const,
    name: '充电 MOS',
    on: mos.charge,
    phase: mosPhase.charge,
    error: mosError.charge,
    hwOff: isAbnormal.value && !mos.charge,
    desc: mos.charge ? '允许充电 · fet_status bit0 = 1' : '禁止充电 · fet_status bit0 = 0',
  },
  {
    key: 'discharge' as const,
    name: '放电 MOS',
    on: mos.discharge,
    phase: mosPhase.discharge,
    error: mosError.discharge,
    hwOff: false,
    desc: mos.discharge ? '允许放电 · fet_status bit1 = 1' : '禁止放电 · fet_status bit1 = 0',
  },
])

async function requestMosChange(which: 'charge' | 'discharge', target: boolean) {
  if (mosPhase[which] !== '' || mos[which] === target) return
  const name = which === 'charge' ? '充电 MOS' : '放电 MOS'
  const action = target ? '导通' : '关断'
  const effect = which === 'charge'
    ? (target ? '导通后允许充电。' : '关断后禁止充电，充电回路会被切断。')
    : (target ? '导通后允许放电。' : '关断后禁止放电，设备会停止输出。')
  // 风险动作的最小确认：只有 确认 / 取消 两键，不填理由、不加必填项。
  const ok = await feedback.confirmDanger(
    '确认将' + name + '设为「' + action + '」？' + effect + '指令下发约需 1~2 秒，完成后自动回读 fet_status 校验。',
    { title: 'MOS 直控 · ' + name + action, confirmText: '确认' + action, cancelText: '取消' },
  )
  if (!ok) return
  await applyMos(which, target)
}

async function applyMos(which: 'charge' | 'discharge', target: boolean) {
  const name = which === 'charge' ? '充电 MOS' : '放电 MOS'
  const action = target ? '导通' : '关断'
  mosPhase[which] = '下发中'
  await sleep(650)
  if (mosPhase[which] === '') return
  mosPhase[which] = '回读校验中'
  await sleep(650)
  if (mosPhase[which] === '') return
  const blocked = target ? mosBlockReason(which) : ''
  if (blocked) {
    mosPhase[which] = ''
    mosError[which] = blocked
    addHistory('MOS 直控', name + ' ' + action + ' 失败', 'failed', '回读 fet_status 与目标不一致')
    feedback.error(name + action + '失败：' + blocked)
    return
  }
  mos[which] = target
  mosPhase[which] = ''
  mosError[which] = ''
  addHistory('MOS 直控', name + ' ' + action + ' 成功', 'success', '回读 fet_status 一致')
  feedback.success(name + '已' + action)
}

/** 硬件保护覆盖时软件无法导通（后端 FAILED 提示的那个判据）。 */
function mosBlockReason(which: 'charge' | 'discharge'): string {
  if (!isAbnormal.value) return ''
  if (which === 'charge' && protectionActive(0)) {
    return '单体过压保护（bit0）已触发，硬件保护已覆盖充电 MOS，软件无法导通。请先排除过压后再试。'
  }
  if (which === 'discharge' && (protectionActive(9) || protectionActive(10))) {
    return '放电过流 / 短路保护（bit9 / bit10）已触发，硬件保护已覆盖放电 MOS，软件无法导通。'
  }
  return ''
}

// ════════════ 诉求④：保护状态（12 位掩码） ════════════
const PROTECTION_BITS: Array<{ bit: number; label: string }> = [
  { bit: 0, label: '单体过压' },
  { bit: 1, label: '单体欠压' },
  { bit: 2, label: '整组过压' },
  { bit: 3, label: '整组欠压' },
  { bit: 4, label: '充电过温' },
  { bit: 5, label: '充电低温' },
  { bit: 6, label: '放电过温' },
  { bit: 7, label: '放电低温' },
  { bit: 8, label: '充电过流' },
  { bit: 9, label: '放电过流' },
  { bit: 10, label: '短路保护' },
  { bit: 11, label: '前端 IC 错误' },
]
const protectionStatus = computed(() => (isAbnormal.value ? (1 << 0) | (1 << 6) : 0))
const protHex = computed(() => protectionStatus.value.toString(16).toUpperCase().padStart(2, '0'))
const protectExpanded = ref(false)
interface ProtectionBit { bit: number; label: string; active: boolean }
const protectionBits = computed<ProtectionBit[]>(() =>
  PROTECTION_BITS.map((p) => ({ ...p, active: protectionActive(p.bit) })),
)
const activeProtections = computed(() => protectionBits.value.filter((p) => p.active))
function protectionActive(bit: number): boolean {
  return (protectionStatus.value & (1 << bit)) !== 0
}

// ════════════ 诉求②：实时数据流（跟随模式，不抢滚动） ════════════
interface StreamRow {
  id: number
  time: string
  item: string
  value: string
  unit: string
  status: 'normal' | 'warning' | 'danger'
  statusText: string
}
const MAX_STREAM_ROWS = 300
const streamRows = ref<StreamRow[]>([])
const streamEl = ref<HTMLElement | null>(null)
const following = ref(true)
const pendingCount = ref(0)
const injecting = ref(false)
let streamSeq = 0
let feedTimer: ReturnType<typeof setInterval> | null = null
let injectTimer: ReturnType<typeof setInterval> | null = null

function appendRows(rows: StreamRow[]) {
  streamRows.value.push(...rows)
  if (streamRows.value.length > MAX_STREAM_ROWS) {
    streamRows.value.splice(0, streamRows.value.length - MAX_STREAM_ROWS)
  }
  if (following.value) {
    // 只在「跟随中」才动 scrollTop —— 暂停时绝不触碰视口（这正是旧页面的缺陷）。
    void nextTick(() => {
      const el = streamEl.value
      if (el) el.scrollTop = el.scrollHeight
    })
  } else {
    pendingCount.value += rows.length
  }
}

function onStreamScroll() {
  const el = streamEl.value
  if (!el) return
  const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight <= 32
  following.value = nearBottom
  if (nearBottom) pendingCount.value = 0
}

function scrollToLatest() {
  following.value = true
  pendingCount.value = 0
  void nextTick(() => {
    const el = streamEl.value
    if (el) el.scrollTop = el.scrollHeight
  })
}

function clearStream() {
  streamRows.value = []
  pendingCount.value = 0
  following.value = true
}

function pushFrame() {
  const t = nowTime()
  const phase = streamSeq
  const rows: StreamRow[] = []
  const push = (item: string, value: string, unit: string, status: StreamRow['status'], statusText: string) => {
    streamSeq++
    rows.push({ id: streamSeq, time: t, item, value, unit, status, statusText })
  }
  push('总电压', (totalVoltage.value + Math.sin(phase / 5) * 0.03).toFixed(2), 'V', 'normal', '正常')
  push('电流', (currentDisplay.value + Math.sin(phase / 3) * 0.12).toFixed(2), 'A', 'normal', currentDisplay.value < 0 ? '放电' : '充电')
  push('SOC', soc.value.toFixed(1), '%', 'normal', '正常')
  push('MOS 状态', '0x' + fetHex.value, '', 'normal', fetText.value)
  const hi = maxCell.value.voltage
  push('单体最高 #' + maxCell.value.index, hi.toFixed(3), 'V', hi > 3.6 ? 'danger' : 'normal', hi > 3.6 ? '越限' : '正常')
  appendRows(rows)
}

/** 「高频注入」：每秒往列表推 1 条，用于人工验证滚动不被抢。 */
function injectOne() {
  const t = nowTime()
  streamSeq++
  const slot = streamSeq % 4
  let row: Omit<StreamRow, 'id' | 'time'>
  if (slot === 0) {
    const c = cells.value[Math.floor(Math.random() * cells.value.length)]
    row = { item: '单体电压 #' + c.index, value: c.voltage.toFixed(3), unit: 'V', status: c.voltage > 3.6 ? 'danger' : 'normal', statusText: c.voltage > 3.6 ? '越限' : '正常' }
  } else if (slot === 1) {
    const tp = temps.value[Math.floor(Math.random() * temps.value.length)]
    row = { item: '温度 探头' + tp.index, value: tp.value.toFixed(1), unit: '℃', status: tp.level === 'success' ? 'normal' : tp.level, statusText: tp.text }
  } else if (slot === 2) {
    row = { item: '剩余容量', value: remainingCapacity.value.toFixed(2), unit: 'Ah', status: 'normal', statusText: '正常' }
  } else {
    row = { item: '循环次数', value: String(CYCLE_COUNT), unit: '次', status: 'normal', statusText: '正常' }
  }
  appendRows([{ id: streamSeq, time: t, ...row }])
}

function toggleInject() {
  if (injecting.value) {
    stopInject()
    return
  }
  injecting.value = true
  injectTimer = setInterval(injectOne, 1000)
}
function stopInject() {
  if (injectTimer) {
    clearInterval(injectTimer)
    injectTimer = null
  }
  injecting.value = false
}

// ════════════ 诉求③：轮询指令配置（3 条上限） ════════════
interface PollCommand { id: string; name: string; desc: string; interval: number; enabled: boolean; lastInterval: number }
const MAX_POLL = 3
const POLL_INITIAL: Array<Omit<PollCommand, 'lastInterval'>> = [
  { id: 'read_basic_info', name: '读取基本信息', desc: '总压 / 电流 / 容量 / SOC / 温度 / FET', interval: 5000, enabled: true },
  { id: 'read_cell_voltage', name: '读取单体电压', desc: '各串电芯电压', interval: 5000, enabled: true },
  { id: 'read_comprehensive', name: '读取综合信息', desc: '综合信息帧', interval: 1000, enabled: true },
  { id: 'read_hardware_version', name: '读取硬件版本', desc: '硬件 / 固件版本确认', interval: 60000, enabled: false },
  { id: 'read_protection_count', name: '读取保护历史次数', desc: '12 种保护触发次数统计', interval: 10000, enabled: false },
]
const pollCommands = reactive<PollCommand[]>(POLL_INITIAL.map((p) => ({ ...p, lastInterval: p.interval })))
const enabledCount = computed(() => pollCommands.filter((c) => c.enabled).length)
const overLimit = computed(() => enabledCount.value > MAX_POLL)
const excessIds = computed(() => new Set(pollCommands.filter((c) => c.enabled).slice(MAX_POLL).map((c) => c.id)))
const excessNames = computed(() => pollCommands.filter((c) => excessIds.value.has(c.id)).map((c) => '「' + c.name + '」').join('、'))
function isExcess(c: PollCommand): boolean {
  return c.enabled && excessIds.value.has(c.id)
}
const pollBadgeType = computed(() => (overLimit.value ? 'danger' : enabledCount.value === MAX_POLL ? 'warning' : 'success'))

function snapshotPoll(): string {
  return JSON.stringify(pollCommands.map((c) => ({ id: c.id, enabled: c.enabled, interval: c.interval })))
}
const pollBaseline = ref(snapshotPoll())
const pollDirty = computed(() => snapshotPoll() !== pollBaseline.value)
const pollSaveState = ref<'idle' | 'saving' | 'syncing' | 'synced'>('idle')
const pollBusy = computed(() => pollSaveState.value === 'saving' || pollSaveState.value === 'syncing')
const pollHint = computed(() => {
  if (overLimit.value) return '超限 ' + enabledCount.value + '/' + MAX_POLL + ' 条，修正后才能保存'
  switch (pollSaveState.value) {
    case 'saving': return '保存中…'
    case 'syncing': return '已保存，正在同步到节点…'
    case 'synced': return '已同步到节点'
    default: return pollDirty.value ? '有未保存改动' : '当前配置与设备一致'
  }
})

function togglePoll(c: PollCommand, on: boolean) {
  c.enabled = on
  if (on && c.interval <= 0) c.interval = c.lastInterval || 5000
  if (!on && c.interval > 0) c.lastInterval = c.interval
  markPollEdited()
}
function onIntervalChange(c: PollCommand) {
  if (c.interval <= 0) {
    c.interval = 0
    c.enabled = false
  } else {
    c.enabled = true
    c.lastInterval = c.interval
  }
  markPollEdited()
}
function markPollEdited() {
  if (pollSaveState.value === 'synced') pollSaveState.value = 'idle'
}
async function savePoll() {
  if (overLimit.value) return
  pollSaveState.value = 'saving'
  await sleep(400)
  pollSaveState.value = 'syncing'
  await sleep(1100)
  pollSaveState.value = 'synced'
  pollBaseline.value = snapshotPoll()
  addHistory('配置更新', '轮询指令启用 ' + enabledCount.value + '/' + MAX_POLL + ' 条', 'success', '节点已确认应用')
  feedback.success('轮询配置已保存并同步到节点')
}

// ════════════ 受控操作（26 项，分组） ════════════
interface OpDef { id: string; name: string; risk: boolean; note?: string; movedTo?: boolean }
interface OpGroup { id: string; name: string; hint: string; icon: Component; ops: OpDef[] }
type OpPhase = 'idle' | 'sent' | 'running' | 'verifying' | 'success' | 'failed' | 'unknown' | 'cancelled'
interface OpState { phase: OpPhase; text: string; reason: string }

const opGroups: OpGroup[] = [
  {
    id: 'read', name: '读取信息', hint: '一次性读取，不改设备配置', icon: Document,
    ops: [
      { id: 'read_basic_info', name: '读取基本信息', risk: false, note: '总压 / 电流 / 容量 / SOC / 温度 / FET' },
      { id: 'read_cell_voltage', name: '读取单体电压', risk: false, note: '各串电芯电压' },
      { id: 'read_hardware_version', name: '读取硬件版本', risk: false },
      { id: 'read_comprehensive', name: '读取综合信息', risk: false },
      { id: 'read_protection_count', name: '读取保护历史次数', risk: false, note: '12 种保护触发次数统计' },
      { id: 'read_custom_attributes', name: '读取自定义属性', risk: false, note: 'F0 读 3 个 uint16' },
      { id: 'read_test_mos_status', name: '读取 MOS 测试状态', risk: false, note: '0C 读' },
    ],
  },
  {
    id: 'diag', name: '诊断与测试', hint: '不写入参数，用于现场排障', icon: Tools,
    ops: [
      { id: 'find_car', name: '寻车（蜂鸣器）', risk: false, note: 'F1 开 / 关，最长 30s 自动关闭' },
      { id: 'test_charge_mos', name: '测试充电 MOS', risk: false },
      { id: 'test_discharge_mos', name: '测试放电 MOS', risk: false },
    ],
  },
  {
    id: 'runtime', name: '运行控制', hint: '直接影响设备当前运行状态', icon: Operation,
    ops: [
      { id: 'set_mos_policy', name: '设置充放电 MOS 软件策略', risk: true, movedTo: true, note: '已改为页首「MOS 直控」直接开关（去掉表单与多余必填，只保留最小风险确认）。' },
      { id: 'force_balance', name: '强制均衡', risk: true, note: 'F5 + 回读 0x03 均衡态' },
      { id: 'clear_alarm', name: '清除告警状态', risk: true, note: 'E6 固定数据' },
      { id: 'auto_test_edv', name: '自动测试 EDV', risk: true, note: '0D 设置静止时间，需 SOC > 95%' },
      { id: 'bms_restart', name: '重启 BMS', risk: true, note: '需观察离线窗口，不能以 ACK 判成功' },
    ],
  },
  {
    id: 'param', name: '参数与标定', hint: '读取 / 写入厂内参数，需进厂模式', icon: SetUp,
    ops: [
      { id: 'read_protection_parameters', name: '读取 BMS 保护参数', risk: true, note: '需进厂模式' },
      { id: 'read_system_parameters', name: '读取 BMS 系统参数', risk: true, note: '需进厂模式' },
      { id: 'write_protection_parameters', name: '写入 BMS 保护参数', risk: true },
      { id: 'write_system_parameters', name: '写入 BMS 系统参数', risk: true },
      { id: 'write_custom_attributes', name: '写入自定义属性', risk: true },
      { id: 'write_internal_resistance', name: '写入电芯内阻', risk: true },
      { id: 'write_sn', name: '写入 SN 码', risk: true },
    ],
  },
  {
    id: 'timer', name: '时间与上报设置', hint: '改变设备行为参数', icon: Timer,
    ops: [
      { id: 'set_static_correction_time', name: '设置静态修正时间', risk: true },
      { id: 'set_report_interval', name: '设置上报时间间隔', risk: true },
      { id: 'set_charge_time_window', name: '设置充电时间窗', risk: true },
      { id: 'set_discharge_time_limit', name: '设置放电时限', risk: true },
    ],
  },
]
const totalOps = computed(() => opGroups.reduce((n, g) => n + g.ops.length, 0))
const openGroups = reactive<Record<string, boolean>>({ runtime: true })
function toggleGroup(id: string) {
  openGroups[id] = !openGroups[id]
}
function riskCount(g: OpGroup): number {
  return g.ops.filter((o) => o.risk).length
}

const opStates = reactive<Record<string, OpState>>({})
// 预初始化：render 期间不写响应式状态（避免渲染中改 dep 触发递归更新）。
opGroups.forEach((g) => {
  g.ops.forEach((o) => {
    opStates[o.id] = { phase: 'idle', text: '', reason: '' }
  })
})
function opState(id: string): OpState {
  const existing = opStates[id]
  if (existing) return existing
  const created: OpState = { phase: 'idle', text: '', reason: '' }
  opStates[id] = created
  return created
}
function setPhase(id: string, phase: OpPhase, text: string) {
  const st = opState(id)
  st.phase = phase
  st.text = text
}
function opBusy(id: string): boolean {
  const p = opState(id).phase
  return p === 'sent' || p === 'running' || p === 'verifying'
}
function stateDot(phase: OpPhase): string {
  switch (phase) {
    case 'success': return 'dot-green'
    case 'failed': return 'dot-red'
    case 'unknown': return 'dot-orange'
    default: return 'dot-blue'
  }
}

/** 演示用：某些操作会失败 / 结果未知，用于评审状态文案与常驻原因。 */
const OP_FAIL: Record<string, string> = {
  write_protection_parameters: '设备未进入厂模式，写入被拒绝。请先执行「读取 BMS 保护参数」并确认已进厂模式。',
}

async function runOp(op: OpDef) {
  if (op.movedTo) return
  if (op.risk) {
    const ok = await feedback.confirmDanger(
      '确认执行「' + op.name + '」？该操作会影响设备运行参数，执行后自动回读校验。',
      { title: '受控操作 · ' + op.name, confirmText: '确认执行', cancelText: '取消' },
    )
    if (!ok) return
  }
  const st = opState(op.id)
  st.reason = ''
  setPhase(op.id, 'sent', '已发送')
  await sleep(450)
  setPhase(op.id, 'running', '执行中')
  await sleep(550)
  setPhase(op.id, 'verifying', '校验中')
  await sleep(650)

  if (op.id === 'bms_restart') {
    setPhase(op.id, 'unknown', '结果未知')
    st.reason = '重启期间设备离线，无法以 ACK 判定成功；请等待设备重新上线后在设备列表确认。'
    addHistory('受控操作', op.name, 'unknown', '已下发重启指令，等待设备重新上线')
    feedback.warning('「' + op.name + '」结果未知：等待设备重新上线')
    return
  }
  const failReason = OP_FAIL[op.id]
  if (failReason) {
    setPhase(op.id, 'failed', '失败')
    st.reason = failReason
    addHistory('受控操作', op.name, 'failed', failReason)
    feedback.error('「' + op.name + '」失败：' + failReason)
    return
  }
  setPhase(op.id, 'success', '成功')
  addHistory('受控操作', op.name, 'success', '执行成功，回读校验一致')
  feedback.success('「' + op.name + '」执行成功')
}

function scrollToMos() {
  const el = document.querySelector('.mos-card')
  if (el) el.scrollIntoView({ behavior: 'smooth', block: 'start' })
}

// ════════════ 操作历史（审计线索） ════════════
interface HistoryRow { id: number; time: string; type: string; content: string; result: 'success' | 'failed' | 'unknown' }
const HISTORY_INITIAL: HistoryRow[] = [
  { id: 1, time: '2026-10-15 15:12:07', type: 'MOS 直控', content: '放电 MOS 导通 成功 — 回读 fet_status 一致', result: 'success' },
  { id: 2, time: '2026-10-15 14:58:44', type: '配置更新', content: '轮询指令启用 3/' + MAX_POLL + ' 条 — 节点已确认应用', result: 'success' },
  { id: 3, time: '2026-10-15 14:31:20', type: '受控操作', content: '读取 BMS 保护参数 — 设备未进入厂模式，读取被拒绝', result: 'failed' },
  { id: 4, time: '2026-10-15 13:47:02', type: '受控操作', content: '重启 BMS — 已下发，等待设备重新上线', result: 'unknown' },
]
const history = ref<HistoryRow[]>(HISTORY_INITIAL.map((h) => ({ ...h })))
let historySeq = HISTORY_INITIAL.length
function addHistory(type: string, content: string, result: HistoryRow['result'], detail?: string) {
  historySeq++
  history.value.unshift({ id: historySeq, time: nowStamp(), type, content: detail ? content + ' — ' + detail : content, result })
  if (history.value.length > 30) history.value.splice(30)
}
function resultTag(r: HistoryRow['result']): 'success' | 'danger' | 'warning' {
  return r === 'success' ? 'success' : r === 'failed' ? 'danger' : 'warning'
}
function resultText(r: HistoryRow['result']): string {
  return r === 'success' ? '成功' : r === 'failed' ? '失败' : '结果未知'
}

// ════════════ 演示态切换 / 重置 ════════════
watch(mode, (m) => {
  // 异常态：单体过压会把充电 MOS 硬件关断，这里同步 mock 出该表现。
  mos.charge = m !== 'abnormal'
  mos.discharge = true
  mosPhase.charge = ''
  mosPhase.discharge = ''
  mosError.charge = ''
  mosError.discharge = ''
  protectExpanded.value = false
})

function resetDemo() {
  mode.value = 'normal'
  mos.charge = true
  mos.discharge = true
  mosPhase.charge = ''
  mosPhase.discharge = ''
  mosError.charge = ''
  mosError.discharge = ''
  streamRows.value = []
  pendingCount.value = 0
  following.value = true
  stopInject()
  Object.keys(opStates).forEach((k) => {
    opStates[k] = { phase: 'idle', text: '', reason: '' }
  })
  pollCommands.forEach((c, i) => {
    c.enabled = POLL_INITIAL[i].enabled
    c.interval = POLL_INITIAL[i].interval
    c.lastInterval = POLL_INITIAL[i].interval
  })
  pollSaveState.value = 'idle'
  pollBaseline.value = snapshotPoll()
  history.value = HISTORY_INITIAL.map((h) => ({ ...h }))
  historySeq = HISTORY_INITIAL.length
  feedback.info('已重置为初始 mock 数据')
}

onMounted(() => {
  pushFrame()
  feedTimer = setInterval(pushFrame, 2500)
  void nextTick(() => {
    const el = streamEl.value
    if (el) el.scrollTop = el.scrollHeight
  })
})

onUnmounted(() => {
  if (feedTimer) clearInterval(feedTimer)
  stopInject()
})
</script>

<style scoped>
/* ══════════ 页面骨架 ══════════ */
.bms-demo2 {
  min-height: 100vh;
  background: var(--el-bg-color-page);
  color: var(--el-text-color-primary);
  font-size: 13px;
  padding-bottom: 24px;
}
.dev-watermark {
  padding: 4px 12px;
  background: repeating-linear-gradient(45deg, #b45309, #b45309 12px, #d97706 12px, #d97706 24px);
  color: #fff;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.5px;
  text-align: center;
}

.page-head {
  background: var(--el-bg-color);
  border-bottom: 1px solid var(--el-border-color);
  padding: 12px 24px 16px;
}
.crumb {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--el-text-color-secondary);
  font-size: 12px;
  margin-bottom: 10px;
}
.crumb-link { cursor: pointer; }
.crumb-link:hover { color: var(--el-color-primary); }
.crumb-current { color: var(--el-text-color-primary); }
.sep { color: var(--el-border-color); }
.head-main { display: flex; align-items: flex-end; gap: 16px; flex-wrap: wrap; }
.dev-ident { min-width: 0; }
.dev-name-line { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.dev-name { margin: 0; font-size: 18px; font-weight: 600; color: var(--el-text-color-primary); }
.dev-sub { margin-top: 6px; font-size: 12px; color: var(--el-text-color-secondary); }
.dev-sub b { color: var(--el-text-color-primary); }
.head-actions { margin-left: auto; display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.mode-switch { display: flex; align-items: center; gap: 8px; }
.mode-label { font-size: 12px; color: var(--el-text-color-secondary); }

.page-body { max-width: 1480px; margin: 0 auto; padding: 16px 24px; }
.row { display: grid; gap: 16px; margin-bottom: 16px; }
/* align-items:start —— 避免旧页面「等高拉伸出大片空白」的同类结构 */
.metrics { grid-template-columns: repeat(4, minmax(0, 1fr)); }
.mos-protect { grid-template-columns: minmax(0, 5fr) minmax(0, 7fr); align-items: start; }
.cell-temp { grid-template-columns: minmax(0, 7fr) minmax(0, 5fr); align-items: start; }
.stream-poll { grid-template-columns: minmax(0, 7fr) minmax(0, 5fr); align-items: start; }
.ops-history { grid-template-columns: minmax(0, 8fr) minmax(0, 4fr); align-items: start; }

/* ══════════ 卡片语言（沿用 .card / card-head） ══════════ */
.card {
  background: var(--card-bg);
  border: 1px solid var(--card-border);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-sm);
  padding: 16px 20px;
  min-width: 0;
}
.card-head { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; margin-bottom: 10px; }
.card-title { font-weight: 600; font-size: 14px; color: var(--el-text-color-primary); }
.card-hint { font-size: 12px; color: var(--el-text-color-secondary); }
.head-right { margin-left: auto; display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.card-note { margin: 0 0 12px; font-size: 12px; line-height: 1.7; color: var(--el-text-color-secondary); }
.card-note code { font-size: 11px; background: var(--el-fill-color-light); border-radius: 3px; padding: 1px 5px; }
.card-note b { color: var(--el-text-color-primary); }
.card-footnote { margin: 12px 0 0; font-size: 12px; line-height: 1.7; color: var(--el-text-color-secondary); }
.card-footnote b { color: var(--el-text-color-primary); }
.mono-badge { font-family: 'SF Mono', Consolas, Monaco, monospace; font-size: 11px; color: var(--el-text-color-secondary); background: var(--el-fill-color-light); border-radius: 4px; padding: 2px 8px; }
.alert-detail { margin-top: 4px; font-size: 12px; line-height: 1.6; }
.inline-alert { margin: 8px 0; }
.txt-warning { color: var(--el-color-warning); }
.txt-danger { color: var(--el-color-danger); }
.txt-secondary { color: var(--el-text-color-regular); }
.link-btn { border: none; background: none; padding: 0; margin-left: auto; color: var(--el-color-primary); cursor: pointer; font-size: 12px; }
.link-btn:hover { text-decoration: underline; }

/* ══════════ MOS 直控 ══════════ */
.mos-item { border-top: 1px solid var(--el-border-color-lighter); padding-top: 8px; margin-top: 8px; }
.mos-item:first-of-type { border-top: none; margin-top: 0; padding-top: 0; }
.mos-row { display: flex; align-items: center; gap: 12px; min-height: 48px; }
.mos-row.is-applying { opacity: 0.72; }
.mos-icon { display: flex; align-items: center; justify-content: center; width: 34px; height: 34px; border-radius: var(--radius-sm); background: var(--el-fill-color-light); flex-shrink: 0; }
.mos-icon.is-on { color: var(--el-color-success); }
.mos-icon.is-off { color: var(--el-text-color-secondary); }
.mos-main { flex: 1; min-width: 0; }
.mos-name-line { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.mos-name { font-size: 14px; font-weight: 600; }
.mos-desc { margin-top: 3px; font-size: 12px; color: var(--el-text-color-secondary); }
.mos-switch { flex-shrink: 0; }
.hw-off-note { display: inline-flex; align-items: center; gap: 4px; font-size: 11px; color: var(--el-color-warning); }

/* ══════════ 保护状态 ══════════ */
.protect-compact { display: flex; align-items: center; gap: 8px; padding: 6px 0; flex-wrap: wrap; }
.protect-compact-text { font-size: 13px; font-weight: 500; }
.protect-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 8px; margin-top: 8px; }
.protect-item {
  display: flex; align-items: center; gap: 6px; padding: 8px 10px;
  background: var(--el-fill-color-lighter); border: 1px solid transparent;
  border-radius: var(--radius-sm); min-width: 0;
}
.protect-item.active { background: var(--el-color-danger-light-9); border-color: var(--el-color-danger-light-5); }
.protect-name { font-size: 12px; flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.protect-bit { font-size: 11px; color: var(--el-text-color-secondary); font-family: 'SF Mono', Consolas, Monaco, monospace; }

/* ══════════ 电芯电压分布 ══════════ */
.cell-summary { font-size: 12px; color: var(--el-text-color-secondary); }
.cell-chart-scroll { overflow-x: auto; }
.cell-chart { display: flex; align-items: flex-end; gap: 6px; height: 190px; min-width: 520px; padding-top: 20px; }
.cell-col { flex: 1; height: 100%; display: flex; flex-direction: column; align-items: center; gap: 4px; justify-content: flex-end; min-width: 0; }
.cell-value { font-size: 10px; line-height: 14px; height: 14px; color: var(--el-text-color-secondary); opacity: 0; white-space: nowrap; }
.cell-value.show { opacity: 1; }
.cell-bar-track { position: relative; width: 100%; max-width: 26px; flex: 1; min-height: 40px; background: var(--el-fill-color-light); border-radius: 4px 4px 0 0; }
.cell-bar { position: absolute; left: 0; right: 0; bottom: 0; border-radius: 4px 4px 0 0; background: var(--el-color-primary); transition: height 0.3s ease; }
.cell-idx { font-size: 10px; color: var(--el-text-color-secondary); }
.cell-col.is-high .cell-bar { background: var(--el-color-warning); }
.cell-col.is-low .cell-bar { background: var(--el-color-primary-light-3); }
.cell-col.is-danger .cell-bar { background: var(--el-color-danger); }
.cell-col.is-high .cell-value { color: var(--el-color-warning); }
.cell-col.is-low .cell-value { color: var(--el-color-primary); }
.cell-col.is-danger .cell-value { color: var(--el-color-danger); }
.cell-legend { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; margin-top: 10px; font-size: 12px; color: var(--el-text-color-secondary); }
.lg { display: inline-flex; align-items: center; gap: 5px; }
.swatch { width: 10px; height: 10px; border-radius: 2px; display: inline-block; }
.swatch-normal { background: var(--el-color-primary); }
.swatch-high { background: var(--el-color-warning); }
.swatch-low { background: var(--el-color-primary-light-3); }
.swatch-danger { background: var(--el-color-danger); }
.cell-thresholds { margin-left: auto; }

/* ══════════ 温度 ══════════ */
.temp-list { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; }
.temp-item { display: flex; align-items: center; gap: 8px; padding: 10px 12px; background: var(--el-fill-color-lighter); border-radius: var(--radius-sm); min-width: 0; }
.temp-label { font-size: 12px; color: var(--el-text-color-regular); white-space: nowrap; }
.temp-value { font-size: 16px; font-weight: 600; margin-left: auto; white-space: nowrap; }
.temp-value.is-success { color: var(--el-color-success); }
.temp-value.is-warning { color: var(--el-color-warning); }
.temp-value.is-danger { color: var(--el-color-danger); }

/* ══════════ 实时数据流 ══════════ */
.stream-actions { gap: 8px; }
.stream-box { position: relative; }
.stream-scroll {
  height: 320px; overflow: auto;
  border: 1px solid var(--el-border-color-lighter); border-radius: var(--radius-sm);
  background: var(--el-fill-color-lighter);
}
.stream-row {
  display: grid; grid-template-columns: 76px minmax(120px, 1fr) 84px 40px 64px;
  gap: 8px; align-items: center; padding: 6px 12px; min-width: 420px;
  border-bottom: 1px solid var(--el-border-color-lighter); font-size: 12px;
}
.stream-row:last-child { border-bottom: none; }
.s-time { font-family: 'SF Mono', Consolas, Monaco, monospace; color: var(--el-text-color-secondary); }
.s-item { color: var(--el-text-color-regular); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.s-value { font-variant-numeric: tabular-nums; text-align: right; color: var(--el-text-color-primary); font-weight: 600; }
.s-unit { color: var(--el-text-color-secondary); }
.s-status { text-align: right; }
.s-status.is-normal { color: var(--el-color-success); }
.s-status.is-warning { color: var(--el-color-warning); }
.s-status.is-danger { color: var(--el-color-danger); }
.stream-empty { padding: 40px 12px; text-align: center; color: var(--el-text-color-secondary); font-size: 12px; }
.back-latest {
  position: absolute; right: 14px; bottom: 14px; z-index: 2;
  display: inline-flex; align-items: center; gap: 6px;
  padding: 6px 14px; border: 1px solid var(--el-color-primary); border-radius: 999px;
  background: var(--el-color-primary); color: #fff; font-size: 12px; cursor: pointer;
  box-shadow: var(--shadow-md);
}
.back-latest:hover { background: var(--el-color-primary-light-3); }
.fade-enter-active, .fade-leave-active { transition: opacity 0.18s; }
.fade-enter-from, .fade-leave-to { opacity: 0; }

/* ══════════ 轮询配置 ══════════ */
.poll-list { display: flex; flex-direction: column; }
.poll-row { display: flex; align-items: center; gap: 12px; padding: 10px 0; border-bottom: 1px solid var(--el-border-color-lighter); flex-wrap: wrap; }
.poll-row:last-child { border-bottom: none; }
.poll-row.is-over { background: var(--el-color-warning-light-9); border-radius: var(--radius-sm); padding-left: 8px; padding-right: 8px; }
.poll-main { flex: 1; min-width: 180px; }
.poll-name-line { display: flex; align-items: center; gap: 6px; }
.poll-name { font-size: 13px; font-weight: 600; }
.poll-desc { font-size: 11px; color: var(--el-text-color-secondary); margin-top: 2px; }
.poll-controls { display: flex; align-items: center; gap: 8px; flex-shrink: 0; }
.poll-interval { width: 118px; }
.poll-unit { font-size: 11px; color: var(--el-text-color-secondary); }
.poll-foot { display: flex; align-items: center; gap: 12px; margin-top: 12px; flex-wrap: wrap; }
.poll-sync { font-size: 12px; color: var(--el-text-color-secondary); }
.poll-sync.is-ok { color: var(--el-color-success); }
.poll-sync.is-warn { color: var(--el-color-warning); }

/* ══════════ 受控操作 ══════════ */
.state-legend { font-size: 12px; color: var(--el-text-color-secondary); }
.op-groups { display: flex; flex-direction: column; gap: 8px; }
.op-group { border: 1px solid var(--el-border-color-lighter); border-radius: var(--radius-sm); overflow: hidden; }
.op-group-head {
  display: flex; align-items: center; gap: 8px; width: 100%; min-height: 44px;
  padding: 8px 12px; border: none; background: var(--el-fill-color-lighter);
  cursor: pointer; text-align: left; font-size: 13px; color: var(--el-text-color-primary);
}
.op-group-head:hover { background: var(--el-fill-color-light); }
.op-caret { color: var(--el-text-color-secondary); transition: transform 0.2s; }
.op-caret.open { transform: rotate(90deg); }
.op-group-icon { color: var(--el-color-primary); }
.op-group-name { font-weight: 600; }
.op-group-count { font-size: 12px; color: var(--el-text-color-secondary); }
.op-group-hint { font-size: 12px; color: var(--el-text-color-secondary); margin-left: auto; }
.op-list { display: flex; flex-direction: column; }
.op-item { display: flex; align-items: center; gap: 12px; padding: 10px 12px; border-top: 1px solid var(--el-border-color-lighter); flex-wrap: wrap; }
.op-info { flex: 1; min-width: 220px; }
.op-name-line { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.op-name { font-size: 13px; font-weight: 500; }
.op-desc { font-size: 12px; color: var(--el-text-color-secondary); margin-top: 3px; line-height: 1.6; }
.op-right { display: flex; align-items: center; gap: 10px; flex-shrink: 0; }
.op-state { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; color: var(--el-text-color-regular); }
.op-reason { flex-basis: 100%; margin: 4px 0 0; }
.dot { display: inline-block; width: 6px; height: 6px; border-radius: 50%; }
.dot-green { background: var(--el-color-success); }
.dot-red { background: var(--el-color-danger); }
.dot-orange { background: var(--el-color-warning); }
.dot-blue { background: var(--el-color-primary); }

/* ══════════ 操作历史 ══════════ */
.history-list { display: flex; flex-direction: column; gap: 8px; max-height: 520px; overflow-y: auto; }
.history-item { padding: 10px 12px; background: var(--el-fill-color-lighter); border-radius: var(--radius-sm); }
.h-top { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.h-type { font-size: 12px; font-weight: 600; }
.h-content { font-size: 12px; color: var(--el-text-color-regular); margin-top: 4px; line-height: 1.6; word-break: break-word; }
.h-meta { font-size: 11px; color: var(--el-text-color-secondary); margin-top: 4px; }

/* ══════════ 响应式 ══════════ */
@media (max-width: 1200px) {
  .metrics { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .mos-protect, .cell-temp, .stream-poll, .ops-history { grid-template-columns: minmax(0, 1fr); }
}
@media (max-width: 768px) {
  .page-head { padding: 12px 12px 14px; }
  .page-body { padding: 12px; }
  .head-actions { margin-left: 0; }
  .card { padding: 14px 12px; }
  .protect-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .temp-list { grid-template-columns: minmax(0, 1fr); }
  .head-right, .stream-actions { margin-left: 0; }
  .cell-thresholds { margin-left: 0; }
  .page-body .el-button:not(.is-circle) { min-height: 44px; }
  .mos-switch, .op-group-head, .back-latest { min-height: 44px; }
  .poll-row { align-items: flex-start; }
  .poll-controls { width: 100%; }
  .poll-interval { flex: 1; width: auto; }
}
@media (pointer: coarse) {
  .mos-switch, .op-group-head, .back-latest { min-height: 44px; }
  .page-body .el-button:not(.is-circle) { min-height: 44px; }
}
</style>
