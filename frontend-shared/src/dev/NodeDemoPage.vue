<template>
  <!-- 节点详情页设计稿像素级 demo:静态 mock 数据,仅开发环境可见(DEV 门禁路由) -->
  <!-- 复刻 designs/node.png 整页:自带侧边栏+顶栏,覆盖在系统布局之上的全屏 demo -->
  <div class="node-demo">
    <!-- ══════════ 左侧边栏 ══════════ -->
    <aside class="sidebar">
      <div class="sidebar-logo">
        <svg width="20" height="20" viewBox="0 0 24 24" fill="none" aria-hidden="true">
          <path d="M5 17 L12 7 L19 17" stroke="#2563EB" stroke-width="1.5" />
          <circle cx="5" cy="17" r="3" fill="#22C55E" />
          <circle cx="12" cy="7" r="3" fill="#2563EB" />
          <circle cx="19" cy="17" r="3" fill="#22C55E" />
        </svg>
        <span class="logo-text">EHomeSystem</span>
      </div>

      <nav class="sidebar-nav">
        <template v-for="item in navItems" :key="item.label">
          <div class="nav-item" :class="{ expanded: item.expanded }">
            <el-icon :size="15"><component :is="item.icon" /></el-icon>
            <span class="nav-label">{{ item.label }}</span>
            <span v-if="item.badge" class="nav-badge">{{ item.badge }}</span>
            <el-icon v-if="item.arrow" :size="10" class="nav-arrow" :class="{ up: item.expanded }">
              <ArrowDown />
            </el-icon>
          </div>
          <div v-if="item.children" class="nav-children">
            <div v-for="c in item.children" :key="c" class="nav-child" :class="{ active: c === '节点详情' }">
              {{ c }}
            </div>
          </div>
        </template>
      </nav>

      <div class="sidebar-bottom">
        <div class="space-switch">
          <el-icon :size="14"><Grid /></el-icon>
          <div class="space-text">
            <div class="space-label">当前空间</div>
            <div class="space-name">默认空间</div>
          </div>
          <el-icon :size="10"><ArrowDown /></el-icon>
        </div>
        <div class="sidebar-version">v2.1.0</div>
      </div>
    </aside>

    <!-- ══════════ 右侧主区 ══════════ -->
    <div class="main-area">
      <!-- 顶栏 -->
      <header class="topbar">
        <el-icon :size="16" class="fold-btn"><Fold /></el-icon>
        <div class="breadcrumb">
          <span class="crumb">节点管理</span>
          <span class="crumb-sep">/</span>
          <span class="crumb crumb-current">节点详情</span>
        </div>
        <div class="topbar-search">
          <el-icon :size="14"><Search /></el-icon>
          <input placeholder="搜索设备、节点、通道、配置..." readonly />
          <kbd>Ctrl + K</kbd>
        </div>
        <div class="topbar-right">
          <span class="bell-wrap">
            <el-icon :size="17"><Bell /></el-icon>
            <span class="bell-badge">9</span>
          </span>
          <el-icon :size="17" class="help-icon"><QuestionFilled /></el-icon>
          <span class="avatar">A</span>
          <span class="username">admin</span>
          <el-icon :size="11"><ArrowDown /></el-icon>
        </div>
      </header>

      <!-- 内容区 -->
      <main class="content">
        <!-- 页头 -->
        <div class="page-head">
          <div class="head-left">
            <div class="head-title-row">
              <el-icon :size="18" class="back-arrow"><ArrowLeft /></el-icon>
              <span class="head-title">节点名称</span>
              <el-icon :size="14" class="edit-icon"><EditPen /></el-icon>
              <span class="status-pill"><span class="dot dot-green"></span>在线</span>
            </div>
            <div class="head-sub">设备ID: 30EDA0A9A808 · 型号: esp32s3</div>
          </div>
          <div class="head-actions">
            <button class="btn btn-default"><el-icon :size="13"><Refresh /></el-icon>刷新</button>
            <button class="btn btn-default"><el-icon :size="13"><SwitchButton /></el-icon>重启节点</button>
            <button class="btn btn-primary">更多操作<el-icon :size="11"><ArrowDown /></el-icon></button>
          </div>
        </div>

        <!-- Row 1:基本信息 / 配置同步 / 运行指标 -->
        <div class="row row1">
          <!-- 基本信息 -->
          <section class="card">
            <div class="card-head">
              <el-icon :size="16" class="card-icon"><InfoFilled /></el-icon>
              <span class="card-title">基本信息</span>
              <el-icon :size="14" class="card-head-action"><Refresh /></el-icon>
            </div>
            <div class="info-grid">
              <div class="info-cell">
                <span class="info-label">设备ID</span>
                <span class="info-value mono">30EDA0A9A808
                  <el-icon :size="13" class="copy-icon"><CopyDocument /></el-icon>
                </span>
              </div>
              <div class="info-cell">
                <span class="info-label">连接质量</span>
                <span class="info-value">
                  <span class="signal"><i></i><i></i><i></i><i></i></span>
                  <span class="value-green">优 (100%)</span>
                </span>
              </div>
              <div class="info-cell">
                <span class="info-label">型号</span>
                <span class="info-value">esp32s3</span>
              </div>
              <div class="info-cell">
                <span class="info-label">延迟</span>
                <span class="info-value value-green">12 ms</span>
              </div>
              <div class="info-cell">
                <span class="info-label">固件版本</span>
                <span class="info-value">2.5.18 <span class="tag tag-green">最新</span></span>
              </div>
              <div class="info-cell">
                <span class="info-label">上线时间</span>
                <span class="info-value">2026-08-08 21:18:42</span>
              </div>
              <div class="info-cell">
                <span class="info-label">状态</span>
                <span class="info-value"><span class="dot dot-green"></span>在线</span>
              </div>
              <div class="info-cell">
                <span class="info-label">在线时长</span>
                <span class="info-value">2天 2小时 13分钟</span>
              </div>
            </div>
          </section>

          <!-- 配置同步状态 -->
          <section class="card">
            <div class="card-head">
              <el-icon :size="16" class="card-icon"><Connection /></el-icon>
              <span class="card-title">配置同步状态</span>
            </div>
            <div class="sync-list">
              <div class="sync-row">
                <span class="sync-label">协议版本</span>
                <span class="sync-value">2.6</span>
              </div>
              <div class="sync-row">
                <span class="sync-label">最近配置清单标识</span>
                <span class="sync-value mono">v2-9d93aa95
                  <el-icon :size="13" class="copy-icon"><CopyDocument /></el-icon>
                </span>
              </div>
              <div class="sync-row">
                <span class="sync-label">最近同步时间</span>
                <span class="sync-value">2026-08-10 11:33:59</span>
              </div>
              <div class="sync-row">
                <span class="sync-label">同步标识</span>
                <span class="sync-value mono uuid">84a9765f-35ac-4a08-9acd-10538ac508d4
                  <el-icon :size="13" class="copy-icon"><CopyDocument /></el-icon>
                </span>
              </div>
              <div class="sync-row">
                <span class="sync-label">状态</span>
                <span class="sync-value"><span class="tag tag-green">已同步</span></span>
              </div>
            </div>
            <div class="sync-foot">
              <button class="btn btn-ghost">查看配置详情</button>
            </div>
          </section>

          <!-- 运行指标(实时) -->
          <section class="card">
            <div class="card-head">
              <el-icon :size="16" class="card-icon"><Odometer /></el-icon>
              <span class="card-title">运行指标(实时)</span>
            </div>
            <div class="metric-body">
              <div class="ring-wrap">
                <div class="ring">
                  <svg width="110" height="110" viewBox="0 0 110 110">
                    <circle cx="55" cy="55" r="47" fill="none" stroke="#E5E7EB" stroke-width="8" />
                    <circle cx="55" cy="55" r="47" fill="none" stroke="#22C55E" stroke-width="8"
                            stroke-linecap="round" transform="rotate(-90 55 55)" />
                  </svg>
                  <div class="ring-text">100%</div>
                </div>
                <div class="ring-label">连接质量</div>
              </div>
              <div class="metric-list">
                <div class="metric-row"><span>内存使用率</span><b>46%</b></div>
                <div class="metric-row"><span>CPU 使用率</span><b>18%</b></div>
                <div class="metric-row"><span>运行时间</span><b>2天2小时</b></div>
                <div class="metric-row"><span>重启次数</span><b>3次</b></div>
                <div class="metric-divider"></div>
                <div class="metric-row"><span>数据上报间隔</span><b>5 s</b></div>
              </div>
            </div>
          </section>
        </div>

        <!-- Row 2:总线配置 / DMA 通道一览 -->
        <div class="row row2">
          <!-- 总线配置 -->
          <section class="card">
            <div class="card-head">
              <el-icon :size="16" class="card-icon"><Operation /></el-icon>
              <span class="card-title">总线配置</span>
            </div>
            <div class="bus-tabs">
              <div v-for="t in busTabs" :key="t" class="bus-tab"
                   :class="{ active: t === activeTab }" @click="activeTab = t">{{ t }}</div>
            </div>
            <div class="bus-body">
              <div class="bus-tree">
                <div v-for="b in busTree" :key="b.name" class="tree-item"
                     :class="{ selected: b.name === selectedBus }" @click="selectedBus = b.name">
                  <el-icon :size="9" class="tree-arrow">
                    <ArrowDown v-if="b.name === selectedBus" />
                    <ArrowRight v-else />
                  </el-icon>
                  <el-icon :size="14" :color="b.color"><component :is="b.icon" /></el-icon>
                  <span class="tree-name">{{ b.name }}</span>
                  <span class="tree-count">{{ b.count }}</span>
                </div>
              </div>
              <div class="bus-detail">
                <div class="bus-detail-head">
                  <span class="bus-detail-title">{{ selectedBus }} ({{ selectedBusCount }})</span>
                  <button class="btn btn-primary btn-sm"><el-icon :size="12"><Plus /></el-icon>添加资源</button>
                </div>
                <table v-if="selectedBus === 'I2C 总线'" class="tbl">
                  <thead>
                    <tr><th>资源名称</th><th>总线地址</th><th>所在通道</th><th>速率</th><th>状态</th><th>操作</th></tr>
                  </thead>
                  <tbody>
                    <tr v-for="r in i2cResources" :key="r.name">
                      <td>{{ r.name }}</td>
                      <td class="mono">{{ r.addr }}</td>
                      <td>{{ r.channel }}</td>
                      <td>{{ r.speed }}</td>
                      <td><span class="dot dot-green"></span><span class="tag tag-green">正常</span></td>
                      <td>
                        <span class="row-actions">
                          <button class="icon-btn"><el-icon :size="14"><EditPen /></el-icon></button>
                          <button class="icon-btn"><el-icon :size="14"><Tools /></el-icon></button>
                          <button class="icon-btn"><el-icon :size="14"><MoreFilled /></el-icon></button>
                        </span>
                      </td>
                    </tr>
                  </tbody>
                </table>
                <div v-else class="bus-empty">暂无资源</div>
              </div>
            </div>
          </section>

          <!-- DMA 通道一览 -->
          <section class="card">
            <div class="card-head">
              <el-icon :size="16" class="card-icon"><Cpu /></el-icon>
              <span class="card-title">DMA 通道一览</span>
            </div>
            <table class="tbl">
              <thead>
                <tr><th>通道</th><th>用途</th><th>源地址</th><th>目标地址</th><th>传输大小</th><th>状态</th><th>操作</th></tr>
              </thead>
              <tbody>
                <tr v-for="d in dmaRows" :key="d.ch">
                  <td>{{ d.ch }}</td>
                  <td>{{ d.purpose }}</td>
                  <td class="mono">{{ d.src }}</td>
                  <td class="mono">{{ d.dst }}</td>
                  <td>{{ d.size }}</td>
                  <td>
                    <span class="dot" :class="d.running ? 'dot-blue' : 'dot-green'"></span>
                    <span v-if="d.running" class="tag tag-blue">进行中</span>
                    <span v-else>空闲</span>
                  </td>
                  <td>
                    <span class="row-actions">
                      <button class="icon-btn"><el-icon :size="14"><VideoPlay /></el-icon></button>
                      <button class="icon-btn"><el-icon :size="14"><EditPen /></el-icon></button>
                      <button class="icon-btn"><el-icon :size="14"><MoreFilled /></el-icon></button>
                    </span>
                  </td>
                </tr>
              </tbody>
            </table>
          </section>
        </div>

        <!-- Row 3:关联设备 / OTA 升级历史 -->
        <div class="row row3">
          <!-- 关联设备 -->
          <section class="card">
            <div class="card-head">
              <el-icon :size="16" class="card-icon"><Monitor /></el-icon>
              <span class="card-title">关联设备</span>
              <button class="btn btn-primary btn-sm card-head-btn">
                <el-icon :size="12"><Plus /></el-icon>创建设备
              </button>
            </div>
            <table class="tbl tbl-compact">
              <thead>
                <tr><th>名称</th><th>类型</th><th>总线地址</th><th>所在通道</th><th>最新数据</th><th>状态</th><th>最后数据时间</th><th>操作</th></tr>
              </thead>
              <tbody>
                <tr v-for="dev in deviceRows" :key="dev.name">
                  <td>{{ dev.name }}</td>
                  <td>{{ dev.type }}</td>
                  <td class="mono">{{ dev.addr }}</td>
                  <td>{{ dev.channel }}</td>
                  <td>{{ dev.data }}</td>
                  <td>
                    <span class="dot" :class="dev.online ? 'dot-green' : 'dot-gray'"></span>
                    <span :class="{ 'value-gray': !dev.online }">{{ dev.online ? '在线' : '离线' }}</span>
                  </td>
                  <td class="nowrap">{{ dev.time }}</td>
                  <td>
                    <span class="row-actions">
                      <button class="icon-btn"><el-icon :size="14"><View /></el-icon></button>
                      <button class="icon-btn"><el-icon :size="14"><EditPen /></el-icon></button>
                      <button class="icon-btn"><el-icon :size="14"><MoreFilled /></el-icon></button>
                    </span>
                  </td>
                </tr>
              </tbody>
            </table>
            <div class="card-foot">
              <span class="total">共 3 条</span>
              <div class="pager">
                <button class="pg-btn">«</button>
                <button class="pg-btn">‹</button>
                <button class="pg-btn current">1</button>
                <button class="pg-btn">›</button>
              </div>
              <div class="page-size">10 条/页<el-icon :size="10"><ArrowDown /></el-icon></div>
            </div>
          </section>

          <!-- OTA 升级历史 -->
          <section class="card">
            <div class="card-head">
              <el-icon :size="16" class="card-icon"><UploadFilled /></el-icon>
              <span class="card-title">OTA 升级历史</span>
            </div>
            <table class="tbl">
              <thead>
                <tr><th>版本号</th><th>升级方式</th><th>开始时间</th><th>结束时间</th><th>状态</th><th>操作人</th><th>操作</th></tr>
              </thead>
              <tbody>
                <tr v-for="o in otaRows" :key="o.version">
                  <td>{{ o.version }}</td>
                  <td>{{ o.method }}</td>
                  <td class="nowrap">{{ o.start }}</td>
                  <td class="nowrap">{{ o.end }}</td>
                  <td><span class="dot dot-green"></span><span class="value-green">成功</span></td>
                  <td>{{ o.operator }}</td>
                  <td><a class="link">详情</a></td>
                </tr>
              </tbody>
            </table>
            <div class="card-foot">
              <span class="total">共 3 条</span>
              <div class="pager">
                <button class="pg-btn">«</button>
                <button class="pg-btn">‹</button>
                <button class="pg-btn current">1</button>
                <button class="pg-btn">›</button>
              </div>
              <div class="page-size">10 条/页<el-icon :size="10"><ArrowDown /></el-icon></div>
            </div>
          </section>
        </div>

        <!-- Row 4:系统日志 -->
        <div class="row">
          <section class="card log-card">
            <div class="log-toolbar">
              <span class="log-bar"></span>
              <span class="log-title">系统日志</span>
              <div class="fake-select">全部级别<el-icon :size="10"><ArrowDown /></el-icon></div>
              <div class="fake-select">全部模块<el-icon :size="10"><ArrowDown /></el-icon></div>
              <div class="date-range">
                <span>2026-08-10 00:00:00</span>
                <span class="range-arrow">→</span>
                <span>2026-08-10 23:59:59</span>
                <el-icon :size="13" class="range-icon"><Calendar /></el-icon>
              </div>
              <div class="log-search">
                <el-icon :size="13"><Search /></el-icon>
                <input placeholder="搜索日志内容..." readonly />
              </div>
              <div class="log-actions">
                <button class="btn btn-primary btn-sm">暂停</button>
                <button class="btn btn-default btn-sm">清屏</button>
                <button class="btn btn-default btn-sm"><el-icon :size="12"><Download /></el-icon>导出</button>
                <button class="btn btn-default btn-sm"><el-icon :size="12"><Bottom /></el-icon>回到底部</button>
              </div>
            </div>
            <div class="log-console">
              <div v-for="(l, i) in logLines" :key="i" class="log-line">
                <span class="log-ts">{{ l.ts }}</span>
                <span class="log-tag" :class="l.cls">[{{ l.level }}]</span>
                <span class="log-tag" :class="l.cls">[{{ l.mod }}]</span>
                <span class="log-msg">{{ l.msg }}</span>
              </div>
              <div class="autoscroll" @click="autoScroll = !autoScroll">
                <span class="autoscroll-text">自动滚动: {{ autoScroll ? '开启' : '关闭' }}</span>
                <span class="switch" :class="{ on: autoScroll }"></span>
              </div>
            </div>
          </section>
        </div>
      </main>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed } from 'vue'
