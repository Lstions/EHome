<template>
  <!-- BMS 设备详情页设计稿像素级 demo:静态 mock 数据,仅开发环境可见(DEV 门禁路由) -->
  <!-- 复刻 designs/bms.png 整页 + 全量交互:导航/搜索/通知/设备操作/趋势切换/MOS/数据流/指令频率/受控操作/历史 -->
  <div class="bms-demo">
    <!-- ══════════ 左侧边栏 ══════════ -->
    <aside class="sidebar">
      <div class="sidebar-logo">
        <span class="logo-hex">⬡</span>
        <span class="logo-text">EHomeSystem</span>
      </div>
      <nav class="sidebar-nav">
        <div v-for="item in navItems" :key="item.label"
             class="nav-item" :class="{ active: activeNav === item.label }"
             @click="onNavClick(item)">
          <el-icon :size="16"><component :is="item.icon" /></el-icon>
          <span>{{ item.label }}</span>
        </div>
      </nav>
      <div class="sidebar-footer">
        <div class="sys-status"><span class="dot dot-green"></span>系统运行正常</div>
        <div class="sys-version">v2.0.0</div>
      </div>
    </aside>

    <!-- ══════════ 右侧主区 ══════════ -->
    <div class="main-area">
      <!-- 顶栏 -->
      <header class="topbar">
        <div class="breadcrumb">
          <el-icon :size="14"><HomeFilled /></el-icon>
          <span class="crumb-sep">/</span>
          <span class="crumb-parent link" @click="goList">边缘设备管理</span>
          <span class="crumb-sep">/</span>
          <span class="crumb-current">设备详情</span>
        </div>
        <div class="topbar-search" @click="searchOpen = true">
          <el-icon :size="14"><Search /></el-icon>
          <input placeholder="搜索设备、节点、通道、数据..." readonly />
          <kbd>Ctrl+K</kbd>
        </div>
        <div class="topbar-right">
          <span class="online-hint"><span class="dot dot-green"></span>在线</span>
          <span class="bell-wrap" @click="notifOpen = true">
            <el-icon :size="18"><Bell /></el-icon>
            <span class="bell-badge">{{ unreadCount }}</span>
          </span>
          <el-dropdown trigger="click" @command="onUserCommand">
            <span class="user-trigger">
              <span class="avatar">A</span>
              <span class="username">admin</span>
              <el-icon :size="12"><ArrowDown /></el-icon>
            </span>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="profile">个人资料</el-dropdown-item>
                <el-dropdown-item command="logout" divided>退出登录</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
      </header>

      <!-- 内容区 -->
      <main class="content">
        <a class="back-link" @click="goList">← 返回设备列表</a>

        <!-- 设备信息卡 -->
        <div class="card device-card">
          <div class="device-left">
            <div class="device-photo">
              <el-icon :size="40" color="#64748b"><Monitor /></el-icon>
            </div>
            <div class="device-meta">
              <div class="device-name-row">
                <span class="device-name">EdgeBox-3000</span>
                <span class="tag" :class="deviceOnline ? 'tag-green' : 'tag-red'">{{ deviceOnline ? '在线' : '离线' }}</span>
              </div>
              <div class="meta-row"><span class="meta-label">设备ID</span><span class="meta-value">EDG3000A1B2C3</span></div>
              <div class="meta-row">
                <span class="meta-label">节点</span>
                <span class="meta-value link" @click="toast('跳转到节点:工厂A / 产线1 / 环境监测点')">工厂A / 产线1 / 环境监测点
                  <el-icon :size="11"><TopRight /></el-icon>
                </span>
              </div>
              <div class="meta-row"><span class="meta-label">固件版本</span><span class="meta-value">v1.4.8</span></div>
              <div class="meta-row"><span class="meta-label">运行时长</span><span class="meta-value">{{ uptimeText }}</span></div>
            </div>
          </div>
          <div class="device-mid">
            <div v-for="f in deviceFields" :key="f.label" class="device-field">
              <span class="field-icon"><el-icon :size="13"><component :is="f.icon" /></el-icon></span>
              <div>
                <div class="field-label">{{ f.label }}</div>
                <div class="field-value">{{ f.value }}</div>
              </div>
            </div>
          </div>
          <div class="device-actions">
            <button class="btn btn-primary" :disabled="restarting" @click="onRestart">
              {{ restarting ? '重启中…' : '重启设备' }}
            </button>
            <button class="btn btn-plain" @click="remoteOpen = true">远程连接</button>
            <el-dropdown trigger="click" @command="onMoreAction">
              <button class="btn btn-plain">更多操作 ▾</button>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item command="export">导出数据</el-dropdown-item>
                  <el-dropdown-item command="log">查看日志</el-dropdown-item>
                  <el-dropdown-item command="offline" divided>{{ deviceOnline ? '设为离线' : '设为在线' }}</el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
          </div>
        </div>

        <!-- 6 张统计卡 -->
        <div class="stat-row">
          <div v-for="s in stats" :key="s.label" class="card stat-card" @click="toast('查看 ' + s.label + ' 明细')">
            <span class="stat-icon" :style="{ background: s.color }">
              <el-icon :size="20" color="#fff"><component :is="s.icon" /></el-icon>
            </span>
            <div class="stat-body">
              <div class="stat-label">{{ s.label }}</div>
              <div class="stat-value">{{ s.value }}<span class="stat-unit">{{ s.unit }}</span></div>
              <div class="stat-delta" :style="{ color: s.deltaColor }">{{ s.delta }}</div>
            </div>
          </div>
        </div>

        <!-- 中部:运行趋势 + 右列三小卡 -->
        <div class="mid-row">
          <div class="card chart-card">
            <div class="card-head">
              <span class="card-title">运行趋势</span>
              <div class="head-right">
                <el-dropdown trigger="click" @command="onMetricChange">
                  <button class="metric-select">{{ currentMetric.label }} ▾</button>
                  <template #dropdown>
                    <el-dropdown-menu>
                      <el-dropdown-item v-for="m in metrics" :key="m.key" :command="m.key"
                                        :class="{ 'is-active': m.key === currentMetricKey }">
                        {{ m.label }}
                      </el-dropdown-item>
                    </el-dropdown-menu>
                  </template>
                </el-dropdown>
                <div class="range-group">
                  <button v-for="r in ranges" :key="r" class="range-btn"
                          :class="{ active: r === activeRange }"
                          @click="onRangeChange(r)">{{ r }}</button>
                </div>
              </div>
            </div>
            <div ref="trendChartRef" class="trend-chart"></div>
          </div>

          <div class="right-col">
            <div class="card probe-card">
              <div class="card-head">
                <span class="card-title">温度探头状态</span>
                <a class="more-link" @click="probeDetailOpen = true">查看详情 &gt;</a>
              </div>
              <div class="probe-list">
                <div v-for="p in probes" :key="p.name" class="probe-item">
                  <div class="probe-name">{{ p.name }}</div>
                  <div class="probe-value" :style="{ color: p.color }">{{ p.value }} ℃</div>
                  <span class="tag" :class="p.tagClass">{{ p.status }}</span>
                </div>
              </div>
            </div>

            <div class="card mos-card">
              <div class="card-head">
                <span class="card-title">MOS状态</span>
                <a class="more-link" @click="mosDetailOpen = true">查看详情 &gt;</a>
              </div>
              <div v-for="m in mosList" :key="m.name" class="mos-row">
                <span class="mos-name">{{ m.name }}</span>
                <span class="mos-right">
                  <span class="switch" :class="{ on: m.on }" @click="onMosToggle(m)"><span class="switch-knob"></span></span>
                  <span class="tag" :class="m.on ? 'tag-green' : 'tag-red'">{{ m.on ? '开启' : '关闭' }}</span>
                </span>
              </div>
            </div>

            <div class="card protect-card" @click="protectOpen = true">
              <el-icon :size="12" class="protect-menu"><MoreFilled /></el-icon>
              <div class="protect-left">
                <el-icon :size="22" color="#22c55e"><CircleCheckFilled /></el-icon>
                <div>
                  <div class="protect-title">设备运行正常</div>
                  <div class="protect-sub">所有保护项均在正常范围内</div>
                </div>
              </div>
              <div class="protect-counters">
                <span class="p-counter"><span class="dot dot-green"></span>正常 <b>8</b></span>
                <span class="p-counter"><span class="dot dot-orange"></span>告警 <b>0</b></span>
                <span class="p-counter"><span class="dot dot-red"></span>异常 <b>0</b></span>
              </div>
            </div>
          </div>
        </div>

        <!-- 底部:实时数据流 + 右列 -->
        <div class="bottom-row">
          <div class="card stream-card">
            <div class="card-head">
              <span class="card-title">实时数据流</span>
              <span class="tag" :class="liveRunning ? 'tag-green' : 'tag-red'">{{ liveRunning ? '实时' : '已暂停' }}</span>
              <span class="stream-freq">更新频率: 1s</span>
              <button class="btn-mini" @click="liveRunning = !liveRunning">{{ liveRunning ? '暂停' : '恢复' }}</button>
            </div>
            <div class="table-scroll">
            <table class="data-table">
              <thead>
                <tr><th>时间</th><th>通道</th><th>数据项</th><th>值</th><th>单位</th><th>状态</th></tr>
              </thead>
              <tbody>
                <tr v-for="row in streamRows" :key="row.id">
                  <td>{{ row.time }}</td><td>{{ row.channel }}</td><td>{{ row.item }}</td>
                  <td class="num">{{ row.value }}</td><td>{{ row.unit }}</td>
                  <td><span class="status-ok"><span class="dot dot-green"></span>正常</span></td>
                </tr>
              </tbody>
            </table>
            </div>
            <div class="table-footer"><a class="more-link" @click="allDataOpen = true">查看全部数据 →</a></div>
          </div>

          <div class="right-col">
            <div class="card collapse-card" @click="freqOpen = !freqOpen">
              <span class="card-title">指令频率配置</span>
              <el-icon :size="13" color="#8a93a6" class="collapse-arrow" :class="{ open: freqOpen }"><ArrowRight /></el-icon>
            </div>
            <transition name="fade">
              <div v-if="freqOpen" class="card freq-panel">
                <div v-for="c in cmdFreqs" :key="c.id" class="freq-row">
                  <div class="freq-info">
                    <div class="freq-name">{{ c.name }}</div>
                    <div class="freq-desc">{{ c.desc }}</div>
                  </div>
                  <div class="freq-edit">
                    <input v-model.number="c.interval" type="number" min="100" step="100" class="freq-input" />
                    <span class="freq-unit">ms</span>
                    <button class="btn-mini" @click="onFreqSave(c)">保存</button>
                  </div>
                </div>
              </div>
            </transition>

            <div class="card ops-card">
              <div class="card-head"><span class="card-title">受控操作</span></div>
              <div class="ops-sub">当前设备可执行的受控操作</div>
              <div class="ops-list">
                <div v-for="op in ops" :key="op.name" class="op-item" @click="onOpClick(op)">
                  <el-icon :size="16" color="#2563eb"><component :is="op.icon" /></el-icon>
                  <div class="op-text">
                    <div class="op-name">{{ op.name }}</div>
                    <div class="op-desc">{{ op.desc }}</div>
                  </div>
                </div>
              </div>
            </div>

            <div class="card history-card">
              <div class="card-head">
                <span class="card-title">操作历史</span>
                <a class="more-link" @click="historyOpen = true">查看全部 &gt;</a>
              </div>
              <div class="table-scroll">
              <table class="data-table">
                <thead>
                  <tr><th>时间</th><th>操作类型</th><th>操作内容</th><th>操作人</th><th>结果</th></tr>
                </thead>
                <tbody>
                  <tr v-for="h in historyRows.slice(0, 3)" :key="h.id">
                    <td>{{ h.time }}</td><td>{{ h.type }}</td>
                    <td>{{ h.content }}</td><td>{{ h.user }}</td>
                    <td><span class="status-ok"><span class="dot" :class="h.ok ? 'dot-green' : 'dot-red'"></span>{{ h.ok ? '成功' : '失败' }}</span></td>
                  </tr>
                </tbody>
              </table>
              </div>
            </div>
          </div>
        </div>
      </main>
    </div>

    <!-- ══════════ 弹窗/抽屉 ══════════ -->
    <el-dialog v-model="searchOpen" title="全局搜索" width="480px" append-to-body>
      <input ref="searchInputRef" v-model="searchQuery" class="dialog-search-input"
             placeholder="输入关键字搜索设备、节点、通道、数据…" @keyup.enter="onSearch" />
      <div v-if="searchQuery" class="search-results">
        <div v-for="r in searchResults" :key="r" class="search-result-item" @click="onSearchPick(r)">{{ r }}</div>
        <div v-if="!searchResults.length" class="search-empty">无匹配结果</div>
      </div>
      <template #footer>
        <button class="btn btn-plain" @click="searchOpen = false">取消</button>
        <button class="btn btn-primary" @click="onSearch">搜索</button>
      </template>
    </el-dialog>

    <el-drawer v-model="notifOpen" title="通知中心" size="360px">
      <div class="notif-head">
        <span>{{ unreadCount }} 条未读</span>
        <button class="btn-mini" @click="markAllRead">全部已读</button>
      </div>
      <div v-for="n in notifications" :key="n.id" class="notif-item" :class="{ unread: n.unread }" @click="n.unread = false">
        <div class="notif-title">{{ n.title }}</div>
        <div class="notif-time">{{ n.time }}</div>
      </div>
    </el-drawer>

    <el-dialog v-model="remoteOpen" title="远程连接" width="420px" append-to-body>
      <div class="remote-body">
        <div class="remote-row"><span>协议</span><b>SSH / Web Terminal</b></div>
        <div class="remote-row"><span>地址</span><b>192.168.1.88:22</b></div>
        <div class="remote-row"><span>状态</span><b :style="{ color: '#22c55e' }">可达</b></div>
      </div>
      <template #footer>
        <button class="btn btn-plain" @click="remoteOpen = false">关闭</button>
        <button class="btn btn-primary" @click="onRemoteConnect">建立连接</button>
      </template>
    </el-dialog>

    <el-dialog v-model="probeDetailOpen" title="温度探头详情" width="520px" append-to-body>
      <table class="data-table">
        <thead><tr><th>探头</th><th>当前值</th><th>量程</th><th>状态</th><th>校准偏移</th></tr></thead>
        <tbody>
          <tr v-for="p in probes" :key="p.name">
            <td>{{ p.name }}</td><td class="num">{{ p.value }} ℃</td><td>-40 ~ 125 ℃</td>
            <td><span class="tag" :class="p.tagClass">{{ p.status }}</span></td><td class="num">+0.0 ℃</td>
          </tr>
        </tbody>
      </table>
    </el-dialog>

    <el-dialog v-model="mosDetailOpen" title="MOS 详情" width="480px" append-to-body>
      <div v-for="m in mosList" :key="m.name" class="mos-detail-row">
        <span>{{ m.name }}</span>
        <span class="tag" :class="m.on ? 'tag-green' : 'tag-red'">{{ m.on ? '开启' : '关闭' }}</span>
        <span class="mos-detail-meta">内阻 3.2mΩ · 温升 4.1℃</span>
      </div>
    </el-dialog>

    <el-dialog v-model="protectOpen" title="保护状态详情" width="560px" append-to-body>
      <table class="data-table">
        <thead><tr><th>保护项</th><th>阈值</th><th>当前</th><th>状态</th></tr></thead>
        <tbody>
          <tr v-for="p in protectItems" :key="p.name">
            <td>{{ p.name }}</td><td class="num">{{ p.threshold }}</td><td class="num">{{ p.current }}</td>
            <td><span class="status-ok"><span class="dot dot-green"></span>正常</span></td>
          </tr>
        </tbody>
      </table>
    </el-dialog>

    <el-dialog v-model="allDataOpen" title="全部实时数据" width="680px" append-to-body>
      <div class="table-scroll">
      <table class="data-table">
        <thead><tr><th>时间</th><th>通道</th><th>数据项</th><th>值</th><th>单位</th><th>状态</th></tr></thead>
        <tbody>
          <tr v-for="row in allStreamRows" :key="row.id">
            <td>{{ row.time }}</td><td>{{ row.channel }}</td><td>{{ row.item }}</td>
            <td class="num">{{ row.value }}</td><td>{{ row.unit }}</td>
            <td><span class="status-ok"><span class="dot dot-green"></span>正常</span></td>
          </tr>
        </tbody>
      </table>
      </div>
    </el-dialog>

    <el-dialog v-model="historyOpen" title="操作历史" width="720px" append-to-body>
      <div class="table-scroll">
      <table class="data-table">
        <thead><tr><th>时间</th><th>操作类型</th><th>操作内容</th><th>操作人</th><th>结果</th></tr></thead>
        <tbody>
          <tr v-for="h in historyRows" :key="h.id">
            <td>{{ h.time }}</td><td>{{ h.type }}</td><td>{{ h.content }}</td><td>{{ h.user }}</td>
            <td><span class="status-ok"><span class="dot" :class="h.ok ? 'dot-green' : 'dot-red'"></span>{{ h.ok ? '成功' : '失败' }}</span></td>
          </tr>
        </tbody>
      </table>
      </div>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onUnmounted, nextTick } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import * as echarts from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { LineChart as LineChartSeries } from 'echarts/charts'
