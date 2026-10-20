<!--
  ══════════════════════════════════════════════════════════════════════════════
  BMS 设备详情页 · 移动端改版视觉稿（A / B / C 三方案对比）
  ══════════════════════════════════════════════════════════════════════════════

  ⚠ 本文件是 **DEMO（视觉稿）**，不是生产页：
    · 全部数据来自 ./mobile-redesign/mockBms.ts 的静态 mock，不接任何 API
    · 路由 /dev/mobile-redesign 由 import.meta.env.DEV 门禁，生产构建静态消除
    · 不修改 src/ 下任何既有文件；三个方案共用同一套零件，切换只换布局编排

  ── 三个方案的核心区别（一句话版）──────────────────────────────────────────
    A 密度优先：一屏尽量装完「状态 + 配置」，图表全部 sparkline 化，信息密度最大化
    B 可读性优先：每块只讲一件事，图表给足高度与标签，用折叠分层而不是压缩
    C 平衡态（推荐）：首屏=结论（指标/保护/MOS），次屏=诊断图表，末屏=配置与操作

  ── 用户六条批评的落点（每方案都覆盖，只是激进程度不同）────────────────────
    ① 没利用满空间 → 页面 padding 8px（生产 20px）、卡片 padding 10px（生产 16/20px），
       卡宽 377/393 = 96%（生产 321/393 = 82%）
    ② 横向溢出 → 所有横向排列项都用 minmax(0,1fr) + min-width:0；
       元信息 chips 用横向滚动容器（不折行、不撑宽）
    ③ 顶部重复 → 页头只留 名称 + 在线 + 刷新 + ⋯；元信息收进一行可横滑 chips，
       不再出现「面包屑 / 标题 / 副行」三处重复设备身份
    ④ UI 不规范 → 开发注释文案全部删除或收进提示；状态一律「文字 + 颜色」双通道
    ⑤ 轮询配置空间利用差 → 输入框 104px（生产 234px），并提供 chips 快捷档位；
       新增间隔条形图让"谁刷得快"一眼可见
    ⑥ 移动端用满两侧 → 同上 ①

  ── 新增的统计类图表（不是旧图换皮，都是新信息）────────────────────────────
    · 温度趋势（3 路随时间）—— 能看出"某一路单独爬升"
    · SOC / 电流趋势 —— 能看出掉电速度与充电是否真的灌进去
    · 保护触发历史统计 —— 当前位掩码为 0 不代表历史上没触发过
    · 轮询间隔条形 —— 把 5 个毫秒数换算成"谁刷得快"
    · 电芯电压 spark + 温度刻度条 —— 截面数据的紧凑可视化
