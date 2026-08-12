<template>
  <!-- 节点总线配置页设计稿 demo: designs/new-node-2.png 像素级复刻 + 全交互 -->
  <!-- Ant Design 风格: 白色侧边栏 / #1677FF 主色 / 严格按设计稿规格 -->
  <div class="bus-demo">
    <!-- ══════════ 左侧边栏 (白色, 216px) ══════════ -->
    <aside class="sidebar">
      <div class="sidebar-logo">
        <svg width="22" height="22" viewBox="0 0 24 24" fill="none">
          <path d="M12 2 L21 7 V17 L12 22 L3 17 V7 Z" fill="#1677FF" />
          <text x="12" y="16" text-anchor="middle" fill="#fff" font-size="11" font-weight="700">E</text>
        </svg>
        <span class="logo-text">EHomeSystem</span>
      </div>

      <nav class="sidebar-nav">
        <div v-for="item in navItems" :key="item.label" class="nav-item"
             :class="{ active: item.label === activeNav }" @click="onNavClick(item.label)">
          <el-icon :size="16"><component :is="item.icon" /></el-icon>
          <span class="nav-label">{{ item.label }}</span>
        </div>
      </nav>

      <div class="sidebar-version">v2.3.0</div>
    </aside>

    <!-- ══════════ 右侧主区 ══════════ -->
    <div class="main-area">
      <!-- 顶栏 (64px, 白色) -->
      <header class="topbar">
        <el-icon :size="16" class="fold-btn" @click="onFold"><Fold /></el-icon>
        <div class="breadcrumb">
          <span class="crumb" @click="onCrumb('首页')">首页</span>
          <span class="crumb-sep">/</span>
          <span class="crumb" @click="onCrumb('节点管理')">节点管理</span>
          <span class="crumb-sep">/</span>
          <span class="crumb crumb-current">设备名称</span>
        </div>
        <div class="topbar-search" @click="searchOpen = true">
          <el-icon :size="14"><Search /></el-icon>
          <input placeholder="搜索节点、设备、通道、或输入快捷指令..." readonly />
          <kbd>⌘ K</kbd>
        </div>
        <div class="topbar-right">
          <span class="status-pill"><span class="dot dot-green"></span>在线</span>
          <span class="bell-wrap" @click="notifyOpen = true">
            <el-icon :size="18"><Bell /></el-icon>
            <span v-if="unreadCount > 0" class="bell-badge">{{ unreadCount }}</span>
          </span>
          <span class="avatar" @click="userMenuOpen = !userMenuOpen">A</span>
          <span class="username">admin</span>
          <el-icon :size="11"><ArrowDown /></el-icon>
          <div v-if="userMenuOpen" class="user-menu">
            <div class="user-menu-item" @click="onUserMenu('个人中心')">个人中心</div>
            <div class="user-menu-item" @click="onUserMenu('修改密码')">修改密码</div>
            <div class="user-menu-item" @click="onUserMenu('退出登录')">退出登录</div>
          </div>
        </div>
      </header>

      <!-- 内容区 (padding 24px) -->
      <main class="content">
        <!-- 页头 -->
        <div class="page-head">
          <div class="head-left">
            <div class="head-title-row">
              <span class="head-title">设备名称</span>
              <el-icon :size="16" class="edit-icon" @click="onEditName"><EditPen /></el-icon>
            </div>
            <div class="head-meta">
              <span class="meta-item">
                <span class="meta-label">设备ID:</span>
                <span class="meta-value mono">{{ deviceId }}</span>
              </span>
              <span class="meta-item">
                <span class="dot dot-green"></span>
                <span class="value-green">在线</span>
              </span>
              <span class="meta-item">
                <span class="signal"><i></i><i></i><i></i><i></i></span>
                <span>{{ connQuality }}%</span>
                <span class="tag tag-green-sm">优秀</span>
              </span>
              <span class="meta-item">
                <el-icon :size="13"><Link /></el-icon>
                <span>{{ lastSync }}</span>
              </span>
            </div>
          </div>
          <div class="head-actions">
            <button class="btn btn-default" @click="onAction('同步配置')">
              <el-icon :size="16"><Refresh /></el-icon>同步配置
            </button>
            <button class="btn btn-default" @click="onAction('OTA 升级')">
              <el-icon :size="16"><Upload /></el-icon>OTA 升级
            </button>
            <button class="btn btn-default" @click="onAction('测延迟')">
              <el-icon :size="16"><TrendCharts /></el-icon>测延迟
            </button>
            <button class="btn btn-default" @click="onAction('刷新')">
              <el-icon :size="16"><RefreshRight /></el-icon>刷新
            </button>
          </div>
        </div>

        <!-- 一级 Tab (46px) -->
        <div class="page-tabs">
          <div v-for="t in pageTabs" :key="t.label" class="page-tab"
               :class="{ active: t.label === activePageTab }" @click="onPageTab(t.label)">
            <el-icon :size="16"><component :is="t.icon" /></el-icon>
            <span>{{ t.label }}</span>
          </div>
        </div>

        <!-- Alert 横幅 (40px) -->
        <div class="alert-banner">
          <el-icon :size="16" color="#FAAD14"><WarningFilled /></el-icon>
          <span><b>仅在线可编辑:</b> 当前设备在线,您可以查看和修改配置</span>
        </div>

        <!-- 主区双栏 (主列:右列 ≈ 2:1) -->
        <div class="main-cols">
          <!-- 左栏 -->
          <div class="col-left">
            <!-- 总线配置卡片 -->
            <section class="card">
              <!-- 协议子 Tab (48px) -->
              <div class="bus-subtabs">
                <div v-for="t in busTypes" :key="t.label" class="bus-subtab"
                     :class="{ active: t.label === activeBus }" @click="onBusTab(t.label)">
                  <el-icon :size="14"><component :is="t.icon" /></el-icon>
                  <span>{{ t.label }}</span>
                </div>
              </div>
              <div class="bus-desc">{{ busDesc }}</div>

              <!-- 统计指标行 (裸排, 48px高) -->
              <div class="stat-row">
                <div v-for="s in statCards" :key="s.label" class="stat-item">
                  <div class="stat-icon" :style="{ background: s.bg }">
                    <el-icon :size="16" :color="s.color"><component :is="s.icon" /></el-icon>
                  </div>
                  <div class="stat-body">
                    <div class="stat-label">{{ s.label }}</div>
                    <div class="stat-value">{{ s.value }}</div>
                  </div>
                </div>
              </div>

              <!-- 资源表格 -->
              <div class="table-wrap">
                <table class="tbl">
                  <thead>
                    <tr>
                      <th style="width:32px"></th>
                      <th>资源名称</th>
                      <th>引脚 (SDA/SCL)</th>
                      <th>关键参数</th>
                      <th>状态</th>
                      <th>已挂载通道</th>
                      <th>DMA 绑定</th>
                      <th>操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="r in resources" :key="r.name"
                        :class="{ selected: r.name === selectedRes, disabled: r.status === 'disabled' }"
                        @click="onSelectRes(r.name)">
                      <td>
                        <span class="radio" :class="{ checked: r.name === selectedRes }"></span>
                      </td>
                      <td class="mono">{{ r.name }}</td>
                      <td class="mono">{{ r.pins }}</td>
                      <td>
                        <span class="tag tag-blue">{{ r.freq }}</span>
                        <span class="tag tag-blue">{{ r.bits }}</span>
                      </td>
                      <td>
                        <template v-if="r.status === 'online'">
                          <span class="tag tag-green">在线</span>
                        </template>
                        <template v-else-if="r.status === 'offline'">
                          <span class="tag tag-red">离线</span>
                        </template>
                        <template v-else>
                          <span class="tag tag-gray"><el-icon :size="10"><Lock /></el-icon> 禁用</span>
                        </template>
                      </td>
                      <td>{{ r.mounted }} / {{ r.max }}</td>
                      <td>
                        <template v-if="r.status !== 'disabled'">
                          <span class="switch" :class="{ on: r.dmaBound }" @click.stop="onToggleDma(r)"></span>
                        </template>
                        <template v-else>—</template>
                      </td>
                      <td>
                        <span class="row-actions">
                          <button class="link-btn" :disabled="r.status === 'disabled'" @click.stop="onRowAction(r.name, '查看')">
                            <el-icon :size="12"><View /></el-icon>查看
                          </button>
                          <button class="link-btn" :disabled="r.status === 'disabled'" @click.stop="onRowAction(r.name, '新建通道')">
                            <el-icon :size="12"><Plus /></el-icon>新建通道
                          </button>
                          <button class="link-btn" :disabled="r.status === 'disabled'" @click.stop="onRowAction(r.name, '编辑')">
                            <el-icon :size="12"><EditPen /></el-icon>编辑
                          </button>
                          <button class="link-btn link-more" :disabled="r.status === 'disabled'" @click.stop="onRowAction(r.name, '更多')">
                            <el-icon :size="12"><MoreFilled /></el-icon>
                          </button>
                        </span>
                      </td>
                    </tr>
                  </tbody>
                </table>
              </div>

              <!-- 分页 (48px) -->
              <div class="pagination">
                <div class="page-left">
                  <span>每页</span>
                  <span class="page-size" @click="pageSizeOpen = !pageSizeOpen">
                    {{ pageSize }} <el-icon :size="10"><ArrowDown /></el-icon>
                  </span>
                  <span>条</span>
                  <span class="page-total">共 {{ resources.length }} 条</span>
                  <div v-if="pageSizeOpen" class="page-size-menu">
                    <div v-for="s in [10, 20, 50]" :key="s" class="page-size-item"
                         @click="pageSize = s; pageSizeOpen = false">{{ s }}</div>
                  </div>
                </div>
                <div class="page-right">
                  <button class="pg-btn" :disabled="currentPage <= 1" @click="currentPage--">上一页</button>
                  <button class="pg-btn pg-current">{{ currentPage }}</button>
                  <button class="pg-btn" :disabled="currentPage >= totalPages" @click="currentPage++">下一页</button>
                  <span class="pg-info">{{ currentPage }} / {{ totalPages }} 页</span>
                </div>
              </div>
            </section>

            <!-- 底部工具卡 (96px高) -->
            <div class="tool-cards">
              <section class="card tool-card">
                <div class="tool-head">
                  <div class="tool-icon" style="background:#E6F4FF">
                    <el-icon :size="16" color="#1677FF"><Search /></el-icon>
                  </div>
                  <div class="tool-title">地址扫描</div>
                </div>
                <div class="tool-desc">扫描 I2C 总线上的从设备地址</div>
                <div class="tool-foot">
                  <button class="btn btn-primary btn-sm" :disabled="scanning" @click="onScan">
                    {{ scanning ? '扫描中...' : '开始扫描' }}
                  </button>
                  <span v-if="scanResult" class="tool-result value-green">发现 {{ scanResult }} 个设备</span>
                </div>
              </section>

              <section class="card tool-card">
                <div class="tool-head">
                  <div class="tool-icon" style="background:#F6FFED">
                    <el-icon :size="16" color="#52C41A"><Odometer /></el-icon>
                  </div>
                  <div class="tool-title">总线诊断</div>
                </div>
                <div class="tool-desc">检测 I2C 总线状态和信号质量</div>
                <div class="tool-foot">
                  <button class="btn btn-default btn-sm" :disabled="diagnosing" @click="onDiagnose">
                    {{ diagnosing ? '诊断中...' : '开始诊断' }}
                  </button>
                </div>
              </section>

              <section class="card tool-card">
                <div class="tool-head">
                  <div class="tool-icon" style="background:#FFF7E6">
                    <el-icon :size="16" color="#FAAD14"><Tools /></el-icon>
                  </div>
                  <div class="tool-title">快速操作 <span class="tag tag-orange-sm">UART 专属</span></div>
                </div>
                <div class="tool-desc">修改波特率 — 批量修改 UART 通道波特率</div>
                <div class="tool-foot">
                  <button class="btn btn-default btn-sm" @click="onOpenBaudTool">打开工具</button>
                </div>
              </section>
            </div>
          </div>

          <!-- 右栏 (468px) -->
          <div class="col-right">
            <!-- 通道详情卡片 -->
            <section class="card">
              <div class="card-head">
                <span class="card-title">通道详情</span>
                <span class="tag tag-gray">只读模式</span>
                <div class="card-head-right">
                  <button class="btn btn-default btn-sm" @click="onScanDetail">
                    <el-icon :size="12"><Search /></el-icon>地址扫描
                  </button>
                  <span v-if="scanResult" class="value-green" style="font-size:12px">发现 {{ scanResult }} 个设备</span>
                  <el-icon :size="14" class="card-head-action" @click="onRefreshDetail"><RefreshRight /></el-icon>
                </div>
              </div>
              <div class="detail-list">
                <div class="detail-row">
                  <span class="detail-label">资源名称</span>
                  <span class="detail-value mono">{{ selectedRes }}</span>
                </div>
                <div class="detail-row">
                  <span class="detail-label">引脚</span>
                  <span class="detail-value mono">{{ selectedResPins }}</span>
                </div>
                <div class="detail-row">
                  <span class="detail-label">工作模式</span>
                  <span class="detail-value">主机模式</span>
                </div>
                <div class="detail-row">
                  <span class="detail-label">时钟频率</span>
                  <span class="detail-value">{{ selectedResFreq }}</span>
                </div>
                <div class="detail-row">
                  <span class="detail-label">地址位宽</span>
                  <span class="detail-value">{{ selectedResBits }}</span>
                </div>
                <div class="detail-row">
                  <span class="detail-label">已挂载通道</span>
                  <span class="detail-value">{{ selectedResMounted }} / 4</span>
                </div>
                <div class="detail-row">
                  <span class="detail-label">DMA 绑定</span>
                  <span class="detail-value" :class="selectedResDma ? 'value-green' : ''">
                    {{ selectedResDma ? '已绑定' : '未绑定' }}
                    <span v-if="selectedResDma" class="mono">(通道 {{ selectedResDmaCh }})</span>
                  </span>
                </div>
              </div>
            </section>

            <!-- 新建 I2C 通道面板 -->
            <section class="card form-card">
              <div class="card-head">
                <span class="card-title">新建 I2C 通道</span>
                <el-icon :size="16" class="card-head-action" @click="onCloseForm"><Close /></el-icon>
              </div>

              <!-- 表单 Tab (36px) -->
              <div class="form-tabs">
                <div v-for="t in formTabs" :key="t" class="form-tab"
                     :class="{ active: t === activeFormTab }" @click="activeFormTab = t">{{ t }}</div>
              </div>

              <!-- 表单内容 -->
              <div v-if="activeFormTab === '基础参数'" class="form-body">
                <div class="form-item">
                  <label class="form-label">通道名称 <span class="required">*</span></label>
                  <div class="input-wrap">
                    <input v-model="form.name" class="form-input" maxlength="32" placeholder="请输入通道名称" />
                    <span class="char-count">{{ form.name.length }} / 32</span>
                  </div>
                </div>
                <div class="form-item">
                  <label class="form-label">描述</label>
                  <div class="input-wrap">
                    <input v-model="form.desc" class="form-input" maxlength="64" placeholder="请输入描述" />
                    <span class="char-count">{{ form.desc.length }} / 64</span>
                  </div>
                </div>
                <div class="form-row">
                  <div class="form-item">
                    <label class="form-label">通道类型 <span class="required">*</span></label>
                    <div class="fake-select" @click="typeOpen = !typeOpen">
                      {{ form.type }}
                      <el-icon :size="10"><ArrowDown /></el-icon>
                      <div v-if="typeOpen" class="select-menu">
                        <div v-for="t in channelTypes" :key="t" class="select-item"
                             @click.stop="form.type = t; typeOpen = false">{{ t }}</div>
                      </div>
                    </div>
                  </div>
                  <div class="form-item">
                    <label class="form-label">数据方向 <span class="required">*</span></label>
                    <div class="fake-select" @click="dirOpen = !dirOpen">
                      {{ form.direction }}
                      <el-icon :size="10"><ArrowDown /></el-icon>
                      <div v-if="dirOpen" class="select-menu">
                        <div v-for="d in dataDirections" :key="d" class="select-item"
                             @click.stop="form.direction = d; dirOpen = false">{{ d }}</div>
                      </div>
                    </div>
                  </div>
                </div>
                <div class="info-banner">
                  <el-icon :size="14" color="#1677FF"><InfoFilled /></el-icon>
                  <span>权限提示: 仅在线可编辑配置,离线状态下只能查看</span>
                </div>
              </div>
              <div v-else-if="activeFormTab === '总线参数'" class="form-body">
                <div class="form-item">
                  <label class="form-label">从机地址</label>
                  <input v-model="form.slaveAddr" class="form-input" placeholder="0x40" />
                </div>
                <div class="form-item">
                  <label class="form-label">时钟频率</label>
                  <div class="fake-select" @click="freqOpen = !freqOpen">
                    {{ form.freq }}
                    <el-icon :size="10"><ArrowDown /></el-icon>
                    <div v-if="freqOpen" class="select-menu">
                      <div v-for="f in ['100kHz', '400kHz', '1MHz']" :key="f" class="select-item"
                           @click.stop="form.freq = f; freqOpen = false">{{ f }}</div>
                    </div>
                  </div>
                </div>
              </div>
              <div v-else-if="activeFormTab === '运行参数'" class="form-body">
                <div class="form-item">
                  <label class="form-label">采样间隔 (ms)</label>
                  <input v-model.number="form.interval" class="form-input" type="number" min="100" />
                </div>
                <div class="form-item">
                  <label class="form-label">超时时间 (ms)</label>
                  <input v-model.number="form.timeout" class="form-input" type="number" min="100" />
                </div>
              </div>
              <div v-else class="form-body">
                <div class="perm-note">
                  <p><b>权限说明</b></p>
                  <p>1. 仅在线设备可编辑配置,离线状态下所有字段只读。</p>
                  <p>2. 修改配置后需要同步到设备才能生效。</p>
                  <p>3. DMA 绑定需要硬件支持,禁用的资源不可操作。</p>
                </div>
              </div>

              <!-- 表单底部按钮 -->
              <div class="form-foot">
                <button class="btn btn-default" @click="onCloseForm">取消</button>
                <button class="btn btn-default" @click="onResetForm">重置</button>
                <button class="btn btn-primary" :disabled="!form.name.trim()" @click="onSaveForm">保存</button>
              </div>
            </section>
          </div>
        </div>
      </main>
    </div>

    <!-- ══════════ 搜索弹窗 ══════════ -->
    <el-dialog v-model="searchOpen" title="全局搜索" width="500px" :close-on-click-modal="true">
      <input v-model="searchQuery" class="search-input" placeholder="搜索节点、设备、通道..." />
      <div v-if="searchQuery" class="search-results">
        <div v-for="r in searchResults" :key="r" class="search-item" @click="onSearchPick(r)">{{ r }}</div>
        <div v-if="searchResults.length === 0" class="search-empty">无匹配结果</div>
      </div>
    </el-dialog>

    <!-- ══════════ 通知抽屉 ══════════ -->
    <el-drawer v-model="notifyOpen" title="通知中心" size="360px">
      <div class="notify-list">
        <div v-for="n in notifications" :key="n.id" class="notify-item" :class="{ unread: !n.read }"
             @click="n.read = true">
          <div class="notify-title">{{ n.title }}</div>
          <div class="notify-time">{{ n.time }}</div>
          <div class="notify-content">{{ n.content }}</div>
        </div>
      </div>
      <div class="notify-foot">
        <button class="btn btn-default btn-sm" @click="markAllRead">全部已读</button>
      </div>
    </el-drawer>

    <!-- ══════════ 波特率工具弹窗 ══════════ -->
    <el-dialog v-model="baudOpen" title="批量修改波特率" width="480px">
      <div class="baud-body">
        <div class="form-item">
          <label class="form-label">目标波特率</label>
          <div class="fake-select" @click="baudSelectOpen = !baudSelectOpen">
            {{ baudTarget }}
            <el-icon :size="10"><ArrowDown /></el-icon>
            <div v-if="baudSelectOpen" class="select-menu">
              <div v-for="b in ['9600', '19200', '38400', '57600', '115200']" :key="b" class="select-item"
                   @click.stop="baudTarget = b; baudSelectOpen = false">{{ b }}</div>
            </div>
          </div>
        </div>
        <div class="form-item">
          <label class="form-label">应用范围</label>
          <div class="baud-channels">
            <label v-for="ch in uartChannels" :key="ch" class="baud-ch">
              <input type="checkbox" :value="ch" v-model="baudSelected" /> {{ ch }}
            </label>
          </div>
        </div>
      </div>
      <template #footer>
        <button class="btn btn-default" @click="baudOpen = false">取消</button>
        <button class="btn btn-primary" :disabled="baudSelected.length === 0" @click="onBaudApply">应用</button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, computed } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  ArrowDown, Fold, Search, Bell, EditPen, Refresh, Upload, TrendCharts,
  RefreshRight, Link, InfoFilled, View, Plus, MoreFilled, Close, Lock,
  Odometer, Share, Monitor, Cpu, Connection, DataLine, Box, Document,
  MagicStick, TrendCharts as TrendIcon, Notebook, Setting, Tools,
  WarningFilled,
} from '@element-plus/icons-vue'