import { TooltipComponent, LegendComponent, GridComponent } from 'echarts/components'
import {
  Odometer, Box, Cpu, Connection, DataAnalysis, Monitor, Document,
  Setting, HomeFilled, Search, Bell, ArrowDown, ArrowRight, TopRight,
  Clock, Warning, Sunny, Lightning, CircleCheckFilled, MoreFilled,
  VideoPlay, RefreshRight, UploadFilled, Lock
} from '@element-plus/icons-vue'

echarts.use([CanvasRenderer, LineChartSeries, TooltipComponent, LegendComponent, GridComponent])

// ── 通用提示 ──
const toast = (msg: string) => ElMessage({ message: msg, type: 'success', grouping: true })
const info = (msg: string) => ElMessage({ message: msg, type: 'info', grouping: true })

// ── 侧边栏 ──
const activeNav = ref('边缘设备')
const navItems = [
  { label: '仪表盘', icon: Odometer },
  { label: '节点', icon: Connection },
  { label: '边缘设备', icon: Box },
  { label: '逻辑设备', icon: Cpu },
  { label: '通道管理', icon: DataAnalysis },
  { label: '数据面板', icon: Monitor },
  { label: '固件管理', icon: Document },
  { label: '配置模板', icon: Setting },
  { label: '系统监控', icon: Monitor },
]
function onNavClick(item: { label: string }) {
  activeNav.value = item.label
  info(`切换到「${item.label}」(demo 无路由)`)
}