-->
<template>
  <div class="demo-root">
    <!-- ═══════════ 评审工具条（仅 demo，不是被评审的页面本身） ═══════════ -->
    <div class="demo-toolbar">
      <div class="toolbar-row">
        <el-radio-group v-model="scheme" size="small" class="scheme-switch" aria-label="选择设计方案">
          <el-radio-button value="A">A 密度</el-radio-button>
          <el-radio-button value="B">B 可读</el-radio-button>
          <el-radio-button value="C">C 平衡</el-radio-button>
        </el-radio-group>
        <span class="demo-badge">DEV DEMO · mock</span>
      </div>
      <div class="toolbar-idea">
        <strong>{{ currentScheme.name }}</strong>
        <span>{{ currentScheme.idea }}</span>
      </div>
    </div>

    <!-- ═══════════ 被评审的「手机页面」 ═══════════ -->
    <div class="phone">

      <!-- ─────────── 页头 ─────────── -->
      <DeviceHead
        :variant="currentScheme.head"
        :poll-enabled="pollEnabledCount"
        :poll-limit="POLL_LIMIT"
        @refresh="refreshTicks++"
        @open-info="infoSheetOpen = true"
        @command="onHeadCommand"
      />

      <main class="page-body">

        <!-- ─────────── 指标卡 ─────────── -->
        <MetricTiles :variant="currentScheme.tiles" />

        <!--
          ═══════════ 方案 A：密度优先 ═══════════
          首屏目标：指标 + 保护 + MOS + 电芯 + 温度 + 轮询全部落在 875px 内。
          手段：指标改单行四列；保护用 12 格 heat 条；MOS 压成两格一行；
               电芯/温度用最小高度图表；轮询用快捷档位（无输入框）。
        -->
        <template v-if="scheme === 'A'">
          <!-- 保护 + MOS 合并成一张「运行状态」卡：两块都是"现在能不能充放"的判断依据 -->
          <section class="card">
            <div class="card-head">
              <span class="card-title">运行状态</span>
              <span class="card-sub">保护 · MOS</span>
            </div>
            <ProtectionMatrix :bits="PROTECTION_BITS" mode="heat" :status-hex="PROTECTION_STATUS_HEX" :show-detail-toggle="true" />
            <div class="divider"></div>
            <MosDirect :switches="mos" :fet-hex="fetHex" variant="strip" @toggle="toggleMos" />
          </section>

          <!-- 电芯电压：spark 压缩到 64px，极值用正文承当 -->
          <section class="card">
            <div class="card-head">
              <span class="card-title">电芯电压</span>
              <span class="card-metric">
                <b>{{ CELL_STATS.max.toFixed(3) }}</b> / <b>{{ CELL_STATS.min.toFixed(3) }}</b> V
                · 压差 {{ CELL_STATS.diffMv }} mV
              </span>
            </div>
            <CellSparkChart :values="CELL_VOLTAGES" height="64px" />
          </section>

          <!-- 温度：刻度条压到 84px -->
          <section class="card">
            <div class="card-head">
              <span class="card-title">温度</span>
              <span class="card-metric">3 路 · 最高 <b>{{ maxTemp.toFixed(1) }}</b> ℃</span>
            </div>
            <!-- 密度档：20~36℃ 放大显示，三路差异可读；量程写在卡头 -->
            <TempScaleChart
              :labels="TEMP_LABELS"
              :values="TEMP_VALUES"
              height="84px"
              :scale-min="20"
              :scale-max="36"
              :warn="TEMP_THRESHOLDS.warn"
              :danger="TEMP_THRESHOLDS.danger"
            />
          </section>

          <!--
            轮询：条形行（图 + 编辑器合并）。
            初版是「ECharts 条形图 + 指令列表」两块，导致同一批指令名出现两次，
            用户需在两处定位同一行。合并后每行同时是：状态 / 条形量 / 快捷档位。
          -->
          <section class="card">
            <div class="card-head">
              <span class="card-title">轮询指令</span>
              <span class="card-badge" :class="pollOverLimit ? 'is-danger' : 'is-success'">
                {{ pollEnabledCount }}/{{ POLL_LIMIT }} 已启用
              </span>
            </div>
            <PollBarRows
              :commands="pollCommands"
              :limit="POLL_LIMIT"
              @interval="setPollInterval"
              @toggle="togglePoll"
            />
          </section>

          <!-- 趋势：温度（左轴 ℃）+ SOC（右轴 %），双轴各带单位 -->
          <section class="card">
            <div class="card-head">
              <span class="card-title">温度与电量趋势</span>
              <span class="card-metric">近 12 小时</span>
            </div>
            <TrendChart
              :categories="TREND_TIMES"
              :series="trendDualSeries"
              height="118px"
              :show-legend="true"
              :decimals="1"
              :y-min="24"
              :y-max="32"
              :right-min="0"
              :right-max="16"
            />
          </section>

          <section class="card">
            <div class="card-head">
              <span class="card-title">保护历史</span>
              <span class="card-metric">{{ PROTECTION_HISTORY_WINDOW }} <b>{{ PROTECTION_HISTORY_TOTAL }}</b> 次</span>
            </div>
            <ProtectionHistoryChart
              :items="PROTECTION_HISTORY"
              :window-text="PROTECTION_HISTORY_WINDOW"
            />
          </section>

          <section class="card">
            <ControlledOps :groups="OP_GROUPS" :total="OP_TOTAL" @run="onRunOp" />
          </section>
        </template>

        <!--
          ═══════════ 方案 B：可读性优先 ═══════════
          每块只讲一件事：保护状态与保护历史拆成两张卡，趋势图给足 168px，
          电芯电压给 100px 并保留极值标签。轮询保留精确输入框（收窄到 104px）。
        -->
        <template v-else-if="scheme === 'B'">
          <section class="card">
            <div class="card-head">
              <span class="card-title">保护状态</span>
              <span class="card-badge" :class="protectAnyActive ? 'is-danger' : 'is-success'">
                {{ protectAnyActive ? '有触发' : '全部正常' }}
              </span>
            </div>
            <ProtectionMatrix :bits="PROTECTION_BITS" mode="cells-3" :status-hex="PROTECTION_STATUS_HEX" :show-detail-toggle="false" />
          </section>

          <section class="card">
            <div class="card-head">
              <span class="card-title">保护触发历史</span>
              <span class="card-metric">{{ PROTECTION_HISTORY_WINDOW }}</span>
            </div>
            <ProtectionHistoryChart
              :items="PROTECTION_HISTORY"
              :window-text="PROTECTION_HISTORY_WINDOW"
            />
            <p class="card-note">当前 12 项均未触发，但近 30 天有 {{ PROTECTION_HISTORY_TOTAL }} 次历史触发 —— 只看当前位掩码会漏掉。</p>
          </section>

          <section class="card">
            <div class="card-head">
              <span class="card-title">MOS 直控</span>
            </div>
            <MosDirect :switches="mos" :fet-hex="fetHex" variant="row" :show-title="false" @toggle="toggleMos" />
          </section>

          <section class="card">
            <div class="card-head">
              <span class="card-title">电芯电压</span>
              <span class="card-metric">
                最高 <b>{{ CELL_STATS.max.toFixed(3) }}</b> · 最低 <b>{{ CELL_STATS.min.toFixed(3) }}</b> V
                · 压差 <b>{{ CELL_STATS.diffMv }}</b> mV
              </span>
            </div>
            <CellSparkChart :values="CELL_VOLTAGES" height="104px" :label-min-cell-width="40" />
            <p class="card-note">{{ DEVICE.cellCount }} 串 · 均值 {{ CELL_STATS.avg.toFixed(3) }} V · 阈值 3.000 / 3.600 V</p>
          </section>

          <section class="card">
            <div class="card-head">
              <span class="card-title">温度探头</span>
              <span class="card-metric">3 路 · 告警线 {{ TEMP_THRESHOLDS.warn }} ℃</span>
            </div>
            <TempScaleChart
              :labels="TEMP_LABELS"
              :values="TEMP_VALUES"
              height="116px"
              :warn="TEMP_THRESHOLDS.warn"
              :danger="TEMP_THRESHOLDS.danger"
            />
          </section>

          <section class="card">
            <div class="card-head">
              <span class="card-title">温度趋势</span>
              <span class="card-metric">近 12 小时 · ℃</span>
            </div>
            <TrendChart
              :categories="TREND_TIMES"
              :series="tempTrendSeries"
              unit="℃"
              height="168px"
              :show-legend="true"
              :decimals="1"
              :y-min="20"
              :y-max="36"
            />
          </section>

          <section class="card">
            <div class="card-head">
              <span class="card-title">SOC 与电流</span>
              <span class="card-metric">近 12 小时</span>
            </div>
            <TrendChart
              :categories="TREND_TIMES"
              :series="socTrendSeries"
              unit="%"
              height="140px"
              :show-legend="true"
              :decimals="1"
              :y-min="0"
              :y-max="16"
            />
          </section>

          <!--
            可读性档：先给一条独立的间隔条形图（看清全局快慢），下方再给精确输入行。
            两块的**指令顺序与名称完全一致**，不构成"同一批数据说两遍"的迷惑
            （上图回答"谁快"，下图回答"改成多少"）。
          -->
          <section class="card">
            <div class="card-head">
              <span class="card-title">轮询间隔分布</span>
              <span class="card-badge" :class="pollOverLimit ? 'is-danger' : 'is-success'">
                {{ pollEnabledCount }}/{{ POLL_LIMIT }} 已启用
              </span>
            </div>
            <PollIntervalChart :items="pollBarItems" height="150px" />
          </section>

          <section class="card">
            <div class="card-head">
              <span class="card-title">轮询指令</span>
            </div>
            <PollCommandList
              :commands="pollCommands"
              :limit="POLL_LIMIT"
              mode="stacked"
              @interval="setPollInterval"
              @toggle="togglePoll"
            />
          </section>

          <section class="card">
            <ControlledOps :groups="OP_GROUPS" :total="OP_TOTAL" @run="onRunOp" />
          </section>
        </template>

        <!--
          ═══════════ 方案 C：平衡态（推荐） ═══════════
          首屏 = 结论：指标 + 保护(+明细按需) + MOS。次屏 = 诊断图表。
          末屏 = 配置与操作。图表高度取中间值（spark 84 / 趋势 128 / 历史 148）。
        -->
        <template v-else>
          <!-- 首屏第 2 块：保护 —— heat 条 + 可展开明细（全绿时明细零信息量） -->
          <section class="card">
            <div class="card-head">
              <span class="card-title">保护状态</span>
              <span class="card-badge" :class="protectAnyActive ? 'is-danger' : 'is-success'">
                {{ protectAnyActive ? '有触发' : '12 项正常' }}
              </span>
            </div>
            <ProtectionMatrix :bits="PROTECTION_BITS" mode="heat" :status-hex="PROTECTION_STATUS_HEX" :show-detail-toggle="true" />
          </section>

          <!-- 首屏第 3 块：MOS —— 两个方块，像电灯开关 -->
          <section class="card">
            <MosDirect :switches="mos" :fet-hex="fetHex" variant="grid" @toggle="toggleMos" />
          </section>

          <!-- 诊断图表区：电芯 + 温度并成一张「电芯与温度」卡（都是"电芯本体"的物理量） -->
          <section class="card">
            <div class="card-head">
              <span class="card-title">电芯与温度</span>
              <span class="card-metric">
                压差 <b>{{ CELL_STATS.diffMv }}</b> mV · 最高温 <b>{{ maxTemp.toFixed(1) }}</b> ℃
              </span>
            </div>
            <CellSparkChart :values="CELL_VOLTAGES" height="84px" />
            <div class="divider"></div>
            <TempScaleChart
              :labels="TEMP_LABELS"
              :values="TEMP_VALUES"
              height="96px"
              :warn="TEMP_THRESHOLDS.warn"
              :danger="TEMP_THRESHOLDS.danger"
            />
          </section>

          <!-- 趋势：温度 3 路（能看出单路偏高） -->
          <section class="card">
            <div class="card-head">
              <span class="card-title">温度趋势</span>
              <span class="card-metric">近 12 小时 · ℃</span>
            </div>
            <TrendChart
              :categories="TREND_TIMES"
              :series="tempTrendSeries"
              unit="℃"
              height="128px"
              :show-legend="true"
              :decimals="1"
              :y-min="20"
              :y-max="36"
            />
          </section>

          <!-- 趋势：SOC（单序列 → 带面积） -->
          <section class="card">
            <div class="card-head">
              <span class="card-title">剩余电量趋势</span>
              <span class="card-metric">当前 <b>{{ METRICS.soc.toFixed(1) }}</b>%</span>
            </div>
            <TrendChart
              :categories="TREND_TIMES"
              :series="socOnlySeries"
              unit="%"
              height="112px"
              :show-legend="false"
              :decimals="1"
              :y-min="0"
              :y-max="16"
            />
          </section>

          <!-- 保护历史：与"保护状态"是否触发的判断互补 -->
          <section class="card">
            <div class="card-head">
              <span class="card-title">保护触发历史</span>
              <span class="card-metric">{{ PROTECTION_HISTORY_WINDOW }} <b>{{ PROTECTION_HISTORY_TOTAL }}</b> 次</span>
            </div>
            <ProtectionHistoryChart
              :items="PROTECTION_HISTORY"
              :window-text="PROTECTION_HISTORY_WINDOW"
            />
          </section>

          <!-- 轮询：条形行（量 + 编辑同一行，不重复两遍指令名） -->
          <section class="card">
            <div class="card-head">
              <span class="card-title">轮询指令</span>
              <span class="card-badge" :class="pollOverLimit ? 'is-danger' : 'is-success'">
                {{ pollEnabledCount }}/{{ POLL_LIMIT }} 已启用
              </span>
            </div>
            <PollBarRows
              :commands="pollCommands"
              :limit="POLL_LIMIT"
              @interval="setPollInterval"
              @toggle="togglePoll"
            />
          </section>

          <section class="card">
            <ControlledOps :groups="OP_GROUPS" :total="OP_TOTAL" @run="onRunOp" />
          </section>
        </template>

        <p class="demo-foot">
          本页全部为静态 mock 数据 · 仅用于视觉评审 · 不连接任何后端
        </p>
      </main>
    </div>

    <!-- 方案 C 的设备信息底部抽屉（页头只留一行，元信息按需展开） -->
    <el-drawer
      v-model="infoSheetOpen"
      direction="btt"
      size="auto"
      title="设备信息"
      class="info-drawer"
    >
      <dl class="info-list">
        <div class="info-row"><dt>设备名称</dt><dd>{{ DEVICE.name }}</dd></div>
        <div class="info-row"><dt>设备型号</dt><dd>嘉佰达 BMS · {{ DEVICE.cellCount }}S</dd></div>
        <div class="info-row"><dt>在线状态</dt><dd class="is-ok">在线 · {{ DEVICE.updatedAt }}</dd></div>
        <div class="info-row"><dt>所属节点</dt><dd>{{ DEVICE.nodeSn }}</dd></div>
        <div class="info-row"><dt>总线通道</dt><dd>{{ DEVICE.channel }}</dd></div>
        <div class="info-row"><dt>设备地址</dt><dd>{{ DEVICE.address }}</dd></div>
        <div class="info-row"><dt>节点固件</dt><dd>{{ DEVICE.firmware }}</dd></div>
        <div class="info-row"><dt>轮询名额</dt><dd>{{ pollEnabledCount }} / {{ POLL_LIMIT }}</dd></div>
      </dl>
      <div class="info-actions">
        <el-button size="small" @click="infoSheetOpen = false">关闭</el-button>
      </div>
    </el-drawer>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { ElMessage } from 'element-plus'
