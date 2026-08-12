<template>
  <!-- 新节点详情页设计稿交互 demo:designs/new-node-1.png 像素级复刻,静态 mock 数据 -->
  <!-- 仅开发环境可见(DEV 门禁路由),全交互原型:按钮/开关/搜索/编辑/弹窗均可用 -->
  <div class="new-node-demo">
    <!-- ══════════ 左侧边栏 ══════════ -->
    <aside class="sidebar">
      <div class="sidebar-logo">
        <svg width="22" height="22" viewBox="0 0 24 24" fill="none" aria-hidden="true">
          <path d="M12 2 L21 7 V17 L12 22 L3 17 V7 Z" stroke="#2E6BFF" stroke-width="1.8" fill="none" />
          <path d="M8 9 H16 M8 12 H15 M8 15 H14" stroke="#2E6BFF" stroke-width="1.8" stroke-linecap="round" />
        </svg>
        <span class="logo-text">EHomeSystem</span>
      </div>

      <nav class="sidebar-nav">
        <div
          v-for="item in navItems"
          :key="item.label"
          class="nav-item"
          :class="{ active: activeNav === item.label }"
          @click="onNavClick(item.label)"
        >
          <el-icon :size="16"><component :is="item.icon" /></el-icon>
          <span class="nav-label">{{ item.label }}</span>
        </div>
      </nav>

      <div class="sidebar-bottom">
        <div class="nav-item" :class="{ active: activeNav === '系统设置' }" @click="onNavClick('系统设置')">
          <el-icon :size="16"><Setting /></el-icon>
          <span class="nav-label">系统设置</span>
        </div>
        <div class="sidebar-version">v2.3.0</div>
      </div>
    </aside>

    <!-- ══════════ 右侧主区 ══════════ -->
    <div class="main">
      <!-- 顶栏 -->
      <header class="topbar">
        <el-icon :size="18" class="topbar-hamburger" @click="info('demo: 侧边栏折叠')"><Menu /></el-icon>
        <div class="breadcrumb">
          <span class="crumb-link" @click="info('demo: 返回首页')">首页</span>
          <span class="crumb-sep">/</span>
          <span class="crumb-link" @click="info('demo: 返回节点管理')">节点管理</span>
          <span class="crumb-sep">/</span>
          <span class="crumb-current">{{ device.name }}</span>
        </div>

        <div class="topbar-search" @click="searchVisible = true">
          <el-icon :size="14"><Search /></el-icon>
          <span class="search-placeholder">搜索节点、设备、通道，或输入快捷指令...</span>
          <span class="search-kbd">⌘ K</span>
        </div>

        <div class="topbar-right">
          <span class="status-pill" @click="info('设备在线,链路正常')">
            <span class="dot dot-green"></span>在线
          </span>
          <span class="bell" @click="notifVisible = true">
            <el-icon :size="18"><Bell /></el-icon>
            <span v-if="unreadCount > 0" class="bell-badge">{{ unreadCount }}</span>
          </span>
          <el-dropdown trigger="click" @command="onUserCommand">
            <span class="user-chip">
              <span class="avatar">A</span>
              <span class="user-name">admin</span>
              <el-icon :size="12"><ArrowDown /></el-icon>
            </span>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="profile">个人资料</el-dropdown-item>
                <el-dropdown-item command="password">修改密码</el-dropdown-item>
                <el-dropdown-item divided command="logout">退出登录</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
      </header>

      <!-- 内容滚动区 -->
      <div class="content">
        <!-- 页头 -->
        <div class="page-header">
          <div class="ph-left">
            <div class="ph-title-row">
              <h1 class="ph-title">{{ device.name }}</h1>
              <el-icon :size="16" class="ph-edit" @click="renameVisible = true"><EditPen /></el-icon>
              <span class="badge badge-green"><span class="dot dot-green"></span>在线</span>
              <span class="quality" @click="info('信号 RSSI -62 dBm,链路质量优秀')">
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" aria-hidden="true">
                  <rect x="3" y="14" width="3.4" height="7" rx="1" fill="#22C55E" />
                  <rect x="8.2" y="10" width="3.4" height="11" rx="1" fill="#22C55E" />
                  <rect x="13.4" y="6" width="3.4" height="15" rx="1" fill="#22C55E" />
                  <rect x="18.6" y="2" width="3.4" height="19" rx="1" fill="#22C55E" opacity="0.35" />
                </svg>
                连接质量 <b class="q-val">92%</b> <b class="q-text">优秀</b>
              </span>
            </div>
            <div class="ph-id">
              设备ID: {{ device.id }}
              <el-icon :size="13" class="copy-icon" @click="copyId"><CopyDocument /></el-icon>
            </div>
          </div>
          <div class="ph-actions">
            <button class="btn btn-plain" :disabled="syncing" @click="onSyncConfig">
              <el-icon :size="14" :class="{ spin: syncing }"><Refresh /></el-icon>{{ syncing ? '同步中...' : '同步配置' }}
            </button>
            <button class="btn btn-primary" @click="otaVisible = true">
              <el-icon :size="14"><UploadFilled /></el-icon>OTA 升级
            </button>
            <button class="btn btn-plain" :disabled="pinging" @click="onPing">
              <el-icon :size="14"><Odometer /></el-icon>{{ pinging ? '测试中...' : '测延迟' }}
            </button>
            <button class="btn btn-plain" :disabled="refreshing" @click="onRefresh">
              <el-icon :size="14" :class="{ spin: refreshing }"><RefreshRight /></el-icon>{{ refreshing ? '刷新中...' : '刷新' }}
            </button>
          </div>
        </div>

        <!-- 五格统计条 -->
        <div class="stat-strip card">
          <div class="stat-item">
            <div class="stat-icon"><el-icon :size="16"><Cpu /></el-icon></div>
            <div class="stat-text">
              <div class="stat-label">型号</div>
              <div class="stat-value">{{ device.model }}</div>
            </div>
          </div>
          <div class="stat-sep"></div>
          <div class="stat-item">
            <div class="stat-icon"><el-icon :size="16"><Cpu /></el-icon></div>
            <div class="stat-text">
              <div class="stat-label">固件版本</div>
              <div class="stat-value">{{ device.fw }}</div>
            </div>
          </div>
          <div class="stat-sep"></div>
          <div class="stat-item">
            <div class="stat-icon"><el-icon :size="16"><Clock /></el-icon></div>
            <div class="stat-text">
              <div class="stat-label">最后上线</div>
              <div class="stat-value">{{ lastOnlineText }}</div>
            </div>
          </div>
          <div class="stat-sep"></div>
          <div class="stat-item">
            <div class="stat-icon"><el-icon :size="16"><Timer /></el-icon></div>
            <div class="stat-text">
              <div class="stat-label">在线时长</div>
              <div class="stat-value">{{ uptimeText }}</div>
            </div>
          </div>
          <div class="stat-sep"></div>
          <div class="stat-item">
            <div class="stat-icon">
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" aria-hidden="true">
                <circle cx="12" cy="12" r="9" stroke="#2E6BFF" stroke-width="1.6" />
                <path d="M3 12 H21 M12 3 c3 3.5 3 14 0 18 M12 3 c-3 3.5 -3 14 0 18" stroke="#2E6BFF" stroke-width="1.6" fill="none" />
              </svg>
            </div>
            <div class="stat-text">
              <div class="stat-label">设备时区</div>
              <div class="stat-value">{{ device.tz }}</div>
            </div>
          </div>
        </div>

        <!-- Tab 栏 -->
        <div class="tab-bar card">
          <div
            v-for="tab in tabs"
            :key="tab.label"
            class="tab-item"
            :class="{ active: activeTab === tab.label }"
            @click="onTabClick(tab.label)"
          >
            <el-icon :size="15"><component :is="tab.icon" /></el-icon>
            <span>{{ tab.label }}</span>
          </div>
        </div>

        <!-- 基本信息 tab 内容 -->
        <template v-if="activeTab === '基本信息'">
          <!-- 第一行三卡 -->
          <div class="card-row row-1">
            <!-- 设备基本信息 -->
            <div class="card info-card">
              <div class="card-head">
                <span class="card-title">设备基本信息</span>
              </div>
              <div class="info-grid">
                <div class="info-col">
                  <div class="info-row"><span class="info-label">节点名称</span><span class="info-val">{{ device.name }} <el-icon :size="12" class="mini-edit" @click="renameVisible = true"><EditPen /></el-icon></span></div>
                  <div class="info-row"><span class="info-label">设备 ID</span><span class="info-val mono">{{ device.id }} <el-icon :size="12" class="mini-edit" @click="copyId"><CopyDocument /></el-icon></span></div>
                  <div class="info-row"><span class="info-label">型号</span><span class="info-val">{{ device.model }}</span></div>
                  <div class="info-row"><span class="info-label">固件版本</span><span class="info-val">{{ device.fw }}</span></div>
                  <div class="info-row"><span class="info-label">状态</span><span class="info-val"><span class="dot dot-green"></span> 在线</span></div>
                  <div class="info-row">
                    <span class="info-label">连接质量</span>
                    <span class="info-val">
                      <span class="qbar"><span class="qbar-fill" :style="{ width: '92%' }"></span></span>
                      <span class="q-num">92%</span> <span class="q-good">优秀</span>
                    </span>
                  </div>
                </div>
                <div class="info-col">
                  <div class="info-row"><span class="info-label">延迟</span><span class="info-val latency">{{ latency }} ms</span></div>
                  <div class="info-row"><span class="info-label">上线时间</span><span class="info-val">{{ lastOnlineText }}</span></div>
                  <div class="info-row"><span class="info-label">在线时长</span><span class="info-val">{{ uptimeText }}</span></div>
                  <div class="info-row"><span class="info-label">设备时区</span><span class="info-val">{{ device.tz }}</span></div>
                  <div class="info-row"><span class="info-label">备注</span><span class="info-val dim">{{ device.remark || '—' }}</span></div>
                </div>
              </div>
            </div>

            <!-- 实时指标 -->
            <div class="card metrics-card">
              <div class="card-head">
                <span class="card-title">实时指标</span>
                <span class="metrics-updated">
                  更新于 {{ metricsAgo }} 秒前
                  <el-icon :size="13" class="mini-refresh" @click="refreshMetrics"><RefreshRight /></el-icon>
                </span>
              </div>
              <div class="metric-list">
                <div v-for="m in metrics" :key="m.name" class="metric-row">
                  <span class="metric-icon" :style="{ background: m.color + '1A', color: m.color }">
                    <el-icon :size="13"><component :is="m.icon" /></el-icon>
                  </span>
                  <span class="metric-name">{{ m.name }}</span>
                  <span class="metric-val">{{ m.display }}<span class="metric-unit">{{ m.unit }}</span></span>
                  <svg class="spark" viewBox="0 0 72 24" preserveAspectRatio="none">
                    <path :d="sparkPath(m.data)" fill="none" :stroke="m.color" stroke-width="1.5" stroke-linecap="round" />
                    <circle :cx="sparkEnd(m.data).x" :cy="sparkEnd(m.data).y" r="1.8" :fill="m.color" />
                  </svg>
                </div>
              </div>
            </div>

            <!-- 设备位置 -->
            <div class="card map-card">
              <div class="card-head">
                <span class="card-title">设备位置</span>
              </div>
              <div class="map-placeholder" @click="info('demo: 打开大地图')">
                <svg class="map-roads" viewBox="0 0 240 180" preserveAspectRatio="none" aria-hidden="true">
                  <path d="M-10 60 L70 40 L130 70 L250 50" stroke="#fff" stroke-width="7" fill="none" opacity="0.9" />
                  <path d="M40 -10 L60 80 L50 190" stroke="#fff" stroke-width="5" fill="none" opacity="0.8" />
                  <path d="M-10 130 L100 110 L180 140 L250 120" stroke="#fff" stroke-width="6" fill="none" opacity="0.85" />
                  <path d="M150 -10 L140 60 L170 120 L160 190" stroke="#fff" stroke-width="4" fill="none" opacity="0.7" />
                  <path d="M-10 20 L120 10 L250 25" stroke="#fff" stroke-width="4" fill="none" opacity="0.6" />
                  <path d="M210 -10 L200 70 L230 130 L215 190" stroke="#fff" stroke-width="5" fill="none" opacity="0.75" />
                  <path d="M90 40 L95 120 L85 190" stroke="#fff" stroke-width="3" fill="none" opacity="0.55" />
                </svg>
                <svg class="map-pin" width="34" height="34" viewBox="0 0 24 24" fill="none">
                  <path d="M12 21s-7-5.5-7-11a7 7 0 1 1 14 0c0 5.5-7 11-7 11z" fill="#2E6BFF" opacity="0.9" />
                  <circle cx="12" cy="10" r="2.6" fill="#fff" />
                </svg>
                <button class="btn btn-plain map-btn" @click.stop="locationVisible = true">
                  <el-icon :size="13"><Aim /></el-icon>{{ device.location ? '修改位置' : '设置位置' }}
                </button>
              </div>
            </div>
          </div>

          <!-- 第二行三卡 -->
          <div class="card-row row-2">
            <!-- 最近事件 -->
            <div class="card events-card">
              <div class="card-head">
                <span class="card-title">最近事件</span>
                <span class="card-link" @click="eventsVisible = true">查看全部</span>
              </div>
              <div class="timeline">
                <div v-for="(ev, i) in recentEvents" :key="i" class="tl-row">
                  <span class="tl-icon" :class="ev.ok ? 'tl-ok' : 'tl-bad'">
                    <el-icon :size="10"><component :is="ev.ok ? Select : SwitchButton" /></el-icon>
                  </span>
                  <span class="tl-text">{{ ev.text }}</span>
                  <span class="tl-time">{{ ev.time }}</span>
                </div>
              </div>
            </div>

            <!-- 通道健康状态 -->
            <div class="card health-card">
              <div class="card-head">
                <span class="card-title">通道健康状态</span>
                <span class="card-link" @click="healthVisible = true">查看全部</span>
              </div>
              <div class="chips">
                <div class="chip chip-total" @click="info('通道总数 12')"><span class="chip-label"><i class="chip-dot" style="background:#8A93A3"></i>总数</span><span class="chip-num">12</span></div>
                <div class="chip chip-ok" @click="info('正常通道 10')"><span class="chip-label"><i class="chip-dot" style="background:#22C55E"></i>正常</span><span class="chip-num">10</span></div>
                <div class="chip chip-warn" @click="info('告警通道 1: UART')"><span class="chip-label"><i class="chip-dot" style="background:#F59E0B"></i>告警</span><span class="chip-num">1</span></div>
                <div class="chip chip-off" @click="info('离线通道 1: ADC')"><span class="chip-label"><i class="chip-dot" style="background:#9CA3AF"></i>离线</span><span class="chip-num dim-num">1</span></div>
              </div>
              <div class="chan-list">
                <div v-for="ch in channels" :key="ch.name" class="chan-row" @click="onChannelClick(ch)">
                  <span class="chan-icon"><el-icon :size="14"><Link /></el-icon></span>
                  <span class="chan-name">{{ ch.name }}</span>
                  <span class="chan-badge" :class="'cb-' + ch.status">{{ ch.statusText }}</span>
                  <span class="chan-ratio">{{ ch.ratio }}</span>
                  <el-icon :size="13" class="chan-arrow"><ArrowRight /></el-icon>
                </div>
              </div>
            </div>

            <!-- 备注 -->
            <div class="card remark-card">
              <div class="card-head">
                <span class="card-title">备注</span>
                <el-icon :size="15" class="mini-edit" @click="remarkVisible = true"><EditPen /></el-icon>
              </div>
              <div v-if="!device.remark" class="remark-empty">
                <svg width="60" height="60" viewBox="0 0 24 24" fill="none" aria-hidden="true">
                  <path d="M6 3h9l4 4v14a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1z" stroke="#C9D2DE" stroke-width="1.4" fill="#F2F6FC" />
                  <path d="M14 3v5h5" stroke="#C9D2DE" stroke-width="1.4" fill="none" />
                  <circle cx="16.5" cy="16.5" r="3.2" stroke="#C9D2DE" stroke-width="1.4" fill="#fff" />
                  <path d="M18.8 18.8 L21 21" stroke="#C9D2DE" stroke-width="1.4" stroke-linecap="round" />
                </svg>
                <div class="re-title">暂无备注信息</div>
                <div class="re-sub">点击右上角编辑备注</div>
              </div>
              <div v-else class="remark-body" @click="remarkVisible = true">{{ device.remark }}</div>
            </div>
          </div>
        </template>

        <!-- 其他 tab 占位 -->
        <div v-else class="card tab-placeholder">
          <el-icon :size="40" color="#C9D2DE"><Files /></el-icon>
          <div class="tp-title">「{{ activeTab }}」设计稿未包含</div>
          <div class="tp-sub">demo 仅复刻 new-node-1.png 的「基本信息」页;总线配置见 designs/new-node-2.png</div>
          <button class="btn btn-plain" @click="activeTab = '基本信息'">返回基本信息</button>
        </div>
      </div>
    </div>

    <!-- ══════════ 搜索弹窗 ══════════ -->
    <el-dialog v-model="searchVisible" width="520px" :show-close="false" class="search-dialog" append-to-body>
      <div class="search-box">
        <el-icon :size="16"><Search /></el-icon>
        <input
          ref="searchInputRef"
          v-model="searchQuery"
          class="search-input"
          placeholder="搜索节点、设备、通道，或输入快捷指令..."
          @keydown.esc="searchVisible = false"
        />
        <span class="search-kbd">ESC</span>
      </div>
      <div class="search-results">
        <div v-if="searchResults.length === 0" class="search-empty">无匹配结果</div>
        <div
          v-for="r in searchResults"
          :key="r"
          class="search-row"
          @click="onSearchPick(r)"
        >
          <el-icon :size="13"><Aim /></el-icon>{{ r }}
        </div>
      </div>
    </el-dialog>

    <!-- ══════════ 通知抽屉 ══════════ -->
    <el-drawer v-model="notifVisible" title="通知中心" size="360px">
      <div class="notif-head">
        <span class="notif-unread">{{ unreadCount }} 条未读</span>
        <span class="card-link" @click="markAllRead">全部已读</span>
      </div>
      <div v-for="(n, i) in notifications" :key="i" class="notif-row" :class="{ unread: n.unread }" @click="n.unread = false">
        <span class="dot" :class="n.unread ? 'dot-red' : 'dot-gray'"></span>
        <div class="notif-body">
          <div class="notif-text">{{ n.text }}</div>
          <div class="notif-time">{{ n.time }}</div>
        </div>
      </div>
    </el-drawer>

    <!-- ══════════ 重命名弹窗 ══════════ -->
    <el-dialog v-model="renameVisible" title="编辑设备名称" width="420px">
      <div class="form-row">
        <label class="form-label">设备名称</label>
        <el-input v-model="renameDraft" maxlength="32" show-word-limit placeholder="请输入设备名称" />
      </div>
      <template #footer>
        <button class="btn btn-plain" @click="renameVisible = false">取消</button>
        <button class="btn btn-primary" @click="saveRename">保存</button>
      </template>
    </el-dialog>

    <!-- ══════════ 备注编辑弹窗 ══════════ -->
    <el-dialog v-model="remarkVisible" title="编辑备注" width="480px">
      <el-input v-model="remarkDraft" type="textarea" :rows="4" maxlength="200" show-word-limit placeholder="请输入备注信息" />
      <template #footer>
        <button class="btn btn-plain" @click="remarkVisible = false">取消</button>
        <button class="btn btn-primary" @click="saveRemark">保存</button>
      </template>
    </el-dialog>

    <!-- ══════════ 位置设置弹窗 ══════════ -->
    <el-dialog v-model="locationVisible" title="设置设备位置" width="480px">
      <div class="form-row">
        <label class="form-label">位置描述</label>
        <el-input v-model="locationDraft" maxlength="64" placeholder="如: 机房 A 区 3 号机柜" />
      </div>
      <div class="form-row">
        <label class="form-label">经纬度</label>
        <el-input v-model="locationCoord" placeholder="如: 31.2304, 121.4737" />
      </div>
      <template #footer>
        <button class="btn btn-plain" @click="locationVisible = false">取消</button>
        <button class="btn btn-primary" @click="saveLocation">保存</button>
      </template>
    </el-dialog>

    <!-- ══════════ OTA 升级弹窗 ══════════ -->
    <el-dialog v-model="otaVisible" title="OTA 固件升级" width="480px">
      <div class="ota-current">
        <span class="info-label">当前版本</span>
        <span class="info-val">{{ device.fw }}</span>
      </div>
      <div class="form-row">
        <label class="form-label">目标版本</label>
        <el-select v-model="otaTarget" style="width: 100%">
          <el-option label="2.5.19 (推荐) - 修复 UART 超时" value="2.5.19" />
          <el-option label="2.6.0-beta - 多总线事件驱动" value="2.6.0-beta" />
        </el-select>
      </div>
      <div class="ota-tip">升级过程约 2-3 分钟,期间设备将重启,请确认当前无关键业务。</div>
      <template #footer>
        <button class="btn btn-plain" @click="otaVisible = false">取消</button>
        <button class="btn btn-primary" @click="startOta">开始升级</button>
      </template>
    </el-dialog>

    <!-- ══════════ 全部事件弹窗 ══════════ -->
    <el-dialog v-model="eventsVisible" title="全部事件" width="560px">
      <div class="all-events">
        <div v-for="(ev, i) in events" :key="i" class="ae-row">
          <span class="tl-icon" :class="ev.ok ? 'tl-ok' : 'tl-bad'">
            <el-icon :size="10"><component :is="ev.ok ? Select : SwitchButton" /></el-icon>
          </span>
          <span class="ae-text">{{ ev.text }}</span>
          <span class="ae-time">{{ ev.time }}</span>
        </div>
      </div>
    </el-dialog>

    <!-- ══════════ 全部通道弹窗 ══════════ -->
    <el-dialog v-model="healthVisible" title="全部通道 (12)" width="560px">
      <div class="all-chans">
        <div v-for="ch in allChannels" :key="ch.name" class="chan-row" @click="onChannelClick(ch)">
          <span class="chan-icon"><el-icon :size="14"><Link /></el-icon></span>
          <span class="chan-name">{{ ch.name }}</span>
          <span class="chan-badge" :class="'cb-' + ch.status">{{ ch.statusText }}</span>
          <span class="chan-ratio">{{ ch.ratio }}</span>
        </div>
      </div>
    </el-dialog>

    <!-- ══════════ 通道详情弹窗 ══════════ -->
    <el-dialog v-model="chanVisible" :title="activeChannel?.name + ' 详情'" width="440px">
      <div class="info-grid" v-if="activeChannel">
        <div class="info-row"><span class="info-label">通道名称</span><span class="info-val">{{ activeChannel.name }}</span></div>
        <div class="info-row"><span class="info-label">状态</span><span class="info-val"><span class="chan-badge" :class="'cb-' + activeChannel.status">{{ activeChannel.statusText }}</span></span></div>
        <div class="info-row"><span class="info-label">挂载设备</span><span class="info-val">{{ activeChannel.ratio }}</span></div>
        <div class="info-row"><span class="info-label">说明</span><span class="info-val dim">{{ chanDesc[activeChannel.status] }}</span></div>
      </div>
      <template #footer>
        <button class="btn btn-plain" @click="chanVisible = false">关闭</button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onUnmounted, nextTick, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  Menu, Search, Bell, ArrowDown, ArrowRight, EditPen, CopyDocument, Refresh, RefreshRight,
  UploadFilled, Odometer, Cpu, Clock, Timer, Connection,
  Link, Select, SwitchButton, Setting, Files, Aim, House, Monitor, Grid, DataLine,
  Document, TopRight, Download, Box, Share, UserFilled, Cloudy, DataAnalysis,
} from '@element-plus/icons-vue'