// ── 顶栏:搜索 / 通知 / 用户 ──
const searchOpen = ref(false)
const searchQuery = ref('')
const searchInputRef = ref<HTMLInputElement>()
const SEARCH_POOL = [
  'EdgeBox-3000 · 边缘设备', '工厂A / 产线1 / 环境监测点', '通道1 · 电池电压',
  '通道2 · 温度探头1', 'SN-3001 雨量计', '固件 v1.4.8', '节点 F0F5BDFFFE02',
]
const searchResults = computed(() =>
  searchQuery.value ? SEARCH_POOL.filter(s => s.toLowerCase().includes(searchQuery.value.toLowerCase())) : []
)
function onSearch() {
  if (!searchQuery.value) { info('请输入关键字'); return }
  toast(`搜索「${searchQuery.value}」:命中 ${searchResults.value.length} 项`)
}
function onSearchPick(r: string) {
  searchOpen.value = false
  toast(`打开:${r}`)
}

const notifOpen = ref(false)
const notifications = reactive([
  { id: 1, title: '探头3 温度偏高 38.2℃', time: '5 分钟前', unread: true },
  { id: 2, title: '固件 v1.4.9 可用', time: '1 小时前', unread: true },
  { id: 3, title: 'EdgeBox-3000 心跳恢复', time: '3 小时前', unread: false },
  { id: 4, title: '通道2 采样延迟告警', time: '昨天 22:10', unread: false },
])
const unreadCount = computed(() => notifications.filter(n => n.unread).length)
function markAllRead() {
  notifications.forEach(n => { n.unread = false })
  toast('已全部标记为已读')
}