import DeviceHead from './mobile-redesign/parts/DeviceHead.vue'
import MetricTiles from './mobile-redesign/parts/MetricTiles.vue'
import ProtectionMatrix from './mobile-redesign/parts/ProtectionMatrix.vue'
import MosDirect from './mobile-redesign/parts/MosDirect.vue'
import PollCommandList from './mobile-redesign/parts/PollCommandList.vue'
import PollBarRows from './mobile-redesign/parts/PollBarRows.vue'
import ControlledOps from './mobile-redesign/parts/ControlledOps.vue'
import CellSparkChart from './mobile-redesign/charts/CellSparkChart.vue'
import TempScaleChart from './mobile-redesign/charts/TempScaleChart.vue'
import PollIntervalChart from './mobile-redesign/charts/PollIntervalChart.vue'
import TrendChart from './mobile-redesign/charts/TrendChart.vue'
import type { TrendSeries } from './mobile-redesign/charts/TrendChart.vue'
import ProtectionHistoryChart from './mobile-redesign/charts/ProtectionHistoryChart.vue'
import {
  DEVICE, METRICS, CELL_VOLTAGES, CELL_STATS, TEMPS, TEMP_THRESHOLDS,
  PROTECTION_BITS, PROTECTION_STATUS_HEX,
  PROTECTION_HISTORY, PROTECTION_HISTORY_TOTAL, PROTECTION_HISTORY_WINDOW,
  POLL_LIMIT, TREND_TIMES, TREND_TEMPS, TREND_SOC, OP_GROUPS, OP_TOTAL,
  cloneMos, clonePollCommands,
} from './mobile-redesign/mockBms'