import {
  ArrowDown, ArrowRight, ArrowLeft, Fold, Search, Bell, QuestionFilled,
  EditPen, Refresh, SwitchButton, Plus, CopyDocument, View, Tools, MoreFilled,
  VideoPlay, Download, Bottom, Calendar, Grid,
  Odometer, Share, Monitor, Cpu, Connection, DataLine, Box, Document,
  MagicStick, TrendCharts, Notebook, Setting, Operation, InfoFilled,
  Lightning, Histogram, UploadFilled,
} from '@element-plus/icons-vue'

// ── 侧边栏菜单 ──
const navItems = [
  { label: '仪表盘', icon: Odometer },
  { label: '节点管理', icon: Share, expanded: true, arrow: true, children: ['节点列表', '节点详情'] },
  { label: '边缘设备', icon: Monitor },
  { label: '逻辑设备', icon: Cpu },
  { label: '通道管理', icon: Connection },
  { label: '数据面板', icon: DataLine },
  { label: '告警中心', icon: Bell, badge: 12 },
  { label: '固件管理', icon: Box },
  { label: '配置模板', icon: Document },
  { label: '规则引擎', icon: MagicStick },
  { label: '系统监控', icon: TrendCharts, arrow: true },
  { label: '审计日志', icon: Notebook },
  { label: '系统设置', icon: Setting, arrow: true },
]