// ── 侧边栏 ──
const navItems = [
  { label: '仪表盘', icon: Odometer },
  { label: '节点', icon: Share },
  { label: '边缘设备', icon: Monitor },
  { label: '通道管理', icon: Connection },
  { label: '数据面板', icon: DataLine },
  { label: '固件管理', icon: Box },
  { label: '配置模板', icon: Document },
  { label: '系统监控', icon: TrendIcon },
  { label: '系统设置', icon: Setting },
]
const activeNav = ref('节点')
function onNavClick(label: string) {
  activeNav.value = label
  ElMessage.info(`切换到「${label}」(demo 无路由)`)
}
function onFold() { ElMessage.info('折叠侧边栏 (demo)') }
function onCrumb(label: string) { ElMessage.info(`返回「${label}」(demo)`) }

// ── 用户菜单 ──
const userMenuOpen = ref(false)
function onUserMenu(action: string) {
  userMenuOpen.value = false
  if (action === '退出登录') {
    ElMessageBox.confirm('确定要退出登录吗?', '提示', { type: 'warning' })
      .then(() => ElMessage.success('已退出 (demo)'))
      .catch(() => {})
  } else {
    ElMessage.info(`${action} (demo)`)
  }
}

// ── 通知 ──
const notifyOpen = ref(false)
const notifications = ref([
  { id: 1, title: '设备上线', time: '2 分钟前', content: '节点 30EDA0A9A808 已上线', read: false },
  { id: 2, title: 'OTA 升级完成', time: '1 小时前', content: '固件 v2.3.0 升级成功', read: false },
  { id: 3, title: '配置同步', time: '3 小时前', content: '配置已同步到设备', read: true },
  { id: 4, title: '告警', time: '昨天', content: 'I2C_3 总线离线', read: true },
])
const unreadCount = computed(() => notifications.value.filter(n => !n.read).length)
function markAllRead() {
  notifications.value.forEach(n => n.read = true)
  ElMessage.success('全部标记已读')
}