type SchemeKey = 'A' | 'B' | 'C'

interface SchemeMeta {
  name: string
  idea: string
  head: 'stacked' | 'compact' | 'sheet'
  tiles: 'tiles' | 'strip' | 'sheet'
}

const SCHEMES: Record<SchemeKey, SchemeMeta> = {
  A: {
    name: '方案 A · 密度优先',
    idea: '一屏装完「状态 + 配置」：指标压成单行带、保护用 12 格热力条、图表全部 sparkline 化，信息密度最大。',
    head: 'stacked',
    tiles: 'strip',
  },
  B: {
    name: '方案 B · 可读性优先',
    idea: '每块只讲一件事：图表给足高度与标签，长列表用折叠分层而不是压缩，适合现场逐项核对。',
    head: 'compact',
    tiles: 'tiles',
  },
  C: {
    name: '方案 C · 平衡态（推荐）',
    idea: '首屏=结论（指标/保护/MOS），次屏=诊断图表，末屏=配置与操作；密度与可读性各取一半。',
    head: 'sheet',
    tiles: 'sheet',
  },
}

const scheme = ref<SchemeKey>('C')
const currentScheme = computed(() => SCHEMES[scheme.value])

const infoSheetOpen = ref(false)
const refreshTicks = ref(0)

/* ── 可交互的本地状态（demo 内可点，不落任何后端）── */
const mos = ref(cloneMos())
const pollCommands = ref(clonePollCommands())