// ── 提示工具 ──
const toast = (msg: string) => ElMessage({ message: msg, type: 'success', grouping: true })
const info = (msg: string) => ElMessage({ message: msg, type: 'info', grouping: true })

// ── 侧边栏 ──
const navItems = [
  { label: '仪表盘', icon: Grid },
  { label: '节点', icon: Monitor },
  { label: '边缘设备', icon: Box },
  { label: '通道管理', icon: Share },
  { label: '数据面板', icon: DataLine },
  { label: '固件管理', icon: Cpu },
  { label: '配置模板', icon: Setting },
  { label: '系统监控', icon: DataAnalysis },
]
const activeNav = ref('节点')
function onNavClick(label: string) {
  activeNav.value = label
  if (label !== '节点') info(`demo: 「${label}」无路由,设计稿仅含节点详情`)
}

// ── 用户下拉 ──
function onUserCommand(cmd: string) {
  if (cmd === 'logout') {
    ElMessageBox.confirm('确认退出登录?', '提示', { confirmButtonText: '退出', cancelButtonText: '取消' })
      .then(() => info('demo: 已退出(无真实会话)')).catch(() => {})
  } else if (cmd === 'profile') info('demo: 个人资料页未复刻')
  else info('demo: 修改密码弹窗未复刻')
}