// ── 总线配置 ──
const busTabs = ['硬件资源管理', '通道终端调试']
const activeTab = ref('硬件资源管理')

const busTree = [
  { name: 'I2C 总线', count: 2, color: '#1677FF', icon: Cpu },
  { name: 'UART 串口', count: 3, color: '#3B82F6', icon: Connection },
  { name: 'SPI 总线', count: 2, color: '#F5483C', icon: Lightning },
  { name: 'ADC 模块', count: 2, color: '#FA8C16', icon: Histogram },
  { name: 'GPIO', count: 8, color: '#722ED1', icon: Grid },
  { name: 'PWM', count: 2, color: '#13C2C2', icon: MagicStick },
]
const selectedBus = ref('I2C 总线')
const selectedBusCount = computed(() => busTree.find(b => b.name === selectedBus.value)?.count ?? 0)

const i2cResources = [
  { name: 'I2C-1', addr: '0x40', channel: '通道 1', speed: '400 kHz' },
  { name: 'I2C-2', addr: '0x48', channel: '通道 1', speed: '100 kHz' },
]

// ── DMA 通道一览 ──
const dmaRows = [
  { ch: 'DMA0', purpose: 'UART1 RX', src: '0x3FFB0000', dst: '0x3FFB1000', size: '256 Bytes', running: false },
  { ch: 'DMA1', purpose: 'SPI1 TX', src: '0x3FFB2000', dst: '0x3FFB3000', size: '512 Bytes', running: true },
  { ch: 'DMA2', purpose: 'I2C1 RX', src: '0x3FFB4000', dst: '0x3FFB5000', size: '128 Bytes', running: false },
  { ch: 'DMA3', purpose: 'ADC', src: '0x3FFB6000', dst: '0x3FFB7000', size: '1024 Bytes', running: false },
]