const fetHex = computed(() =>
  '0x' + mos.value.reduce((mask, m, i) => (m.on ? mask | (1 << i) : mask), 0)
    .toString(16).padStart(2, '0'))

const pollEnabledCount = computed(() => pollCommands.value.filter((c) => c.intervalMs > 0).length)
const pollOverLimit = computed(() => pollEnabledCount.value > POLL_LIMIT)

const pollBarItems = computed(() =>
  pollCommands.value.map((c) => ({ name: c.name, intervalMs: c.intervalMs })))

const protectAnyActive = computed(() => PROTECTION_BITS.some((b) => b.active))

const TEMP_LABELS = TEMPS.map((t) => t.label)
const TEMP_VALUES = TEMPS.map((t) => t.value)
const maxTemp = computed(() => Math.max(...TEMP_VALUES))

/* ── 趋势序列 ── */
const tempTrendSeries = computed<TrendSeries[]>(() => [
  { name: '探头 1', tone: 'danger', data: TREND_TEMPS[0] },
  { name: '探头 2', tone: 'success', data: TREND_TEMPS[1] },
  { name: '探头 3', tone: 'primary', data: TREND_TEMPS[2] },
])

/**
 * 方案 A 的紧凑趋势：温度峰值（左轴 ℃）+ SOC（右轴 %）。
 * 两条量纲不同，**必须双轴**：同轴会把 "28℃ vs 7%" 画成一条高一条低，
 * 读者容易误以为温度与电量可以直接比大小（规范 §4.5.3）。
 */