function onUserCommand(cmd: string) {
  if (cmd === 'profile') info('个人资料(demo)')
  if (cmd === 'logout') {
    ElMessageBox.confirm('确认退出登录?', '提示', { confirmButtonText: '退出', cancelButtonText: '取消' })
      .then(() => toast('已退出(demo)'))
      .catch(() => {})
  }
}

// ── 设备信息 ──
const deviceOnline = ref(true)
const bootTime = Date.now() - (24 * 24 * 3600 + 18 * 3600 + 32 * 60) * 1000
const nowTick = ref(Date.now())
let tickTimer: ReturnType<typeof setInterval> | null = null
const uptimeText = computed(() => {
  const s = Math.floor((nowTick.value - bootTime) / 1000)
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60)
  return `${d}天 ${h}小时 ${m}分`
})

const deviceFields = computed(() => [
  { label: '设备时间', value: new Date(nowTick.value).toLocaleString('zh-CN', { hour12: false }), icon: Clock },
  { label: 'IP地址', value: '192.168.1.88', icon: Connection },
  { label: 'MAC地址', value: '00:1A:2B:3C:4D:5E', icon: Cpu },
])

const restarting = ref(false)
function onRestart() {
  ElMessageBox.confirm('确认立即重启 EdgeBox-3000?', '重启设备', {
    confirmButtonText: '重启', cancelButtonText: '取消', type: 'warning',
  }).then(() => {
    restarting.value = true
    addHistory('控制操作', '重启设备')
    setTimeout(() => {
      restarting.value = false
      toast('重启指令已下发,设备正在重启')
    }, 1500)
  }).catch(() => {})
}

const remoteOpen = ref(false)
function onRemoteConnect() {
  remoteOpen.value = false
  addHistory('远程连接', '建立 SSH 会话 192.168.1.88:22')
  toast('远程终端会话已建立(demo)')
}

function onMoreAction(cmd: string) {
  if (cmd === 'export') { addHistory('数据导出', '导出近 7 天运行数据'); toast('数据导出任务已创建') }
  if (cmd === 'log') info('打开日志查看器(demo)')
  if (cmd === 'offline') {
    deviceOnline.value = !deviceOnline.value
    addHistory('状态变更', deviceOnline.value ? '设备上线' : '设备离线')
    toast(deviceOnline.value ? '设备已上线' : '设备已离线')
  }
}

function goList() { info('返回设备列表(demo 无路由)') }

// ── 统计卡 ──
const stats = [
  { label: '在线设备', value: '128', unit: '', icon: Monitor, color: '#3b82f6', delta: '较昨日 ↑ 12 (10.34%)', deltaColor: '#22c55e' },
  { label: '离线设备', value: '12', unit: '', icon: Clock, color: '#9ca3af', delta: '较昨日 ↓ 2 (14.29%)', deltaColor: '#8a93a6' },
  { label: '告警数量', value: '3', unit: '', icon: Warning, color: '#f59e0b', delta: '较昨日 ↑ 1 (50.00%)', deltaColor: '#ef4444' },
  { label: '平均温度', value: '36.5', unit: '℃', icon: Sunny, color: '#22c55e', delta: '较昨日 ↓ 0.8 ℃', deltaColor: '#22c55e' },
  { label: '平均电压', value: '12.6', unit: 'V', icon: Lightning, color: '#22c55e', delta: '较昨日 ↑ 0.4 V', deltaColor: '#22c55e' },
  { label: '平均SOC', value: '78.6', unit: '%', icon: Odometer, color: '#8b5cf6', delta: '较昨日 ↑ 2.1 %', deltaColor: '#22c55e' },
]

// ── 运行趋势 ──
const ranges = ['1小时', '6小时', '12小时', '24小时', '7天']
const activeRange = ref('24小时')
const metrics = [
  { key: 'temp', label: '温度(℃)', unit: '℃', names: ['探头1', '探头2', '探头3'], colors: ['#3b82f6', '#22c55e', '#f59e0b'], bases: [36, 34, 38], amps: [4, 3.5, 4.5] },
  { key: 'voltage', label: '电压(V)', unit: 'V', names: ['总电压'], colors: ['#3b82f6'], bases: [12.6], amps: [0.4] },
  { key: 'current', label: '电流(A)', unit: 'A', names: ['电池电流'], colors: ['#8b5cf6'], bases: [-2.4], amps: [0.8] },
  { key: 'soc', label: 'SOC(%)', unit: '%', names: ['SOC'], colors: ['#22c55e'], bases: [78], amps: [3] },
]
const currentMetricKey = ref('temp')
const currentMetric = computed(() => metrics.find(m => m.key === currentMetricKey.value)!)

const trendChartRef = ref<HTMLElement>()
let chart: echarts.ECharts | null = null
let resizeObserver: ResizeObserver | null = null

function genSeriesData(base: number, amp: number, phase: number, points: number): [string, number][] {
  const pts: [string, number][] = []
  const spanMs = activeRange.value === '1小时' ? 3600e3 : activeRange.value === '6小时' ? 6 * 3600e3
    : activeRange.value === '12小时' ? 12 * 3600e3 : activeRange.value === '7天' ? 7 * 86400e3 : 86400e3
  const start = Date.now() - spanMs
  for (let i = 0; i <= points; i++) {
    const t = new Date(start + (i / points) * spanMs)
    const label = activeRange.value === '7天'
      ? `${t.getMonth() + 1}-${t.getDate()} ${String(t.getHours()).padStart(2, '0')}时`
      : `${String(t.getHours()).padStart(2, '0')}:${String(t.getMinutes()).padStart(2, '0')}`
    const v = base + amp * Math.sin((i / points) * Math.PI * 2 + phase) + Math.sin(i * 1.7) * amp * 0.12
    pts.push([label, Number(v.toFixed(1))])
  }
  return pts
}