// ── 关联设备 ──
const deviceRows = [
  { name: '温湿度传感器', type: '传感器', addr: '0x40', channel: 'I2C-1', data: '25.6℃ / 60%', online: true, time: '2026-08-10 11:34:21' },
  { name: '环境光传感器', type: '传感器', addr: '0x23', channel: 'I2C-2', data: '1280 lux', online: true, time: '2026-08-10 11:34:20' },
  { name: '气压传感器', type: '传感器', addr: '0x76', channel: 'SPI-1', data: '1013.2 hPa', online: false, time: '2026-08-10 10:58:03' },
]

// ── OTA 升级历史 ──
const otaRows = [
  { version: '2.5.18', method: '远程升级', start: '2026-08-08 21:18:20', end: '2026-08-08 21:18:42', operator: 'admin' },
  { version: '2.5.15', method: '远程升级', start: '2026-07-15 10:22:11', end: '2026-07-15 10:23:02', operator: 'admin' },
  { version: '2.5.10', method: '本地升级', start: '2026-06-20 09:18:09', end: '2026-06-20 09:18:58', operator: 'admin' },
]

// ── 系统日志 ──
const autoScroll = ref(true)
const logLines = [
  { ts: '2026-08-10 11:34:21.123', level: 'INFO', mod: 'NET', msg: '[MQTT] 连接已建立, clientId=esp32s3_30EDA0A9A808', cls: 'lv-info' },
  { ts: '2026-08-10 11:34:21.456', level: 'INFO', mod: 'DATA', msg: '上报数据成功, topic=ehome/node/30EDA0A9A808/data, size=128B', cls: 'lv-info' },
  { ts: '2026-08-10 11:34:22.789', level: 'WARN', mod: 'I2C', msg: 'I2C-2 读写超时, addr=0x48', cls: 'lv-warn' },
  { ts: '2026-08-10 11:34:23.012', level: 'INFO', mod: 'SYNC', msg: '配置同步成功, 版本=v2-9d93aa95', cls: 'lv-info' },
  { ts: '2026-08-10 11:34:24.567', level: 'ERROR', mod: 'SENSOR', msg: '温湿度传感器读取失败, 错误码=0xE001', cls: 'lv-error' },
]
</script>