// ── 设备数据 ──
const device = reactive({
  name: '设备名称',
  id: '30EDA0A9A808',
  model: 'esp32s3',
  fw: '2.5.18',
  tz: 'Asia/Shanghai',
  remark: '',
  location: '',
})

// 上线时间: 固定 mock 为 2026-08-08 21:18:42 (设计稿值); 在线时长接真实时钟走字
const bootTime = new Date('2026-08-08T21:18:42+08:00').getTime()
const nowTick = ref(Date.now())
const lastOnlineText = '2026-08-08 21:18:42'
const uptimeText = computed(() => {
  // demo 起点 = 设计稿 2天2小时13分钟, 之后随真实时间走字
  const base = (2 * 24 * 60 + 2 * 60 + 13) * 60 * 1000
  const elapsed = base + (nowTick.value - mountTime)
  const m = Math.floor(elapsed / 60000)
  const d = Math.floor(m / 1440)
  const h = Math.floor((m % 1440) / 60)
  const mm = m % 60
  return `${d}天 ${h}小时 ${mm}分钟`
})

// ── 页头操作 ──
const syncing = ref(false)
function onSyncConfig() {
  syncing.value = true
  setTimeout(() => {
    syncing.value = false
    pushEvent('配置同步成功', true)
    toast('配置同步完成')
  }, 900)
}
const pinging = ref(false)
const latency = ref(12)
function onPing() {
  pinging.value = true
  setTimeout(() => {
    pinging.value = false
    latency.value = 10 + Math.round(Math.random() * 8)
    toast(`延迟测试完成: ${latency.value} ms`)
  }, 800)
}
const refreshing = ref(false)
function onRefresh() {
  refreshing.value = true
  setTimeout(() => {
    refreshing.value = false
    refreshMetrics()
    toast('数据已刷新')
  }, 700)
}
function copyId() {
  navigator.clipboard?.writeText(device.id).then(() => toast('设备 ID 已复制')).catch(() => info(device.id))
}