const trendDualSeries = computed<TrendSeries[]>(() => [
  { name: '温度峰值', tone: 'warning', data: TREND_TEMPS[0], axis: 'left', unit: '℃' },
  { name: '剩余电量', tone: 'primary', data: TREND_SOC, axis: 'right', unit: '%' },
])

/** 方案 B 的 SOC + 电流双序列（不同量纲共用一张图时只做趋势对照，读数看 tooltip） */
const socTrendSeries = computed<TrendSeries[]>(() => [
  { name: 'SOC', tone: 'primary', data: TREND_SOC },
])

/** 方案 C 的单序列 SOC（带面积） */
const socOnlySeries = computed<TrendSeries[]>(() => [
  { name: '剩余电量', tone: 'primary', data: TREND_SOC },
])

/* ── 交互 ── */
function toggleMos(key: 'charge' | 'discharge', on: boolean) {
  const target = mos.value.find((m) => m.key === key)
  if (!target || target.on === on) return
  target.on = on
  // demo 里不做真确认弹窗，只给一次即时反馈（生产实现需接极简确认 + 回读校验）
  ElMessage.success(`${target.name}：${on ? '导通' : '关断'}（demo，未下发）`)
}

function setPollInterval(id: string, ms: number) {
  const cmd = pollCommands.value.find((c) => c.id === id)
  if (!cmd) return
  cmd.intervalMs = Math.max(0, Math.min(Math.floor(ms), 60000))
}