<style scoped>
/* ═══════ 整页布局 ═══════ */
.node-demo {
  --primary: #1677FF;
  --primary-bg: #E6F4FF;
  --sidebar-bg: #0D1526;
  --sidebar-active: #182136;
  --sidebar-text: #AAB4C0;
  --bg: #F2F4F7;
  --border: #E6E8EE;
  --text: #1F2329;
  --text-2: #646A73;
  --text-3: #8F959E;
  --green: #16A34A;
  --green-dot: #22C55E;
  --green-bg: #E7F6EC;
  --red: #FF4D4F;
  position: fixed; inset: 0; z-index: 3000;
  display: flex; background: var(--bg);
  font-size: 14px; color: var(--text);
  overflow: hidden;
  font-family: 'PingFang SC', 'Microsoft YaHei', system-ui, sans-serif;
}

/* ═══════ 侧边栏 ═══════ */
.sidebar {
  width: 190px; flex-shrink: 0; height: 100%;
  background: var(--sidebar-bg); color: var(--sidebar-text);
  display: flex; flex-direction: column;
}
.sidebar-logo {
  height: 52px; display: flex; align-items: center; gap: 8px; padding: 0 16px;
}
.logo-text { color: #fff; font-size: 15px; font-weight: 700; }
.sidebar-nav {
  flex: 1; padding: 6px 8px; display: flex; flex-direction: column; gap: 2px;
  overflow-y: auto;
}
.nav-item {
  height: 36px; display: flex; align-items: center; gap: 10px;
  padding: 0 10px; border-radius: 6px; font-size: 14px; color: var(--sidebar-text);
  cursor: pointer; flex-shrink: 0;
}
.nav-item:hover { color: #fff; }
.nav-item.expanded { background: var(--sidebar-active); color: #fff; }
.nav-label { flex: 1; }
.nav-badge {
  background: var(--red); color: #fff; font-size: 11px; line-height: 16px;
  border-radius: 8px; padding: 0 5px;
}
.nav-arrow { color: #6B7688; transition: transform .2s; }
.nav-arrow.up { transform: rotate(180deg); }
.nav-children { padding: 2px 0 4px; }
.nav-child {
  height: 32px; display: flex; align-items: center;
  margin: 2px 8px 2px 20px; padding: 0 12px;
  border-radius: 6px; font-size: 13px; color: var(--sidebar-text); cursor: pointer;
}
.nav-child.active { background: var(--primary); color: #fff; }
.sidebar-bottom { padding: 12px 16px 10px; border-top: 1px solid #1B2540; }
.space-switch { display: flex; align-items: center; gap: 8px; cursor: pointer; }
.space-text { flex: 1; }
.space-label { font-size: 11px; color: #6B7688; }
.space-name { font-size: 13px; color: #fff; }
.sidebar-version { margin-top: 10px; text-align: center; font-size: 11px; color: #5A6478; }

/* ═══════ 顶栏 ═══════ */
.main-area { flex: 1; display: flex; flex-direction: column; min-width: 0; }
.topbar {
  height: 52px; flex-shrink: 0; background: #fff;
  border-bottom: 1px solid var(--border);
  display: flex; align-items: center; padding: 0 20px; gap: 12px;
}
.fold-btn { color: var(--text-2); cursor: pointer; }
.breadcrumb { display: flex; align-items: center; gap: 8px; font-size: 13px; }
.crumb { color: var(--text-3); }
.crumb-current { color: var(--text); }
.crumb-sep { color: #C9CDD4; }
.topbar-search {
  width: 400px; height: 32px; margin: 0 auto;
  background: #F5F6F8; border-radius: 6px;
  display: flex; align-items: center; gap: 8px; padding: 0 10px;
  color: var(--text-3);
}
.topbar-search input {
  flex: 1; border: none; outline: none; background: transparent;
  font-size: 13px; color: var(--text); min-width: 0;
}
.topbar-search kbd {
  background: #fff; border: 1px solid var(--border); border-radius: 4px;
  padding: 1px 6px; font-size: 11px; color: var(--text-3); font-family: inherit;
}
.topbar-right { display: flex; align-items: center; gap: 14px; color: var(--text-2); }
.bell-wrap { position: relative; display: flex; cursor: pointer; }
.bell-badge {
  position: absolute; top: -6px; right: -8px;
  background: var(--red); color: #fff; font-size: 10px;
  border-radius: 8px; padding: 0 4px; line-height: 14px;
}
.help-icon { cursor: pointer; }
.avatar {
  width: 28px; height: 28px; border-radius: 50%;
  background: linear-gradient(135deg, #1677FF, #22C55E); color: #fff;
  display: flex; align-items: center; justify-content: center;
  font-size: 13px; font-weight: 600;
}
.username { font-size: 13px; color: var(--text); }

/* ═══════ 内容区 ═══════ */
.content { flex: 1; overflow-y: auto; padding: 16px 20px 24px; }
.row { display: grid; gap: 16px; margin-top: 16px; }
.row1 { grid-template-columns: 1.5fr 1.05fr 1fr; }
.row2, .row3 { grid-template-columns: 48fr 52fr; }

.card {
  background: #fff; border: 1px solid var(--border); border-radius: 8px;
  box-shadow: 0 1px 2px rgba(16, 24, 40, 0.04);
  padding: 16px;
  min-width: 0;
}
.card-head { display: flex; align-items: center; gap: 8px; margin-bottom: 14px; }
.card-icon { color: var(--primary); }
.card-title { font-size: 15px; font-weight: 600; }
.card-head-action { margin-left: auto; color: var(--text-3); cursor: pointer; }
.card-head-btn { margin-left: auto; }

/* 通用元素 */
.dot { display: inline-block; width: 6px; height: 6px; border-radius: 50%; margin-right: 6px; vertical-align: 1px; }
.dot-green { background: var(--green-dot); }
.dot-blue { background: var(--primary); }
.dot-gray { background: #C0C4CC; }
.tag { border-radius: 4px; padding: 1px 8px; font-size: 12px; margin-left: 6px; }
.tag-green { color: var(--green); background: var(--green-bg); }
.tag-blue { color: var(--primary); background: var(--primary-bg); }
.mono { font-family: ui-monospace, SFMono-Regular, Consolas, Menlo, monospace; font-size: 12.5px; }
.nowrap { white-space: nowrap; }
.value-green { color: var(--green); }
.value-gray { color: var(--text-3); }
.copy-icon { color: var(--text-3); cursor: pointer; margin-left: 4px; vertical-align: -2px; }
.copy-icon:hover { color: var(--primary); }
.link { color: var(--primary); cursor: pointer; }

.btn {
  height: 32px; padding: 0 15px; border-radius: 6px; font-size: 13px;
  cursor: pointer; border: 1px solid transparent; background: none;
  display: inline-flex; align-items: center; gap: 6px; white-space: nowrap;
}
.btn-sm { height: 28px; padding: 0 12px; font-size: 12.5px; }
.btn-primary { background: var(--primary); color: #fff; }
.btn-primary:hover { background: #4096FF; }
.btn-default { border-color: #D9D9D9; color: var(--text); background: #fff; }
.btn-default:hover { border-color: var(--primary); color: var(--primary); }
.btn-ghost { background: var(--primary-bg); color: var(--primary); }
.btn-ghost:hover { background: #BAE0FF; }

/* ═══════ 页头 ═══════ */
.page-head { display: flex; align-items: flex-start; justify-content: space-between; }
.head-title-row { display: flex; align-items: center; gap: 10px; }
.back-arrow { color: var(--text-2); cursor: pointer; }
.head-title { font-size: 20px; font-weight: 600; }
.edit-icon { color: var(--text-3); cursor: pointer; }
.status-pill {
  display: inline-flex; align-items: center;
  background: var(--green-bg); color: var(--green);
  border-radius: 11px; padding: 2px 10px; font-size: 12px;
}
.head-sub { margin-top: 6px; font-size: 12px; color: var(--text-3); }
.head-actions { display: flex; gap: 8px; }

/* ═══════ 基本信息 ═══════ */
.info-grid { display: grid; grid-template-columns: 1fr 1fr; column-gap: 24px; }
.info-cell {
  display: flex; align-items: center; justify-content: space-between;
  min-height: 34px; font-size: 13px; gap: 8px;
}
.info-label { color: var(--text-3); flex-shrink: 0; }
.info-value { color: var(--text); text-align: right; }

.signal { display: inline-flex; align-items: flex-end; gap: 1.5px; margin-right: 6px; vertical-align: -1px; }
.signal i { width: 3px; border-radius: 1px; background: var(--green-dot); }
.signal i:nth-child(1) { height: 4px; }
.signal i:nth-child(2) { height: 6px; }
.signal i:nth-child(3) { height: 8px; }
.signal i:nth-child(4) { height: 10px; }

/* ═══════ 配置同步状态 ═══════ */
.sync-list { display: flex; flex-direction: column; }
.sync-row {
  display: flex; align-items: center; justify-content: space-between;
  min-height: 32px; font-size: 13px; gap: 8px;
}
.sync-label { color: var(--text-3); flex-shrink: 0; }
.sync-value { color: var(--text); text-align: right; word-break: break-all; }
.sync-value.uuid { font-size: 11px; max-width: 210px; }
.sync-foot { display: flex; justify-content: flex-end; margin-top: 10px; }

/* ═══════ 运行指标 ═══════ */
.metric-body { display: flex; align-items: center; gap: 16px; }
.ring-wrap { flex-shrink: 0; text-align: center; }
.ring { position: relative; width: 110px; height: 110px; }
.ring-text {
  position: absolute; inset: 0;
  display: flex; align-items: center; justify-content: center;
  font-size: 20px; font-weight: 600; color: var(--green);
}
.ring-label { margin-top: 6px; font-size: 12px; color: var(--text-3); }
.metric-list { flex: 1; min-width: 0; }
.metric-row {
  display: flex; justify-content: space-between; align-items: center;
  font-size: 13px; min-height: 26px;
}
.metric-row span { color: var(--text-3); }
.metric-row b { font-weight: 500; color: var(--text); }
.metric-divider { border-top: 1px solid #EEF0F3; margin: 6px 0; }

/* ═══════ 总线配置 ═══════ */
.bus-tabs { display: flex; gap: 24px; border-bottom: 1px solid #EEF0F3; }
.bus-tab {
  padding: 6px 0 8px; font-size: 13px; color: var(--text-2);
  cursor: pointer; border-bottom: 2px solid transparent; margin-bottom: -1px;
}
.bus-tab.active { color: var(--primary); border-bottom-color: var(--primary); font-weight: 500; }
.bus-body { display: flex; gap: 12px; margin-top: 12px; }
.bus-tree { width: 160px; flex-shrink: 0; display: flex; flex-direction: column; gap: 2px; }
.tree-item {
  height: 32px; display: flex; align-items: center; gap: 6px;
  padding: 0 8px; border-radius: 6px; font-size: 13px; color: var(--text-2);
  cursor: pointer;
}
.tree-item:hover { background: #F7F8FA; }
.tree-item.selected { background: var(--primary-bg); color: var(--primary); }
.tree-arrow { color: #B0B6BF; flex-shrink: 0; }
.tree-item.selected .tree-arrow { color: var(--primary); }
.tree-name { flex: 1; white-space: nowrap; }
.tree-count { color: var(--text-3); font-size: 12px; }
.tree-item.selected .tree-count { color: var(--primary); }
.bus-detail { flex: 1; min-width: 0; }
.bus-detail-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 10px; }
.bus-detail-title { font-size: 13px; font-weight: 600; }
.bus-empty { padding: 32px 0; text-align: center; color: var(--text-3); font-size: 13px; }

/* ═══════ 表格 ═══════ */
.tbl { width: 100%; border-collapse: collapse; font-size: 13px; }
.tbl th {
  background: #F7F8FA; color: #6B7280; font-weight: 500; font-size: 12.5px;
  text-align: left; padding: 8px 10px; border-bottom: 1px solid #EEF0F3;
  white-space: nowrap;
}
.tbl td {
  padding: 9px 10px; border-bottom: 1px solid #EEF0F3; color: var(--text);
  white-space: nowrap;
}
.tbl tbody tr:last-child td { border-bottom: none; }
/* 关联设备表 8 列紧凑版:缩 padding 适配卡片宽度(设计稿中该表列距更紧) */
.tbl-compact th { padding: 8px 6px; font-size: 12px; }
.tbl-compact td { padding: 9px 6px; font-size: 12.5px; }
.row-actions { display: inline-flex; gap: 2px; }
.icon-btn {
  width: 24px; height: 24px; border: none; background: none; border-radius: 4px;
  color: var(--text-3); cursor: pointer;
  display: inline-flex; align-items: center; justify-content: center;
}
.icon-btn:hover { color: var(--primary); background: #F5F6F8; }

/* ═══════ 卡脚分页 ═══════ */
.card-foot {
  display: flex; align-items: center; gap: 12px;
  margin-top: 12px; font-size: 13px; color: var(--text-2);
}
.total { margin-right: auto; }
.pager { display: flex; gap: 4px; }
.pg-btn {
  min-width: 26px; height: 26px; padding: 0 4px;
  border: 1px solid var(--border); border-radius: 4px;
  background: #fff; color: var(--text-2); font-size: 13px; cursor: pointer;
  display: inline-flex; align-items: center; justify-content: center;
}
.pg-btn.current { border: 1px dashed var(--primary); color: var(--primary); }
.page-size {
  display: inline-flex; align-items: center; gap: 4px;
  border: 1px solid var(--border); border-radius: 4px;
  height: 26px; padding: 0 8px; font-size: 12.5px; cursor: pointer;
}

/* ═══════ 系统日志 ═══════ */
.log-card { padding-bottom: 12px; }
.log-toolbar {
  display: flex; align-items: center; gap: 8px; flex-wrap: wrap;
  margin-bottom: 12px;
}
.log-bar { width: 3px; height: 16px; background: var(--primary); border-radius: 2px; }
.log-title { font-size: 15px; font-weight: 600; margin-right: 8px; }
.fake-select {
  display: inline-flex; align-items: center; gap: 6px;
  height: 30px; padding: 0 10px;
  border: 1px solid var(--border); border-radius: 6px;
  font-size: 13px; color: var(--text-2); cursor: pointer; background: #fff;
}
.date-range {
  display: inline-flex; align-items: center; gap: 8px;
  height: 30px; padding: 0 10px;
  border: 1px solid var(--border); border-radius: 6px;
  font-size: 12.5px; color: var(--text); background: #fff;
}
.range-arrow { color: var(--text-3); }
.range-icon { color: var(--text-3); }
.log-search {
  flex: 1; min-width: 160px; max-width: 260px; height: 30px;
  display: flex; align-items: center; gap: 6px; padding: 0 10px;
  border: 1px solid var(--border); border-radius: 6px;
  color: var(--text-3); background: #fff;
}
.log-search input {
  flex: 1; border: none; outline: none; background: transparent;
  font-size: 13px; color: var(--text); min-width: 0;
}
.log-actions { display: flex; gap: 8px; margin-left: auto; }

.log-console {
  position: relative;
  background: #0B0F1D; border-radius: 8px;
  padding: 12px 14px 30px;
  height: 132px; overflow-y: auto;
  font-family: ui-monospace, SFMono-Regular, Consolas, Menlo, monospace;
  font-size: 12px; line-height: 20px;
}
.log-console::-webkit-scrollbar { width: 6px; }
.log-console::-webkit-scrollbar-thumb { background: #3A4152; border-radius: 3px; }
.log-console::-webkit-scrollbar-track { background: transparent; }
.log-line { white-space: nowrap; }
.log-ts { color: #7D8590; margin-right: 8px; }
.log-tag { margin-right: 8px; }
.lv-info { color: #3FB950; }
.lv-warn { color: #D29922; }
.lv-error { color: #F85149; }
.log-msg { color: #C9D1D9; }
.autoscroll {
  position: absolute; right: 12px; bottom: 8px;
  display: flex; align-items: center; gap: 6px; cursor: pointer;
}
.autoscroll-text { color: #3FB950; font-size: 12px; font-family: 'PingFang SC', 'Microsoft YaHei', system-ui, sans-serif; }
.switch {
  width: 28px; height: 16px; border-radius: 8px; background: #3A4152;
  position: relative; transition: background .2s;
}
.switch::after {
  content: ''; position: absolute; top: 2px; left: 2px;
  width: 12px; height: 12px; border-radius: 50%; background: #fff;
  transition: left .2s;
}
.switch.on { background: #22C55E; }
.switch.on::after { left: 14px; }
</style>