// ── Tab 栏 ──
const tabs = [
  { label: '基本信息', icon: House },
  { label: '总线配置', icon: Share },
  { label: 'DMA 通道', icon: Grid },
  { label: '关联设备', icon: UserFilled },
  { label: 'OTA 历史', icon: Cloudy },
  { label: '系统日志', icon: Document },
]
const activeTab = ref('基本信息')
function onTabClick(label: string) {
  activeTab.value = label
  if (label === '总线配置') info('demo: 总线配置见 designs/new-node-2.png,本页未复刻')
}

// ── 实时指标 ──
function genSeries(base: number, amp: number): number[] {
  const arr: number[] = []
  for (let i = 0; i < 18; i++) arr.push(base + Math.sin(i / 2.6) * amp + (Math.random() - 0.5) * amp * 0.5)
  return arr
}
interface Metric { name: string; icon: unknown; color: string; unit: string; value: number; display: string; data: number[] }
const metrics = reactive<Metric[]>([
  { name: 'CPU 使用率', icon: Cpu, color: '#2E6BFF', unit: '%', value: 18, display: '18', data: genSeries(18, 6) },
  { name: '内存使用率', icon: Odometer, color: '#22C55E', unit: '%', value: 42, display: '42', data: genSeries(42, 4) },
  { name: '信号强度(RSSI)', icon: Connection, color: '#8B5CF6', unit: ' dBm', value: -62, display: '-62', data: genSeries(50, 8) },
  { name: '上行速率', icon: TopRight, color: '#F59E0B', unit: ' KB/s', value: 1.2, display: '1.2', data: genSeries(1.2, 0.5) },
  { name: '下行速率', icon: Download, color: '#2E6BFF', unit: ' KB/s', value: 2.8, display: '2.8', data: genSeries(2.8, 0.9) },
])