function togglePoll(id: string, on: boolean) {
  const cmd = pollCommands.value.find((c) => c.id === id)
  if (!cmd) return
  cmd.intervalMs = on ? (cmd.intervalMs > 0 ? cmd.intervalMs : cmd.defaultMs) : 0
}

function onRunOp(id: string) {
  ElMessage.info(`受控操作「${id}」在 demo 中不执行`)
}

function onHeadCommand(cmd: string) {
  const text = cmd === 'edit' ? '编辑设备' : cmd === 'sync' ? '同步到 HA' : '删除设备'
  ElMessage.info(`${text}：demo 中不执行`)
}
</script>

<style scoped>
/* ═══════════ 评审工具条 ═══════════ */
.demo-root {
  /*
    本页是**独立 DEV 路由**（不在 MainLayout 之下），必须自己当滚动容器。
    根因（本仓已记录过的坑，见 BmsDetailPage.vue 同类注释）：
      App.vue 全局样式是「应用外壳」模式 —— html,body{height:100%;overflow:hidden}、
      #app{height:100%}，滚动由 MainLayout 内部的 .main-content 承担。
      本页直接渲染进 #app、没有 MainLayout ⇒ 没有任何元素承担滚动 ⇒
      内容比视口高时既滚不动、fullPage 截图也会在视口高度处截断。
    用 height:100% + overflow-y:auto 让本页恰好占满 #app 并在**自己内部**滚动。
  */
  height: 100%;
  overflow-y: auto;
  overflow-x: hidden;
  background: var(--bg-color-page);
  -webkit-overflow-scrolling: touch;
}
.demo-toolbar {
  position: sticky;
  top: 0;
  z-index: 20;
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 5px 8px 6px;
  background: var(--card-bg);
  border-bottom: 1px solid var(--el-border-color);
  box-shadow: var(--shadow-sm);
}
.toolbar-row {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
/*
  评审工具条必须比被评审的页面**更轻**，否则它会成为视觉焦点。
  App.vue 的移动端媒体查询会把 .el-button 字号提到 15px（防 iOS 聚焦缩放），
  这里显式压回 12px / 28px 高 —— 工具条不是页面内容，不需要触控友好尺寸。
*/
.scheme-switch {
  display: flex;
  flex-wrap: wrap;
}
.scheme-switch :deep(.el-radio-button__inner) {
  height: 28px;
  padding: 0 9px;
  font-size: 12px;
  line-height: 1;
  display: inline-flex;
  align-items: center;
}
.demo-badge {
  margin-left: auto;
  padding: 1px 6px;
  border-radius: var(--radius-sm);
  background: var(--el-color-warning-light-9);
  color: var(--el-color-warning);
  font-size: 10px;
  font-weight: 600;
  white-space: nowrap;
}
.toolbar-idea {
  display: flex;
  gap: 6px;
  font-size: 11px;
  line-height: 1.45;
  color: var(--el-text-color-secondary);
}
.toolbar-idea strong {
  flex-shrink: 0;
  color: var(--el-color-primary);
  font-weight: 600;
}
.toolbar-idea span { min-width: 0; }

/* ═══════════ 手机页面容器 ═══════════ */
.phone {
  /* 393px 是本次评审基准机型（iPhone 14/15 逻辑宽度）。
     桌面打开时居中成手机样张；在 393 视口下正好铺满 = 真实渲染。 */
  width: 100%;
  max-width: 393px;
  margin: 0 auto;
  min-height: calc(100vh - 60px);
  display: flex;
  flex-direction: column;
  background: var(--bg-color-page);
}
@media (min-width: 460px) {
  .phone {
    margin-top: 16px;
    border: 1px solid var(--el-border-color);
    border-radius: var(--radius-lg);
    box-shadow: var(--shadow-lg);
    overflow: hidden;
  }
}

/* ═══════════ 页面主体 ═══════════ */
.page-body {
  /* 用户批评①⑥：生产页 el-main padding 20px + 卡片 padding 16/20px ⇒ 卡宽仅 321/393。
     这里页面级 padding 取 8px、卡片内距 10px ⇒ 卡宽 377/393（96%）。 */
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 8px;
  flex: 1;
  min-width: 0;
}

.card {
  min-width: 0;
  padding: 10px;
  background: var(--card-bg);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: var(--radius-md);
  /* 内容卡圆角 ≤8px（规范 §4.2.3） */
}

.card-head {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 8px;
  min-width: 0;
}
.card-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  word-break: keep-all;
  white-space: nowrap;
}
.card-sub {
  font-size: 11px;
  color: var(--el-text-color-secondary);
}
/* 卡头右侧的数值摘要：把"图要看什么"直接写在标题旁，不占图内空间 */
.card-metric {
  margin-left: auto;
  min-width: 0;
  font-size: 11px;
  line-height: 1.4;
  color: var(--el-text-color-secondary);
  text-align: right;
}
.card-metric b {
  color: var(--el-text-color-primary);
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}
.card-badge {
  flex-shrink: 0;
  padding: 1px 6px;
  border-radius: 999px;
  font-size: 10px;
  line-height: 1.6;
}
.card-badge.is-success {
  color: var(--el-color-success);
  background: var(--el-color-success-light-9);
}
.card-badge.is-danger {
  color: var(--el-color-danger);
  background: var(--el-color-danger-light-9);
}
.card-note {
  margin: 8px 0 0;
  font-size: 11px;
  line-height: 1.5;
  color: var(--el-text-color-secondary);
}