function buildOption(): echarts.EChartsOption {
  const m = currentMetric.value
  const points = activeRange.value === '7天' ? 56 : 48
  const series = m.names.map((name, i) => ({
    name, type: 'line' as const, smooth: true, symbol: 'circle', symbolSize: 4,
    lineStyle: { width: 2, color: m.colors[i] }, itemStyle: { color: m.colors[i] },
    areaStyle: { color: m.colors[i], opacity: 0.06 },
    data: genSeriesData(m.bases[i], m.amps[i], i * 0.8, points).map(p => p[1]),
  }))
  const xData = genSeriesData(m.bases[0], m.amps[0], 0, points).map(p => p[0])
  return {
    grid: { left: 40, right: 16, top: 16, bottom: 46 },
    tooltip: {
      trigger: 'axis', backgroundColor: '#fff', borderColor: '#e5e9f0',
      textStyle: { color: '#1f2d3d', fontSize: 12 },
      valueFormatter: (v: number) => `${v} ${m.unit}`,
    },
    legend: { bottom: 0, itemWidth: 16, itemHeight: 2, textStyle: { fontSize: 12, color: '#3a4556' } },
    xAxis: {
      type: 'category', data: xData,
      axisLine: { lineStyle: { color: '#eef1f6' } }, axisTick: { show: false },
      axisLabel: { fontSize: 11, color: '#9aa3b2', interval: Math.floor(points / 6) },
    },
    yAxis: {
      type: 'value', scale: true,
      splitLine: { lineStyle: { color: '#eef1f6', type: 'dashed' } },
      axisLabel: { fontSize: 11, color: '#9aa3b2' },
    },
    series,
  }
}

function refreshChart() {
  chart?.setOption(buildOption(), { notMerge: true })
}

function onRangeChange(r: string) {
  activeRange.value = r
  refreshChart()
}
function onMetricChange(key: string) {
  currentMetricKey.value = key
  refreshChart()
}

// ── 温度探头 / MOS / 保护 ──
const probeDetailOpen = ref(false)
const mosDetailOpen = ref(false)
const protectOpen = ref(false)
const probes = [
  { name: '探头1', value: '36.2', color: '#22c55e', status: '正常', tagClass: 'tag-green' },
  { name: '探头2', value: '33.9', color: '#22c55e', status: '正常', tagClass: 'tag-green' },
  { name: '探头3', value: '38.2', color: '#f59e0b', status: '偏高', tagClass: 'tag-orange' },
]
const mosList = reactive([
  { name: '充电MOS', on: true },
  { name: '放电MOS', on: true },
])
function onMosToggle(m: { name: string; on: boolean }) {
  const target = !m.on
  ElMessageBox.confirm(`确认${target ? '开启' : '关闭'}${m.name}?`, 'MOS 控制', {
    confirmButtonText: '确认', cancelButtonText: '取消', type: 'warning',
  }).then(() => {
    m.on = target
    addHistory('MOS控制', `${m.name} ${target ? '开启' : '关闭'}`)
    toast(`${m.name}已${target ? '开启' : '关闭'}`)
  }).catch(() => {})
}

const protectItems = [
  { name: '单体过压保护', threshold: '3.65 V', current: '3.32 V' },
  { name: '单体欠压保护', threshold: '2.50 V', current: '3.32 V' },
  { name: '充电过流保护', threshold: '10 A', current: '2.1 A' },
  { name: '放电过流保护', threshold: '15 A', current: '2.4 A' },
  { name: '充电高温保护', threshold: '60 ℃', current: '38.2 ℃' },
  { name: '放电高温保护', threshold: '65 ℃', current: '38.2 ℃' },
  { name: '短路保护', threshold: '—', current: '未触发' },
  { name: '低温保护', threshold: '-10 ℃', current: '33.9 ℃' },
]

// ── 实时数据流(1s 滚动刷新) ──
const liveRunning = ref(true)
let liveTimer: ReturnType<typeof setInterval> | null = null
let streamSeq = 0

interface StreamRow { id: string; time: string; channel: string; item: string; value: string; unit: string }
const streamDefs = [
  { channel: '通道1', item: '电池电压', unit: 'V', base: 12.58, amp: 0.05 },
  { channel: '通道1', item: '电池电流', unit: 'A', base: -2.38, amp: 0.3 },
  { channel: '通道1', item: 'SOC', unit: '%', base: 78.6, amp: 0.4 },
  { channel: '通道2', item: '温度探头1', unit: '℃', base: 36.2, amp: 0.6 },
  { channel: '通道2', item: '温度探头2', unit: '℃', base: 33.9, amp: 0.5 },
]
const streamRows = ref<StreamRow[]>([])
const allStreamRows = ref<StreamRow[]>([])

function pushStreamTick() {
  const t = new Date()
  const time = `${String(t.getHours()).padStart(2, '0')}:${String(t.getMinutes()).padStart(2, '0')}:${String(t.getSeconds()).padStart(2, '0')}`
  streamSeq++
  const batch: StreamRow[] = streamDefs.map((d, i) => ({
    id: `s${streamSeq}-${i}`,
    time, channel: d.channel, item: d.item,
    value: (d.base + Math.sin(streamSeq / 6 + i) * d.amp).toFixed(d.unit === 'A' ? 2 : 1),
    unit: d.unit,
  }))
  streamRows.value = [...batch.slice(0, 5)]
  allStreamRows.value = [...batch, ...allStreamRows.value].slice(0, 50)
}

// ── 指令频率配置 ──
const freqOpen = ref(false)
const cmdFreqs = reactive([
  { id: 'read-basic', name: '读取基本信息', desc: '单体电压 + 温度基础帧', interval: 5000 },
  { id: 'read-cell', name: '读取单体电压', desc: '16 节单体电压', interval: 5000 },
  { id: 'read-hw', name: '读取硬件版本', desc: '硬件/固件版本确认', interval: 60000 },
  { id: 'read-combined', name: '读取综合信息', desc: 'SOC/电压/电流/MOS/保护', interval: 1000 },
  { id: 'read-prot', name: '读取保护历史', desc: '历史保护事件计数', interval: 10000 },
])
function onFreqSave(c: { id: string; name: string; interval: number }) {
  if (!c.interval || c.interval < 100) { ElMessage({ message: '频率不能小于 100ms', type: 'error' }); return }
  addHistory('配置更新', `修改「${c.name}」轮询频率为 ${c.interval}ms`)
  toast(`「${c.name}」频率已保存:${c.interval}ms`)
}