// ── 搜索 ──
const searchOpen = ref(false)
const searchQuery = ref('')
const searchPool = [
  '节点 30EDA0A9A808', 'I2C_0 总线', 'I2C_1 总线', 'UART_0 串口',
  '温度传感器', '湿度传感器', '通道 i2c0_temp_sensor', 'DMA 通道 1',
]
const searchResults = computed(() =>
  searchPool.filter(s => s.toLowerCase().includes(searchQuery.value.toLowerCase()))
)
function onSearchPick(r: string) {
  searchOpen.value = false
  searchQuery.value = ''
  ElMessage.success(`跳转到: ${r} (demo)`)
}

// ── 页头 ──
const deviceId = '30EDA0A9A808'
const connQuality = 92
const lastSync = ref('2026/08/08 21:18:42')
function onEditName() {
  ElMessageBox.prompt('请输入新的设备名称', '编辑设备名称', {
    inputValue: '设备名称',
    inputPattern: /\S+/,
    inputErrorMessage: '名称不能为空',
  }).then(({ value }) => {
    ElMessage.success(`设备名称已更新为「${value}」(demo)`)
  }).catch(() => {})
}
function onAction(action: string) {
  if (action === '刷新') {
    lastSync.value = new Date().toLocaleString('zh-CN', { hour12: false }).replace(/\//g, '/')
    ElMessage.success('数据已刷新')
  } else if (action === '同步配置') {
    ElMessageBox.confirm('确定要将当前配置同步到设备吗?', '同步配置', { type: 'info' })
      .then(() => ElMessage.success('配置同步指令已下发'))
      .catch(() => {})
  } else if (action === 'OTA 升级') {
    ElMessage.info('OTA 升级向导 (demo)')
  } else if (action === '测延迟') {
    ElMessage.info('正在测量延迟...')
    setTimeout(() => ElMessage.success(`延迟: ${Math.floor(Math.random() * 20 + 5)}ms`), 800)
  }
}

// ── 一级 Tab ──
const pageTabs = [
  { label: '基本信息', icon: InfoFilled },
  { label: '总线配置', icon: Cpu },
  { label: 'DMA 通道', icon: Connection },
  { label: '关联设备', icon: Monitor },
  { label: 'OTA 历史', icon: Box },
  { label: '系统日志', icon: Notebook },
]
const activePageTab = ref('总线配置')
function onPageTab(label: string) {
  activePageTab.value = label
  if (label !== '总线配置') {
    ElMessage.info(`「${label}」页面 (demo 仅总线配置)`)
  }
}

// ── 总线类型 Tab ──
const busTypes = [
  { label: 'I2C', icon: Cpu },
  { label: 'UART', icon: Connection },
  { label: 'SPI', icon: MagicStick },
  { label: 'ADC', icon: DataLine },
  { label: 'GPIO', icon: Share },
  { label: 'PWM', icon: Tools },
]
const activeBus = ref('I2C')
const busDesc = computed(() => {
  const descs: Record<string, string> = {
    I2C: 'I2C 总线用于连接低速外设,支持多主多从通信',
    UART: 'UART 串口用于点对点异步通信,支持全双工',
    SPI: 'SPI 总线用于高速外设,支持主从模式',
    ADC: 'ADC 模块用于模拟信号采集',
    GPIO: 'GPIO 引脚直控,用于数字输入输出',
    PWM: 'PWM 用于脉冲宽度调制输出',
  }
  return descs[activeBus.value] || ''
})
function onBusTab(label: string) {
  activeBus.value = label
  if (label !== 'I2C') {
    ElMessage.info(`「${label}」配置 (demo 仅 I2C)`)
  }
}

// ── 统计指标 ──
const statCards = computed(() => [
  { label: '资源总数', value: 8, icon: Box, color: '#1677FF', bg: '#E6F4FF' },
  { label: '已挂载', value: 5, icon: Link, color: '#52C41A', bg: '#F6FFED' },
  { label: '可用', value: 2, icon: View, color: '#13C2C2', bg: '#E6FFFB' },
  { label: '禁用', value: 1, icon: Lock, color: '#FF4D4F', bg: '#FFF2F0' },
  { label: 'DMA 支持', value: 6, icon: Cpu, color: '#52C41A', bg: '#F6FFED' },
  { label: '告警', value: 0, icon: Bell, color: '#FAAD14', bg: '#FFF7E6' },
])

// ── 资源表格 ──
interface Resource {
  name: string
  pins: string
  freq: string
  bits: string
  status: 'online' | 'offline' | 'disabled'
  mounted: number
  max: number
  dmaBound: boolean
  dmaCh?: number
}
const resources = ref<Resource[]>([
  { name: 'I2C_0', pins: 'GPIO21 / GPIO22', freq: '100kHz', bits: '7bit', status: 'online', mounted: 2, max: 4, dmaBound: true, dmaCh: 1 },
  { name: 'I2C_1', pins: 'GPIO25 / GPIO26', freq: '400kHz', bits: '7bit', status: 'online', mounted: 1, max: 4, dmaBound: true, dmaCh: 2 },
  { name: 'I2C_2', pins: 'GPIO32 / GPIO33', freq: '400kHz', bits: '7bit', status: 'online', mounted: 0, max: 4, dmaBound: false },
  { name: 'I2C_3', pins: 'GPIO4 / GPIO5', freq: '1MHz', bits: '7bit', status: 'offline', mounted: 0, max: 4, dmaBound: false },
  { name: 'I2C_4', pins: 'GPIO18 / GPIO19', freq: '100kHz', bits: '10bit', status: 'online', mounted: 2, max: 4, dmaBound: true, dmaCh: 3 },
  { name: 'I2C_5', pins: 'GPIO23 / GPIO24', freq: '400kHz', bits: '7bit', status: 'disabled', mounted: 0, max: 4, dmaBound: false },
  { name: 'I2C_6', pins: 'GPIO27 / GPIO14', freq: '400kHz', bits: '7bit', status: 'online', mounted: 0, max: 4, dmaBound: false },
  { name: 'I2C_7', pins: 'GPIO2 / GPIO15', freq: '100kHz', bits: '7bit', status: 'online', mounted: 0, max: 4, dmaBound: false },
])
const selectedRes = ref('I2C_0')
function onSelectRes(name: string) {
  selectedRes.value = name
}
const selectedResObj = computed(() => resources.value.find(r => r.name === selectedRes.value))
const selectedResPins = computed(() => selectedResObj.value?.pins ?? '')
const selectedResFreq = computed(() => selectedResObj.value?.freq ?? '')
const selectedResBits = computed(() => selectedResObj.value?.bits ?? '')
const selectedResMounted = computed(() => selectedResObj.value?.mounted ?? 0)
const selectedResDma = computed(() => selectedResObj.value?.dmaBound ?? false)
const selectedResDmaCh = computed(() => selectedResObj.value?.dmaCh ?? '')

function onToggleDma(r: Resource) {
  const action = r.dmaBound ? '解绑' : '绑定'
  ElMessageBox.confirm(`确定要${action} ${r.name} 的 DMA 吗?`, 'DMA 绑定', { type: 'warning' })
    .then(() => {
      r.dmaBound = !r.dmaBound
      if (r.dmaBound) {
        r.dmaCh = Math.floor(Math.random() * 6) + 1
        ElMessage.success(`${r.name} DMA 已绑定到通道 ${r.dmaCh}`)
      } else {
        r.dmaCh = undefined
        ElMessage.success(`${r.name} DMA 已解绑`)
      }
    })
    .catch(() => {})
}

function onRowAction(name: string, action: string) {
  if (action === '查看') {
    selectedRes.value = name
    ElMessage.info(`查看 ${name} 详情`)
  } else if (action === '新建通道') {
    selectedRes.value = name
    ElMessage.success(`在 ${name} 上新建通道`)
  } else if (action === '编辑') {
    ElMessage.info(`编辑 ${name} 配置`)
  } else {
    ElMessage.info(`${name} 更多操作`)
  }
}

// ── 分页 ──
const pageSize = ref(10)
const pageSizeOpen = ref(false)
const currentPage = ref(1)
const totalPages = computed(() => Math.ceil(resources.value.length / pageSize.value))

// ── 工具区 ──
const scanning = ref(false)
const scanResult = ref<number | null>(3)
function onScan() {
  scanning.value = true
  scanResult.value = null
  setTimeout(() => {
    scanning.value = false
    scanResult.value = Math.floor(Math.random() * 5) + 1
    ElMessage.success(`扫描完成,发现 ${scanResult.value} 个设备`)
  }, 1500)
}
function onScanDetail() { onScan() }

const diagnosing = ref(false)
function onDiagnose() {
  diagnosing.value = true
  setTimeout(() => {
    diagnosing.value = false
    ElMessage.success('总线诊断完成: 信号质量良好,无短路/断路')
  }, 2000)
}

// ── 波特率工具 ──
const baudOpen = ref(false)
const baudTarget = ref('115200')
const baudSelectOpen = ref(false)
const uartChannels = ['UART_0', 'UART_1', 'UART_2']
const baudSelected = ref<string[]>([])
function onOpenBaudTool() {
  baudSelected.value = [...uartChannels]
  baudOpen.value = true
}
function onBaudApply() {
  ElMessageBox.confirm(
    `确定要将 ${baudSelected.value.join(', ')} 的波特率改为 ${baudTarget.value} 吗?`,
    '批量修改波特率',
    { type: 'warning' },
  ).then(() => {
    baudOpen.value = false
    ElMessage.success(`已修改 ${baudSelected.value.length} 个通道波特率为 ${baudTarget.value}`)
  }).catch(() => {})
}

// ── 新建通道表单 ──
const formTabs = ['基础参数', '总线参数', '运行参数', '权限提示']
const activeFormTab = ref('基础参数')
const form = ref({
  name: 'i2c0_temp_sensor',
  desc: '温度传感器采集通道',
  type: '传感器',
  direction: '从设备读取',
  slaveAddr: '0x40',
  freq: '100kHz',
  interval: 5000,
  timeout: 1000,
})
const channelTypes = ['传感器', '执行器', '显示器', '存储器']
const dataDirections = ['从设备读取', '向设备写入', '双向']
const typeOpen = ref(false)
const dirOpen = ref(false)
const freqOpen = ref(false)

const formVisible = ref(true)
function onCloseForm() {
  ElMessageBox.confirm('确定要关闭表单吗?未保存的修改将丢失。', '提示', { type: 'warning' })
    .then(() => {
      formVisible.value = false
      ElMessage.info('表单已关闭 (demo 重新打开请点击「新建通道」)')
    })
    .catch(() => {})
}
function onResetForm() {
  form.value = {
    name: '',
    desc: '',
    type: '传感器',
    direction: '从设备读取',
    slaveAddr: '0x40',
    freq: '100kHz',
    interval: 5000,
    timeout: 1000,
  }
  ElMessage.info('表单已重置')
}
function onSaveForm() {
  if (!form.value.name.trim()) {
    ElMessage.error('通道名称不能为空')
    return
  }
  ElMessage.success(`通道「${form.value.name}」保存成功 (demo)`)
}

function onRefreshDetail() {
  ElMessage.success('详情已刷新')
}
</script>

<style scoped>
/* ══════════ 设计稿色板 (Ant Design v5 Token) ══════════ */
/* 主色: #1677FF / hover #4096FF / 浅底 #E6F4FF / 选中行 #F0F5FF */
/* 成功: #52C41A / 底 #F6FFED */
/* 警告: #FAAD14 / 底 #FFFBE6 / 边 #FFE58F */
/* 错误: #FF4D4F / 底 #FFF2F0 */
/* 文字: 主 #1F1F1F / 次 #595959 / 弱 #8C8C8C / 占位 #BFBFBF */
/* 边框: #D9D9D9 (控件) / #F0F0F0 (分隔线) */
/* 页面背景: #F5F6FA */

/* ══════════ 整页覆盖 ══════════ */
.bus-demo {
  position: fixed;
  inset: 0;
  z-index: 3000;
  display: flex;
  background: #F5F6FA;
  font-family: -apple-system, BlinkMacSystemFont, 'PingFang SC', 'Microsoft YaHei', sans-serif;
  font-size: 13px;
  color: #1F1F1F;
  overflow: hidden;
}

/* ══════════ 侧边栏 (白色, 216px) ══════════ */
.sidebar {
  width: 216px;
  background: #FFFFFF;
  border-right: 1px solid #F0F0F0;
  display: flex;
  flex-direction: column;
  flex-shrink: 0;
}
.sidebar-logo {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 20px 24px 16px;
}
.logo-text {
  font-size: 16px;
  font-weight: 600;
  color: #1F1F1F;
  letter-spacing: 0.3px;
}
.sidebar-nav {
  flex: 1;
  padding: 4px 8px;
  overflow-y: auto;
}
.nav-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 0 16px;
  height: 40px;
  margin-bottom: 4px;
  border-radius: 6px;
  color: #595959;
  cursor: pointer;
  transition: all 0.15s;
}
.nav-item:hover { background: #F5F5F5; }
.nav-item.active {
  background: #E6F4FF;
  color: #1677FF;
}
.nav-label { font-size: 14px; }
.sidebar-version {
  padding: 12px 24px 16px;
  font-size: 12px;
  color: #BFBFBF;
}

/* ══════════ 主区 ══════════ */
.main-area {
  flex: 1;
  display: flex;
  flex-direction: column;
  min-width: 0;
}

/* ── 顶栏 (64px) ── */
.topbar {
  height: 64px;
  background: #FFFFFF;
  border-bottom: 1px solid #F0F0F0;
  display: flex;
  align-items: center;
  padding: 0 24px;
  gap: 16px;
  flex-shrink: 0;
}
.fold-btn { cursor: pointer; color: #595959; }
.breadcrumb { display: flex; align-items: center; gap: 8px; white-space: nowrap; }
.crumb { color: #8C8C8C; cursor: pointer; font-size: 14px; }
.crumb:hover { color: #1677FF; }
.crumb-sep { color: #BFBFBF; }
.crumb-current { color: #1F1F1F; font-weight: 500; cursor: default; }
.crumb-current:hover { color: #1F1F1F; }

.topbar-search {
  flex: 1;
  max-width: 480px;
  display: flex;
  align-items: center;
  gap: 8px;
  background: #FFFFFF;
  border: 1px solid #D9D9D9;
  border-radius: 6px;
  padding: 0 12px;
  height: 32px;
  margin: 0 auto;
  cursor: pointer;
}
.topbar-search input {
  flex: 1;
  border: none;
  background: none;
  outline: none;
  font-size: 13px;
  color: #BFBFBF;
  cursor: pointer;
}
.topbar-search kbd {
  font-size: 12px;
  color: #BFBFBF;
  background: #F5F5F5;
  padding: 2px 6px;
  border-radius: 4px;
  font-family: inherit;
}

.topbar-right {
  display: flex;
  align-items: center;
  gap: 24px;
  position: relative;
}
.status-pill {
  display: flex;
  align-items: center;
  gap: 5px;
  font-size: 13px;
  color: #52C41A;
  white-space: nowrap;
}
.dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  display: inline-block;
}
.dot-green { background: #52C41A; }
.dot-red { background: #FF4D4F; }

.bell-wrap {
  position: relative;
  cursor: pointer;
  color: #595959;
  display: flex;
}
.bell-badge {
  position: absolute;
  top: -5px;
  right: -7px;
  background: #FF4D4F;
  color: #fff;
  font-size: 10px;
  min-width: 15px;
  height: 15px;
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 0 3px;
  font-weight: 600;
}

.avatar {
  width: 32px;
  height: 32px;
  border-radius: 50%;
  background: #1677FF;
  color: #fff;
  font-size: 14px;
  font-weight: 600;
  display: flex;
  align-items: center;
  justify-content: center;
  cursor: pointer;
}
.username { font-size: 14px; color: #1F1F1F; }

.user-menu {
  position: absolute;
  top: 46px;
  right: 0;
  background: #fff;
  border: 1px solid #F0F0F0;
  border-radius: 8px;
  box-shadow: 0 4px 12px rgba(0,0,0,0.08);
  padding: 4px 0;
  min-width: 120px;
  z-index: 100;
}
.user-menu-item {
  padding: 8px 14px;
  font-size: 13px;
  color: #595959;
  cursor: pointer;
}
.user-menu-item:hover { background: #F5F5F5; color: #1677FF; }

/* ── 内容区 (padding 24px) ── */
.content {
  flex: 1;
  overflow-y: auto;
  padding: 24px;
}

/* ── 页头 ── */
.page-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  margin-bottom: 16px;
  flex-wrap: wrap;
  gap: 10px;
}
.head-title-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
}
.head-title {
  font-size: 22px;
  font-weight: 600;
  color: #1F1F1F;
}
.edit-icon {
  color: #8C8C8C;
  cursor: pointer;
}
.edit-icon:hover { color: #1677FF; }
.head-meta {
  display: flex;
  align-items: center;
  gap: 32px;
  flex-wrap: wrap;
}
.meta-item {
  display: flex;
  align-items: center;
  gap: 5px;
  font-size: 13px;
  color: #595959;
}
.meta-label { color: #8C8C8C; }
.meta-value { color: #1F1F1F; }
.mono { font-family: 'SF Mono', 'Fira Code', monospace; font-size: 13px; }
.value-green { color: #52C41A; font-weight: 500; }

.signal {
  display: inline-flex;
  align-items: flex-end;
  gap: 1.5px;
  height: 12px;
}
.signal i {
  width: 3px;
  background: #52C41A;
  border-radius: 1px;
}
.signal i:nth-child(1) { height: 4px; }
.signal i:nth-child(2) { height: 6px; }
.signal i:nth-child(3) { height: 9px; }
.signal i:nth-child(4) { height: 12px; }

.head-actions {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
}

/* ── 按钮 (36px高, Ant Design 风格) ── */
.btn {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 0 16px;
  height: 36px;
  border-radius: 6px;
  font-size: 14px;
  cursor: pointer;
  border: 1px solid #D9D9D9;
  background: #FFFFFF;
  color: #1F1F1F;
  transition: all 0.15s;
  white-space: nowrap;
}
.btn:hover { border-color: #1677FF; color: #1677FF; }
.btn:disabled { opacity: 0.5; cursor: not-allowed; }
.btn-primary {
  background: #1677FF;
  color: #fff;
  border-color: #1677FF;
}
.btn-primary:hover { background: #4096FF; border-color: #4096FF; color: #fff; }
.btn-default { background: #FFFFFF; }
.btn-sm { height: 28px; padding: 0 12px; font-size: 13px; }

/* ── 一级 Tab (46px) ── */
.page-tabs {
  display: flex;
  gap: 0;
  border-bottom: 1px solid #F0F0F0;
  margin-bottom: 16px;
  overflow-x: auto;
  height: 46px;
}
.page-tab {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 0 18px;
  font-size: 14px;
  color: #595959;
  cursor: pointer;
  border-bottom: 2px solid transparent;
  margin-bottom: -1px;
  white-space: nowrap;
  transition: all 0.15s;
}
.page-tab:hover { color: #1677FF; }
.page-tab.active {
  color: #1677FF;
  border-bottom-color: #1677FF;
  font-weight: 500;
}

/* ── Alert 横幅 (40px) ── */
.alert-banner {
  display: flex;
  align-items: center;
  gap: 8px;
  background: #FFFBE6;
  border: 1px solid #FFE58F;
  border-radius: 6px;
  padding: 0 14px;
  height: 40px;
  margin-bottom: 16px;
  font-size: 13px;
  color: #595959;
}
.alert-banner b { color: #1F1F1F; }

/* ── 主区双栏 (主:右 ≈ 2:1) ── */
.main-cols {
  display: flex;
  gap: 16px;
  align-items: flex-start;
}
.col-left {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.col-right {
  width: 468px;
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

/* ── 卡片 (8px圆角, 轻阴影) ── */
.card {
  background: #FFFFFF;
  border-radius: 8px;
  border: 1px solid #F0F0F0;
  padding: 16px;
  box-shadow: 0 1px 2px rgba(0,0,0,0.03), 0 2px 8px rgba(0,0,0,0.04);
}
.card-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 14px;
  flex-wrap: wrap;
}
.card-title {
  font-size: 15px;
  font-weight: 600;
  color: #1F1F1F;
}
.card-head-right {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 8px;
}
.card-head-action {
  color: #8C8C8C;
  cursor: pointer;
}
.card-head-action:hover { color: #1677FF; }

/* ── 协议子 Tab (48px) ── */
.bus-subtabs {
  display: flex;
  gap: 0;
  border-bottom: 1px solid #F0F0F0;
  margin-bottom: 12px;
  overflow-x: auto;
  height: 48px;
}
.bus-subtab {
  display: flex;
  align-items: center;
  gap: 5px;
  padding: 0 16px;
  font-size: 13px;
  color: #595959;
  cursor: pointer;
  border-bottom: 2px solid transparent;
  margin-bottom: -1px;
  white-space: nowrap;
  transition: all 0.15s;
}
.bus-subtab:hover { color: #1677FF; }
.bus-subtab.active {
  color: #1677FF;
  border-bottom-color: #1677FF;
  font-weight: 500;
}
.bus-desc {
  font-size: 12px;
  color: #8C8C8C;
  margin-bottom: 16px;
}

/* ── 统计行 (裸排, 48px高) ── */
.stat-row {
  display: grid;
  grid-template-columns: repeat(6, 1fr);
  gap: 8px;
  margin-bottom: 16px;
  height: 48px;
}
.stat-item {
  display: flex;
  align-items: center;
  gap: 8px;
}
.stat-icon {
  width: 32px;
  height: 32px;
  border-radius: 6px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}
.stat-label {
  font-size: 12px;
  color: #8C8C8C;
  white-space: nowrap;
}
.stat-value {
  font-size: 16px;
  font-weight: 600;
  color: #1F1F1F;
  line-height: 1.2;
}

/* ── 表格 (行高44px) ── */
.table-wrap {
  overflow-x: auto;
}
.tbl {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.tbl th {
  text-align: left;
  padding: 10px 8px;
  color: #595959;
  font-weight: 500;
  font-size: 13px;
  border-bottom: 1px solid #F0F0F0;
  white-space: nowrap;
}
.tbl td {
  padding: 10px 8px;
  border-bottom: 1px solid #F0F0F0;
  vertical-align: middle;
  white-space: nowrap;
  height: 44px;
}
.tbl tbody tr {
  cursor: pointer;
  transition: background 0.1s;
}
.tbl tbody tr:hover { background: #F5F5F5; }
.tbl tbody tr.selected { background: #F0F5FF; }
.tbl tbody tr.disabled { opacity: 0.5; }
.tbl tbody tr.disabled .link-btn { cursor: not-allowed; }

/* ── 单选圈 ── */
.radio {
  width: 14px;
  height: 14px;
  border-radius: 50%;
  border: 2px solid #D9D9D9;
  display: inline-block;
  position: relative;
  flex-shrink: 0;
}
.radio.checked {
  border-color: #1677FF;
}
.radio.checked::after {
  content: '';
  position: absolute;
  inset: 2px;
  border-radius: 50%;
  background: #1677FF;
}

/* ── 标签 ── */
.tag {
  display: inline-flex;
  align-items: center;
  padding: 1px 8px;
  border-radius: 4px;
  font-size: 12px;
  font-weight: 400;
  margin-right: 4px;
}
.tag-green { background: #F6FFED; color: #52C41A; }
.tag-red { background: #FFF2F0; color: #FF4D4F; }
.tag-blue { background: #E6F4FF; color: #1677FF; }
.tag-gray { background: #F5F5F5; color: #8C8C8C; }
.tag-green-sm {
  background: #F6FFED; color: #52C41A;
  padding: 1px 8px; border-radius: 4px; font-size: 12px;
}
.tag-orange-sm {
  background: #FFF7E6; color: #FAAD14;
  padding: 1px 6px; border-radius: 4px; font-size: 11px; font-weight: 500;
}

/* ── 开关 (28x16px, Ant Design 小尺寸) ── */
.switch {
  width: 28px;
  height: 16px;
  border-radius: 8px;
  background: #BFBFBF;
  position: relative;
  cursor: pointer;
  transition: background 0.2s;
  display: inline-block;
}
.switch::after {
  content: '';
  position: absolute;
  top: 2px;
  left: 2px;
  width: 12px;
  height: 12px;
  border-radius: 50%;
  background: #fff;
  transition: transform 0.2s;
  box-shadow: 0 1px 3px rgba(0,0,0,0.15);
}
.switch.on {
  background: #52C41A;
}
.switch.on::after {
  transform: translateX(12px);
}

/* ── 行操作 (文字链接+图标, Ant Design 风格) ── */
.row-actions {
  display: flex;
  gap: 12px;
  align-items: center;
}
.link-btn {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  border: none;
  background: none;
  color: #1677FF;
  font-size: 13px;
  cursor: pointer;
  padding: 0;
  transition: color 0.15s;
}
.link-btn:hover { color: #4096FF; }
.link-btn:disabled { color: #BFBFBF; cursor: not-allowed; }
.link-btn:disabled:hover { color: #BFBFBF; }
.link-more {
  color: #8C8C8C;
}
.link-more:hover { color: #1677FF; }

/* ── 分页 (48px) ── */
.pagination {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding-top: 12px;
  font-size: 13px;
  color: #595959;
  flex-wrap: wrap;
  gap: 8px;
  height: 48px;
}
.page-left {
  display: flex;
  align-items: center;
  gap: 4px;
  position: relative;
}
.page-total { margin-left: 8px; }
.page-size {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  padding: 0 8px;
  height: 28px;
  border: 1px solid #D9D9D9;
  border-radius: 6px;
  cursor: pointer;
  background: #FFFFFF;
  font-size: 13px;
}
.page-size:hover { border-color: #1677FF; }
.page-size-menu {
  position: absolute;
  bottom: 32px;
  left: 30px;
  background: #fff;
  border: 1px solid #F0F0F0;
  border-radius: 6px;
  box-shadow: 0 4px 12px rgba(0,0,0,0.08);
  padding: 4px 0;
  z-index: 10;
}
.page-size-item {
  padding: 6px 16px;
  cursor: pointer;
  font-size: 13px;
}
.page-size-item:hover { background: #E6F4FF; color: #1677FF; }

.page-right {
  display: flex;
  align-items: center;
  gap: 6px;
}
.pg-btn {
  padding: 0 10px;
  height: 28px;
  border: 1px solid #D9D9D9;
  border-radius: 6px;
  background: #FFFFFF;
  font-size: 13px;
  color: #595959;
  cursor: pointer;
  transition: all 0.15s;
}
.pg-btn:hover:not(:disabled) { border-color: #1677FF; color: #1677FF; }
.pg-btn:disabled { opacity: 0.4; cursor: not-allowed; }
.pg-current {
  background: #1677FF;
  color: #fff;
  border-color: #1677FF;
  min-width: 28px;
}
.pg-info { font-size: 13px; color: #8C8C8C; margin-left: 8px; }

/* ── 工具卡 (96px高) ── */
.tool-cards {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 16px;
}
.tool-card {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-height: 96px;
}
.tool-head {
  display: flex;
  align-items: center;
  gap: 8px;
}
.tool-icon {
  width: 24px;
  height: 24px;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}
.tool-title {
  font-size: 14px;
  font-weight: 600;
  color: #1F1F1F;
}
.tool-desc {
  font-size: 12px;
  color: #8C8C8C;
  line-height: 1.4;
}
.tool-foot {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-top: auto;
}
.tool-result { font-size: 12px; }

/* ── 详情列表 ── */
.detail-list {
  display: flex;
  flex-direction: column;
  gap: 0;
}
.detail-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 0;
  height: 28px;
  border-bottom: 1px solid #F0F0F0;
  font-size: 13px;
}
.detail-row:last-child { border-bottom: none; }
.detail-label { color: #8C8C8C; }
.detail-value { color: #1F1F1F; font-weight: 500; }

/* ── 表单卡片 ── */
.form-card {
  position: relative;
}
.form-tabs {
  display: flex;
  gap: 0;
  border-bottom: 1px solid #F0F0F0;
  margin-bottom: 14px;
  overflow-x: auto;
  height: 36px;
}
.form-tab {
  padding: 0 14px;
  font-size: 13px;
  color: #595959;
  cursor: pointer;
  border-bottom: 2px solid transparent;
  margin-bottom: -1px;
  white-space: nowrap;
  transition: all 0.15s;
  display: flex;
  align-items: center;
}
.form-tab:hover { color: #1677FF; }
.form-tab.active {
  color: #1677FF;
  border-bottom-color: #1677FF;
  font-weight: 500;
}

.form-body {
  min-height: 200px;
}
.form-item {
  margin-bottom: 16px;
  flex: 1;
}
.form-row {
  display: flex;
  gap: 12px;
}
.form-label {
  display: block;
  font-size: 13px;
  color: #1F1F1F;
  margin-bottom: 8px;
  font-weight: 500;
}
.required { color: #FF4D4F; }

.input-wrap {
  position: relative;
}
.form-input {
  width: 100%;
  padding: 0 12px;
  height: 32px;
  border: 1px solid #D9D9D9;
  border-radius: 6px;
  font-size: 13px;
  outline: none;
  transition: border-color 0.15s;
  box-sizing: border-box;
}
.form-input:focus {
  border-color: #1677FF;
  box-shadow: 0 0 0 2px rgba(22,119,255,0.1);
}
.char-count {
  position: absolute;
  right: 8px;
  top: 50%;
  transform: translateY(-50%);
  font-size: 12px;
  color: #BFBFBF;
  pointer-events: none;
}

/* ── 下拉选择 ── */
.fake-select {
  position: relative;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 12px;
  height: 32px;
  border: 1px solid #D9D9D9;
  border-radius: 6px;
  font-size: 13px;
  cursor: pointer;
  background: #FFFFFF;
  transition: border-color 0.15s;
}
.fake-select:hover { border-color: #1677FF; }
.select-menu {
  position: absolute;
  top: 100%;
  left: 0;
  right: 0;
  margin-top: 4px;
  background: #fff;
  border: 1px solid #F0F0F0;
  border-radius: 6px;
  box-shadow: 0 4px 12px rgba(0,0,0,0.08);
  padding: 4px 0;
  z-index: 20;
}
.select-item {
  padding: 7px 12px;
  font-size: 13px;
  color: #595959;
  cursor: pointer;
}
.select-item:hover { background: #E6F4FF; color: #1677FF; }

/* ── 信息横幅 (36px) ── */
.info-banner {
  display: flex;
  align-items: center;
  gap: 6px;
  background: #E6F4FF;
  border: 1px solid #91CAFF;
  border-radius: 6px;
  padding: 0 12px;
  height: 36px;
  font-size: 12px;
  color: #1F1F1F;
  margin-top: 8px;
}

.perm-note {
  font-size: 13px;
  color: #595959;
  line-height: 1.8;
}
.perm-note p { margin: 0 0 6px; }

.form-foot {
  display: flex;
  justify-content: flex-end;
  gap: 12px;
  margin-top: 16px;
  padding-top: 14px;
  border-top: 1px solid #F0F0F0;
}

/* ── 搜索弹窗 ── */
.search-input {
  width: 100%;
  padding: 8px 12px;
  border: 1px solid #D9D9D9;
  border-radius: 6px;
  font-size: 14px;
  outline: none;
  margin-bottom: 10px;
  box-sizing: border-box;
}
.search-input:focus { border-color: #1677FF; }
.search-results { max-height: 300px; overflow-y: auto; }
.search-item {
  padding: 8px 10px;
  border-radius: 4px;
  cursor: pointer;
  font-size: 13px;
  color: #595959;
}
.search-item:hover { background: #E6F4FF; color: #1677FF; }
.search-empty { padding: 20px; text-align: center; color: #BFBFBF; }

/* ── 通知 ── */
.notify-list { display: flex; flex-direction: column; gap: 8px; }
.notify-item {
  padding: 12px;
  border-radius: 8px;
  border: 1px solid #F0F0F0;
  cursor: pointer;
  transition: all 0.15s;
}
.notify-item:hover { border-color: #1677FF; }
.notify-item.unread { background: #E6F4FF; border-color: #91CAFF; }
.notify-title { font-size: 13px; font-weight: 600; margin-bottom: 3px; }
.notify-time { font-size: 11px; color: #BFBFBF; margin-bottom: 4px; }
.notify-content { font-size: 12px; color: #595959; }
.notify-foot { margin-top: 16px; text-align: center; }

/* ── 波特率工具 ── */
.baud-body { padding: 4px 0; }
.baud-channels {
  display: flex;
  gap: 16px;
  padding: 8px 0;
}
.baud-ch {
  display: flex;
  align-items: center;
  gap: 5px;
  font-size: 13px;
  color: #595959;
  cursor: pointer;
}

/* ══════════ 响应式 ══════════ */
@media (max-width: 1200px) {
  .stat-row { grid-template-columns: repeat(3, 1fr); height: auto; }
  .tool-cards { grid-template-columns: 1fr; }
  .main-cols { flex-direction: column; }
  .col-right { width: 100%; }
}

@media (max-width: 768px) {
  .sidebar { display: none; }
  .topbar-search { display: none; }
  .stat-row { grid-template-columns: repeat(2, 1fr); height: auto; }
  .head-actions { width: 100%; }
  .page-head { flex-direction: column; }
  .tool-cards { grid-template-columns: 1fr; }
  .content { padding: 16px; }
  .topbar { padding: 0 16px; }
}

@media (max-width: 480px) {
  .stat-row { grid-template-columns: 1fr; }
  .stat-value { font-size: 14px; }
  .form-row { flex-direction: column; gap: 0; }
  .pagination { flex-direction: column; align-items: flex-start; height: auto; }
  .content { padding: 12px; }
}
</style>