/* 卡内分节细线：同卡两块内容之间唯一的层级线索（不新造嵌套卡片） */
.divider {
  height: 1px;
  margin: 9px 0;
  background: var(--el-border-color-lighter);
}

/* 图在区块内的上间距 */
.chart-inline { margin-top: 9px; }

.demo-foot {
  margin: 4px 0 16px;
  text-align: center;
  font-size: 10px;
  color: var(--el-text-color-placeholder);
}

/* ═══════════ 设备信息抽屉 ═══════════ */
.info-list {
  margin: 0;
  display: flex;
  flex-direction: column;
}
.info-row {
  display: flex;
  align-items: baseline;
  gap: 12px;
  padding: 9px 0;
  border-bottom: 1px solid var(--el-border-color-lighter);
  font-size: 13px;
}
.info-row:last-child { border-bottom: none; }
.info-row dt {
  flex-shrink: 0;
  width: 76px;
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.info-row dd {
  margin: 0;
  min-width: 0;
  color: var(--el-text-color-primary);
  word-break: break-all;
}
.info-row dd.is-ok { color: var(--el-color-success); }
.info-actions {
  display: flex;
  justify-content: flex-end;
  margin-top: 12px;
}
</style>

<style>
/* 抽屉 teleport 到 body，需全局选择器；最大宽高兜底防窄屏溢出（规范 §4.3 表单 3） */
.info-drawer { max-width: 100vw; }
.info-drawer .el-drawer__body { padding: 0 16px 16px; }
</style>