// ── 受控操作 ──
const ops = [
  { name: '重启设备', desc: '立即重启设备', icon: VideoPlay },
  { name: '恢复默认配置', desc: '恢复设备默认参数', icon: RefreshRight },
  { name: '升级固件', desc: '当前版本 v1.4.8', icon: UploadFilled },
  { name: '清除告警', desc: '清除所有告警记录', icon: Lock },
]
function onOpClick(op: { name: string }) {
  if (op.name === '重启设备') { onRestart(); return }
  const msgs: Record<string, string> = {
    '恢复默认配置': '确认恢复设备默认参数?当前自定义配置将被覆盖。',
    '升级固件': '确认将固件从 v1.4.8 升级到 v1.4.9?',
    '清除告警': '确认清除所有告警记录?该操作不可恢复。',
  }
  ElMessageBox.confirm(msgs[op.name] || `确认执行「${op.name}」?`, op.name, {
    confirmButtonText: '执行', cancelButtonText: '取消', type: 'warning',
  }).then(() => {
    addHistory('受控操作', op.name)
    toast(`「${op.name}」已执行`)
  }).catch(() => {})
}

// ── 操作历史 ──
let histSeq = 1
const historyRows = ref([
  { id: histSeq++, time: '2025-05-26 14:25:18', type: '配置更新', content: '修改轮询指令频率为 800ms', user: 'admin', ok: true },
])
function addHistory(type: string, content: string, ok = true) {
  const t = new Date()
  const time = `${t.getFullYear()}-${String(t.getMonth() + 1).padStart(2, '0')}-${String(t.getDate()).padStart(2, '0')} ${String(t.getHours()).padStart(2, '0')}:${String(t.getMinutes()).padStart(2, '0')}:${String(t.getSeconds()).padStart(2, '0')}`
  historyRows.value.unshift({ id: histSeq++, time, type, content, user: 'admin', ok })
}
const historyOpen = ref(false)
const allDataOpen = ref(false)

// ── 生命周期 ──
onMounted(() => {
  pushStreamTick()
  liveTimer = setInterval(() => { if (liveRunning.value) pushStreamTick() }, 1000)
  tickTimer = setInterval(() => { nowTick.value = Date.now() }, 60000)
  if (trendChartRef.value) {
    chart = echarts.init(trendChartRef.value)
    refreshChart()
    resizeObserver = new ResizeObserver(() => chart?.resize())
    resizeObserver.observe(trendChartRef.value)
  }
  nextTick(() => {
    if (searchOpen.value) searchInputRef.value?.focus()
  })
})

onUnmounted(() => {
  if (liveTimer) clearInterval(liveTimer)
  if (tickTimer) clearInterval(tickTimer)
  resizeObserver?.disconnect()
  chart?.dispose()
})
</script>

<style scoped>
/* ═══════ 整页布局 ═══════ */
.bms-demo {
  position: fixed; inset: 0; z-index: 3000;
  display: flex; background: #f4f6fa;
  font-size: 12px; color: #1f2d3d;
  overflow: hidden;
}