const metricsAgo = ref(2)
function refreshMetrics() {
  metrics.forEach((m, idx) => {
    m.data.shift()
    m.data.push(m.data[m.data.length - 1] + (Math.random() - 0.5) * 3)
    if (idx === 0) { m.value = Math.max(5, Math.min(60, m.value + Math.round((Math.random() - 0.5) * 6))); m.display = String(m.value) }
    if (idx === 1) { m.value = Math.max(30, Math.min(70, m.value + Math.round((Math.random() - 0.5) * 4))); m.display = String(m.value) }
    if (idx === 2) { m.value = Math.max(-80, Math.min(-45, m.value + Math.round((Math.random() - 0.5) * 4))); m.display = String(m.value) }
    if (idx === 3) { m.value = Math.max(0.2, +(m.value + (Math.random() - 0.5) * 0.4).toFixed(1)); m.display = m.value.toFixed(1) }
    if (idx === 4) { m.value = Math.max(0.5, +(m.value + (Math.random() - 0.5) * 0.6).toFixed(1)); m.display = m.value.toFixed(1) }
  })
  metricsAgo.value = 0
}
function sparkPath(data: number[]): string {
  const w = 72, h = 24, pad = 2
  const min = Math.min(...data), max = Math.max(...data)
  const span = max - min || 1
  const pts = data.map((v, i) => ({
    x: pad + (i / (data.length - 1)) * (w - pad * 2),
    y: h - pad - ((v - min) / span) * (h - pad * 2),
  }))
  return pts.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x.toFixed(1)},${p.y.toFixed(1)}`).join(' ')
}
function sparkEnd(data: number[]): { x: number; y: number } {
  const w = 72, h = 24, pad = 2
  const min = Math.min(...data), max = Math.max(...data)
  const span = max - min || 1
  const v = data[data.length - 1]
  return { x: w - pad, y: h - pad - ((v - min) / span) * (h - pad * 2) }
}

// ── 最近事件 ──
interface Ev { text: string; time: string; ok: boolean }
const events = reactive<Ev[]>([
  { text: '设备上线', time: '2026-08-08 21:18:42', ok: true },
  { text: '配置同步成功', time: '2026-08-08 21:18:40', ok: true },
  { text: '通道数据接收正常', time: '2026-08-08 21:18:30', ok: true },
  { text: 'OTA 升级完成 (2.5.17 → 2.5.18)', time: '2026-08-07 16:22:11', ok: false },
  { text: '设备重启', time: '2026-08-07 16:21:02', ok: false },
])
const recentEvents = computed(() => events.slice(0, 5))
function pushEvent(text: string, ok: boolean) {
  const now = new Date()
  const p = (n: number) => String(n).padStart(2, '0')
  events.unshift({ text, time: `${now.getFullYear()}-${p(now.getMonth() + 1)}-${p(now.getDate())} ${p(now.getHours())}:${p(now.getMinutes())}:${p(now.getSeconds())}`, ok })
}

// ── 通道健康 ──
interface Chan { name: string; status: string; statusText: string; ratio: string }
const channels: Chan[] = [
  { name: 'I2C 总线', status: 'ok', statusText: '正常', ratio: '8/8' },
  { name: 'UART 通道', status: 'warn', statusText: '告警', ratio: '1/2' },
  { name: 'SPI 总线', status: 'ok', statusText: '正常', ratio: '2/2' },
  { name: 'ADC 通道', status: 'off', statusText: '离线', ratio: '0/1' },
]
const allChannels: Chan[] = [
  ...channels,
  { name: 'GPIO 通道', status: 'ok', statusText: '正常', ratio: '0/0' },
  { name: 'PWM 通道', status: 'ok', statusText: '正常', ratio: '0/0' },
  { name: 'I2S 通道', status: 'ok', statusText: '正常', ratio: '0/0' },
  { name: 'CAN 总线', status: 'ok', statusText: '正常', ratio: '0/0' },
  { name: '1-Wire 通道', status: 'ok', statusText: '正常', ratio: '0/0' },
  { name: 'SDIO 通道', status: 'ok', statusText: '正常', ratio: '0/0' },
  { name: 'USB 通道', status: 'ok', statusText: '正常', ratio: '0/0' },
  { name: 'RMT 通道', status: 'ok', statusText: '正常', ratio: '0/0' },
]
const chanVisible = ref(false)
const activeChannel = ref<Chan | null>(null)
const chanDesc: Record<string, string> = {
  ok: '通道运行正常,数据收发无异常',
  warn: '存在告警: 数据重传率偏高,建议检查总线负载',
  off: '通道离线: 设备未挂载或总线未使能',
}
function onChannelClick(ch: Chan) {
  activeChannel.value = ch
  chanVisible.value = true
}

// ── 搜索 ──
const searchVisible = ref(false)
const searchQuery = ref('')
const searchInputRef = ref<HTMLInputElement | null>(null)
const searchPool = ['设备名称 (当前节点)', 'I2C 总线通道', 'UART 通道', 'SPI 总线', 'ADC 通道', 'OTA 升级', '同步配置', '测延迟', '节点管理', '边缘设备', '固件管理']
const searchResults = computed(() => {
  const q = searchQuery.value.trim().toLowerCase()
  if (!q) return searchPool
  return searchPool.filter(s => s.toLowerCase().includes(q))
})
watch(searchVisible, async v => {
  if (v) { await nextTick(); searchInputRef.value?.focus() }
})
function onSearchPick(r: string) {
  searchVisible.value = false
  searchQuery.value = ''
  info(`demo: 定位「${r}」`)
}

// ── 通知 ──
const notifVisible = ref(false)
const notifications = reactive([
  { text: 'UART 通道出现数据重传告警', time: '10 分钟前', unread: true },
  { text: '固件 2.5.19 已发布,建议升级', time: '1 小时前', unread: true },
  { text: 'ADC 通道离线超过 30 分钟', time: '2 小时前', unread: true },
  { text: 'I2C_3 总线离线告警已恢复', time: '3 小时前', unread: true },
  { text: '节点「设备名称」连接质量下降至 89%', time: '5 小时前', unread: true },
  { text: 'DMA 通道 2 绑定变更已生效', time: '8 小时前', unread: true },
  { text: '通道数据接收延迟超过阈值', time: '昨天 23:40', unread: true },
  { text: '系统监控发现内存使用率偏高', time: '昨天 23:10', unread: true },
  { text: '配置模板「esp32s3 标准」有新版本', time: '昨天 22:45', unread: true },
  { text: '配置同步计划任务执行成功', time: '昨天 22:00', unread: true },
  { text: 'OTA 升级包校验完成', time: '昨天 21:50', unread: true },
  { text: '节点「设备名称」上线', time: '昨天 21:18', unread: true },
])
const unreadCount = computed(() => notifications.filter(n => n.unread).length)
function markAllRead() {
  notifications.forEach(n => { n.unread = false })
  toast('全部标记为已读')
}

// ── 弹窗表单 ──
const renameVisible = ref(false)
const renameDraft = ref('')
watch(renameVisible, v => { if (v) renameDraft.value = device.name })
function saveRename() {
  const v = renameDraft.value.trim()
  if (!v) { ElMessage({ message: '名称不能为空', type: 'error' }); return }
  device.name = v
  renameVisible.value = false
  pushEvent(`设备重命名为「${v}」`, true)
  toast('设备名称已更新')
}

const remarkVisible = ref(false)
const remarkDraft = ref('')
watch(remarkVisible, v => { if (v) remarkDraft.value = device.remark })
function saveRemark() {
  device.remark = remarkDraft.value.trim()
  remarkVisible.value = false
  pushEvent('备注信息更新', true)
  toast('备注已保存')
}

const locationVisible = ref(false)
const locationDraft = ref('')
const locationCoord = ref('')
watch(locationVisible, v => { if (v) { locationDraft.value = device.location; locationCoord.value = '' } })
function saveLocation() {
  device.location = locationDraft.value.trim()
  locationVisible.value = false
  pushEvent('设备位置更新', true)
  toast(device.location ? `位置已设置: ${device.location}` : '位置已保存')
}

const otaVisible = ref(false)
const otaTarget = ref('2.5.19')
function startOta() {
  otaVisible.value = false
  ElMessageBox.confirm(`确认将设备从 ${device.fw} 升级到 ${otaTarget.value}? 升级后设备将自动重启。`, 'OTA 升级确认', {
    confirmButtonText: '开始升级', cancelButtonText: '取消', type: 'warning',
  }).then(() => {
    pushEvent(`OTA 升级开始 (${device.fw} → ${otaTarget.value})`, true)
    toast('OTA 升级任务已下发')
    setTimeout(() => {
      device.fw = otaTarget.value
      pushEvent(`OTA 升级完成,当前版本 ${device.fw}`, true)
      toast(`升级完成,当前版本 ${device.fw}`)
    }, 2600)
  }).catch(() => {})
}

// ── 其他弹窗 ──
const eventsVisible = ref(false)
const healthVisible = ref(false)

// ── 生命周期 ──
const mountTime = Date.now()
let tickTimer: ReturnType<typeof setInterval> | null = null
let metricsTimer: ReturnType<typeof setInterval> | null = null
onMounted(() => {
  tickTimer = setInterval(() => { nowTick.value = Date.now() }, 60000)
  metricsTimer = setInterval(() => {
    metricsAgo.value += 1
    if (metricsAgo.value >= 3) refreshMetrics()
  }, 1000)
})
onUnmounted(() => {
  if (tickTimer) clearInterval(tickTimer)
  if (metricsTimer) clearInterval(metricsTimer)
})
</script>

<style scoped>
/* ── 整页覆盖: 盖在 MainLayout 之上 ── */
.new-node-demo {
  position: fixed; inset: 0; z-index: 3000;
  display: flex; background: #F5F7FA;
  font-family: -apple-system, BlinkMacSystemFont, 'PingFang SC', 'Microsoft YaHei', sans-serif;
  color: #1F2329; font-size: 13px; line-height: 20px;
}
.card {
  background: #fff; border-radius: 8px;
  box-shadow: 0 1px 2px rgba(16,24,40,.04), 0 1px 3px rgba(16,24,40,.06);
}
.dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; }
.dot-green { background: #22C55E; }
.dot-red { background: #EF4444; }
.dot-gray { background: #C9D2DE; }

/* ── 侧边栏 ── */
.sidebar {
  width: 224px; flex-shrink: 0; background: #fff; border-right: 1px solid #E8EBF0;
  display: flex; flex-direction: column; padding: 16px 0;
}
.sidebar-logo { display: flex; align-items: center; gap: 10px; padding: 0 20px 18px; border-bottom: 1px solid #F0F2F5; }
.logo-text { font-size: 16px; font-weight: 700; color: #1F2329; }
.sidebar-nav { flex: 1; padding: 12px 0; overflow-y: auto; }
.nav-item {
  display: flex; align-items: center; gap: 10px; height: 42px; padding: 0 20px;
  color: #3D4450; font-size: 14px; cursor: pointer; transition: all .15s;
}
.nav-item:hover { color: #2E6BFF; background: #F7FAFF; }
.nav-item.active { color: #2E6BFF; background: #EBF2FF; font-weight: 500; }
.sidebar-bottom { border-top: 1px solid #F0F2F5; padding-top: 8px; }
.sidebar-version { padding: 8px 20px 0; font-size: 12px; color: #8A93A3; }

/* ── 顶栏 ── */
.main { flex: 1; display: flex; flex-direction: column; min-width: 0; }
.topbar {
  height: 64px; background: #fff; border-bottom: 1px solid #E8EBF0;
  display: flex; align-items: center; gap: 16px; padding: 0 24px;
}
.topbar-hamburger { color: #3D4450; cursor: pointer; }
.breadcrumb { display: flex; align-items: center; gap: 8px; font-size: 14px; }
.crumb-link { color: #8A93A3; cursor: pointer; }
.crumb-link:hover { color: #2E6BFF; }
.crumb-sep { color: #C9D2DE; }
.crumb-current { color: #1F2329; font-weight: 600; }
.topbar-search {
  flex: 1; max-width: 420px; margin: 0 auto; height: 36px; border: 1px solid #E8EBF0; border-radius: 6px;
  display: flex; align-items: center; gap: 8px; padding: 0 12px; color: #8A93A3; cursor: pointer; background: #F7F9FC;
}
.topbar-search:hover { border-color: #2E6BFF; }
.search-placeholder { flex: 1; font-size: 13px; }
.search-kbd { font-size: 12px; color: #8A93A3; border: 1px solid #E8EBF0; border-radius: 4px; padding: 1px 6px; background: #fff; }
.topbar-right { display: flex; align-items: center; gap: 18px; }
.status-pill {
  display: flex; align-items: center; gap: 6px; height: 28px; padding: 0 12px;
  background: #E8F9EF; color: #16A34A; border-radius: 14px; font-size: 12px; cursor: pointer;
}
.bell { position: relative; cursor: pointer; color: #3D4450; display: flex; }
.bell:hover { color: #2E6BFF; }
.bell-badge {
  position: absolute; top: -6px; right: -8px; min-width: 16px; height: 16px; border-radius: 8px;
  background: #EF4444; color: #fff; font-size: 10px; line-height: 16px; text-align: center; padding: 0 4px;
}
.user-chip { display: flex; align-items: center; gap: 8px; cursor: pointer; }
.avatar {
  width: 30px; height: 30px; border-radius: 50%; background: #2E6BFF; color: #fff;
  display: flex; align-items: center; justify-content: center; font-size: 13px; font-weight: 600;
}
.user-name { font-size: 13px; color: #1F2329; }

/* ── 内容区 ── */
.content { flex: 1; overflow-y: auto; padding: 24px; }

/* 页头 */
.page-header { display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 16px; gap: 16px; }
.ph-title-row { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.ph-title { font-size: 20px; font-weight: 600; margin: 0; line-height: 28px; }
.ph-edit { color: #8A93A3; cursor: pointer; }
.ph-edit:hover { color: #2E6BFF; }
.badge { font-size: 12px; padding: 2px 10px; border-radius: 11px; display: inline-flex; align-items: center; gap: 5px; }
.badge-green { background: #E8F9EF; color: #16A34A; }
.badge-green .dot { width: 6px; height: 6px; }
.quality { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; color: #3D4450; cursor: pointer; }
.q-val { color: #16A34A; font-weight: 600; }
.q-text { color: #16A34A; font-weight: 600; }
.ph-id { margin-top: 8px; font-size: 12px; color: #8A93A3; display: flex; align-items: center; gap: 6px; }
.copy-icon { cursor: pointer; color: #8A93A3; }
.copy-icon:hover { color: #2E6BFF; }
.ph-actions { display: flex; gap: 12px; flex-wrap: wrap; }

.btn {
  height: 36px; padding: 0 16px; border-radius: 6px; font-size: 13px; cursor: pointer;
  display: inline-flex; align-items: center; gap: 6px; border: 1px solid transparent; transition: all .15s;
}
.btn:disabled { opacity: .6; cursor: not-allowed; }
.btn-plain { background: #fff; border-color: #D9DEE7; color: #3D4450; }
.btn-plain:hover:not(:disabled) { color: #2E6BFF; border-color: #2E6BFF; }
.btn-primary { background: #2E6BFF; color: #fff; }
.btn-primary:hover:not(:disabled) { background: #1F56E0; }
.btn-block { width: 100%; justify-content: center; }
.spin { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }

/* 统计条 */
.stat-strip { display: flex; align-items: center; height: 88px; padding: 16px 24px; margin-bottom: 16px; }
.stat-item { flex: 1; display: flex; align-items: center; gap: 12px; min-width: 0; }
.stat-icon {
  width: 32px; height: 32px; border-radius: 6px; background: #EBF2FF; color: #2E6BFF;
  display: flex; align-items: center; justify-content: center; flex-shrink: 0;
}
.stat-text { min-width: 0; }
.stat-label { font-size: 12px; color: #8A93A3; }
.stat-value { font-size: 14px; font-weight: 500; margin-top: 4px; white-space: nowrap; }
.stat-sep { width: 1px; height: 40px; background: #E8EBF0; flex-shrink: 0; }

/* Tab 栏 */
.tab-bar { display: flex; align-items: center; height: 48px; padding: 0 20px; gap: 24px; margin-bottom: 16px; border-bottom: 1px solid #E8EBF0; border-radius: 8px 8px 0 0; }
.tab-item {
  display: flex; align-items: center; gap: 6px; height: 48px; padding: 0 4px;
  font-size: 14px; color: #3D4450; cursor: pointer; position: relative;
}
.tab-item:hover { color: #2E6BFF; }
.tab-item.active { color: #2E6BFF; font-weight: 500; }
.tab-item.active::after {
  content: ''; position: absolute; left: 0; right: 0; bottom: -1px; height: 2px; background: #2E6BFF;
}

/* 三卡行 */
.card-row { display: flex; gap: 16px; margin-bottom: 16px; }
.row-1 .info-card { flex: 5; }
.row-1 .metrics-card { flex: 3.2; }
.row-1 .map-card { flex: 2.4; }
.row-2 > .card { flex: 1; min-width: 0; }

.card-head {
  display: flex; align-items: center; justify-content: space-between;
  padding: 16px 20px 12px; border-bottom: 1px solid #F0F2F5;
}
.card-title { font-size: 15px; font-weight: 600; }
.card-link { font-size: 13px; color: #2E6BFF; cursor: pointer; }
.card-link:hover { text-decoration: underline; }

/* 设备基本信息: 设计稿为两列并排键值对 */
.info-grid { display: flex; gap: 32px; padding: 12px 20px 16px; }
.info-col { flex: 1; min-width: 0; }
.info-row { display: flex; align-items: center; height: 36px; font-size: 13px; border-bottom: 1px solid #F7F9FC; }
.info-col .info-row:last-child { border-bottom: none; }
.info-label { width: 92px; flex-shrink: 0; color: #8A93A3; font-size: 12px; }
.info-val { color: #1F2329; display: flex; align-items: center; gap: 6px; }
.info-val.mono { font-family: ui-monospace, monospace; }
.info-val.dim { color: #8A93A3; }
.mini-edit { color: #8A93A3; cursor: pointer; }
.mini-edit:hover { color: #2E6BFF; }
.qbar { display: inline-block; width: 64px; height: 6px; border-radius: 3px; background: #EEF1F5; overflow: hidden; flex-shrink: 0; }
.qbar-fill { display: block; height: 100%; background: #22C55E; border-radius: 3px; }
.q-num { color: #16A34A; font-weight: 600; white-space: nowrap; }
.q-good { color: #16A34A; font-size: 12px; white-space: nowrap; }
.latency { color: #16A34A; font-weight: 600; }

/* 实时指标 */
.metrics-updated { font-size: 12px; color: #8A93A3; display: flex; align-items: center; gap: 6px; }
.mini-refresh { cursor: pointer; }
.mini-refresh:hover { color: #2E6BFF; }
.metric-list { padding: 12px 20px 16px; }
.metric-row { display: flex; align-items: center; height: 44px; gap: 8px; }
.metric-icon {
  width: 24px; height: 24px; border-radius: 50%; flex-shrink: 0;
  display: flex; align-items: center; justify-content: center;
}
.metric-name { font-size: 13px; color: #3D4450; flex: 1; white-space: nowrap; }
.metric-val { font-size: 13px; font-weight: 500; white-space: nowrap; }
.metric-unit { font-size: 12px; color: #8A93A3; font-weight: 400; }
.spark { width: 72px; height: 24px; flex-shrink: 0; }

/* 地图卡 */
.map-card { display: flex; flex-direction: column; }
.map-placeholder {
  flex: 1; margin: 16px 20px 20px; border-radius: 6px; background: #EDF0F5;
  position: relative; overflow: hidden;
  display: flex; align-items: center; justify-content: center; cursor: pointer; min-height: 180px;
}
.map-roads { position: absolute; inset: 0; width: 100%; height: 100%; }
.map-pin { position: relative; z-index: 1; filter: drop-shadow(0 2px 4px rgba(46,107,255,.4)); }
.map-btn {
  position: absolute; left: 12px; bottom: 12px; z-index: 2;
  height: 30px; padding: 0 12px; font-size: 12px;
  box-shadow: 0 1px 3px rgba(16,24,40,.12);
}

/* 时间线 */
.timeline { padding: 12px 20px 16px; position: relative; }
.timeline::before { content: ''; position: absolute; left: 27px; top: 20px; bottom: 24px; width: 2px; background: #E8EBF0; }
.tl-row { display: flex; align-items: center; height: 40px; gap: 12px; position: relative; }
.tl-icon {
  width: 16px; height: 16px; border-radius: 50%; background: #fff; border: 1.5px solid;
  display: flex; align-items: center; justify-content: center; flex-shrink: 0; z-index: 1;
}
.tl-ok { border-color: #22C55E; color: #22C55E; }
.tl-bad { border-color: #EF4444; color: #EF4444; }
.tl-text { flex: 1; font-size: 13px; color: #3D4450; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.tl-time { font-size: 12px; color: #8A93A3; font-variant-numeric: tabular-nums; white-space: nowrap; }

/* 通道健康 */
.chips { display: flex; gap: 8px; padding: 14px 20px 10px; }
.chip { flex: 1; border-radius: 6px; padding: 8px 12px; cursor: pointer; display: flex; flex-direction: column; gap: 2px; }
.chip-total { background: #F2F4F7; }
.chip-ok { background: #E8F9EF; }
.chip-warn { background: #FEF3DE; }
.chip-off { background: #F2F4F7; }
.chip-label { font-size: 12px; color: #3D4450; display: flex; align-items: center; gap: 6px; }
.chip-dot { width: 6px; height: 6px; border-radius: 50%; display: inline-block; }
.chip-num { font-size: 18px; font-weight: 600; line-height: 26px; }
.dim-num { color: #9CA3AF; }
.chan-list { padding: 0 20px 12px; }
.chan-row {
  display: flex; align-items: center; height: 44px; gap: 10px; cursor: pointer;
  border-bottom: 1px solid #F0F2F5; padding: 0 4px; transition: background .15s;
}
.chan-row:last-child { border-bottom: none; }
.chan-row:hover { background: #F8FAFC; }
.chan-icon {
  width: 28px; height: 28px; border-radius: 6px; background: #EBF2FF; color: #2E6BFF;
  display: flex; align-items: center; justify-content: center; flex-shrink: 0;
}
.chan-name { flex: 1; font-size: 13px; font-weight: 500; white-space: nowrap; }
.chan-badge { font-size: 12px; height: 22px; line-height: 22px; padding: 0 10px; border-radius: 11px; }
.cb-ok { color: #16A34A; background: #E8F9EF; }
.cb-warn { color: #D97706; background: #FEF3DE; }
.cb-off { color: #6B7280; background: #F2F4F7; }
.chan-ratio { font-size: 13px; color: #8A93A3; }
.chan-arrow { color: #C9D2DE; }

/* 备注卡 */
.remark-card .card-head .mini-edit { font-size: 15px; }
.remark-empty { display: flex; flex-direction: column; align-items: center; padding: 28px 20px 32px; }
.re-title { font-size: 13px; color: #8A93A3; margin-top: 12px; }
.re-sub { font-size: 12px; color: #B0B7C3; margin-top: 4px; }
.remark-body { padding: 16px 20px 20px; font-size: 13px; color: #3D4450; cursor: pointer; white-space: pre-wrap; }

/* tab 占位 */
.tab-placeholder { display: flex; flex-direction: column; align-items: center; padding: 60px 20px; gap: 10px; }
.tp-title { font-size: 15px; font-weight: 600; }
.tp-sub { font-size: 12px; color: #8A93A3; margin-bottom: 8px; }

/* ── 弹窗内部 ── */
.form-row { margin-bottom: 16px; }
.form-label { display: block; font-size: 12px; color: #8A93A3; margin-bottom: 6px; }
.ota-current { display: flex; align-items: center; gap: 10px; margin-bottom: 16px; }
.ota-tip { font-size: 12px; color: #D97706; background: #FEF3DE; border-radius: 6px; padding: 8px 12px; }
.search-box { display: flex; align-items: center; gap: 10px; border-bottom: 1px solid #F0F2F5; padding-bottom: 12px; }
.search-input { flex: 1; border: none; outline: none; font-size: 14px; color: #1F2329; }
.search-results { max-height: 300px; overflow-y: auto; padding-top: 8px; }
.search-row { display: flex; align-items: center; gap: 8px; height: 36px; padding: 0 8px; border-radius: 6px; cursor: pointer; font-size: 13px; }
.search-row:hover { background: #F7FAFF; color: #2E6BFF; }
.search-empty { text-align: center; color: #8A93A3; font-size: 13px; padding: 24px 0; }
.notif-head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; }
.notif-unread { font-size: 12px; color: #8A93A3; }
.notif-row { display: flex; gap: 10px; padding: 10px 6px; border-radius: 6px; cursor: pointer; align-items: flex-start; }
.notif-row:hover { background: #F8FAFC; }
.notif-row .dot { margin-top: 6px; flex-shrink: 0; }
.notif-text { font-size: 13px; color: #3D4450; }
.notif-row.unread .notif-text { color: #1F2329; font-weight: 500; }
.notif-time { font-size: 12px; color: #8A93A3; margin-top: 2px; }
.all-events, .all-chans { max-height: 400px; overflow-y: auto; }
.ae-row { display: flex; align-items: center; gap: 12px; height: 38px; border-bottom: 1px solid #F0F2F5; }
.ae-text { flex: 1; font-size: 13px; color: #3D4450; }
.ae-time { font-size: 12px; color: #8A93A3; font-variant-numeric: tabular-nums; }

/* ── 响应式 ── */
@media (max-width: 1200px) {
  .row-1 { flex-direction: column; }
  .row-1 > .card { width: 100%; }
  .stat-strip { flex-wrap: wrap; height: auto; gap: 12px; }
  .stat-item { flex: 1 1 30%; }
  .stat-sep { display: none; }
}
@media (max-width: 900px) {
  .row-2 { flex-direction: column; }
  .row-2 > .card { width: 100%; }
  .info-grid { flex-direction: column; gap: 0; }
}
@media (max-width: 768px) {
  .sidebar { display: none; }
  .topbar-search { display: none; }
  .page-header { flex-direction: column; }
  .ph-actions { width: 100%; }
  .ph-actions .btn { flex: 1; justify-content: center; }
  .content { padding: 12px; }
  .stat-item { flex: 1 1 45%; }
}
</style>