/* ═══════ 侧边栏 ═══════ */
.sidebar {
  width: 168px; flex-shrink: 0; height: 100%;
  background: #0b1b33; color: #c0c6d4;
  display: flex; flex-direction: column;
}
.sidebar-logo {
  height: 56px; display: flex; align-items: center; gap: 8px; padding: 0 16px;
}
.logo-hex { color: #3b82f6; font-size: 20px; }
.logo-text { color: #fff; font-size: 15px; font-weight: 600; }
.sidebar-nav { flex: 1; padding: 8px; display: flex; flex-direction: column; gap: 4px; }
.nav-item {
  height: 36px; display: flex; align-items: center; gap: 10px;
  padding: 0 12px; border-radius: 6px; font-size: 13px; color: #8a93a6;
  cursor: pointer; transition: background 0.15s;
}
.nav-item:hover { background: rgba(255, 255, 255, 0.06); color: #c0c6d4; }
.nav-item.active { background: #2563eb; color: #fff; }
.sidebar-footer { padding: 16px; }
.sys-status { display: flex; align-items: center; gap: 6px; font-size: 12px; color: #7ec98a; }
.sys-version { margin-top: 6px; font-size: 11px; color: #5a6b85; }

/* ═══════ 顶栏 ═══════ */
.main-area { flex: 1; display: flex; flex-direction: column; min-width: 0; }
.topbar {
  height: 56px; flex-shrink: 0; background: #fff;
  border-bottom: 1px solid #e5e9f0;
  display: flex; align-items: center; padding: 0 24px; gap: 24px;
}
.breadcrumb { display: flex; align-items: center; gap: 8px; color: #8a93a6; }
.crumb-parent { color: #8a93a6; }
.crumb-parent.link { cursor: pointer; }
.crumb-parent.link:hover { color: #2563eb; }
.crumb-current { color: #1f2d3d; }
.crumb-sep { color: #d9dee8; }
.topbar-search {
  flex: 1; max-width: 360px; height: 32px; margin: 0 auto;
  background: #f2f4f8; border-radius: 6px;
  display: flex; align-items: center; gap: 8px; padding: 0 10px;
  color: #9aa3b2; cursor: pointer; transition: background 0.15s;
}
.topbar-search:hover { background: #e8ecf3; }
.topbar-search input {
  flex: 1; border: none; outline: none; background: transparent;
  font-size: 12px; color: #1f2d3d; cursor: pointer;
}
.topbar-search kbd {
  background: #e5e9f0; border-radius: 4px; padding: 1px 6px;
  font-size: 11px; color: #8a93a6; font-family: inherit;
}
.topbar-right { display: flex; align-items: center; gap: 14px; }
.online-hint { display: flex; align-items: center; gap: 5px; color: #22c55e; }
.bell-wrap { position: relative; display: flex; color: #3a4556; cursor: pointer; }
.bell-badge {
  position: absolute; top: -6px; right: -8px;
  background: #ef4444; color: #fff; font-size: 10px;
  border-radius: 8px; padding: 0 4px; line-height: 14px;
}
.user-trigger { display: flex; align-items: center; gap: 6px; cursor: pointer; outline: none; }
.avatar {
  width: 28px; height: 28px; border-radius: 50%;
  background: #3b82f6; color: #fff;
  display: flex; align-items: center; justify-content: center; font-size: 13px;
}
.username { font-size: 13px; }

/* ═══════ 内容区 ═══════ */
.content { flex: 1; overflow-y: auto; padding: 16px 24px 24px; }
.back-link { color: #2563eb; cursor: pointer; display: inline-block; margin-bottom: 12px; }
.card {
  background: #fff; border-radius: 8px;
  box-shadow: 0 1px 2px rgba(16, 24, 40, 0.04);
  padding: 16px 20px;
}
.card-head { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; }
.card-title { font-size: 14px; font-weight: 600; }
.more-link { margin-left: auto; color: #2563eb; cursor: pointer; }
.more-link:hover { text-decoration: underline; }

/* 圆点 / 标签 */
.dot { display: inline-block; width: 6px; height: 6px; border-radius: 50%; }
.dot-green { background: #22c55e; }
.dot-orange { background: #f59e0b; }
.dot-red { background: #ef4444; }
.tag { border-radius: 4px; padding: 2px 8px; font-size: 11px; }
.tag-green { color: #22c55e; background: #e8f7ee; }
.tag-orange { color: #f59e0b; background: #fef3e2; }
.tag-red { color: #ef4444; background: #fdecec; }

/* ═══════ 设备信息卡 ═══════ */
.device-card { display: flex; align-items: center; gap: 32px; min-height: 150px; }
.device-left { display: flex; gap: 16px; align-items: flex-start; }
.device-photo {
  width: 110px; height: 80px; flex-shrink: 0;
  background: #f2f4f8; border-radius: 6px;
  display: flex; align-items: center; justify-content: center;
}
.device-name-row { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
.device-name { font-size: 16px; font-weight: 600; }
.meta-row { display: flex; gap: 12px; line-height: 22px; }
.meta-label { color: #8a93a6; width: 52px; flex-shrink: 0; }
.meta-value { color: #3a4556; }
.meta-value.link { color: #2563eb; display: inline-flex; align-items: center; gap: 3px; cursor: pointer; }
.device-mid { display: flex; flex-direction: column; gap: 14px; margin-left: auto; }
.device-field { display: flex; align-items: center; gap: 8px; }
.field-icon {
  width: 24px; height: 24px; border-radius: 50%; background: #f2f4f8;
  display: flex; align-items: center; justify-content: center; color: #64748b;
}
.field-label { font-size: 11px; color: #8a93a6; }
.field-value { font-size: 12px; color: #1f2d3d; }
.device-actions { display: flex; gap: 8px; margin-left: 24px; }
.btn {
  height: 32px; padding: 0 16px; border-radius: 6px; font-size: 12px;
  cursor: pointer; border: 1px solid transparent; background: none;
  transition: opacity 0.15s, background 0.15s;
}
.btn:disabled { opacity: 0.6; cursor: not-allowed; }
.btn-primary { background: #2563eb; color: #fff; }
.btn-primary:hover:not(:disabled) { background: #1d4fd7; }
.btn-plain { border-color: #d9dee8; color: #3a4556; background: #fff; }
.btn-plain:hover { background: #f7f9fc; }
.btn-mini {
  height: 22px; padding: 0 10px; border-radius: 4px; font-size: 11px;
  border: 1px solid #d9dee8; background: #fff; color: #3a4556; cursor: pointer;
}
.btn-mini:hover { background: #f7f9fc; }

/* ═══════ 统计卡 ═══════ */
.stat-row { display: grid; grid-template-columns: repeat(6, 1fr); gap: 16px; margin-top: 16px; }
.stat-card {
  display: flex; align-items: center; gap: 12px; min-height: 96px; padding: 14px 16px;
  cursor: pointer; transition: box-shadow 0.15s, transform 0.15s;
}
.stat-card:hover { box-shadow: 0 4px 12px rgba(16, 24, 40, 0.08); transform: translateY(-1px); }
.stat-icon {
  width: 40px; height: 40px; border-radius: 50%; flex-shrink: 0;
  display: flex; align-items: center; justify-content: center;
}
.stat-label { color: #8a93a6; }
.stat-value { font-size: 22px; font-weight: 700; line-height: 1.3; }
.stat-unit { font-size: 13px; font-weight: 400; margin-left: 4px; }
.stat-delta { font-size: 11px; }

/* ═══════ 中部行 ═══════ */
.mid-row { display: flex; gap: 16px; margin-top: 16px; }
.chart-card { width: 55%; }
.head-right { margin-left: auto; display: flex; align-items: center; gap: 12px; }
.metric-select {
  height: 24px; padding: 0 10px; background: #fff; border: 1px solid #d9dee8;
  border-radius: 4px; font-size: 12px; color: #3a4556; cursor: pointer;
}
.range-group { display: flex; gap: 2px; }
.range-btn {
  height: 24px; padding: 0 10px; border: 1px solid transparent; border-radius: 4px;
  background: none; font-size: 12px; color: #8a93a6; cursor: pointer;
}
.range-btn.active { border-color: #2563eb; color: #2563eb; background: #eff6ff; }
.trend-chart { height: 290px; }
.right-col { flex: 1; display: flex; flex-direction: column; gap: 16px; }

.probe-list { display: flex; gap: 12px; }
.probe-item {
  flex: 1; background: #f7f9fc; border-radius: 6px; padding: 10px;
  display: flex; flex-direction: column; gap: 6px; align-items: flex-start;
}
.probe-name { color: #8a93a6; }
.probe-value { font-size: 16px; font-weight: 600; }

.mos-row { display: flex; align-items: center; justify-content: space-between; padding: 6px 0; }
.mos-name { color: #3a4556; }
.mos-right { display: flex; align-items: center; gap: 10px; }
.switch {
  width: 36px; height: 20px; border-radius: 10px; background: #d9dee8;
  position: relative; transition: background 0.2s; cursor: pointer;
}
.switch.on { background: #2563eb; }
.switch-knob {
  position: absolute; top: 2px; left: 2px; width: 16px; height: 16px;
  border-radius: 50%; background: #fff; transition: left 0.2s;
}
.switch.on .switch-knob { left: 18px; }

.protect-card {
  position: relative; display: flex; align-items: center; justify-content: space-between;
  cursor: pointer; transition: box-shadow 0.15s;
}
.protect-card:hover { box-shadow: 0 4px 12px rgba(16, 24, 40, 0.08); }
.protect-menu { position: absolute; top: 12px; right: 14px; color: #8a93a6; }
.protect-left { display: flex; align-items: center; gap: 10px; }
.protect-title { font-size: 13px; font-weight: 600; color: #22c55e; }
.protect-sub { font-size: 11px; color: #8a93a6; margin-top: 2px; }
.protect-counters { display: flex; gap: 10px; }
.p-counter {
  display: flex; align-items: center; gap: 5px;
  border: 1px solid #e5e9f0; border-radius: 4px; padding: 4px 10px; color: #3a4556;
}
.p-counter b { font-size: 14px; }

/* ═══════ 底部行 ═══════ */
.bottom-row { display: flex; gap: 16px; margin-top: 16px; }
.stream-card { width: 55%; }
.stream-freq { font-size: 11px; color: #8a93a6; }
.data-table { width: 100%; border-collapse: collapse; }
.data-table th {
  background: #f7f9fc; color: #8a93a6; font-weight: 400;
  text-align: left; padding: 8px 12px;
}
.data-table th:first-child { border-radius: 4px 0 0 4px; }
.data-table th:last-child { border-radius: 0 4px 4px 0; }
.data-table td {
  padding: 8px 12px; color: #3a4556;
  border-bottom: 1px solid #f0f2f7;
}
.data-table td.num { font-variant-numeric: tabular-nums; }
.status-ok { display: inline-flex; align-items: center; gap: 5px; color: #22c55e; }
.table-footer { text-align: center; padding-top: 12px; }
.table-footer .more-link { margin-left: 0; }

.collapse-card {
  display: flex; align-items: center; justify-content: space-between;
  padding: 14px 20px; cursor: pointer;
}
.collapse-arrow { transition: transform 0.2s; }
.collapse-arrow.open { transform: rotate(90deg); }
.freq-panel { padding: 8px 20px 16px; }
.freq-row {
  display: flex; align-items: center; justify-content: space-between;
  padding: 10px 0; border-bottom: 1px solid #f0f2f7; gap: 12px;
}
.freq-row:last-child { border-bottom: none; }
.freq-name { font-size: 12px; font-weight: 600; }
.freq-desc { font-size: 11px; color: #9aa3b2; margin-top: 2px; }
.freq-edit { display: flex; align-items: center; gap: 6px; flex-shrink: 0; }
.freq-input {
  width: 76px; height: 26px; border: 1px solid #d9dee8; border-radius: 4px;
  padding: 0 8px; font-size: 12px; outline: none;
}
.freq-input:focus { border-color: #2563eb; }
.freq-unit { color: #8a93a6; font-size: 11px; }

.ops-card { position: relative; }
.ops-sub { color: #9aa3b2; font-size: 11px; margin: -6px 0 10px; }
.ops-list { display: flex; align-items: stretch; gap: 12px; }
.op-item {
  flex: 1; min-width: 0; display: flex; align-items: center; gap: 10px;
  border: 1px solid #e5e9f0; border-radius: 6px; padding: 10px 12px; cursor: pointer;
  transition: border-color 0.15s, box-shadow 0.15s;
}
.op-item:hover { border-color: #2563eb; box-shadow: 0 2px 8px rgba(37, 99, 235, 0.12); }
.op-text { min-width: 0; }
.op-name { font-size: 12px; font-weight: 600; white-space: nowrap; }
.op-desc { font-size: 11px; color: #9aa3b2; margin-top: 2px; white-space: nowrap; }

/* ═══════ 弹窗内部 ═══════ */
.dialog-search-input {
  width: 100%; height: 36px; border: 1px solid #d9dee8; border-radius: 6px;
  padding: 0 12px; font-size: 13px; outline: none; box-sizing: border-box;
}
.dialog-search-input:focus { border-color: #2563eb; }
.search-results { margin-top: 10px; max-height: 260px; overflow-y: auto; }
.search-result-item {
  padding: 8px 10px; border-radius: 4px; cursor: pointer; font-size: 13px;
}
.search-result-item:hover { background: #f2f6ff; color: #2563eb; }
.search-empty { padding: 20px; text-align: center; color: #9aa3b2; font-size: 12px; }

.notif-head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; color: #8a93a6; font-size: 12px; }
.notif-item {
  padding: 10px 12px; border-radius: 6px; cursor: pointer; margin-bottom: 6px;
  background: #f7f9fc; transition: background 0.15s;
}
.notif-item.unread { background: #eff6ff; }
.notif-item:hover { background: #e8effe; }
.notif-title { font-size: 13px; color: #1f2d3d; }
.notif-time { font-size: 11px; color: #9aa3b2; margin-top: 3px; }

.remote-body { display: flex; flex-direction: column; gap: 10px; }
.remote-row { display: flex; justify-content: space-between; font-size: 13px; padding: 8px 12px; background: #f7f9fc; border-radius: 6px; }
.remote-row span { color: #8a93a6; }

.mos-detail-row {
  display: flex; align-items: center; gap: 12px; padding: 10px 0;
  border-bottom: 1px solid #f0f2f7; font-size: 13px;
}
.mos-detail-row:last-child { border-bottom: none; }
.mos-detail-meta { margin-left: auto; color: #9aa3b2; font-size: 11px; }

.fade-enter-active, .fade-leave-active { transition: opacity 0.18s; }
.fade-enter-from, .fade-leave-to { opacity: 0; }

/* ═══════ 响应式断点 ═══════ */
/* ≤1200px 平板:统计卡 6→3 列,中部/底部行纵向堆叠 */
@media (max-width: 1200px) {
  .stat-row { grid-template-columns: repeat(3, 1fr); }
  .mid-row, .bottom-row { flex-direction: column; }
  .chart-card, .stream-card { width: 100%; }
}

/* ≤768px 移动端:收侧边栏、设备卡堆叠、统计卡 2 列、表格横向滚动 */
@media (max-width: 768px) {
  .sidebar { display: none; }
  .topbar { padding: 0 12px; gap: 12px; }
  .topbar-search { display: none; }
  .breadcrumb { font-size: 12px; }
  .content { padding: 12px; }

  .device-card { flex-direction: column; align-items: stretch; gap: 16px; }
  .device-mid { margin-left: 0; flex-direction: row; flex-wrap: wrap; gap: 10px 24px; }
  .device-actions { margin-left: 0; flex-wrap: wrap; }

  .stat-row { grid-template-columns: repeat(2, 1fr); gap: 10px; }
  .stat-card { min-height: 0; padding: 12px; }
  .stat-value { font-size: 18px; }

  .probe-list { flex-direction: column; }
  .protect-card { flex-direction: column; align-items: flex-start; gap: 12px; }
  .protect-counters { flex-wrap: wrap; }

  .ops-list { flex-wrap: wrap; }
  .op-item { flex: 1 1 45%; min-width: 140px; }

  .stream-card, .history-card { overflow: hidden; }
  .data-table { min-width: 560px; }
  .table-scroll { overflow-x: auto; }

  .range-group { flex-wrap: wrap; }
  .head-right { flex-wrap: wrap; justify-content: flex-end; }
  .trend-chart { height: 220px; }
}

/* ≤480px 小屏:统计卡数值缩小、按钮占满 */
@media (max-width: 480px) {
  .stat-icon { width: 32px; height: 32px; }
  .stat-delta { font-size: 10px; }
  .device-photo { width: 84px; height: 64px; }
  .device-actions .btn { flex: 1; }
  .card { padding: 12px 14px; }
  .card-head { flex-wrap: wrap; }
}
</style>
