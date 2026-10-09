<template>
  <!-- 节点总览（生产页）：骨架复刻 designs/new-node-1.png，数据全部来自真实 API -->
  <!-- 与 demo(/dev/new-node-demo) 的差异：MainLayout 提供导航 chrome；无 mock 数据； -->
  <!-- 位置/备注/时区/搜索/通知卡已裁剪（后端模型无对应字段）；指标卡仅保留真实指标。 -->
  <div class="node-overview-page">
    <!-- 面包屑（层级线索，规范 §4.1.2 MUST「至少表达列表 -> 当前实体」）
         桌面：首页 / 节点管理 / <当前实体>
         移动端（<=768px）：隐去根级「首页」，压缩为「节点管理 / <当前实体>」两段。
         改前移动端整条 display:none，且 MainLayout 顶栏面包屑同时 display:none、
         错误分支里的「返回列表」又不在渲染路径上 ⇒ 390px 下只剩页面标题，
         用户无法判断自己处在哪一层。此处不新增按钮，因为「节点管理」这一段
         本身带 :to 链接：同一元素既给出位置又给出返回入口，不额外占用紧张的页头空间。 -->
    <el-breadcrumb separator="/" class="no-breadcrumb" data-testid="node-breadcrumb">
      <el-breadcrumb-item :to="{ path: '/dashboard' }">首页</el-breadcrumb-item>
      <el-breadcrumb-item :to="{ path: '/node' }">节点管理</el-breadcrumb-item>
      <el-breadcrumb-item data-testid="node-breadcrumb-current">{{ pageTitle }}</el-breadcrumb-item>
    </el-breadcrumb>

    <!-- 加载骨架 -->
    <div v-if="loading && !node" class="no-loading">
      <el-skeleton :rows="6" animated />
    </div>

    <template v-else-if="node">
      <!-- 页头 -->
      <div class="page-header">
        <div class="ph-left">
          <div class="ph-title-row">
            <h1 class="ph-title">{{ pageTitle }}</h1>
            <button
              type="button"
              class="ph-edit"
              :aria-label="`重命名节点 ${node.node_id}`"
              @click="renameVisible = true"
            ><el-icon :size="16"><EditPen /></el-icon></button>
            <span class="badge" :class="nodeOnline ? 'badge-green' : 'badge-gray'">
              <span class="dot" :class="nodeOnline ? 'dot-green' : 'dot-gray'"></span>{{ nodeOnline ? '在线' : '离线' }}
            </span>
            <span v-if="nodeOnline" class="quality" data-testid="quality-pill">
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" aria-hidden="true">
                <rect x="3" y="14" width="3.4" height="7" rx="1" :fill="qualityColor" />
                <rect x="8.2" y="10" width="3.4" height="11" rx="1" :fill="qualityColor" />
                <rect x="13.4" y="6" width="3.4" height="15" rx="1" :fill="qualityColor" />
                <rect x="18.6" y="2" width="3.4" height="19" rx="1" :fill="qualityColor" opacity="0.35" />
              </svg>
              连接质量 <b class="q-val" :style="{ color: qualityColor }">{{ node.connection_quality ?? 0 }}%</b>
              <b class="q-text" :style="{ color: qualityColor }">{{ qualityText }}</b>
            </span>
          </div>
          <div class="ph-id">
            设备ID: {{ node.node_id }}
            <button
              type="button"
              class="copy-icon"
              :aria-label="`复制设备 ID ${node.node_id}`"
              @click="copyId"
            ><el-icon :size="13"><CopyDocument /></el-icon></button>
          </div>
        </div>
        <div class="ph-actions">
          <el-tooltip content="设备离线，无法操作" placement="top" :disabled="!nodeOffline">
            <span>
              <button class="btn btn-plain" :disabled="nodeOffline || syncing" @click="handleSyncConfig">
                <el-icon :size="14" :class="{ spin: syncing }"><Refresh /></el-icon>{{ syncing ? '同步中...' : '同步配置' }}
              </button>
            </span>
          </el-tooltip>
          <el-tooltip content="设备离线，无法操作" placement="top" :disabled="!nodeOffline">
            <span>
              <button class="btn btn-primary" :disabled="nodeOffline" @click="showOTADialog = true">
                <el-icon :size="14"><UploadFilled /></el-icon>OTA 升级
              </button>
            </span>
          </el-tooltip>
          <el-tooltip content="设备离线，无法操作" placement="top" :disabled="!nodeOffline">
            <span>
              <button class="btn btn-plain" :disabled="nodeOffline || pinging" @click="handlePing">
                <el-icon :size="14"><Odometer /></el-icon>{{ pinging ? '测试中...' : '测延迟' }}
              </button>
            </span>
          </el-tooltip>
          <!-- 节点级设备操作（重启 / 恢复出厂）。
               仅在服务端确实具备设备传输时出现：否则点了也只会得到 501，
               把「后端没这个能力」伪装成「操作失败」对操作员没有帮助。 -->
          <template v-if="deviceOpReady">
            <el-tooltip content="设备离线，无法操作" placement="top" :disabled="!nodeOffline">
              <span>
                <button class="btn btn-plain" :disabled="nodeOffline || deviceOpBusy" @click="handleDeviceOp('reboot')">
                  <el-icon :size="14" :class="{ spin: deviceOpBusy && deviceOpInFlight === 'reboot' }"><SwitchButton /></el-icon>{{ deviceOpInFlight === 'reboot' ? '重启中...' : '重启' }}
                </button>
              </span>
            </el-tooltip>
            <el-tooltip content="设备离线，无法操作" placement="top" :disabled="!nodeOffline">
              <span>
                <button class="btn btn-plain" :disabled="nodeOffline || deviceOpBusy" @click="handleDeviceOp('factory_reset')">
                  <el-icon :size="14" :class="{ spin: deviceOpBusy && deviceOpInFlight === 'factory_reset' }"><Delete /></el-icon>{{ deviceOpInFlight === 'factory_reset' ? '恢复中...' : '恢复出厂' }}
                </button>
              </span>
            </el-tooltip>
          </template>
          <button class="btn btn-plain" :disabled="refreshing" @click="refreshAll">
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
            <div class="stat-value">{{ node.model || UNKNOWN }}</div>
          </div>
        </div>
        <div class="stat-item">
          <div class="stat-icon"><el-icon :size="16"><Document /></el-icon></div>
          <div class="stat-text">
            <div class="stat-label">固件版本</div>
            <div class="stat-value">{{ node.firmware_version || UNKNOWN }}</div>
          </div>
        </div>
        <div class="stat-item">
          <div class="stat-icon"><el-icon :size="16"><Clock /></el-icon></div>
          <div class="stat-text">
            <div class="stat-label">最后上线</div>
            <div class="stat-value">{{ lastOnlineText }}</div>
          </div>
        </div>
        <div class="stat-item">
          <div class="stat-icon"><el-icon :size="16"><Timer /></el-icon></div>
          <div class="stat-text">
            <div class="stat-label">在线时长</div>
            <div class="stat-value">{{ sessionDuration }}</div>
          </div>
        </div>
        <div class="stat-item">
          <div class="stat-icon"><el-icon :size="16"><Share /></el-icon></div>
          <div class="stat-text">
            <div class="stat-label">协议版本</div>
            <div class="stat-value">{{ node.protocol_version || UNKNOWN }}</div>
          </div>
        </div>
      </div>

      <!-- Tab 栏 -->
      <div class="tab-bar card" ref="tabBarRef">
        <div
          v-for="tab in tabs"
          :key="tab.label"
          class="tab-item"
          :class="{ active: activeTab === tab.label }"
          :data-tab="tab.label"
          role="tab"
          tabindex="0"
          :aria-selected="activeTab === tab.label"
          @click="activateTab(tab.label)"
          @keydown.enter.prevent="activateTab(tab.label)"
          @keydown.space.prevent="activateTab(tab.label)"
        >
          <el-icon :size="15"><component :is="tab.icon" /></el-icon>
          <span>{{ tab.label }}</span>
        </div>
      </div>

      <!-- 基本信息 tab 内容 -->
      <template v-if="activeTab === '基本信息'">
      <!-- 第一行：设备基本信息 + 实时指标 + 最近事件 -->
      <div class="card-row row-1">
        <!-- 设备基本信息 -->
        <div class="card info-card">
          <div class="card-head">
            <span class="card-title">设备基本信息</span>
          </div>
          <div class="info-grid">
            <div class="info-col">
              <div class="info-row"><span class="info-label">节点名称</span><span class="info-val">{{ node.name || UNKNOWN }} <button type="button" class="mini-edit" :aria-label="`重命名节点 ${node.node_id}`" @click="renameVisible = true"><el-icon :size="12"><EditPen /></el-icon></button></span></div>
              <div class="info-row"><span class="info-label">设备 ID</span><span class="info-val mono">{{ node.node_id }} <button type="button" class="mini-edit" :aria-label="`复制设备 ID ${node.node_id}`" @click="copyId"><el-icon :size="12"><CopyDocument /></el-icon></button></span></div>
              <div class="info-row"><span class="info-label">型号</span><span class="info-val">{{ node.model || UNKNOWN }}</span></div>
              <div class="info-row"><span class="info-label">固件版本</span><span class="info-val">{{ node.firmware_version || UNKNOWN }}</span></div>
              <div class="info-row">
                <span class="info-label">状态</span>
                <span class="info-val"><span class="dot" :class="nodeOnline ? 'dot-green' : 'dot-gray'"></span> {{ nodeOnline ? '在线' : '离线' }}</span>
              </div>
              <div class="info-row">
                <span class="info-label">连接质量</span>
                <span class="info-val" v-if="nodeOnline">
                  <span class="qbar"><span class="qbar-fill" :style="{ width: (node.connection_quality ?? 0) + '%', background: qualityColor }"></span></span>
                  <span class="q-num" :style="{ color: qualityColor }">{{ node.connection_quality ?? 0 }}%</span>
                  <span class="q-good" :style="{ color: qualityColor }">{{ qualityText }}</span>
                </span>
                <span class="info-val dim" v-else>—</span>
              </div>
            </div>
            <div class="info-col">
              <div class="info-row"><span class="info-label">延迟</span><span class="info-val latency" v-if="latencyMs > 0">{{ latencyMs }} ms</span><span class="info-val dim" v-else>—</span></div>
              <div class="info-row"><span class="info-label">上线时间</span><span class="info-val">{{ lastOnlineText }}</span></div>
              <div class="info-row"><span class="info-label">在线时长</span><span class="info-val">{{ sessionDuration }}</span></div>
              <div class="info-row"><span class="info-label">连接方式</span><span class="info-val">{{ connectionTypeText }}</span></div>
              <div class="info-row"><span class="info-label">配置同步</span><span class="info-val">{{ syncStateLabel }}</span></div>
            </div>

            <!-- ⚠ 2026-10-09（用户要求："DMA 分不到降级为提示"）：
                 配置**告警**（不是错误）—— 配置成功应用了，只是某些通道从
                 DMA 降级为中断/轮询。刻意与"配置同步"分开显示、用 warning
                 色而非 danger 色，避免用户以为配置失败。
                 message 由后端写好中文，前端原样显示（不在这里拼文案，
                 否则两端各写一份会漂移）。 -->
            <div v-if="configWarnings.length" class="config-warnings">
              <div v-for="(w, i) in configWarnings" :key="`${w.code}-${w.channel_id ?? i}`" class="config-warning-row">
                <el-icon :size="13" class="config-warning-icon"><WarningFilled /></el-icon>
                <span class="config-warning-text">{{ w.message }}</span>
              </div>
            </div>
          </div>
        </div>

        <!-- 实时指标（仅真实数据：WiFi 信号 / 空闲堆内存 / 延迟；离线时为最后上报缓存值并弱化） -->
        <div class="card metrics-card" :class="{ 'metrics-offline': nodeOffline }">
          <div class="card-head">
            <span class="card-title">实时指标<span v-if="nodeOffline" class="metrics-offline-tag">离线·最后上报</span></span>
            <span class="metrics-updated">
              {{ metricsUpdatedText }}
              <el-icon :size="13" class="mini-refresh" @click="refreshAll"><RefreshRight /></el-icon>
            </span>
          </div>
          <div class="metric-list">
            <div class="metric-row">
              <span class="metric-icon" style="background: var(--no-success-bg); color: var(--no-success-text)">
                <el-icon :size="13"><Connection /></el-icon>
              </span>
              <span class="metric-name">WiFi 信号强度</span>
              <span class="metric-val" v-if="node.wifi_rssi !== 0">{{ node.wifi_rssi }}<span class="metric-unit"> dBm</span></span>
              <span class="metric-val dim" v-else>—</span>
            </div>
            <div class="metric-row">
              <span class="metric-icon" style="background: var(--no-accent-bg); color: var(--no-accent)">
                <el-icon :size="13"><Odometer /></el-icon>
              </span>
              <span class="metric-name">空闲堆内存</span>
              <span class="metric-val" v-if="(node.free_heap_bytes ?? 0) > 0">{{ freeHeapText }}<span class="metric-unit"> KB</span></span>
              <span class="metric-val dim" v-else>—</span>
            </div>
            <div class="metric-row">
              <span class="metric-icon" style="background: rgba(46,107,255,.1); color: var(--no-primary)">
                <el-icon :size="13"><Timer /></el-icon>
              </span>
              <span class="metric-name">通信延迟</span>
              <span class="metric-val" v-if="latencyMs > 0">{{ latencyMs }}<span class="metric-unit"> ms</span></span>
              <span class="metric-val dim" v-else>—</span>
            </div>
            <div class="metric-row">
              <span class="metric-icon" style="background: var(--no-warning-bg); color: var(--no-warning-text)">
                <el-icon :size="13"><Clock /></el-icon>
              </span>
              <span class="metric-name">固件在线时长</span>
              <span class="metric-val">{{ uptimeText }}</span>
            </div>
          </div>
        </div>

        <!-- 最近事件 -->
        <div class="card events-card">
          <div class="card-head">
            <span class="card-title">最近事件</span>
            <button type="button" class="card-link" @click="eventsVisible = true">查看全部</button>
          </div>
          <div v-if="eventsLoading" class="card-loading"><el-skeleton :rows="3" animated /></div>
          <div v-else-if="nodeEvents.length === 0" class="card-empty">暂无事件</div>
          <div v-else class="timeline">
            <div v-for="ev in recentEvents" :key="ev.id" class="tl-row">
              <span class="tl-icon" :class="ev.new_status === 'online' ? 'tl-ok' : 'tl-bad'">
                <el-icon :size="10"><component :is="ev.new_status === 'online' ? Select : SwitchButton" /></el-icon>
              </span>
              <span class="tl-text">{{ eventText(ev) }}</span>
              <span class="tl-time">{{ formatTime(ev.created_at) }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- 第二行：通道健康状态 -->
      <div class="card-row row-2">
        <div class="card health-card">
          <div class="card-head">
            <span class="card-title">通道健康状态</span>
            <!-- D1 修复：原为 <span @click="goToDetail">，而 goToDetail 推的是**当前页**
                 (router.push(`/node/${nodeSerial}`)) ⇒ 点"查看全部"和点通道行都等于原地打转，
                 用户看不到任何变化。目标改为产品既有的、可直达的「通道管理」页并带上节点过滤。 -->
            <button type="button" class="card-link" data-node-channels-entry @click="navigateToNodeChannels">查看全部</button>
          </div>
          <div v-if="channelsLoading" class="card-loading"><el-skeleton :rows="3" animated /></div>
          <template v-else>
            <div class="chips" data-channel-totals>
              <div class="chip chip-total"><span class="chip-label"><i class="chip-dot" style="background: var(--no-text-muted)"></i>总数</span><span class="chip-num">{{ channelStats.total }}</span></div>
              <div class="chip" :class="channelStats.ok > 0 ? 'chip-ok' : 'chip-off'"><span class="chip-label"><i class="chip-dot" style="background: var(--no-success)"></i>正常</span><span class="chip-num">{{ channelStats.ok }}</span></div>
              <div class="chip" :class="channelStats.error > 0 ? 'chip-warn' : 'chip-off'"><span class="chip-label"><i class="chip-dot" style="background: var(--no-warning)"></i>异常</span><span class="chip-num" :class="{ 'dim-num': channelStats.error === 0 }">{{ channelStats.error }}</span></div>
              <div class="chip chip-off"><span class="chip-label"><i class="chip-dot" style="background: var(--no-text-faint)"></i>其他</span><span class="chip-num dim-num">{{ channelStats.other }}</span></div>
            </div>
            <div v-if="channels.length === 0" class="card-empty">该节点暂无通道</div>
            <div v-else class="chan-list">
              <!-- D1：通道行点击进入「该节点的通道列表」（/channel?node=<序列号>，
                   那里每行有「编辑通道」入口）；行内「编辑」按钮直接打开本行通道的
                   配置对话框（ChannelManager 的 initial-data ⇒ 编辑态）。
                   改前这里是 @click="goToDetail"，而 goToDetail 推的是**当前页**，
                   点击等于原地打转；同时它是不可键盘聚焦的裸 div。 -->
              <div
                v-for="ch in channels.slice(0, 6)"
                :key="ch.id"
                class="chan-row"
                role="button"
                tabindex="0"
                :aria-label="`查看通道 ${channelName(ch)}（前往通道管理）`"
                @click="navigateToNodeChannels"
                @keydown.enter.prevent="navigateToNodeChannels"
                @keydown.space.prevent="navigateToNodeChannels"
              >
                <span class="chan-icon"><el-icon :size="14"><Link /></el-icon></span>
                <span class="chan-name">{{ channelName(ch) }}</span>
                <span class="chan-badge" :class="channelBadgeClass(ch)">{{ channelStatusText(ch) }}</span>
                <button
                  type="button"
                  class="link-btn chan-edit"
                  :disabled="nodeOffline"
                  :aria-label="'编辑通道 ' + channelName(ch)"
                  @click.stop="editChannel(ch)"
                ><el-icon :size="12"><EditPen /></el-icon>编辑</button>
                <!-- C5 能力补线：删除通道入口此前**只存在于死文件** ChannelPanel.vue
                     （其 el-tag 的 @close 承担删除），而存活页面只能新建/编辑 ——
                     用户永远删不掉通道。后端 `DELETE /api/v1/channels/:id` 早已支持
                     （handler_device.go:894），前端 API 也有（channelApi.delete）。
                     这里把能力接到存活路径，使 ChannelPanel 可被安全删除。 -->
                <button
                  v-if="!nodeOffline"
                  type="button"
                  class="link-btn chan-del"
                  data-testid="delete-channel"
                  :aria-label="'删除通道 ' + channelName(ch)"
                  @click.stop="deleteChannel(ch)"
                ><el-icon :size="12"><Delete /></el-icon>删除</button>
                <el-icon :size="13" class="chan-arrow"><ArrowRight /></el-icon>
              </div>
              <!-- D2：列表是预览（前 N 条）而"总数"是全量 —— 截断必须可发现：
                   范围行常驻，超出时再显式写出差额。否则用户会把「看到 6 行」读成「只有 6 个通道」。 -->
              <div class="chan-more" data-chan-range>
                共 {{ channelStats.total }} 条，此处显示前 {{ Math.min(channels.length, CHANNEL_HEALTH_PREVIEW_LIMIT) }} 条<template v-if="channels.length > CHANNEL_HEALTH_PREVIEW_LIMIT">（还有 {{ channels.length - CHANNEL_HEALTH_PREVIEW_LIMIT }} 条未显示，点上方「查看全部」）</template>
              </div>
            </div>
          </template>
        </div>
      </div>
      </template>

      <!-- 总线配置：布局对齐 designs/new-node-2.png；资源全部来自节点上报能力 -->
      <template v-else-if="activeTab === '总线配置'">
        <div class="bus-alert" :class="{ 'bus-alert-offline': nodeOffline }">
          <el-icon :size="16"><WarningFilled /></el-icon>
          <span><b>仅在线可编辑：</b>{{ nodeOnline ? '当前设备在线，您可以查看和修改配置' : '当前设备离线，只能查看已上报的资源配置' }}</span>
          <button class="link-btn bus-alert-refresh" type="button" :disabled="nodeOffline || resourceQuerying" @click="requestBusResourceRefresh"><el-icon :size="12"><RefreshRight /></el-icon>{{ resourceQuerying ? '查询中…' : '查询资源' }}</button>
        </div>

        <div class="bus-main-cols bus-workbench">
          <div class="bus-col-left">
            <section class="card bus-resource-card">
              <div class="bus-subtabs" role="tablist" aria-label="总线类型">
                <button
                  v-for="bus in busTabs"
                  :key="bus.type"
                  class="bus-subtab"
                  :class="{ active: activeBusType === bus.type }"
                  type="button"
                  @click="selectBusType(bus.type)"
                >
                  <el-icon :size="14"><component :is="bus.icon" /></el-icon>
                  {{ bus.label }}
                </button>
              </div>
              <p class="bus-desc">{{ activeBusDescription }}</p>

              <div v-if="busLoading" class="bus-loading"><el-skeleton :rows="5" animated /></div>
              <template v-else-if="busLoadError">
                <el-empty description="总线资源加载失败">
                  <button class="btn btn-plain" @click="fetchBusData">重试</button>
                </el-empty>
              </template>
              <template v-else>
                <div class="bus-stat-row">
                  <div v-for="stat in busStats" :key="stat.label" class="bus-stat-item">
                    <span class="bus-stat-icon" :class="stat.className"><el-icon :size="16"><component :is="stat.icon" /></el-icon></span>
                    <span>
                      <span class="bus-stat-label">{{ stat.label }}</span>
                      <b class="bus-stat-value">{{ stat.value }}</b>
                    </span>
                  </div>
                </div>

                <div v-if="activeBusResources.length === 0" class="bus-empty">
                  <el-empty description="该节点未上报此类型总线资源" />
                </div>
                <div v-else class="bus-table-wrap">
                  <table class="bus-table">
                    <thead>
                      <tr>
                        <th class="bus-select-col" aria-label="选择"></th>
                        <th>资源名称</th>
                        <th>引脚</th>
                        <th>关键参数</th>
                        <th>状态</th>
                        <th>已挂载通道</th>
                        <th>DMA 绑定</th>
                        <th>操作</th>
                      </tr>
                    </thead>
                    <tbody>
                      <tr
                        v-for="resource in pagedBusResources"
                        :key="resource.id"
                        :data-resource-id="resource.id"
                        :class="{ selected: selectedResourceId === resource.id, disabled: resource.enabled === false }"
                        @click="selectedResourceId = resource.id"
                      >
                        <td><span class="bus-radio" :class="{ checked: selectedResourceId === resource.id }"></span></td>
                        <td><button class="bus-resource-name" type="button" @click.stop="selectedResourceId = resource.id">{{ resource.id }}</button></td>
                        <td class="bus-pins">{{ resourcePins(resource) }}</td>
                        <td><span v-for="parameter in resourceParameters(resource)" :key="parameter" class="bus-tag bus-tag-blue">{{ parameter }}</span></td>
                        <td><span class="bus-tag" :class="resource.enabled === false ? 'bus-tag-gray' : 'bus-tag-green'">{{ resource.enabled === false ? '禁用' : '可用' }}</span></td>
                        <td>{{ resourceMountedChannels(resource).length }}</td>
                        <td>
                          <span v-if="resource.enabled === false || !busSupportsDma" class="dma-na">—</span>
                          <!-- B1（2026-09-20）：改前这里只有一个 el-switch，用户**无法选择**用哪条 DMA。
                               而 DMA 候选常常是多个（本节点 SPI mask=4 → CH0/CH1/CH2 三条），
                               开关只能作用于"隐式选中的第一条"。
                               现在：候选 >1 时给选择器，=1 时保留开关（形态不变、无多余点击）。
                               候选为 0 时**必须说明原因**，而不是留空（B3）。 -->
                          <template v-else-if="resourceDmaCandidates(resource).length > 1">
                            <el-select
                              class="dma-select"
                              size="small"
                              :data-dma-select="resource.id"
                              :aria-label="`${resource.id} 绑定的 DMA 资源`"
                              :model-value="dmaSelectionFor(resource)"
                              :disabled="nodeOffline || !canToggleResourceDma(resource)"
                              placeholder="选择 DMA 资源"
                              :title="dmaBoundValueLabel(resourceDmaBinding(resource))"
                              @click.stop
                              @change="(value: any) => changeResourceDma(resource, String(value))"
                            >
                              <el-option label="不使用 DMA" value="" />
                              <!--
                                选项文案分两档（2026-10-02）：
                                · 已绑定本资源的那条 → 紧凑文案（名字 + #id）。它同时是选择器的
                                  **显示值**，而单元格只有 ~138px，全量文案必被截断成
                                  "GDMA_CH0（#…"，等于没显示。
                                · 其余候选（只在下拉里出现）→ 全量文案，带兼容总线便于比较。
                              -->
                              <el-option
                                v-for="dma in resourceDmaCandidates(resource)"
                                :key="dma.dma_id"
                                :label="isDmaBoundToResource(dma, resource) ? dmaBoundValueLabel(dma) : dmaOptionLabel(dma)"
                                :value="String(dma.dma_id)"
                                :disabled="!isDmaRebindable(dma.state, dma.bound_to) && !isDmaBoundToResource(dma, resource)"
                              />
                            </el-select>
                          </template>
                          <el-switch
                            v-else-if="resourceDmaCandidates(resource).length === 1"
                            :aria-label="`${resource.id} 的 DMA 绑定开关（${resourceDmaCandidates(resource)[0].name}）`"
                            :model-value="resourceDmaBinding(resource)?.bound_to ? true : false"
                            size="small"
                            :disabled="nodeOffline || !canToggleResourceDma(resource)"
                            :loading="resourceDmaBinding(resource) ? Boolean(dmaStore.toggling[resourceDmaBinding(resource)!.dma_id]) : false"
                            @click.stop
                            @change="toggleResourceDma(resource, $event)"
                          />
                          <span v-else class="dma-none" :data-dma-none="resource.id">{{ resourceDmaUnavailableText() }}</span>
                        </td>
                        <td>
                          <div class="bus-row-actions">
                            <!-- D4：显式调用 selectResource(row)（而不是内联赋值），
                                 由它统一重置该资源的分页/扫描态，并保证详情卡读的就是本行。
                                 按钮上带 data-view-resource 便于 E2E 逐行点击取证。 -->
                            <button class="link-btn" type="button" :data-view-resource="resource.id" @click.stop="selectedResourceId = resource.id"><el-icon :size="12"><View /></el-icon>查看</button>
                            <button class="link-btn" type="button" :disabled="nodeOffline || resource.enabled === false || !busSupportsChannels" @click.stop="openChannelManager(resource)"><el-icon :size="12"><Plus /></el-icon>新建通道</button>
                          </div>
                        </td>
                      </tr>
                    </tbody>
                  </table>
                </div>

                <div v-if="activeBusResources.length > 0" class="bus-pagination">
                  <span>每页 {{ busPageSize }} 条</span>
                  <span>共 {{ activeBusResources.length }} 条</span>
                  <span class="bus-page-controls">
                    <button class="bus-page-btn" type="button" :disabled="busPage <= 1" @click="busPage--">上一页</button>
                    <span class="bus-page-current">{{ busPage }}</span>
                    <button class="bus-page-btn" type="button" :disabled="busPage >= busTotalPages" @click="busPage++">下一页</button>
                    <span>{{ busPage }} / {{ busTotalPages }} 页</span>
                  </span>
                </div>
              </template>
            </section>

            <!--
              总线工具：**只显示当前 TAB 自己的工具**。

              改前（2026-10-02 生产实测）：I2C TAB 上同时渲染了「I2C 总线工具」和
              「快速操作 UART 专属」两块 —— 后者与 I2C 无关，用户会以为在 I2C 页能改波特率。
              现在按 activeBusType 收窄：每块工具只在所属总线 TAB 出现。
              「资源刷新」对所有总线通用，因此与「地址扫描」并列放在当前 TAB 的工具组里，
              而不是固定挂在 I2C 组下。
            -->
            <div class="bus-tool-layout" :class="{ 'single-group': !isUartTab }">
              <section class="card bus-tool-group bus-current-tools">
                <div class="bus-tool-group-head">
                  <span class="bus-tool-group-title">{{ activeBusTab.label }} 总线工具</span>
                  <span class="bus-tool-group-hint">仅展示后端已支持的操作</span>
                </div>
                <div class="bus-tool-cards">
                  <section v-if="activeBusType === 'i2c'" class="bus-tool-card">
                    <div class="bus-tool-head"><span class="bus-tool-icon bus-tool-blue"><el-icon :size="15"><Search /></el-icon></span><b>地址扫描</b></div>
                    <p>扫描所选 I2C 资源上的从设备地址</p>
                    <div class="bus-tool-foot">
                      <button class="btn btn-primary btn-sm" :disabled="nodeOffline || !selectedResource" @click="scanSelectedI2C">{{ i2cScanning ? '扫描中…' : '开始扫描' }}</button>
                      <span v-if="scanResult !== null" :class="scanResult.length ? 'scan-found' : 'scan-empty'">{{ scanResult.length ? `发现 ${scanResult.length} 个设备` : '未发现设备' }}</span>
                    </div>
                  </section>
                  <section class="bus-tool-card">
                    <div class="bus-tool-head"><span class="bus-tool-icon bus-tool-green"><el-icon :size="15"><RefreshRight /></el-icon></span><b>资源刷新</b></div>
                    <p>请求节点重新上报当前总线资源状态</p>
                    <div class="bus-tool-foot"><button class="btn btn-plain btn-sm" :disabled="nodeOffline || resourceQuerying" @click="requestBusResourceRefresh">{{ resourceQuerying ? '查询中…' : '查询资源' }}</button></div>
                  </section>
                  <section v-if="isUartTab" class="bus-tool-card">
                    <div class="bus-tool-head"><span class="bus-tool-icon bus-tool-orange"><el-icon :size="15"><Tools /></el-icon></span><b>修改波特率</b></div>
                    <p>选中下方「已创建通道」里的 UART 通道即可单独改；未选中时按总线资源批量改该资源上的全部 UART 通道。</p>
                    <div class="bus-tool-foot">
                      <span class="bus-tool-hint" :class="{ 'baud-picked': baudToolSelection }" data-baud-pick-hint>{{ baudToolSelectionText }}</span>
                      <button class="btn btn-plain btn-sm" data-open-baud-tool :disabled="!baudToolAvailable" @click="openBaudToolInline">修改波特率</button>
                    </div>
                  </section>
                </div>
              </section>
            </div>
          </div>

          <aside class="bus-col-right">
            <section class="card bus-detail-card">
              <div class="bus-detail-head"><b>资源详情</b><span class="bus-tag bus-tag-gray">设备上报</span><button class="link-btn" type="button" :disabled="nodeOffline || activeBusType !== 'i2c' || !selectedResource" @click="scanSelectedI2C"><el-icon :size="12"><Search /></el-icon>地址扫描</button></div>
              <!--
                可编辑性与"事实来源"对齐（2026-10-02）：
                · 资源名称/引脚/工作模式/关键参数 —— 是设备上报的**物理事实**（能力报告），
                  前端不能改：改了只是本地假象，下次上报就被覆盖，等于制造第二真相。
                  故这几行保持只读。
                · DMA 绑定 —— 是**真正可写的配置**（走节点配置下发），此前只有资源表里
                  那个窄小的选择器能改。这里就地提供同样的编辑能力，用户在看详情时
                  顺手就能改，不必回到表格里找那一行。
                · 已挂载通道 —— 只读计数，但给出进「通道管理」的入口，省一次跳转。
              -->
              <div v-if="selectedResource" class="bus-detail-list" data-bus-detail>
                <div><span>资源名称</span><b class="mono">{{ selectedResource.id }}</b></div>
                <div><span>引脚</span><b class="mono">{{ resourcePins(selectedResource) }}</b></div>
                <div><span>工作模式</span><b>{{ resourceMode(selectedResource) }}</b></div>
                <div><span>关键参数</span><b>{{ resourceParameters(selectedResource).join(' · ') || '—' }}</b></div>
                <div>
                  <span>已挂载通道</span>
                  <b>
                    {{ resourceMountedChannels(selectedResource).length }}
                    <button v-if="resourceMountedChannels(selectedResource).length > 0" class="link-btn bus-detail-action" type="button" data-detail-channels @click="navigateToNodeChannels">查看</button>
                  </b>
                </div>
                <div>
                  <span>DMA 绑定</span>
                  <b v-if="selectedResource.enabled !== false && busSupportsDma && resourceDmaCandidates(selectedResource).length > 0" class="bus-detail-edit">
                    <el-select
                      class="dma-select"
                      size="small"
                      :data-dma-detail-select="selectedResource.id"
                      :aria-label="`${selectedResource.id} 绑定的 DMA 资源`"
                      :model-value="dmaSelectionFor(selectedResource)"
                      :disabled="nodeOffline || !canToggleResourceDma(selectedResource)"
                      placeholder="选择 DMA 资源"
                      @change="(value: any) => changeResourceDma(selectedResource!, String(value))"
                    >
                      <el-option label="不使用 DMA" value="" />
                      <el-option
                        v-for="dma in resourceDmaCandidates(selectedResource)"
                        :key="dma.dma_id"
                        :label="dmaOptionLabel(dma)"
                        :value="String(dma.dma_id)"
                      />
                    </el-select>
                  </b>
                  <b v-else :class="{ 'dma-bound': resourceDmaBinding(selectedResource)?.bound_to }">{{ resourceDmaBinding(selectedResource)?.bound_to || '未绑定' }}</b>
                </div>
              </div>
              <el-empty v-else description="请选择资源" :image-size="72" />
            </section>

            <section class="card bus-create-card">
              <div class="bus-create-head"><b>新建 {{ activeBusTab.label }} 通道</b></div>
              <div class="bus-create-summary">
                <span class="bus-create-resource-label">已选资源</span>
                <b class="mono">{{ selectedResource?.id || '请选择资源' }}</b>
                <span class="bus-tag" :class="busSupportsChannels ? 'bus-tag-blue' : 'bus-tag-gray'">{{ busSupportsChannels ? '支持通道' : '引脚直控资源' }}</span>
              </div>
              <div v-if="busSupportsChannels" class="bus-create-fields">
                <div class="bus-create-section-title">创建表单支持的字段</div>
                <div class="bus-create-field-grid">
                  <div class="bus-create-field"><span>硬件类型</span><b>{{ activeBusTab.label }}</b></div>
                  <div class="bus-create-field"><span>硬件资源</span><b class="mono">{{ selectedResource?.id || '—' }}</b></div>
                  <template v-if="activeBusType === 'i2c'">
                    <div class="bus-create-field"><span>从机地址</span><b>创建时填写</b></div>
                    <div class="bus-create-field"><span>时钟频率</span><b>能力范围内选择</b></div>
                  </template>
                  <template v-else-if="activeBusType === 'uart'">
                    <div class="bus-create-field"><span>波特率</span><b>能力范围内选择</b></div>
                    <div class="bus-create-field"><span>串口参数</span><b>数据位 / 停止位 / 校验</b></div>
                  </template>
                  <template v-else-if="activeBusType === 'spi'">
                    <div class="bus-create-field"><span>CS 引脚</span><b>能力范围内选择</b></div>
                    <div class="bus-create-field"><span>SPI 模式</span><b>能力范围内选择</b></div>
                  </template>
                  <template v-else-if="activeBusType === 'adc'">
                    <div class="bus-create-field"><span>衰减</span><b>能力范围内选择</b></div>
                    <div class="bus-create-field"><span>位宽</span><b>能力范围内选择</b></div>
                  </template>
                  <div class="bus-create-field"><span>通道名称</span><b>可选</b></div>
                  <div class="bus-create-field"><span>启用状态</span><b>可设置</b></div>
                </div>
              </div>
              <div class="bus-create-actions">
                <button class="btn btn-plain" :disabled="!selectedResource" @click="selectedResourceId = ''">取消选择</button>
                <button class="btn btn-primary" :disabled="nodeOffline || !selectedResource || selectedResource.enabled === false || !busSupportsChannels" @click="selectedResource ? openChannelManager(selectedResource) : undefined">{{ busSupportsChannels ? '新建通道' : '此资源不支持通道' }}</button>
              </div>
            </section>
          </aside>
        </div>

        <!-- A（2026-09-20）：已创建通道列表。
             「资源」与「通道」是两个层级：资源是设备上报的 UART0/UART1/I2C0…（物理能力），
             通道是用户基于某个资源创建的采集实例（有名称/使能/波特率/关联设备）。
             改前本页只有资源表，通道的唯一痕迹是「已挂载通道」列里的一个**数字**
             （resourceMountedChannels(resource).length）—— 用户看不到通道本身。
             实测（隔离栈 18096，节点离线）：.channel-tag=0、整页无「已创建通道」字样、
             body 不含作为通道呈现的 "UART1"；而 GET /api/v1/channels 明明返回 total=1。
             数据源与「通道健康状态」卡同源（fetchChannels → /api/v1/channels），
             这里渲染**全量**（不截断），每行有进入该通道配置的入口。
             注意：本卡与资源表并列而非合并 —— 合并会再次把两个层级混为一谈。 -->
        <section class="card bus-channels-card" data-created-channels>
          <div class="bus-channels-head">
            <b>已创建通道</b>
            <span class="bus-tag" :class="channels.length ? 'bus-tag-green' : 'bus-tag-gray'" data-created-channels-count>{{ channelsLoading ? '加载中…' : channels.length + ' 条' }}</span>
            <span class="bus-channels-hint">通道建立在硬件资源之上，是采集实例（与上面的「资源」不是同一层级）</span>
            <button class="link-btn" type="button" data-created-channels-all @click="navigateToNodeChannels"><el-icon :size="12"><View /></el-icon>在通道管理中查看</button>
          </div>
          <div v-if="channelsLoading && channels.length === 0" class="bus-channels-empty">正在读取通道列表…</div>
          <div v-else-if="channels.length === 0" class="bus-channels-empty" data-created-channels-empty>该节点暂无已创建通道 —— 在上方资源表选一行后点「新建通道」即可创建</div>
          <div v-else class="bus-channels-list">
            <div v-for="ch in channels" :key="ch.id" class="bus-channel-item" :data-channel-id="ch.id">
              <span class="bus-channel-name">{{ channelName(ch) }}</span>
              <el-tag size="small" effect="plain">{{ channelTypeLabel(ch) }}</el-tag>
              <span class="bus-channel-hw mono">{{ channelHardwareKey(ch) }}</span>
              <span class="channel-state-tag" :class="channelEnabled(ch) ? 'state-on' : 'state-off'">{{ channelEnabled(ch) ? '已启用' : '已禁用' }}</span>
              <span class="bus-channel-res">资源 {{ channelResourceLabel(ch) }}</span>
              <span class="bus-channel-actions">
                <!-- 内联编辑态：就在这一行里改，Enter 提交 / Esc 取消，不再弹窗遮页 -->
                <template v-if="baudInlineId === String(ch.id)">
                  <el-input-number
                    v-model="baudInlineValue"
                    size="small"
                    controls-position="right"
                    :min="1200"
                    :max="baudMax"
                    :step="1"
                    :disabled="baudInlineSubmitting"
                    class="baud-inline-input"
                    data-baud-inline-input
                    @keyup.enter="submitBaudInline(ch)"
                    @keyup.esc="cancelBaudInline"
                  />
                  <button class="link-btn" type="button" data-baud-inline-save :disabled="baudInlineSubmitting" @click="submitBaudInline(ch)">{{ baudInlineSubmitting ? '下发中…' : '保存' }}</button>
                  <button class="link-btn" type="button" data-baud-inline-cancel :disabled="baudInlineSubmitting" @click="cancelBaudInline">取消</button>
                  <span v-if="baudInlineError" class="baud-inline-error" data-baud-inline-error>{{ baudInlineError }}</span>
                </template>
                <template v-else>
                  <button class="link-btn" type="button" :data-edit-channel="ch.id" :disabled="nodeOffline" @click="editChannel(ch)"><el-icon :size="12"><EditPen /></el-icon>配置</button>
                  <button v-if="isUARTChannel(ch)" class="link-btn" type="button" :data-baud-channel="ch.id" :disabled="nodeOffline" @click="openBaudInline(ch)"><el-icon :size="12"><Tools /></el-icon>改波特率</button>
                </template>
              </span>
            </div>
          </div>
        </section>
      </template>

      <!-- 外设控制：GPIO/PWM 直控（PeriphCmd 0x1B / PeriphRsp 0x1C，不走通道）。
           改前该能力只存在于不可达组件（PeripheralControl → GPIOResourceList/PWMResourceList，
           仅由死文件 NodeDetail.vue 经 ChannelPanel.vue 消费），用户完全看不到；
           FR-I1/I2/I3 已标 ✅ 但生产无入口 —— 本 TAB 是能力接线，不是新功能。
           写后回填：本页订阅 WS periph_result，经 registerPending* 登记请求后按 request_id
           代际校验回填（与 ChannelPanel 同一套语义，见 onPeriphResult）。 -->
      <template v-else-if="activeTab === '外设控制'">
        <PeripheralControl
          ref="peripheralControlRef"
          :node-id="nodeSerial"
          :offline="nodeOffline"
          :register-pending-gpio="registerPendingPeripheral"
          :register-pending-pwm="registerPendingPWM"
          @configure-gpio="(pin: number) => onPeripheralConfigure(`GPIO ${pin}`)"
          @edit-gpio="(pin: number) => onPeripheralConfigure(`GPIO ${pin}`)"
          @configure-pwm="(id: string) => onPeripheralConfigure(`PWM ${id}`)"
          @edit-pwm="(id: string) => onPeripheralConfigure(`PWM ${id}`)"
        />
      </template>

      <!-- DMA 通道：只读资源视图，绑定操作在总线配置 TAB -->
      <template v-else-if="activeTab === 'DMA 通道'">
        <section class="card dma-card">
          <div v-if="dmaLoading" class="card-loading"><el-skeleton :rows="3" animated /></div>
          <!-- D5：DMA 与其它总线不同——设备可能**根本没上报 DMA 资源**
               （本仓实测 nodes.capabilities.buses.dma 缺失，而 uart/spi 却报 dma_supported=true，
                 属设备侧数据矛盾）。此时既不能给"点不动的空控件"，也不能只说"暂无"：
                 必须区分「设备未上报」与「上报了但一条都没有」，否则用户会把
                 "节点没有这个能力"读成"这台设备确实没有 DMA"。 -->
          <div v-else-if="dmaChannels.length === 0" class="card-empty dma-empty" data-dma-empty>
            <template v-if="dmaNotReported">
              该节点未上报 DMA 资源（设备能力上报中没有 dma 项）—— 这不是"没有 DMA 通道"，而是本节点未提供该能力信息。
            </template>
            <template v-else>该节点已上报 {{ dmaReportedCount }} 条 DMA 资源，但当前没有可用的 DMA 通道信息</template>
          </div>
          <div v-else class="dma-grid">
            <div v-for="dma in dmaChannels" :key="dma.dma_id" class="dma-item">
              <div class="dma-item-head">
                <span class="mono">{{ dma.name || `DMA${dma.dma_id}` }}</span>
                <span class="bus-tag" :class="dmaTagClass(dma.state)">{{ dmaStateText(dma.state) }}</span>
              </div>
              <div class="dma-item-row"><span>通道类型</span><b class="mono">{{ dmaTypeText(dma.dma_type) }}</b></div>
              <div class="dma-item-row"><span>支持能力</span><b class="mono">{{ capText(dma.capabilities) }}</b></div>
              <div class="dma-item-row"><span>最大突发长度</span><b class="mono">{{ dma.max_burst }}</b></div>
              <div class="dma-item-row"><span>DMA 绑定</span><b class="mono">{{ dmaBoundToText(dma.bound_to) }}</b></div>
              <div class="dma-item-row"><span>可服务总线</span><b class="mono">{{ busText(dma.compatible_bus) }}</b></div>
            </div>
          </div>
        </section>
      </template>

      <!-- 关联设备 -->
      <template v-else-if="activeTab === '关联设备'">
        <section class="card device-card">
          <div class="card-head">
            <span class="card-title">关联设备<span v-if="devices.length > 0" class="device-count">（共 {{ devices.length }}）</span></span>
            <span class="device-card-actions">
              <span class="view-switch" role="tablist" aria-label="视图切换">
                <button type="button" class="view-switch-btn" :class="{ active: deviceViewMode === 'list' }" @click="deviceViewMode = 'list'">列表</button>
                <button type="button" class="view-switch-btn" :class="{ active: deviceViewMode === 'card' }" @click="deviceViewMode = 'card'">卡片</button>
              </span>
              <button class="btn btn-primary btn-sm" :disabled="!node?.node_id || nodeOffline" @click="showQuickCreate = true"><el-icon :size="13"><Plus /></el-icon>创建设备</button>
              <button class="btn btn-plain btn-sm" :disabled="devicesLoading" @click="fetchDevices"><el-icon :size="13" :class="{ spin: devicesLoading }"><RefreshRight /></el-icon>{{ devicesLoading ? '加载中…' : '刷新' }}</button>
            </span>
          </div>
          <div v-if="devicesLoading" class="card-loading"><el-skeleton :rows="3" animated /></div>
          <div v-else-if="devices.length === 0" class="card-empty">暂无设备</div>
          <!-- 列表视图 -->
          <div v-else-if="deviceViewMode === 'list'" class="chan-list">
            <div v-for="row in devices" :key="row.id" class="chan-row device-row">
              <span class="chan-icon"><el-icon :size="14"><Connection /></el-icon></span>
              <span class="chan-name">{{ row.name }}</span>
              <span class="chan-sub">{{ getDeviceTypeLabel(row.device_type) }}</span>
              <span class="chan-sub mono">{{ String(row.hardware_id || '') }}</span>
              <span class="chan-sub mono">{{ deviceChannelText(row) }}</span>
              <span class="chan-sub device-last-data">{{ formatLastData(row.last_data) }}</span>
              <StatusBadge :status="row.status" />
              <span class="chan-sub device-last-time">{{ row.last_data_time ? formatTime(row.last_data_time) : '—' }}</span>
              <button class="link-btn" type="button" @click="viewDevice(row)"><el-icon :size="12"><View /></el-icon>查看</button>
            </div>
          </div>
          <!-- 卡片视图（设计稿 new-node-edge.png 卡片模式） -->
          <div v-else class="device-grid">
            <!--
              is-offline 只对**真正离线/禁用**的设备生效（2026-10-03 审查发现）。
              原判据 `row.status !== 'online'` 会把 pending（已创建、还没采到数据）
              也变暗（CSS 里 is-offline 是 opacity .72），而同一行的 StatusBadge 却
              渲染 warning 色的「等待中」—— 一行里两个相反信号。
              与 StatusBadge 同口径：只有 offline/disabled/error 才算"非正常在线"。
            -->
            <div
              v-for="row in devices"
              :key="row.id"
              class="device-tile"
              :class="{ 'is-offline': isDeviceOffline(row.status) }"
              @click="viewDevice(row)"
            >
              <div class="device-tile-head">
                <span class="device-tile-icon"><el-icon :size="18"><Connection /></el-icon></span>
                <div class="device-tile-title">
                  <b class="device-tile-name" :title="row.name">{{ row.name }}</b>
                  <span class="device-tile-type">{{ getDeviceTypeLabel(row.device_type) }}</span>
                </div>
                <StatusBadge :status="row.status" />
              </div>
              <div class="device-tile-body">
                <div class="device-tile-row"><span>总线地址</span><b class="mono">{{ String(row.hardware_id || '—') }}</b></div>
                <div class="device-tile-row"><span>所在通道</span><b class="mono">{{ deviceChannelText(row) }}</b></div>
              </div>
              <div class="device-tile-reading" :class="{ 'no-data': !row.last_data }">
                <template v-if="row.last_data">
                  <span class="reading-label">最新读数</span>
                  <strong class="reading-value" :title="formatLastData(row.last_data)">{{ formatLastData(row.last_data) }}</strong>
                  <span class="reading-time">{{ row.last_data_time ? formatTime(row.last_data_time) : '—' }}</span>
                </template>
                <template v-else><span class="reading-none">等待首条采集数据</span></template>
              </div>
            </div>
          </div>
        </section>
      </template>

      <!-- OTA 历史 -->
      <template v-else-if="activeTab === 'OTA 历史'">
        <section class="card ota-card">
          <div class="card-head">
            <span class="card-title">OTA 升级历史</span>
            <button class="btn btn-plain btn-sm" :disabled="otaHistoryLoading" @click="fetchOTAHistory"><el-icon :size="13" :class="{ spin: otaHistoryLoading }"><RefreshRight /></el-icon>{{ otaHistoryLoading ? '加载中…' : '刷新' }}</button>
          </div>
          <div v-if="otaHistoryLoading" class="card-loading"><el-skeleton :rows="3" animated /></div>
          <div v-else-if="otaHistory.length === 0" class="card-empty">暂无升级记录</div>
          <div v-else class="bus-table-wrap ota-table-wrap">
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
                    <button v-if="record.status === 'pending' || record.status === 'downloading'" class="link-btn link-btn-danger" type="button" :disabled="nodeOffline" @click="handleCancelOTA(record)">取消</button>
                    <span v-else class="ota-na">—</span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>
      </template>

      <!-- 系统日志 -->
      <template v-else-if="activeTab === '系统日志'">
        <section class="card log-card">
          <div class="card-head"><span class="card-title">系统日志</span></div>
          <LogPanel :collector-id="nodeSerial" :node-device-id="node?.node_id" />
        </section>
      </template>

      <!-- 通道终端 -->
      <template v-else-if="activeTab === '通道终端'">
        <section class="card terminal-card">
          <div class="card-head"><span class="card-title">通道终端</span></div>
          <ChannelTerminal :collector-id="nodeSerial" :node-device-id="node?.node_id" :channels="channels" />
        </section>
      </template>
    </template>

    <!-- 加载失败空态 -->
    <div v-else class="card no-error">
      <el-empty description="节点不存在或加载失败">
        <el-button type="primary" @click="refreshAll">重试</el-button>
        <el-button @click="goBack">返回列表</el-button>
      </el-empty>
    </div>

    <!-- 重命名弹窗 -->
    <el-dialog v-model="renameVisible" title="编辑设备名称" width="420px">
      <div class="form-row">
        <label class="form-label">设备名称</label>
        <el-input v-model="renameDraft" maxlength="64" show-word-limit placeholder="请输入设备名称" />
      </div>
      <template #footer>
        <button class="btn btn-plain" @click="renameVisible = false">取消</button>
        <button class="btn btn-primary" :disabled="renameSaving" @click="saveRename">{{ renameSaving ? '保存中...' : '保存' }}</button>
      </template>
    </el-dialog>

    <!-- 全部事件弹窗 -->
    <el-dialog v-model="eventsVisible" title="全部事件" width="560px">
      <div v-if="nodeEvents.length === 0" class="card-empty">暂无事件</div>
      <div v-else class="all-events">
        <div v-for="ev in nodeEvents" :key="ev.id" class="ae-row">
          <span class="tl-icon" :class="ev.new_status === 'online' ? 'tl-ok' : 'tl-bad'">
            <el-icon :size="10"><component :is="ev.new_status === 'online' ? Select : SwitchButton" /></el-icon>
          </span>
          <span class="ae-text">{{ eventText(ev) }}</span>
          <span class="ae-time">{{ formatTime(ev.created_at) }}</span>
        </div>
      </div>
    </el-dialog>

    <!-- OTA 升级对话框（复用生产组件） -->
    <OTAForm
      :visible="showOTADialog"
      :collector-id="nodeSerial"
      :collector-model="node?.model"
      :current-firmware-version="node?.firmware_version"
      @success="handleOTASuccess"
      @update:visible="showOTADialog = $event"
    />

    <!-- 通道创建复用生产组件；禁止复制 demo 的 mock 表单。 -->
    <!-- 通道编辑：由通道健康卡某一行发起时携带该行数据（initial-data ⇒ 编辑态）。 -->
    <ChannelManager
      v-model="channelManagerVisible"
      :initial-data="channelManagerInitialData"
      :collector-id="nodeSerial"
      :capabilities="capabilities"
      :preset-hardware-type="activeBusType"
      :preset-hardware-id="selectedResourceId"
      :collector-status="node?.status"
      :occupied-hardware-ids="occupiedHardwareIds"
      @refresh="handleChannelManagerRefresh"
    />

    <!-- 关联设备 TAB：快速创建设备（写操作，node_id 就绪才可打开） -->
    <QuickCreateDeviceDialog
      v-model="showQuickCreate"
      :node-id="nodeSerial"
      :node-name="node?.name"
      :channels="channels"
      :channels-loading="channelsLoading"
      @created="handleDeviceCreated"
    />

    <!-- 波特率重配置：服务端 2026-09-16 起已真实改写 bus_config 并触发下发
         （POST /channels/:id/reconfigure，见 handler_device.go）。
         本前端的旧文案「服务端尚未实现」是 2026-08-13 的占位，已过时 —— 见下方注释。 -->
    <el-dialog v-model="baudToolVisible" title="修改波特率" width="480px">
      <div v-if="baudToolSelection" class="baud-target" data-baud-target>
        {{ baudToolTargetText }}
      </div>
      <div v-else class="baud-pick-hint" data-baud-scope-hint>
        未选中通道：本次将按「总线资源」批量配置该资源上的全部 UART 通道。
      </div>
      <div class="baud-field">
        <label for="baud-tool-input">目标波特率</label>
        <!-- 既有 validateBaudrate() 的枚举不含 230400/460800，而 921600 这个既有的
             「能力兜底」值也不在白名单内 —— 抽屉在这里的话后端明明会接受
             （withUARTBaudrate 只要求正整数），前端却先失败。失败方向是「不发请求」，
             不会谎报成功，故保持既有白名单不变，只把上限交给能力数据约束。 -->
        <el-input-number id="baud-tool-input" data-baud-input v-model="baudTarget" :min="1200" :max="baudMax" :step="1200" />
        <span class="baud-range">范围 1200 ~ {{ baudMax }}</span>
      </div>
      <p class="baud-note">改完即写入通道 bus_config 并向节点下发配置；节点按新波特率重建总线。改错会让该通道上的设备失联，请先在设备侧确认目标值。</p>
      <el-alert v-if="baudToolError" type="error" :closable="false" :title="baudToolError" data-baud-error />
      <template #footer>
        <button class="btn btn-plain" data-baud-cancel @click="baudToolVisible = false">取消</button>
        <button class="btn btn-primary" data-baud-confirm :disabled="baudSubmitting" @click="confirmBaudReconfigure">
          {{ baudSubmitting ? '下发中…' : '确认修改' }}
        </button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch, nextTick } from 'vue'
import { configSyncStateLabel } from '@/utils/configSyncState'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import feedback from '@/utils/feedback'
import {
  ArrowRight, Clock, Cloudy, Connection, CopyDocument, Cpu, DataLine, Delete, Document,
  EditPen, Grid, House, InfoFilled, Link, Lock, MagicStick, Monitor, Odometer, Plus, Refresh,
  RefreshRight, Search, Select, Share, SwitchButton, Timer, Tools, UploadFilled, UserFilled,
  View, WarningFilled,
} from '@element-plus/icons-vue'
import { nodeApi, type Capabilities, type DmaChannelInfo, type Node, type OTARecord } from '@/api/node'
import { nodeDeviceOpApi, type NodeDeviceOp } from '@/api/nodeDeviceOp'
import { channelApi, type Channel } from '@/api/channel'
import client from '@/api/client'
import OTAForm from '@/components/forms/OTAForm.vue'
import ChannelManager from '@/components/channel/ChannelManager.vue'
import ChannelTerminal from '@/components/channel/ChannelTerminal.vue'
import LogPanel from '@/components/node/LogPanel.vue'
import PeripheralControl from '@/components/periph/PeripheralControl.vue'
import QuickCreateDeviceDialog from '@/components/node/QuickCreateDeviceDialog.vue'
import StatusBadge from '@/components/common/StatusBadge.vue'
import { useWebSocketStore, type WebSocketMessage } from '@/stores/websocket'
import { useDmaStore } from '@/stores/dma'
import { useEdgeDeviceStore } from '@/stores/edgeDevice'
import { WS_EVENT } from '@/events/events'
import { getSessionGeneration, assertSessionGeneration } from '@/utils/sessionCache'
import { DmaState, dmaStateText, isDmaRebindable } from '@/utils/dmaState'
import { UNKNOWN, formatTime } from '@/utils/format'
import { sensorNameMap, sensorUnitMap } from '@/utils/sensor'
import { getDeviceTypeLabel } from '@/utils/deviceType'
import { logger } from '@/utils/logger'
import { validateBaudrate } from '@/utils/validate'
import { getHardwareDisplay, getHardwareLabel } from '@/utils/hardwareTag'

// ── 类型 ──
interface NodeEvent {
  id: number
  node_id: string
  event_type: string
  old_status: string
  new_status: string
  created_at: string
}

type BusType = 'i2c' | 'uart' | 'spi' | 'adc' | 'gpio' | 'pwm'
type BusResource = Record<string, any> & { id: string; enabled?: boolean }

const route = useRoute()
const router = useRouter()
const wsStore = useWebSocketStore()
const dmaStore = useDmaStore()
const edgeDeviceStore = useEdgeDeviceStore()

// ── 防竞态序列号（NodeDetail 模式） ──
let detailSequence = 0
let channelsSequence = 0
let eventsSequence = 0
let capabilitiesSequence = 0
let devicesSequence = 0
let otaSequence = 0
let componentOperationGeneration = 0

// ── 页面状态 ──
const loading = ref(false)
const refreshing = ref(false)
const node = ref<Node | null>(null)
const channels = ref<Channel[]>([])
const channelsLoading = ref(false)
const nodeEvents = ref<NodeEvent[]>([])
const eventsLoading = ref(false)
const devices = ref<any[]>([])
const devicesLoading = ref(false)
const otaHistory = ref<OTARecord[]>([])
const otaHistoryLoading = ref(false)

// ── 节点级设备操作（重启 / 恢复出厂） ──
//
// 判据与用户可见后果，逐条写清：
//
// 1) deviceOpSupported：只有服务端**确实**有设备传输时才显示按钮
//    （GET /nodes/device-ops 的 supported）。否则点了必然 501 ——
//    把「后端没这个能力」显示成「操作失败」是在误导操作员。
//
// 2) **acked=false 是"结果未知"，不是失败**。后端在设备没回 ACK 时返回 202，
//    axios 把 2xx 当成功 ⇒ 这里走 resolve 分支。此时**绝不能**说"重启失败"：
//    ACK 丢了的重启照样重启了，说失败会让操作员白跑一趟现场。
//
// 3) 恢复出厂**不擦除 WiFi**（后端与固件都是这个语义），确认文案必须写明，
//    否则这个功能没人敢用。
//
// 4) 成功提示要提醒"页面可能短暂失联"：设备重启时连接会断，
//    不提前说明的话，操作员会以为是自己把设备搞坏了。
const deviceOpSupported = ref(false)
const deviceOpBusy = ref(false)
const deviceOpInFlight = ref<NodeDeviceOp | null>(null)
const deviceOpReady = computed(() => deviceOpSupported.value)

async function loadDeviceOpSupport() {
  try {
    const catalog = await nodeDeviceOpApi.catalog()
    deviceOpSupported.value = catalog.supported
  } catch (err) {
    // 能力探测失败**不能**让页面报错或影响别的行为：它只决定两个按钮显不显示。
    // 保守取 false（不显示），debug 级留痕，不打扰用户。
    deviceOpSupported.value = false
    logger.debug('读取设备操作能力失败', { error: String(err) })
  }
}

async function handleDeviceOp(op: NodeDeviceOp) {
  if (!node.value || nodeOffline.value || deviceOpBusy.value) return
  const serial = nodeSerial.value
  const sessionGeneration = getSessionGeneration()

  const label = op === 'reboot' ? '重启' : '恢复出厂'

  // 破坏性操作必须二次确认（走既有 feedback.confirmDanger：danger 按钮语义 +
  // 焦点确定落在"取消"侧）。
  const message = op === 'reboot'
    ? '确认重启节点 ' + serial + '？设备会断开连接约 10~30 秒后自动重连；' +
      '配置与 WiFi 连接信息都会保留。'
    : '确认将节点 ' + serial + ' 恢复出厂？设备上的配置会被清除，' +
      '但 WiFi 连接信息与设备身份会保留，设备会自动重新上线。'
  const ok = await feedback.confirmDanger(message, {
    title: label + ' ' + serial,
    confirmText: label,
  })
  if (!ok) return

  deviceOpBusy.value = true
  deviceOpInFlight.value = op
  try {
    const result = await nodeDeviceOpApi.run(serial, op)
    if (getSessionGeneration() !== sessionGeneration) return

    if (result.acked && result.result === 'ok') {
      ElMessage.success(
        label + '指令已被设备接受。设备即将断开，页面可能短暂失联，稍后刷新即可。'
      )
    } else if (!result.acked) {
      // 202：已送达但没收到确认 ⇒ **结果未知**。措辞不能是"失败"。
      ElMessage.warning(
        label + '指令已送达，但设备未确认收到。操作**可能已经生效**——' +
        '请稍后刷新页面查看设备是否重新上线，不要直接重复操作。'
      )
    } else {
      // 设备回了 ACK 但不是 ok：设备明确拒绝，带它自己的原因。
      // 走 feedback.error 而不是裸 ElMessage.error —— 本仓有静态守卫
      // (I-1) 强制错误提示必须经 utils/feedback 汇聚，我第一次提交时
      // **正是被那条守卫抓到的**。
      feedback.error('设备拒绝了' + label + '：' + result.result)
    }
  } catch (err: any) {
    if (getSessionGeneration() !== sessionGeneration) return
    // 设备明确拒绝时后端给 409 + errorCode device_rejected:<原因>，
    // 把设备的原始原因透出来，不要被兜底文案盖掉。
    feedback.handleError(err, label + '失败')
  } finally {
    deviceOpBusy.value = false
    deviceOpInFlight.value = null
  }
}

// 页头操作
const syncing = ref(false)
const pinging = ref(false)
const pendingPingTimeout = ref<ReturnType<typeof setTimeout> | null>(null)
const showOTADialog = ref(false)

// 弹窗
const renameVisible = ref(false)
const renameDraft = ref('')
const renameSaving = ref(false)
const eventsVisible = ref(false)

// ── 总线配置：能力数据是设备 ResourceReport 的只读映射。 ──
const capabilities = ref<Capabilities>({ buses: {} })
const busLoading = ref(false)
const busLoadError = ref(false)
const busDataLoaded = ref(false)
const activeBusType = ref<BusType>('i2c')
/** 当前是否在 UART TAB。"改波特率"等 UART 专属工具只在此时渲染。 */
const isUartTab = computed(() => activeBusType.value === 'uart')
const selectedResourceId = ref('')
const busPage = ref(1)
const busPageSize = 10
const i2cScanning = ref(false)
const scanResult = ref<string[] | null>(null)
const resourceQuerying = ref(false)
const channelManagerVisible = ref(false)
// 通道健康卡「编辑」入口的数据源：打开 ChannelManager 时传给 initial-data（编辑而非新建）。
const channelManagerInitialData = ref<Channel | null>(null)

// ── 波特率重配置（POST /channels/:id/reconfigure）────────────────────────
// 目标选择两档（见下方 baudToolSelection）：
//   1. 用户在「已创建通道」行里点了「改波特率」 ⇒ 只改该通道；
//   2. 否则用上方资源表选中的 UART 资源 ⇒ 批量改该资源上的全部 UART 通道。
// 两档都不成立时入口**禁用**并说明怎么选 —— 既不让用户白点一下再吃 400，
// 也不伪造一个「默认通道」当目标。
const baudToolVisible = ref(false)
/**
 * 内联波特率编辑：正在编辑的通道 id（'' = 未编辑）。
 *
 * 为什么保留弹窗之外再加内联：弹窗在"只改一条通道的波特率"这个最高频场景里
 * 要多两次点击（打开、关闭）且遮住整页。内联直接在通道行上改，Enter 提交、Esc 取消。
 * 弹窗仍保留，用于"按资源批量改多条通道"那种确实需要确认范围的场景。
 */
const baudInlineId = ref<string>('')
const baudInlineValue = ref<number>(9600)
const baudInlineSubmitting = ref(false)
const baudInlineError = ref('')
const baudPickedChannel = ref<Channel | null>(null)
const baudTarget = ref(9600)
const baudSubmitting = ref(false)
/** 失败原因就地展示在对话框里（活动元素被禁用/卸载时 ElMessage 可能不可达）。 */
const baudToolError = ref('')

// ── Tab 栏 ──
const tabs = [
  { label: '基本信息', icon: House },
  { label: '总线配置', icon: Share },
  { label: '外设控制', icon: Tools },
  { label: 'DMA 通道', icon: Grid },
  { label: '关联设备', icon: UserFilled },
  { label: 'OTA 历史', icon: Cloudy },
  { label: '系统日志', icon: Document },
  { label: '通道终端', icon: Monitor },
]
const activeTab = ref('基本信息')

/**
 * 消费列表页快捷入口的 `?tab=` 深链（NodeList.vue 的「配置」按钮推 `?tab=config`）。
 *
 * 改前该 query 全仓无消费者 —— 用户从节点列表点「配置」只是原地换了个 URL，
 * 落在「基本信息」TAB，看起来像点了没反应。映射表把外部的短名收敛到本页 TAB 标签，
 * 未知值一律忽略（不报错、不白屏），保持「基本信息」默认。
 */
const TAB_QUERY_MAP: Record<string, string> = {
  config: '总线配置',
  bus: '总线配置',
  peripheral: '外设控制',
  gpio: '外设控制',
  pwm: '外设控制',
  dma: 'DMA 通道',
  devices: '关联设备',
  ota: 'OTA 历史',
  logs: '系统日志',
  terminal: '通道终端',
  info: '基本信息',
}
function applyTabQuery(value: unknown) {
  if (typeof value !== 'string') return
  const target = TAB_QUERY_MAP[value]
  if (target) activeTab.value = target
}
applyTabQuery(route.query.tab)
// 同一路由内 query 变化（如在页内再次点「配置」）也要生效，故 watch 而非仅初始化。
watch(() => route.query.tab, applyTabQuery)

/**
 * M1：把**激活的**页签滚入视野。
 *
 * 390px 实测缺陷：`?tab=terminal` 深链进来后激活标签「通道终端」在 776–861px，
 * 而 .tab-bar 可视区右边界只有 362px、`scrollLeft` 仍是 0 ⇒ 手机上栏里只显示
 * 「基本信息/总线配置/外设控制」，**用户看不到自己在哪个页签**。
 *
 * 为什么必须 nextTick + 元素查询（时序）：
 *  · 标签栏在 `<template v-else-if="node">` 里，而 `node` 由 fetchDetail() **异步**填充
 *    —— 首屏那一刻 DOM 里根本没有 `.tab-item`，此刻 scrollIntoView 会拿到 null 而静默无效；
 *  · 深链场景下 `applyTabQuery` 在 setup 阶段就改好了 activeTab，早于首次渲染，
 *    所以「watch activeTab」的即时回调同样查不到元素。
 *  故同时观察 `tabBarRef`：它在元素**真正挂载**时才由 null 变成节点，
 *  那正是"标签栏已存在"的唯一可靠信号；再排到 nextTick 确保 activeTab 的新样式
 *  已落到 DOM。查不到就安静返回 —— 后续 activeTab/挂载变化仍会再次触发，不会永久失效。
 *
 * 为什么不用 `scrollLeft = 0` 之类的"重置"：那会让每次切换页签都跳回栏首，
 * 既没解决"激活项在右侧看不见"，又新增了跳动。`inline:'nearest'` 只在必要时滚动。
 * `block:'nearest'` 不可省：省略时浏览器按默认 `block:'start'` 处理，会把标签栏
 * 滚到视口顶部，导致页面纵向跳动（移动端尤其明显）。
 */
const tabBarRef = ref<HTMLElement | null>(null)
async function scrollActiveTabIntoView() {
  await nextTick()
  const bar = tabBarRef.value
  if (!bar) return
  // 用 data-tab 逐项比对定位，不靠文本匹配、也不用 CSS.escape 拼选择器：
  // 文本匹配会因"包含关系"选错元素，而 CSS.escape 在 happy-dom(单测环境)下不存在。
  const active = Array.from(bar.querySelectorAll<HTMLElement>('.tab-item'))
    .find(el => el.dataset.tab === activeTab.value)
  if (!active) return
  active.scrollIntoView({ inline: 'nearest', block: 'nearest' })
}
// flush:'post' 保证在 DOM 更新后执行；同时观察 tabBarRef 覆盖"异步数据到达后标签栏才挂载"。
watch([activeTab, tabBarRef], () => { void scrollActiveTabIntoView() }, { immediate: true, flush: 'post' })
const deviceViewMode = ref<'list' | 'card'>('list')
const showQuickCreate = ref(false)

function activateTab(label: string) {
  activeTab.value = label
  if (label === '总线配置' || label === 'DMA 通道') {
    // D5：DMA 空态必须能区分「设备未上报该能力」与「上报了但一条不可用」，
    // 而后者需要 capabilities.buses.dma —— 所以进 DMA 页签同样要保证能力数据已加载
    // （改前只有"总线配置"会拉 capabilities，DMA 页签永远拿不到，只能笼统说"暂无"）。
    if (node.value && !busLoading.value && !busDataLoaded.value) void fetchBusData()
  }
  if (label === 'DMA 通道') {
    const serial = nodeSerial.value
    if (serial && dmaStore.mergedChannels.length === 0 && !dmaStore.loading) {
      void dmaStore.fetch(serial).catch(err => logger.warn('获取 DMA 通道失败', { error: String(err) }))
    }
  }
  if (label === '关联设备') {
    // 首次进入或创建后才强制刷新；来回切换走 store 30s TTL 缓存，不穿透
    if (devices.value.length === 0 && !devicesLoading.value) void fetchDevices()
  }
  if (label === 'OTA 历史' && otaHistory.value.length === 0 && !otaHistoryLoading.value) {
    void fetchOTAHistory()
  }
}

// 会话时钟
const nowTick = ref(Date.now())
const lastRefreshAt = ref(0)
let sessionTimer: ReturnType<typeof setInterval> | null = null
let unsubscribe: (() => void) | null = null

// ── 派生状态 ──
const nodeSerial = computed(() => node.value?.node_id || (route.params.id as string))
const nodeOnline = computed(() => node.value?.status === 'online')
const nodeOffline = computed(() => node.value?.status !== 'online')
const dmaChannels = computed(() => dmaStore.mergedChannels)
const dmaLoading = computed(() => dmaStore.loading)
// D5：设备能力上报里是否存在 dma 项。缺失 = 设备未上报该能力（而不是"上报了 0 条"）。
const dmaNotReported = computed(() => {
  const buses = (capabilities.value as any)?.buses
  return !buses || buses.dma === undefined || buses.dma === null || (Array.isArray(buses.dma) && buses.dma.length === 0)
})
/** 设备在能力上报里声明的 DMA 资源条数（用于区分"未上报"与"上报了但拿不到通道"）。 */
const dmaReportedCount = computed(() => {
  const dma = (capabilities.value as any)?.buses?.dma
  return Array.isArray(dma) ? dma.length : 0
})
/** 通道健康卡的预览条数（与模板 v-for 的 slice 上限共用同一常量，避免两处漂移）。 */
const CHANNEL_HEALTH_PREVIEW_LIMIT = 6

// label 一律取共享源，避免此处再维护一份总线展示名；description 是说明性文案，不参与实体命名。
const busTabs: Array<{ type: BusType; label: string; icon: any; description: string }> = [
  { type: 'i2c', label: getHardwareLabel('i2c'), icon: Cpu, description: 'I2C 总线用于连接低速外设，支持多主多从通信' },
  { type: 'uart', label: getHardwareLabel('uart'), icon: Connection, description: 'UART 串口用于异步串行通信' },
  { type: 'spi', label: getHardwareLabel('spi'), icon: MagicStick, description: 'SPI 总线用于高速同步串行通信' },
  { type: 'adc', label: getHardwareLabel('adc'), icon: DataLine, description: 'ADC 通道用于采集模拟量输入' },
  { type: 'gpio', label: getHardwareLabel('gpio'), icon: Share, description: 'GPIO 是引脚直控资源，不属于通道协议总线' },
  { type: 'pwm', label: getHardwareLabel('pwm'), icon: Tools, description: 'PWM 资源用于占空比控制输出' },
]
const activeBusTab = computed(() => busTabs.find(tab => tab.type === activeBusType.value) || busTabs[0])
const activeBusDescription = computed(() => activeBusTab.value.description)
const activeBusResources = computed<BusResource[]>(() => {
  const resources = capabilities.value?.buses?.[activeBusType.value] || []
  return Array.isArray(resources) ? resources.map((resource: any) => ({ ...resource, id: String(resource.id) })) : []
})
const selectedResource = computed(() => activeBusResources.value.find(resource => resource.id === selectedResourceId.value) || null)
const busTotalPages = computed(() => Math.max(1, Math.ceil(activeBusResources.value.length / busPageSize)))
const pagedBusResources = computed(() => activeBusResources.value.slice((busPage.value - 1) * busPageSize, busPage.value * busPageSize))
const busSupportsDma = computed(() => ['uart', 'i2c', 'spi'].includes(activeBusType.value))
// GPIO/PWM 是直接控制资源，不能错误地纳入协议通道系统。
const busSupportsChannels = computed(() => ['uart', 'i2c', 'spi', 'adc'].includes(activeBusType.value))
const busStats = computed(() => {
  const resources = activeBusResources.value
  const available = resources.filter(resource => resource.enabled !== false).length
  const mounted = resources.reduce((total, resource) => total + resourceMountedChannels(resource).length, 0)
  const dmaSupported = resources.filter(resource => resourceDmaCandidates(resource).length > 0).length
  const alerts = resources.filter(resource => ['alarm', 'error', 'warning'].includes(String(resource.status || '').toLowerCase())).length
  return [
    { label: '资源总数', value: resources.length, icon: Cpu, className: 'bus-stat-blue' },
    { label: '已挂载', value: mounted, icon: Link, className: 'bus-stat-green' },
    { label: '可用', value: available, icon: Select, className: 'bus-stat-blue' },
    { label: '禁用', value: resources.length - available, icon: Lock, className: 'bus-stat-muted' },
    { label: 'DMA 支持', value: dmaSupported, icon: Timer, className: 'bus-stat-green' },
    { label: '告警', value: alerts, icon: InfoFilled, className: 'bus-stat-warning' },
  ]
})

const pageTitle = computed(() => {
  if (node.value?.name) return node.value.name
  const id = nodeSerial.value
  return id ? `节点 ${id.slice(0, 8)}` : '节点总览'
})

const latencyMs = computed(() => node.value?.ping_latency_ms || node.value?.latency_ms || 0)

const qualityText = computed(() => {
  const q = node.value?.connection_quality ?? 0
  if (q >= 80) return '优秀'
  if (q >= 60) return '良好'
  if (q >= 40) return '一般'
  return '较差'
})
const qualityColor = computed(() => {
  const q = node.value?.connection_quality ?? 0
  // F32：改为页面级 token 引用。该返回值只喂给 :style 的 color/background，
  // Vue 不解析它，最终由 CSS 引擎解析 var() ⇒ 亮/暗主题各自取到正确的页面 token。
  // 修复前返回字面量 hex，实测同一元素在亮/暗两主题下计算色**完全相同**。
  if (q >= 80) return 'var(--no-success-text)'
  if (q >= 60) return 'var(--no-primary)'
  if (q >= 40) return 'var(--no-warning-text)'
  return 'var(--no-danger)'
})

const lastOnlineText = computed(() => {
  const t = node.value?.last_online_time
  if (!t) return '—'
  return formatTime(t)
})

// 在线时长：从 last_online_time 到现在（每秒走字），离线显示统一未知占位符 UNKNOWN（'—'）
const sessionDuration = computed(() => {
  const t = node.value?.last_online_time
  if (!t || !nodeOnline.value) return '—'
  const start = new Date(t).getTime()
  if (isNaN(start)) return '—'
  const diff = Math.floor((nowTick.value - start) / 1000)
  if (diff < 0) return '—'
  const days = Math.floor(diff / 86400)
  const hours = Math.floor((diff % 86400) / 3600)
  const minutes = Math.floor((diff % 3600) / 60)
  const parts: string[] = []
  if (days > 0) parts.push(`${days}天`)
  if (hours > 0) parts.push(`${hours}小时`)
  parts.push(`${minutes}分钟`)
  return parts.join(' ')
})

// 固件上报的 uptime（秒）转可读文本
const uptimeText = computed(() => {
  const s = node.value?.uptime_seconds
  if (!s || !nodeOnline.value) return '—'
  const days = Math.floor(s / 86400)
  const hours = Math.floor((s % 86400) / 3600)
  const minutes = Math.floor((s % 3600) / 60)
  const parts: string[] = []
  if (days > 0) parts.push(`${days}天`)
  if (hours > 0) parts.push(`${hours}小时`)
  parts.push(`${minutes}分钟`)
  return parts.join(' ')
})

const freeHeapText = computed(() => {
  const b = node.value?.free_heap_bytes
  if (!b || b <= 0) return ''
  return (b / 1024).toFixed(0)
})

const connectionTypeText = computed(() => {
  const ct = node.value?.connection_type
  if (!ct) return '—'
  return { wifi: 'WiFi', ethernet: '以太网', mqtt: 'MQTT' }[ct] || ct
})

const syncStateLabel = computed(() => {
  if (nodeOffline.value) return '离线'
  return configSyncStateLabel(node.value?.config_sync_state)
})

/**
 * ⚠ 2026-10-09（用户要求："DMA 分不到降级为提示"）：
 * 设备上报的配置**告警**（不是错误）。
 *
 * ⚠ 与 syncStateLabel 分开：配置是**成功**的，只是某些通道降级了。
 *   合在一起显示会让用户以为配置失败。
 *
 * 容错：后端可能给 null/undefined（老节点没这个字段），
 * 且元素形状不保证 —— 只保留有 message 的项，避免渲染出空白行。
 */
const configWarnings = computed(() => {
  const raw = node.value?.config_warnings
  if (!Array.isArray(raw)) return []
  return raw.filter((w): w is { code: string; channel_id?: number; message: string } =>
    !!w && typeof w === 'object' && typeof (w as { message?: unknown }).message === 'string' && (w as { message: string }).message !== '')
})

const metricsUpdatedText = computed(() => {
  if (!lastRefreshAt.value) return ''
  const ago = Math.max(0, Math.floor((nowTick.value - lastRefreshAt.value) / 1000))
  return `更新于 ${ago} 秒前`
})

// ── 通道健康 ──
function channelStatusText(ch: Channel): string {
  const s = (ch.status || '').toLowerCase()
  if (s === 'ok' || s === 'normal' || s === 'online' || s === 'running') return '正常'
  if (s === 'error' || s === 'failed' || s === 'warn') return '异常'
  return s ? s : '正常'
}
function channelBadgeClass(ch: Channel): string {
  const t = channelStatusText(ch)
  if (t === '正常') return 'cb-ok'
  if (t === '异常') return 'cb-warn'
  return 'cb-off'
}
/**
 * 通道的硬件标识展示（name 为空时的回退）。
 * 判据（自带总线名则不重复前缀，如 'UART0' 不再拼成 "UART UART0"）集中在共享
 * hardwareIdIncludesBusName/getHardwareDisplay，避免本页再维护一份总线名清单。
 */
function channelHardwareDisplay(ch: Channel): string {
  return getHardwareDisplay(ch.hardware_type, String(ch.hardware_id || ''))
}

function channelName(ch: Channel): string {
  return ch.name || channelHardwareDisplay(ch) || `通道 #${ch.id}`
}
const channelStats = computed(() => {
  const total = channels.value.length
  const ok = channels.value.filter(c => channelStatusText(c) === '正常').length
  const error = channels.value.filter(c => channelStatusText(c) === '异常').length
  return { total, ok, error, other: total - ok - error }
})

// ── DMA 辅助（与 NodeDetail.vue 相同的共享工具） ──
function dmaTypeText(type: number): string {
  return type === 0 ? 'GDMA' : `类型${type}`
}
/**
 * DMA 能力位的中文文案。
 *
 * 2026-10-02 统一：改前是 'TX' / 'RX' / 'Burst' —— 前两个是缩写、第三个是英文单词，
 * 混在同一行里既不同语言也不同粒度；而同一张卡上「最大突发」用的是中文。
 * 现在三项都用中文，与卡片内其它字段一致。
 */
function capText(cap: number): string {
  const parts: string[] = []
  if (cap & 1) parts.push('发送')
  if (cap & 2) parts.push('接收')
  if (cap & 4) parts.push('突发')
  return parts.join(' · ') || '无'
}

/**
 * DMA 通道绑定的展示文案。
 *
 * 后端 bound_to 是内部键 `bus/RESOURCE`（总线类型小写 + 资源名，如 uart/UART0）。
 * 2026-10-02 统一：改前把它**原样**渲染在 DMA 卡上，于是同一个资源在
 * 资源表/DMA 列/资源详情里是 "UART0"，在 DMA 卡里却成了 "uart/UART0" ——
 * 大小写与写法都不一致，用户会怀疑是不是两个不同的东西。
 * 解析不出来时如实回退原值，而不是丢弃信息。
 */
function dmaBoundToText(boundTo: string | undefined | null): string {
  const raw = String(boundTo || '').trim()
  if (!raw) return '未绑定'
  const slash = raw.indexOf('/')
  if (slash <= 0) return raw
  const bus = raw.slice(0, slash).toLowerCase()
  const resource = raw.slice(slash + 1)
  // busText 已把总线掩码映射为规范大写（UART/I2C/SPI），复用它保证口径一致。
  const label = busText(busTypeMask(bus as BusType)) || bus.toUpperCase()
  return resource + '（' + label + '）'
}
/**
 * DMA 能力掩码 → 总线名列表。
 *
 * 2026-10-02 统一分隔符：改前用半角 ', '，而本页其它并列项（关键参数、能力位）
 * 用的是 ' · '。同一个页面里两种并列符属于纯噪声，这里统一为 ' · '。
 */
function busText(bus: number): string {
  const parts: string[] = []
  if (bus & 1) parts.push('UART')
  if (bus & 2) parts.push('I2C')
  if (bus & 4) parts.push('SPI')
  return parts.join(' · ') || '无'
}
function dmaTagClass(state: number): string {
  // 复用 DMA 状态枚举，映射到页面 .bus-tag 色系（蓝=空闲/绿=已分配/灰=已禁用）
  if (state === DmaState.ALLOCATED) return 'bus-tag-green'
  if (state === DmaState.DISABLED) return 'bus-tag-gray'
  return 'bus-tag-blue'
}

// ── 关联设备辅助 ──

// isDeviceOffline 决定设备卡是否显示"变暗"（.is-offline → opacity .72）。
//
// 2026-10-03 审查发现：原判据 `status !== 'online'` 把 pending 也算成离线，
// 而同一行的 StatusBadge 对 pending 渲染 warning 色的「等待中」——
// 一行之内给出两个相反信号。pending 的语义是"已创建、还在等第一帧数据"，
// 既不是正常在线，也不是故障，不该被当作离线变暗。
// 与 StatusBadge 的 warning/danger 口径保持一致：offline/disabled/error 才算。
function isDeviceOffline(status: unknown): boolean {
  const s = String(status ?? '').toLowerCase()
  return s === 'offline' || s === 'disabled' || s === 'error'
}

function deviceChannelText(row: any): string {
  const channel = channels.value.find(ch => ch.id === row.channel_id)
  if (!channel) return '—'
  // 与 channelName 同一口径：hardware_id 已带总线名时不再重复前缀（旧写法出 "I2C I2C0"）。
  return channelHardwareDisplay(channel) || '—'
}
function viewDevice(row: any) {
  router.push(`/edge-device/${row.id}`)
}
function formatLastData(data: Record<string, any> | null): string {
  if (!data) return '—'
  const entries = Object.entries(data).filter(([k]) => k !== 'error_code' && k !== 'raw_data')
  if (entries.length === 0) return '—'
  return entries.slice(0, 3).map(([k, v]) => {
    const unit = sensorUnitMap[k] || ''
    const name = sensorNameMap[k] || k
    return `${name}: ${typeof v === 'number' ? v.toFixed(v < 10 ? 2 : 0) : v}${unit ? unit : ''}`
  }).join('  ')
}

// ── OTA 辅助 ──
//
// 状态全集以 backend/internal/ota/ota.go 的 Status* 常量为唯一真源（8 态）。
// 历史缺陷（2026-09-17）：otaStatusText / otaTagClass 只覆盖 5 态，漏掉
//   verifying / timeout / needs_retry；文案走 `texts[status] || status` ⇒ 中文界面
//   原样显示英文；颜色回退到中性灰 ⇒「升级超时」「需要重试」与「等待中」视觉无异。
//   timeout 是真实可达状态（ota.go 的 timeoutScanner 会写入），不是理论分支。
// 'cancelled' 是设计文档遗留的死条目：后端从未定义该状态（取消复用 failed +
//   error_msg）。这里不再为它保留映射，见 OtaStatusTruthSource.spec.ts 门禁。
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

// 位掩码表只覆盖有掩码位的总线类型；gpio/pwm/adc 无掩码位（索引结果为 undefined → 走 || 0）。
const BUS_TYPE_MASKS: Partial<Record<BusType, number>> = { uart: 1, i2c: 2, spi: 4 }

function busTypeMask(type: BusType): number {
  return BUS_TYPE_MASKS[type] || 0
}

// ── A：已创建通道列表的展示辅助（数据源 = channels，与「通道健康状态」卡同源） ──
/** 通道类型标签：后端 hardware_type 实测为大写（'UART'），统一归一后显示。 */
function channelTypeLabel(ch: Channel): string {
  return getHardwareLabel(ch.hardware_type) || '未知总线'
}
/** 通道自身的硬件标识（如 UART1）+ 通道号，用于与「资源」区分开。 */
function channelHardwareKey(ch: Channel): string {
  const hw = String(ch.hardware_id || '').trim() || '—'
  return `${hw} #${ch.id}`
}
/** 使能态：后端 enabled 为布尔；缺失时按未启用呈现（不假装已启用）。 */
function channelEnabled(ch: Channel): boolean {
  return (ch as any).enabled === true
}
/**
 * 该通道"绑定在哪个资源上"。
 * 资源 id 与通道 hardware_id 的对应关系与资源表**同一套口径**
 * （hardwareResourceMatchesChannel → canonicalHardwareId），因此这里反查 resources，
 * 保证本卡显示的"资源"与资源表「已挂载通道」列指向同一个对象，不会出现两份真相。
 */
function channelResourceLabel(ch: Channel): string {
  const type = String(ch.hardware_type || '').toLowerCase() as BusType
  const resources = (capabilities.value?.buses?.[type] || []) as any[]
  const hit = resources.find(resource => hardwareResourceMatchesChannel({ ...resource, id: String(resource.id) }, ch))
  if (hit) return String(hit.id)
  // 资源未上报/不匹配时如实回落到通道自己的 hardware_id，而不是编造一个资源名。
  return String(ch.hardware_id || '').trim() || '未匹配到已上报资源'
}

/**
 * 已被**已启用通道**占用的资源 id（给 ChannelManager 的下拉框标灰用）。
 *
 * 口径与后端 validateChannelCrossChannelPins 一致：只看 enabled 的通道。
 * 停用通道不占资源——否则"想换个串口"的用户会发现自己哪儿都选不了。
 * 用 hardware_id（"UART1"）而不是引脚号：下拉框的选项就是资源 id。
 */
const occupiedHardwareIds = computed<string[]>(() => {
  return channels.value
    .filter(channel => channel.enabled !== false)
    .map(channel => String(channel.hardware_id || '').trim())
    .filter(Boolean)
})

function resourceMountedChannels(resource: BusResource): Channel[] {
  return channels.value.filter(channel => (
    String(channel.hardware_type || '').toLowerCase() === activeBusType.value
    && hardwareResourceMatchesChannel(resource, channel)
  ))
}

/**
 * 资源层级显示的「当前波特率」：只从该资源上已创建通道的 bus_config 解出（见 baudCurrentRate）。
 * 没有通道时返回 0，调用方如实显示"未配置"——不用 max_baud（能力上限）冒充当前值。
 * 多条通道时取第一条可解出的值：本页资源行是**资源视角的摘要**，
 * 逐通道的精确值在下方「已创建通道」列表里（每行一个当前值），不在此处编造聚合。
 */
function resourceCurrentBaud(resource: BusResource): number {
  if (activeBusType.value !== 'uart') return 0
  for (const ch of resourceMountedChannels(resource)) {
    const rate = baudCurrentRate(ch)
    if (rate !== null) return rate
  }
  return 0
}

function canonicalHardwareId(type: BusType, rawId: unknown): number | null {
  const raw = String(rawId || '').trim()
  if (/^\d+$/.test(raw)) return Number(raw)
  const index = Number(raw.replace(/\D/g, ''))
  if (!Number.isFinite(index)) return null
  // 资源 ID 基数表只覆盖有基数约定的总线类型；pwm 无基数（→ undefined 时直接用索引）。
  const base = ({ i2c: 1, spi: 10, uart: 20, gpio: 30, adc: 40 } as Partial<Record<BusType, number>>)[type]
  return base === undefined ? index : base + index
}

function hardwareResourceMatchesChannel(resource: BusResource, channel: Channel): boolean {
  const resourceId = String(resource.id || '').toLowerCase()
  const channelId = String(channel.hardware_id || '').toLowerCase()
  if (resourceId === channelId) return true
  const resourceNumericId = canonicalHardwareId(activeBusType.value, resource.id)
  const channelNumericId = canonicalHardwareId(activeBusType.value, channel.hardware_id)
  return resourceNumericId !== null && resourceNumericId === channelNumericId
}

function resourcePins(resource: BusResource): string {
  const pairs: Array<[string, unknown]> = activeBusType.value === 'i2c'
    ? [['SDA', resource.default_sda_pin], ['SCL', resource.default_scl_pin]]
    : activeBusType.value === 'uart'
      ? [['TX', resource.default_tx_pin], ['RX', resource.default_rx_pin]]
      : activeBusType.value === 'spi'
        ? [['MOSI', resource.default_mosi_pin], ['MISO', resource.default_miso_pin], ['SCLK', resource.default_sclk_pin], ['CS', resource.default_cs_pin]]
        : activeBusType.value === 'gpio' || activeBusType.value === 'adc'
          ? [['GPIO', resource.pin]]
          : [['CH', resource.channel]]
  return pairs.filter(([, pin]) => pin !== undefined && pin !== null).map(([label, pin]) => `${label}${pin}`).join(' / ') || '—'
}

function formatFrequency(value: unknown): string | null {
  const numeric = Number(value)
  if (!Number.isFinite(numeric) || numeric <= 0) return null
  if (numeric >= 1_000_000 && numeric % 1_000_000 === 0) return `${numeric / 1_000_000}MHz`
  if (numeric >= 1000 && numeric % 1000 === 0) return `${numeric / 1000}kHz`
  return `${numeric}Hz`
}

function resourceParameters(resource: BusResource): string[] {
  const values: string[] = []
  if (activeBusType.value === 'i2c') {
    const frequency = formatFrequency(resource.freq_hz || resource.max_freq_hz)
    if (frequency) values.push(frequency)
    if (resource.mode) values.push(resource.mode === 'master' ? '主机' : '从机')
  } else if (activeBusType.value === 'uart') {
    // 这里显示的是**能力上限**，不是当前波特率（2026-10-03 缺陷 4）。
    // 旧代码写 `resource.baud_rate || resource.max_baud`，而 ResourceReport 的
    // uartEntry 只有 max_baud（固件能力），没有 baud_rate 字段 —— 于是永远走
    // 回退分支，把能力上限当成当前值显示。实测现场 max_baud=5000000，
    // 资源表就显示「5000000 baud」，而通道里真实生效的是 4800，
    // 这正是用户说的"波特率参数都对不上"。
    // 当前波特率只存在通道的 bus_config 里（复用 baudCurrentRate 的读取口径），
    // 资源层级没有通道时如实显示"未配置"，绝不拿 max_baud 冒充。
    const maxBaud = Number(resource.max_baud)
    if (maxBaud > 0) values.push(`最高 ${maxBaud} baud`)
    const currentBaud = resourceCurrentBaud(resource)
    if (currentBaud > 0) values.push(`当前 ${currentBaud} baud`)
    else values.push('当前波特率未配置（未创建通道）')
    if (resource.data_bits) values.push(`${resource.data_bits}bit`)
  } else if (activeBusType.value === 'spi') {
    const frequency = formatFrequency(resource.clock_hz || resource.max_freq_hz)
    if (frequency) values.push(frequency)
    if (resource.mode) values.push(resource.mode === 'master' ? '主机' : '从机')
  } else if (activeBusType.value === 'adc') {
    if (resource.bits || resource.max_bits) values.push(`${resource.bits || resource.max_bits}bit`)
    if (resource.attenuation) values.push(String(resource.attenuation))
  } else if (activeBusType.value === 'pwm') {
    if (resource.max_resolution_bits) values.push(`${resource.max_resolution_bits}bit`)
    if (resource.timer_count) values.push(`${resource.timer_count} 定时器`)
  } else if (resource.direction) {
    values.push(resource.direction === 'input' ? '输入' : '输出')
  }
  return values
}

function resourceMode(resource: BusResource): string {
  if (resource.mode === 'master') return '主机模式'
  if (resource.mode === 'slave') return '从机模式'
  if (resource.direction === 'input') return '输入'
  if (resource.direction === 'output') return '输出'
  return '—'
}

function resourceBindingKey(resource: BusResource): string {
  return `${activeBusType.value}/${resource.id}`.toLowerCase()
}

function resourceDmaCandidates(_resource: BusResource): DmaChannelInfo[] {
  const mask = busTypeMask(activeBusType.value)
  return mask ? dmaStore.mergedChannels.filter(dma => (dma.compatible_bus & mask) !== 0) : []
}

function resourceDmaBinding(resource: BusResource): DmaChannelInfo | undefined {
  const key = resourceBindingKey(resource)
  return resourceDmaCandidates(resource).find(dma => String(dma.bound_to || '').toLowerCase() === key)
}

/**
 * 该 DMA 是否**已经**绑定在本资源上。
 * 与 resourceDmaBinding 的区别：resourceDmaBinding 只看候选集内（按掩码筛过），
 * 而"已绑定的那条"可能因为掩码调整/数据陈旧而不在候选集内 —— 那种情况下
 * 选择器仍要把它显示为当前值，否则用户会以为"没绑定"，一改动就等于静默解绑。
 */
function isDmaBoundToResource(dma: DmaChannelInfo, resource: BusResource): boolean {
  return String(dma.bound_to || '').toLowerCase() === resourceBindingKey(resource)
}

/** 选择器当前值：'' = 不使用 DMA，否则是已绑定本资源那条的 dma_id。 */
function dmaSelectionFor(resource: BusResource): string {
  const all = dmaStore.mergedChannels as DmaChannelInfo[]
  const bound = all.find(dma => isDmaBoundToResource(dma, resource))
  if (bound) return String(bound.dma_id)
  return resourceDmaBinding(resource) ? String(resourceDmaBinding(resource)!.dma_id) : ''
}

/** 选项文案必须带 dma_id，否则两条同名/同前缀的通道无法区分。 */
function dmaOptionLabel(dma: DmaChannelInfo): string {
  return `${dma.name || 'DMA' + dma.dma_id}（#${dma.dma_id} · ${busText(dma.compatible_bus)}）`
}

/**
 * 资源表里已绑定值的**紧凑**文案。
 *
 * 表格的「DMA 绑定」列只有 ~138px，用完整选项文案（名字 + id + 兼容总线）
 * 必然截断成 "GDMA_CH0（#…"，反而看不出绑的是哪条。表内只需要回答
 * "绑了哪一条"，兼容总线在当前 TAB 下是冗余信息（下拉选项里仍给全）。
 */
function dmaBoundValueLabel(dma: DmaChannelInfo | undefined): string {
  if (!dma) return '不使用 DMA'
  // 单元格仅 ~126px（可用 ~102px）：连 "GDMA_CH0（#0）" 都会截断。
  // 表内只需回答"绑了哪一条"，通道名本身在节点内已唯一；dma_id 交给
  // title 提示与下拉选项（下拉里仍带 #id 与兼容总线）。
  return dma.name || 'DMA' + dma.dma_id
}

/**
 * B3：无可用 DMA 时**必须说明原因**，不能静默留空。
 * 掩码为 0（adc/gpio/pwm 无 DMA 位）与"有掩码但设备没上报兼容通道"是两种不同成因，
 * 混成一句"暂无"会让用户把"这条总线本来就不支持 DMA"读成"功能坏了"。
 */
function resourceDmaUnavailableText(): string {
  if (!busTypeMask(activeBusType.value)) return '该总线类型不支持 DMA'
  if ((dmaStore.mergedChannels as DmaChannelInfo[]).length === 0) return '该节点未上报 DMA 资源'
  return '该总线暂无可兼容的 DMA 资源'
}

/** B1：选择器选中后绑定/解绑。复用 dmaStore.toggle（含并发守卫与"必须指定 bindTo"防御）。 */
async function changeResourceDma(resource: BusResource, value: string) {
  if (nodeOffline.value) return
  const all = dmaStore.mergedChannels as DmaChannelInfo[]
  const target = value ? all.find(dma => String(dma.dma_id) === value) : undefined
  if (value && !target) {
    ElMessage.warning('所选 DMA 资源已不存在，请刷新后重试')
    return
  }
  if (!target) {
    // 选择「不使用 DMA」= 解绑当前绑定在本资源上的那条
    const bound = all.find(dma => isDmaBoundToResource(dma, resource))
    if (!bound) return
    await toggleResourceDma(resource, false)
    return
  }
  // 同一资源上只能有一条 DMA：先把旧的解绑，否则会留下两条都指向本资源
  const previous = all.find(dma => isDmaBoundToResource(dma, resource) && dma.dma_id !== target.dma_id)
  if (previous) await toggleResourceDma(resource, false)
  await toggleResourceDma(resource, true, target)
}

function canToggleResourceDma(resource: BusResource): boolean {
  if (resourceDmaBinding(resource)) return true
  return resourceDmaCandidates(resource).some(dma => isDmaRebindable(dma.state, dma.bound_to))
}

function selectBusType(type: BusType) {
  activeBusType.value = type
  busPage.value = 1
  scanResult.value = null
  selectedResourceId.value = activeBusResources.value[0]?.id || ''
}

// D4：三个选中入口（整行点击、资源名按钮、「查看」按钮）都只写 selectedResourceId，
// 右侧「资源详情」由 selectedResource 统一派生（资源名称/引脚/关键参数/已挂载通道/DMA 绑定）。
// 清掉只属于上一行的瞬态（I2C 地址扫描结果）放在这里做一次，避免三个入口各写一份、
// 漏掉任意一个就会把上一行的扫描结果挂到新资源上（张冠李戴）。
watch(selectedResourceId, () => { scanResult.value = null })

// ── 事件 ──
const recentEvents = computed(() => nodeEvents.value.slice(0, 5))
function eventText(ev: NodeEvent): string {
  if (ev.event_type === 'status') {
    return ev.new_status === 'online' ? '设备上线' : '设备离线'
  }
  // event_type 其他取值（如 hello/ota/config）做可读映射，避免英文原文泄漏
  const typeMap: Record<string, string> = {
    hello: '设备握手', ota: 'OTA 升级', config: '配置变更', offline: '设备离线', online: '设备上线',
  }
  const label = typeMap[ev.event_type] || ev.event_type
  // old/new_status 是英文枚举，仅在与 type 不同且能提供信息时追加，且翻译为中文
  const statusMap: Record<string, string> = { online: '在线', offline: '离线' }
  const newSt = statusMap[ev.new_status] || ev.new_status
  if (ev.event_type === 'offline' || ev.event_type === 'online') return label
  return newSt ? `${label}（${newSt}）` : label
}

// ── 数据加载 ──
async function fetchDetail() {
  const id = route.params.id as string
  if (!id) return
  loading.value = true
  const sequence = ++detailSequence
  try {
    const result = await nodeApi.getDetail(id)
    if (sequence !== detailSequence || route.params.id !== id) return
    node.value = result
    lastRefreshAt.value = Date.now()
    // 序列号就绪后拉通道/事件（channel/events 按 node_id 序列号过滤）
    void fetchChannels()
    void fetchEvents()
    void fetchDevices()
    if (activeTab.value === '总线配置') void fetchBusData()
    if (activeTab.value === 'OTA 历史') void fetchOTAHistory()
    // 在线且无延迟数据时自动测一次延迟
    if (result.status === 'online' && !result.ping_latency_ms && !result.latency_ms) {
      void handlePing()
    }
  } catch (err: any) {
    if (sequence === detailSequence) {
      node.value = null
      logger.error('获取节点详情失败', { error: String(err) })
    }
  } finally {
    if (sequence === detailSequence) loading.value = false
  }
}

async function fetchChannels() {
  const id = route.params.id as string
  const serial = nodeSerial.value
  if (!id || !serial) return
  const sequence = ++channelsSequence
  channelsLoading.value = true
  try {
    const res = await channelApi.getList(serial)
    if (sequence !== channelsSequence || route.params.id !== id) return
    channels.value = Array.isArray(res) ? res : (res.items || [])
  } catch (err: any) {
    if (sequence === channelsSequence) {
      channels.value = []
      logger.error('获取通道列表失败', { error: String(err) })
    }
  } finally {
    if (sequence === channelsSequence) channelsLoading.value = false
  }
}

async function fetchBusData() {
  const id = route.params.id as string
  const serial = nodeSerial.value
  if (!id || !serial) return
  const sequence = ++capabilitiesSequence
  busLoading.value = true
  busLoadError.value = false
  try {
    const reportedCapabilities = await nodeApi.getCapabilities(serial)
    if (sequence !== capabilitiesSequence || route.params.id !== id) return
    capabilities.value = reportedCapabilities || { buses: {} }
    busDataLoaded.value = true
    const resources = activeBusResources.value
    selectedResourceId.value = resources.some(resource => resource.id === selectedResourceId.value)
      ? selectedResourceId.value
      : (resources[0]?.id || '')
    busPage.value = Math.min(busPage.value, busTotalPages.value)
    // DMA 是增强信息；失败不可伪装成总线资源读取失败。
    void dmaStore.fetch(serial).catch(err => logger.warn('获取 DMA 通道失败', { error: String(err) }))
  } catch (err: any) {
    if (sequence !== capabilitiesSequence || route.params.id !== id) return
    capabilities.value = { buses: {} }
    busLoadError.value = true
    logger.error('获取节点总线能力失败', { error: String(err) })
  } finally {
    if (sequence === capabilitiesSequence) busLoading.value = false
  }
}

async function requestBusResourceRefresh() {
  if (nodeOffline.value || resourceQuerying.value) return
  const id = route.params.id as string
  const serial = nodeSerial.value
  const operation = componentOperationGeneration
  const sessionGeneration = getSessionGeneration()
  resourceQuerying.value = true
  try {
    await nodeApi.queryResources(serial)
    assertSessionGeneration(sessionGeneration)
    if (operation !== componentOperationGeneration || route.params.id !== id) return
    // ResourceReport 异步回写；等待与 ChannelPanel 相同的上报窗口，避免旧缓存冒充新值。
    await new Promise<void>(resolve => setTimeout(resolve, 2000))
    if (operation !== componentOperationGeneration || route.params.id !== id) return
    await fetchBusData()
    if (operation !== componentOperationGeneration || route.params.id !== id) return
    ElMessage.success('已读取设备最新资源上报')
  } catch (err: any) {
    if (operation !== componentOperationGeneration || route.params.id !== id) return
    feedback.handleError(err, '资源刷新请求失败')
  } finally {
    if (operation === componentOperationGeneration && route.params.id === id) resourceQuerying.value = false
  }
}

async function toggleResourceDma(resource: BusResource, enabled: boolean | string | number, explicit?: DmaChannelInfo) {
  if (nodeOffline.value) return
  const id = route.params.id as string
  const serial = nodeSerial.value
  const desired = Boolean(enabled)
  const bound = resourceDmaBinding(resource)
  // B1：显式指定的目标优先（用户在选择器里选的那条），
  // 否则退回"已绑定那条 / 第一条可重绑定的候选"——即改前开关的隐式行为，保持向后兼容。
  const candidate = explicit || bound || resourceDmaCandidates(resource).find(dma => isDmaRebindable(dma.state, dma.bound_to))
  if (!candidate) {
    ElMessage.warning('没有可用于此资源的 DMA 通道')
    return
  }
  const operation = componentOperationGeneration
  const sessionGeneration = getSessionGeneration()
  try {
    await dmaStore.toggle(serial, candidate, desired, desired ? resourceBindingKey(resource) : '')
    assertSessionGeneration(sessionGeneration)
    if (operation !== componentOperationGeneration || route.params.id !== id) return
    ElMessage.success(desired ? 'DMA 已绑定' : 'DMA 已解绑')
  } catch (err: any) {
    if (operation !== componentOperationGeneration || route.params.id !== id) return
    feedback.handleError(err, 'DMA 配置保存失败')
  }
}

async function scanSelectedI2C() {
  if (nodeOffline.value || activeBusType.value !== 'i2c' || !selectedResource.value) return
  const id = route.params.id as string
  const serial = nodeSerial.value
  const operation = componentOperationGeneration
  const sessionGeneration = getSessionGeneration()
  i2cScanning.value = true
  scanResult.value = null
  try {
    const result = await nodeApi.scanI2C(serial, selectedResource.value.id)
    assertSessionGeneration(sessionGeneration)
    if (operation !== componentOperationGeneration || route.params.id !== id) return
    scanResult.value = Array.isArray(result?.devices) ? result.devices : []
    if (scanResult.value.length) ElMessage.success(`发现 ${scanResult.value.length} 个设备`)
    else ElMessage.info('设备未返回可发现的 I2C 地址')
  } catch (err: any) {
    if (operation !== componentOperationGeneration || route.params.id !== id) return
    feedback.handleError(err, '地址扫描失败')
  } finally {
    if (operation === componentOperationGeneration && route.params.id === id) i2cScanning.value = false
  }
}

function closeBaudTool() {
  baudToolVisible.value = false
  baudToolError.value = ''
  baudSubmitting.value = false
}

// ── 内联波特率编辑（通道行内直接改，不走弹窗）─────────────────────────────

/** 打开某一行的内联编辑；复用了与弹窗完全相同的"当前值"读取口径。 */
function openBaudInline(ch: Channel) {
  if (nodeOffline.value) {
    ElMessage.warning('节点离线，无法修改波特率')
    return
  }
  if (!isUARTChannel(ch)) {
    ElMessage.warning('仅 UART 通道支持修改波特率')
    return
  }
  baudInlineId.value = String(ch.id)
  const current = baudCurrentRate(ch)
  baudInlineValue.value = current ?? 9600
  baudInlineError.value = ''
}

function cancelBaudInline() {
  baudInlineId.value = ''
  baudInlineError.value = ''
  baudInlineSubmitting.value = false
}

/**
 * 提交单条通道的波特率。成功/未改动/失败都**逐条如实**反馈，
 * 不复用批量提交的措辞（那会说"共 N 条"，在单条场景下是误导）。
 */
async function submitBaudInline(ch: Channel) {
  if (baudInlineSubmitting.value) return
  const baud = Number(baudInlineValue.value)
  if (!validateBaudrate(baud)) {
    baudInlineError.value = '请选择标准波特率'
    return
  }
  baudInlineSubmitting.value = true
  baudInlineError.value = ''
  const serial = nodeSerial.value
  try {
    const res = await channelApi.reconfigure(Number(ch.id), baud)
    if (serial !== nodeSerial.value) return
    await fetchChannels()
    if (res?.status === 'unchanged') ElMessage.info('目标波特率与当前一致，未做改动')
    else ElMessage.success(`已下发波特率 ${baud} bit/s`)
    cancelBaudInline()
  } catch (err: any) {
    if (serial !== nodeSerial.value) return
    // 就地显示错误，不关编辑态 —— 用户可以直接改一个值重试，不用重新打开。
    baudInlineError.value = err?.message || '下发失败'
  } finally {
    if (serial === nodeSerial.value) baudInlineSubmitting.value = false
  }
}

/** 弹窗入口（按资源批量改）。保留原语义，仅重命名以区别于内联入口。 */
function openBaudToolInline() {
  baudPickedChannel.value = null
  openBaudTool()
}

// ── 波特率重配置：目标选择与提交 ──────────────────────────────────────────

/** 该通道是否是 UART。后端 hardware_type 实测为大写，故必须归一后比较。 */
function isUARTChannel(ch: Channel): boolean {
  return String(ch.hardware_type || '').toUpperCase() === 'UART'
}

/** 上方资源表里当前选中的资源（仅 UART 有意义）。 */
const baudSelectedResource = computed<BusResource | null>(() => (
  activeBusType.value === 'uart' ? selectedResource.value : null
))

/** 本次是否有明确目标（选中通道 或 选中 UART 资源）。布尔，避免 'none' 这类真值字符串。 */
const baudToolSelection = computed(() => Boolean(baudPickedChannel.value || baudSelectedResource.value))

/** 该资源上的 UART 通道（口径与资源表「已挂载通道」列一致）。 */
const baudScopeChannels = computed<Channel[]>(() => {
  if (baudPickedChannel.value) return [baudPickedChannel.value]
  const resource = baudSelectedResource.value
  if (!resource) return []
  return channels.value.filter(ch => (
    isUARTChannel(ch)
    && hardwareResourceMatchesChannel({ ...resource, id: String(resource.id) }, ch)
  ))
})
const baudScopeCount = computed(() => baudScopeChannels.value.length)

/**
 * 入口可用性：节点离线或没有目标时**禁用**按钮，并说明原因，而不是让用户提交后吃 400。
 * 这里只做前端可知的判断（节点状态 + 选中通道/资源）；通道归属仍由后端独立校验
 * —— 前端不重复实现它，两套规则迟早分叉。
 */
const baudToolAvailable = computed(() => !nodeOffline.value && baudToolSelection.value)
const baudToolSelectionText = computed(() => {
  if (baudPickedChannel.value) return '已选通道：' + channelName(baudPickedChannel.value)
  if (baudSelectedResource.value) return '已选资源：' + baudSelectedResource.value.id + '（' + baudScopeCount.value + ' 条 UART 通道）'
  return '未选中通道：请先在上方资源表选一条 UART 资源，或在通道行点「改波特率」'
})

const baudToolTargetText = computed(() => {
  const picked = baudPickedChannel.value
  if (!picked) return ''
  const current = baudCurrentRate(picked)
  return '目标通道「' + channelName(picked) + '」（资源 ' + channelResourceLabel(picked) + '）'
    + (current === null ? '' : '，当前 ' + current + ' bit/s')
})

/**
 * 该通道 bus_config 里当前生效的波特率（字节 2..5，big-endian）；解不出返回 null。
 * 布局与 withUARTBaudrate（backend/internal/api/channel_reconfigure.go）逐字节一致；
 * 本函数只**读**它，写入永远走服务端，避免前端自造一份 hex 布局形成第二真相。
 */
function baudCurrentRate(ch: Channel): number | null {
  const raw = String((ch as any).bus_config || '').trim().replace(/^\\x/i, '')
  if (!/^[0-9a-f]+$/i.test(raw) || raw.length < 12) return null
  const value = Number.parseInt(raw.slice(4, 12), 16)
  return Number.isFinite(value) && value > 0 ? value : null
}

/** 能力驱动上限；无能力数据时回落到与「创建通道」兜底一致的 921600。 */
const baudMax = computed(() => {
  const picked = baudPickedChannel.value
  const capsMax = picked ? resourceCapsFor(picked)?.baud_rate_max : null
  const resourceMax = (baudSelectedResource.value as any)?.max_baud
  const raw = Number(capsMax ?? resourceMax)
  return Number.isFinite(raw) && raw > 0 ? raw : 921600
})

/** 从能力数据里取该通道所属资源的能力项（找不到返回 null，不做推测）。 */
function resourceCapsFor(ch: Channel): any | null {
  const resources = (capabilities.value?.buses?.uart || []) as any[]
  return resources.find(r => hardwareResourceMatchesChannel({ ...r, id: String(r.id) }, ch)) || null
}

function openBaudTool() {
  const targets = baudScopeChannels.value
  if (targets.length) {
    const first = baudCurrentRate(targets[0])
    const allSame = targets.every(ch => baudCurrentRate(ch) === first)
    // 预填「当前值」而不是某个常量：常量预填会让用户一确认就把一批通道改成 9600。
    baudTarget.value = allSame && first !== null ? first : 9600
  }
  baudToolError.value = ''
  baudToolVisible.value = true
}

/** 提交：逐个通道调用真实重配置端点，逐条如实汇报成功/未改动/失败。 */
async function confirmBaudReconfigure() {
  if (baudSubmitting.value) return
  const baud = Number(baudTarget.value)
  if (!validateBaudrate(baud)) {
    baudToolError.value = '请选择标准波特率（300/600/1200/2400/4800/9600/19200/38400/57600/115200）'
    return
  }
  const targets = baudScopeChannels.value
  if (!targets.length) {
    baudToolError.value = '没有可修改的 UART 通道：请先在资源表选一条 UART 资源，或在通道行点「改波特率」'
    return
  }
  baudSubmitting.value = true
  baudToolError.value = ''
  const serial = nodeSerial.value
  let changed = 0
  let unchanged = 0
  const failures: string[] = []
  for (const ch of targets) {
    try {
      const res = await channelApi.reconfigure(Number(ch.id), baud)
      if (res?.status === 'unchanged') unchanged += 1
      else changed += 1
    } catch (err: any) {
      failures.push(channelName(ch) + '：' + (err?.message || '下发失败'))
    }
  }
  // 提交期间节点可能已切换/组件已卸载 ⇒ 不再改 UI、也不再提示（避免切页后弹上一次的结果）。
  if (serial !== nodeSerial.value) return
  baudSubmitting.value = false
  if (failures.length) {
    baudToolError.value = failures.join('；')
    if (changed || unchanged) {
      ElMessage.warning('部分通道已提交：成功 ' + changed + ' 条、未改动 ' + unchanged + ' 条、失败 ' + failures.length + ' 条')
      await fetchChannels()
    }
    return
  }
  closeBaudTool()
  if (changed === 0) ElMessage.info('目标波特率与当前一致，未做改动')
  else if (unchanged === 0) ElMessage.success('已提交波特率修改并下发（' + changed + ' 条通道）')
  else ElMessage.success('已提交波特率修改：' + changed + ' 条已下发，' + unchanged + ' 条与当前一致')
  await fetchChannels()
}

function openChannelManager(resource: BusResource) {
  if (nodeOffline.value || resource.enabled === false || !busSupportsChannels.value) return
  selectedResourceId.value = resource.id
  // 走「新建通道」路径：必须清掉上一轮「编辑」留下的 initial-data，
  // 否则对话框会以编辑态打开并复用旧的通道数据。
  channelManagerInitialData.value = null
  channelManagerVisible.value = true
}

function handleChannelManagerRefresh() {
  void fetchChannels()
  void fetchBusData()
}

async function fetchEvents() {
  const id = route.params.id as string
  if (!id) return
  const sequence = ++eventsSequence
  eventsLoading.value = true
  try {
    const res = await client.get<unknown, { data: NodeEvent[] }>(`/api/v1/nodes/${id}/status-history`, { params: { limit: 50 } })
    if (sequence !== eventsSequence || route.params.id !== id) return
    nodeEvents.value = Array.isArray(res?.data) ? res.data : []
  } catch (err: any) {
    if (sequence === eventsSequence) {
      nodeEvents.value = []
      logger.error('获取节点事件失败', { error: String(err) })
    }
  } finally {
    if (sequence === eventsSequence) eventsLoading.value = false
  }
}

// ── 关联设备 ──
async function fetchDevices() {
  const id = route.params.id as string
  const serial = nodeSerial.value
  if (!id || !serial) return
  const sequence = ++devicesSequence
  devicesLoading.value = true
  try {
    const params = { node_id: serial, page: 1, page_size: 100 }
    await edgeDeviceStore.fetchList(params, true)
    if (sequence !== devicesSequence || route.params.id !== id) return
    devices.value = edgeDeviceStore.getCachedList(params)?.items || []
  } catch (err: any) {
    if (sequence === devicesSequence) {
      devices.value = []
      logger.error('获取关联设备失败', { error: String(err) })
    }
  } finally {
    if (sequence === devicesSequence) devicesLoading.value = false
  }
}

function handleDeviceCreated() {
  edgeDeviceStore.invalidateLists()
  void fetchDevices()
}

// ── OTA 历史 ──
async function fetchOTAHistory() {
  const id = route.params.id as string
  if (!id) return
  const sequence = ++otaSequence
  otaHistoryLoading.value = true
  try {
    const history = (await nodeApi.getOTAHistory(id)) || []
    if (sequence !== otaSequence || route.params.id !== id) return
    otaHistory.value = history
  } catch (err: any) {
    if (sequence === otaSequence) {
      otaHistory.value = []
      logger.error('获取 OTA 历史失败', { error: String(err) })
    }
  } finally {
    if (sequence === otaSequence) otaHistoryLoading.value = false
  }
}

async function handleCancelOTA(record: OTARecord) {
  if (nodeOffline.value) return
  const id = route.params.id as string
  const operation = componentOperationGeneration
  const sessionGeneration = getSessionGeneration()
  // 取消 OTA 会中断设备侧升级流程：按破坏性动作处理（danger 确认按钮 + 焦点不落在破坏性按钮上）。
  const confirmed = await feedback.confirmDanger('确定要取消此 OTA 升级吗？', {
    title: '提示',
    confirmText: '确定',
    cancelText: '取消',
  })
  if (!confirmed) return
  if (operation !== componentOperationGeneration || route.params.id !== id) return

  try {
    await nodeApi.cancelOTA(id, record.id)
    assertSessionGeneration(sessionGeneration)
    if (operation !== componentOperationGeneration || route.params.id !== id) return
    ElMessage.success('已取消 OTA 升级')
    void fetchOTAHistory()
  } catch {
    if (operation !== componentOperationGeneration || route.params.id !== id) return
    feedback.error('取消 OTA 失败')
  }
}

async function refreshAll() {
  refreshing.value = true
  try {
    await fetchDetail()
    if (activeTab.value === '总线配置') await fetchBusData()
    lastRefreshAt.value = Date.now()
  } finally {
    refreshing.value = false
  }
}

// ── 页头操作 ──
async function handleSyncConfig() {
  const id = route.params.id as string
  if (!id || nodeOffline.value) return
  syncing.value = true
  const sequence = detailSequence
  const sessionGeneration = getSessionGeneration()
  try {
    await nodeApi.syncConfig(id)
    assertSessionGeneration(sessionGeneration)
    if (sequence !== detailSequence || route.params.id !== id) return
    ElMessage.success('配置同步已触发')
  } catch (err: any) {
    if (sequence !== detailSequence || route.params.id !== id) return
    feedback.handleError(err, '配置同步失败')
  } finally {
    if (sequence === detailSequence && route.params.id === id) syncing.value = false
  }
}

async function handlePing() {
  if (!node.value || nodeOffline.value) return
  const id = route.params.id as string
  const serial = nodeSerial.value
  const sessionGeneration = getSessionGeneration()
  const operation = componentOperationGeneration
  pinging.value = true
  try {
    await nodeApi.ping(serial)
    assertSessionGeneration(sessionGeneration)
    if (operation !== componentOperationGeneration || route.params.id !== id) return
    // 等待 ping_result WS 事件；5s 超时兜底
    pendingPingTimeout.value = setTimeout(() => {
      pinging.value = false
      ElMessage.warning('延迟测量超时，节点可能离线')
    }, 5000)
  } catch (err: any) {
    if (operation !== componentOperationGeneration || route.params.id !== id) return
    pinging.value = false
    feedback.handleError(err, '发送 Ping 失败')
  }
}

function copyId() {
  const id = nodeSerial.value
  navigator.clipboard?.writeText(id).then(() => ElMessage.success('设备 ID 已复制')).catch(() => ElMessage.info(id))
}

async function saveRename() {
  const v = renameDraft.value.trim()
  if (!v) { feedback.error('名称不能为空'); return }
  const id = route.params.id as string
  renameSaving.value = true
  const sessionGeneration = getSessionGeneration()
  try {
    await nodeApi.update(id, { name: v })
    assertSessionGeneration(sessionGeneration)
    if (route.params.id !== id) return
    if (node.value) node.value = { ...node.value, name: v }
    renameVisible.value = false
    ElMessage.success('设备名称已更新')
  } catch (err: any) {
    // 同文件上方 DMA 保存已写明对象（「DMA 配置保存失败」），此处也应写明是重命名。
    feedback.handleErrorWithContext(err, '重命名设备失败')
  } finally {
    renameSaving.value = false
  }
}

function handleOTASuccess() {
  ElMessage.success('OTA 升级已完成')
  showOTADialog.value = false
  void fetchDetail()
}

// ── 导航 ──
function goBack() { router.push('/node') }

/**
 * D1 修复：进入「该节点的通道视图」。
 *
 * 改前是 goToDetail() { router.push(`/node/${nodeSerial.value}`) } —— 它推的是**当前页**
 * （本页路由就是 /node/:id，见 router/index.ts 的 NodeDetail/NodeOverview 两条），
 * 于是点通道行、点「查看全部」都等于原地打转，URL 不变（实测 urlAfter === urlBefore）。
 * 更糟的是既有测试只断言「mockRouterPush 被调用过」⇒ 永远绿，掩盖了这一点。
 *
 * 目标必须是**已存在且可达**的页面：产品里通道的独立页是 /channel（通道管理，
 * 支持节点过滤 + 每行「编辑通道」入口，可编辑通道名称/参数）。因此这里跳
 * /channel 并带上 node 过滤，用户点一行即可看到该节点的全部通道。
 * 用 { name, query } 对象形态而非拼字符串：参数不会被手工拼接漏编码。
 */
function editChannel(channel: Channel) {
  if (nodeOffline.value) {
    ElMessage.warning('节点离线，无法编辑通道')
    return
  }
  channelManagerVisible.value = true
  // 通道配置面板复用生产组件 ChannelManager，编辑数据由 initial-data 传入。
  channelManagerInitialData.value = channel
}

/**
 * 删除通道（C5 能力补线）。
 *
 * 为什么需要确认：后端 `handler_device.go:894` 是**硬删**（`tx.Delete(&ch)`），
 * 并向设备下发 `CfgChangeChannel/CfgActionDelete` 配置变更 ⇒ 不可逆。
 *
 * 为什么 409 要单独讲清楚：后端在**有边缘设备引用该通道时拒绝删除**
 * （`cannot delete channel referenced by edge devices` → 409）。
 * 这是**保护**而不是故障 —— 若按普通错误报红，用户只会看到 "删除失败"，
 * 却不知道原因是"先去把挂在这条通道上的设备挪走"。
 */
async function deleteChannel(channel: Channel) {
  if (nodeOffline.value) {
    ElMessage.warning('节点离线，无法删除通道')
    return
  }
  const name = channelName(channel)
  const channelId = channel.id
  if (typeof channelId !== 'number') {
    ElMessage.warning('该通道缺少 ID，无法删除')
    return
  }
  const ok = await feedback.confirmDanger(
    `删除通道「${name}」？该通道配置将被删除，并向节点下发配置变更（不可恢复）。`,
    { title: '确认删除通道', confirmText: '删除', cancelText: '取消' },
  )
  if (!ok) return
  try {
    await channelApi.delete(channelId)
    ElMessage.success(`通道「${name}」已删除`)
    await fetchChannels()
  } catch (error: unknown) {
    const msg = (error as { message?: string })?.message || ''
    if (/referenced by edge devices|Conflict|409/i.test(msg)) {
      // 如实说明原因 + 下一步（不是故障，是保护）
      ElMessage.warning(`通道「${name}」仍有边缘设备引用，无法删除：请先移除或改绑该通道下的设备。`)
      return
    }
    feedback.handleErrorWithContext(error, '删除通道失败')
  }
}

function navigateToNodeChannels() {
  const serial = nodeSerial.value
  if (!serial) { router.push({ name: 'ChannelList' }); return }
  router.push({ name: 'ChannelList', query: { node: serial } })
}

// 说明：旧的函数名 goToDetail（推当前页）已删除。它既是缺陷本体，也无法被任何
// 合法目标复用 —— 保留一个"推当前页"的别名只会给回退留后门，故不再提供任何别名。

// ── 弹窗初始化 ──
watch(renameVisible, v => { if (v) renameDraft.value = node.value?.name || '' })
watch(channelManagerVisible, v => { if (!v) channelManagerInitialData.value = null })

// ── 路由切换重置 ──
watch(() => route.params.id, () => {
  detailSequence++
  channelsSequence++
  eventsSequence++
  capabilitiesSequence++
  devicesSequence++
  otaSequence++
  componentOperationGeneration++
  node.value = null
  channels.value = []
  nodeEvents.value = []
  devices.value = []
  otaHistory.value = []
  capabilities.value = { buses: {} }
  busDataLoaded.value = false
  selectedResourceId.value = ''
  scanResult.value = null
  dmaStore.clearCache()
  pinging.value = false
  syncing.value = false
  periphGeneration++
  pendingPeriphRequests.clear()
  if (pendingPingTimeout.value) { clearTimeout(pendingPingTimeout.value); pendingPingTimeout.value = null }
  void fetchDetail()
})

// ── 外设直控：写后状态回填 ──
//
// 语义与 components/node/ChannelPanel.vue 完全一致（同一套 pending + 代际校验），
// 差异只在于回填入口改为本页持有的 PeripheralControl 引用。
// periph_generation 在节点切换/卸载时自增，使在途请求的 ACK 失效 —— 否则旧节点的
// 响应会写进新节点的行控件（跨节点状态串台）。
const peripheralControlRef = ref<InstanceType<typeof PeripheralControl> | null>(null)
let periphGeneration = 0
const pendingPeriphRequests = new Map<number, { generation: number; type: number; resource: string; action: number }>()

/** 登记一次外设写/读请求，返回 true 表示「已登记，将由 WS 回填」。 */
const registerPendingPeripheral = (payload: { requestId: number; pin: number; action: number }): boolean => {
  pendingPeriphRequests.set(payload.requestId, { generation: periphGeneration, type: 1, resource: String(payload.pin), action: payload.action })
  return true
}
const registerPendingPWM = (payload: { requestId: number; hardwareId: string; action: number }): boolean => {
  pendingPeriphRequests.set(payload.requestId, { generation: periphGeneration, type: 2, resource: payload.hardwareId, action: payload.action })
  return true
}

/**
 * 按 ChannelPanel.tryApplyPeriphResult 的判据回填：三重比对全部通过才消费该 request_id。
 * 比对项：代际（防跨节点）、periph_type + action（防类型/动作错配）、资源身份（防同批多请求串台）。
 */
const onPeriphResult = (message: WebSocketMessage) => {
  const payload = message.payload as {
    node_id?: string; request_id?: number; success?: boolean; periph_type?: number
    hardware_id?: string; pin?: number; value?: number; running?: boolean; action?: number
  } | undefined
  if (!payload || typeof payload.request_id !== 'number') return
  const pending = pendingPeriphRequests.get(payload.request_id)
  if (!pending || pending.generation !== periphGeneration) return
  if (pending.type !== payload.periph_type || pending.action !== payload.action) return
  const responseResource = payload.periph_type === 1 ? String(payload.pin) : payload.hardware_id
  if (pending.resource !== responseResource) return
  pendingPeriphRequests.delete(payload.request_id)

  // GPIO（type=1）：读(2)/写(0,1,5) 回填电平；失败时回填 null 由行控件显示「设备操作失败 · 重试」。
  if (payload.periph_type === 1 && typeof payload.pin === 'number' && [0, 1, 2, 5].includes(payload.action ?? -1)) {
    peripheralControlRef.value?.applyRuntimeLevel(payload.pin, payload.success ? payload.value ?? null : null)
    return
  }
  // PWM（type=2）：成功回填 running/duty；失败时 running=null 表示状态未知。
  if (payload.periph_type === 2 && payload.hardware_id) {
    if (payload.success) {
      const duty = (payload.action === 0 || payload.action === 4) ? payload.value : undefined
      peripheralControlRef.value?.applyRuntimeState(payload.hardware_id, payload.running ?? null, duty)
    } else {
      const duty = payload.action === 0 ? payload.value : undefined
      peripheralControlRef.value?.applyRuntimeState(payload.hardware_id, null, duty)
      if (payload.action === 1) peripheralControlRef.value?.reload()
    }
  }
}

/**
 * GPIO/PWM 的「配置 / 编辑」联动。
 *
 * **本函数不再弹提示**（历史：它曾弹「配置编辑入口尚未接线」）。
 * 原因：`PeripheralControl.vue` 现已自带 `PeripheralConfigDialog`，点击
 * 「配置」/「编辑」会真正打开表单 —— 若本函数仍弹"尚未接线"，用户会**先看到对话框、
 * 再看到一句说它不存在的提示**，自相矛盾。
 *
 * 保留该 emit 链路的意义：父组件需要有"用户正在配置外设"的信号位，
 * 将来若要在配置前后做额外联动（如暂停轮询、埋点、权限校验）由此接入。
 *
 * ⚠️ 仍**不要**切到「总线配置」TAB（这是 E1 第一版的错误）：
 * 该 TAB 对 gpio/pwm 有 `busSupportsChannels=false`（本文件 :1030 只含 uart/i2c/spi/adc），
 * 主按钮显示「此资源不支持通道」且 disabled，全文件对 gpioApi/pwmApi 零调用。
 */
function onPeripheralConfigure(_resourceName: string) {
  // 对话框由 PeripheralControl 自行打开（见其 @configure/@edit 处理）。
  // 此处刻意不弹消息：配置动作本身已有明确视觉反馈。
}

// ── 生命周期 ──
onMounted(() => {
  void fetchDetail()
  // 探测服务端是否具备节点级设备操作能力；只影响两个按钮显不显示。
  void loadDeviceOpSupport()
  sessionTimer = setInterval(() => { nowTick.value = Date.now() }, 1000)

  // 节点状态更新（node_status 无延迟字段，仅状态/uptime）
  const unsubStatus = wsStore.subscribe(WS_EVENT.NODE_STATUS, (message: WebSocketMessage) => {
    if (message.payload?.node_id !== nodeSerial.value) return
    if (node.value && message.payload?.status) {
      node.value = {
        ...node.value,
        status: message.payload.status === 'online' ? 'online' : 'offline',
        uptime_seconds: message.payload.uptime_seconds ?? node.value.uptime_seconds,
      }
    }
  })

  // 延迟结果经 ping_result 事件到达
  const unsubPing = wsStore.subscribe(WS_EVENT.PING_RESULT, (message: WebSocketMessage) => {
    if (message.payload?.node_id !== nodeSerial.value) return
    if (message.payload?.latency_ms !== undefined) {
      if (node.value) node.value = { ...node.value, latency_ms: message.payload.latency_ms }
      pinging.value = false
      if (pendingPingTimeout.value) { clearTimeout(pendingPingTimeout.value); pendingPingTimeout.value = null }
      ElMessage.success(`延迟: ${message.payload.latency_ms} ms`)
    }
  })

  // 外设写后回填：GPIO/PWM 直控的 ACK 经 periph_result 到达（PeriphRsp 0x1C）。
  // 与 ChannelPanel 同一套语义：先由 registerPending* 登记 request_id，收到后按
  // request_id + 代际 + 资源身份三重比对才回填，避免旧节点的 ACK 打到新节点的 UI。
  const unsubPeriph = wsStore.subscribe(WS_EVENT.PERIPH_RESULT, onPeriphResult)

  unsubscribe = () => { unsubStatus(); unsubPing(); unsubPeriph() }
})

onUnmounted(() => {
  componentOperationGeneration++
  detailSequence++
  channelsSequence++
  eventsSequence++
  capabilitiesSequence++
  devicesSequence++
  otaSequence++
  periphGeneration++
  // 选中态只在「用户刚点了某一行」的上下文里有意义；留在组件外会让下次挂载
  // 带着上一次的通道/弹窗打开状态。
  baudPickedChannel.value = null
  baudToolVisible.value = false
  baudToolError.value = ''
  baudSubmitting.value = false
  pendingPeriphRequests.clear()
  if (unsubscribe) unsubscribe()
  if (pendingPingTimeout.value) clearTimeout(pendingPingTimeout.value)
  if (sessionTimer) clearInterval(sessionTimer)
  dmaStore.clearCache()
})
</script>

<style scoped>
/* ── 页面级 CSS token：设计稿色板，不污染全局 theme.css ── */
.node-overview-page {
  --no-primary: #2E6BFF;
  --no-primary-hover: #1F56E0;
  --no-success: #22C55E;
  --no-success-text: #16A34A;
  --no-success-bg: #E8F9EF;
  --no-warning: #F59E0B;
  --no-warning-text: #D97706;
  --no-warning-bg: #FEF3DE;
  --no-danger: #EF4444;
  --no-text: #1F2329;
  /* 可读的三层文字：正文、字段/说明、仅作占位或禁用提示的弱文本。 */
  --no-text-secondary: #526072;
  --no-text-muted: #69778B;
  --no-text-faint: #A7B1BF;
  --no-border: #E8EBF0;
  --no-border-light: #F0F2F5;
  --no-bg-page: #F5F7FA;
  --no-bg-hover: #F7FAFF;
  --no-bg-active: #EBF2FF;
  /* 表头/分组底的"极浅面"，用于在白卡片上分出层次（--no-border-light 太淡，
     单靠它画不出结构）。必须与 --no-bg-page 区分：后者是整页底色，不是面。 */
  --no-bg-subtle: #F7F8FA;
  --no-chip-off-bg: #F2F4F7;
  /* F32：紫色强调色（空闲堆内存图标）。设计稿原值是内联硬编码 #8B5CF6，
     它没有对应的全局语义 token（theme.css 里没有紫色语义），故此处置为**页面级 token**，
     与其余 --no-* 同族；并补暗色覆盖，使其真正随主题变化（修复前亮暗计算色完全相同）。 */
  --no-accent: #8B5CF6;
  --no-accent-bg: rgba(139, 92, 246, 0.1);
  /* I-4 残留清零：三处此前直接写死的色值收进页面级 token（与 --no-* 同族），
     并补暗色覆盖 —— 它们原本在暗色下**仍取亮色值**，属于真缺陷而非风格问题。 */
  --no-dot-off: #C9D2DE; /* 离线圆点/通道箭头（原 #C9D2DE 写死两处） */
  --no-warning-border: #FCD98C; /* 总线告警条描边（原 #FCD98C 写死） */
  color: var(--no-text);
  font-size: 13px;
  line-height: 20px;
}
html.dark .node-overview-page {
  --no-primary: #4D7FFF;
  --no-primary-hover: #6B93FF;
  --no-text: var(--text-color-primary, #E5EAF3);
  --no-text-secondary: var(--text-color-regular, #C0C6D0);
  --no-text-muted: var(--text-color-secondary, #8A93A3);
  --no-border: var(--border-color, #3A4150);
  --no-border-light: var(--border-color-lighter, #2E3442);
  --no-bg-hover: rgba(77, 127, 255, 0.12);
  --no-bg-active: rgba(77, 127, 255, 0.18);
  --no-bg-subtle: rgba(255, 255, 255, 0.04);
  --no-success-bg: rgba(34, 197, 94, 0.15);
  --no-warning-bg: rgba(245, 158, 11, 0.15);
  --no-chip-off-bg: rgba(255, 255, 255, 0.06);
  /* F8：补齐此前只在亮色定义的 6 个 token。语义色改为引用 theme.css 的暗色语义 token，
     亮色块保持不变；-text 变体落在各自 tinted 底上，同样引用主题 token。 */
  --no-danger: var(--color-danger, #F78989);
  --no-success: var(--color-success, #85CE61);
  --no-warning: var(--color-warning, #EBB563);
  --no-success-text: var(--color-success, #85CE61);
  --no-warning-text: var(--color-warning, #EBB563);
  /* F8 层级修正：原值 #A7B1BF 在卡片底上实测 6.70，比 muted(#A3A6AD, 5.96) 还亮 → 层级反转。
     #7D8694 实测 3.95：明显弱于 muted，且仍高于 3.0 的禁用态下限。 */
  --no-text-faint: #7D8694;
  /* F8：零使用，但属设计稿色板；补暗色覆盖，使其一旦被用即为正确的暗色页面底色。 */
  --no-bg-page: var(--bg-color-page, #0D0D0D);
  /* F32：紫色强调色的暗色档。深色底上原 #8B5CF6 偏暗，提亮一档保持可读性，
     底纹同步提高不透明度（与既有 --no-success-bg/--no-warning-bg 的暗色处理一致）。 */
  --no-accent: #A78BFA;
  --no-accent-bg: rgba(167, 139, 250, 0.16);
  /* I-4：上面两个亮色 token 的暗色档。离线圆点/描边在深色底上需提亮才可见。 */
  --no-dot-off: #4A5568;
  --no-warning-border: rgba(245, 158, 11, 0.45);
}

.no-breadcrumb { margin-bottom: 12px; }
.no-loading { padding: 24px; background: var(--card-bg, #fff); border-radius: 8px; }

/* 卡片基座 */
.card {
  background: var(--card-bg, #fff);
  border-radius: 8px;
  box-shadow: 0 1px 2px rgba(16, 24, 40, .04), 0 1px 3px rgba(16, 24, 40, .06);
}
.dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; }
.dot-green { background: var(--no-success); }
.dot-gray { background: var(--no-dot-off); }

/* ── 页头 ── */
.page-header { display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 16px; gap: 16px; }
.ph-title-row { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.ph-title { font-size: 20px; font-weight: 600; margin: 0; line-height: 28px; }
/* 图标动作入口：<button> 承载（自带焦点与 Enter/Space）。
   UA 复位是必需的：全站没有裸 button 复位（theme.css 无该选择器，Element Plus 只覆盖
   .el-button），少了它铅笔会被套上浏览器默认的灰色描边盒子 —— 与改前的纯图标视觉不符。 */
.ph-edit { display: inline-flex; align-items: center; border: 0; background: transparent; padding: 0; font: inherit; color: var(--no-text-muted); cursor: pointer; }
.ph-edit:hover { color: var(--no-primary); }
.ph-edit:focus-visible { outline: 2px solid var(--no-primary); outline-offset: 2px; border-radius: 4px; }
.badge { font-size: 12px; padding: 2px 10px; border-radius: 11px; display: inline-flex; align-items: center; gap: 5px; }
.badge-green { background: var(--no-success-bg); color: var(--no-success-text); }
.badge-gray { background: var(--no-chip-off-bg); color: var(--no-text-muted); }
.badge .dot { width: 6px; height: 6px; }
.quality { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; color: var(--no-text-secondary); }
.q-val, .q-text { font-weight: 600; }
.ph-id { margin-top: 8px; font-size: 12px; color: var(--no-text-muted); display: flex; align-items: center; gap: 6px; }
.copy-icon { display: inline-flex; align-items: center; border: 0; background: transparent; padding: 0; font: inherit; cursor: pointer; color: var(--no-text-muted); }
.copy-icon:hover { color: var(--no-primary); }
.ph-actions { display: flex; gap: 12px; flex-wrap: wrap; }

.btn {
  height: 36px; padding: 0 16px; border-radius: 6px; font-size: 13px; font-weight: 500; line-height: 20px; cursor: pointer;
  display: inline-flex; align-items: center; gap: 6px; border: 1px solid transparent; transition: all .15s;
  background: none; font-family: inherit;
}
.btn:disabled { opacity: .6; cursor: not-allowed; }
.btn-plain { background: var(--card-bg, #fff); border-color: var(--no-border); color: var(--no-text-secondary); }
.btn-plain:hover:not(:disabled) { color: var(--no-primary); border-color: var(--no-primary); }
/* 前景用 --text-on-fill：--no-primary 亮 #2E6BFF / 暗 #4D7FFF，
   白字在暗色下只有 3.62（低于 4.5），而它同时还要当**文字色**用（链接/选项卡），
   不能为了当底而调暗 ⇒ 按主题切换前景（亮白 4.50 / 暗深 4.80）。 */
.btn-primary { background: var(--no-primary); color: var(--text-on-fill); }
.btn-primary:hover:not(:disabled) { background: var(--no-primary-hover); }
.spin { animation: no-spin 1s linear infinite; }
@keyframes no-spin { to { transform: rotate(360deg); } }

/* 统计条 */
.stat-strip { display: flex; align-items: center; min-height: 88px; padding: 16px 24px; margin-bottom: 16px; }
.stat-item { flex: 1; display: flex; align-items: center; gap: 12px; min-width: 0; }
.stat-icon {
  width: 32px; height: 32px; border-radius: 6px; background: var(--no-bg-active); color: var(--no-primary);
  display: flex; align-items: center; justify-content: center; flex-shrink: 0;
}
.stat-text { min-width: 0; }
.stat-label { font-size: 12px; line-height: 18px; color: var(--no-text-secondary); }
.stat-value { font-size: 14px; font-weight: 500; line-height: 20px; margin-top: 4px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

/* Tab 栏 */
.tab-bar { display: flex; align-items: center; height: 48px; padding: 0 20px; gap: 24px; margin-bottom: 16px; border-bottom: 1px solid var(--no-border); border-radius: 8px 8px 0 0; }
.tab-item {
  display: flex; align-items: center; gap: 6px; height: 48px; padding: 0 4px;
  font-size: 14px; color: var(--no-text-secondary); cursor: pointer; position: relative;
}
.tab-item:hover { color: var(--no-primary); }
.tab-item:focus-visible { outline: 2px solid var(--no-primary); outline-offset: -2px; border-radius: 4px; }
.tab-item.active { color: var(--no-primary); font-weight: 500; }
.tab-item.active::after {
  content: ''; position: absolute; left: 0; right: 0; bottom: -1px; height: 2px; background: var(--no-primary);
}

/* DMA 通道卡片网格 */
.dma-card { padding: 16px 20px; }
.dma-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 12px; }
.dma-item {
  min-width: 0; border: 1px solid var(--no-border-light); border-radius: 6px; padding: 12px 14px;
  display: flex; flex-direction: column; gap: 6px; background: var(--card-bg, #fff);
}
.dma-item-head { min-height: 24px; display: flex; align-items: center; justify-content: space-between; gap: 8px; margin-bottom: 4px; }
.dma-item-head > span:first-child { color: var(--no-text); font-size: 14px; font-weight: 600; line-height: 20px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.dma-item-head .bus-tag { margin-right: 0; flex: 0 0 auto; }
.dma-item-row { display: flex; align-items: center; justify-content: space-between; gap: 12px; font-size: 13px; line-height: 20px; }
.dma-item-row span { color: var(--no-text-secondary); font-size: 12px; line-height: 18px; }
.dma-item-row b { color: var(--no-text); font-weight: 500; text-align: right; overflow-wrap: anywhere; }

/* 关联设备 */
.device-card .chan-sub { font-size: 12px; color: var(--no-text-muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.device-row { gap: 10px; }
.device-row .device-last-data { flex: 1 1 18%; min-width: 0; color: var(--no-text-secondary); }
.device-row .device-last-time { flex: 0 0 auto; color: var(--no-text-muted); }
.device-row .link-btn { flex: 0 0 auto; white-space: nowrap; }
.device-count { font-size: 13px; font-weight: 400; color: var(--no-text-muted); }

/* 列表/卡片切换（设计稿 segmented 文字切换） */
.view-switch { display: inline-flex; align-items: center; background: var(--no-chip-off-bg); border-radius: 6px; padding: 2px; gap: 2px; }
.view-switch-btn {
  height: 26px; padding: 0 14px; border: 0; border-radius: 5px; font-size: 12px; cursor: pointer;
  background: transparent; color: var(--no-text-secondary); font-family: inherit; transition: all .15s;
}
.view-switch-btn.active { background: var(--no-primary); color: var(--text-on-fill); font-weight: 500; }
.view-switch-btn:not(.active):hover { color: var(--no-primary); }

/* 卡片视图网格 */
.device-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 12px; }
.device-tile {
  min-width: 0; border: 1px solid var(--no-border-light); border-radius: 8px; padding: 14px 16px;
  display: flex; flex-direction: column; gap: 10px; background: var(--card-bg, #fff);
  cursor: pointer; transition: border-color .15s, box-shadow .15s;
}
.device-tile:hover { border-color: var(--no-primary); box-shadow: var(--no-shadow, 0 2px 8px rgba(16,24,40,.08)); }
.device-tile.is-offline { opacity: .72; }
.device-tile-head { display: flex; align-items: flex-start; gap: 10px; }
.device-tile-icon {
  width: 36px; height: 36px; border-radius: 8px; flex: 0 0 auto;
  background: var(--no-bg-active); color: var(--no-primary);
  display: flex; align-items: center; justify-content: center;
}
.device-tile-title { min-width: 0; flex: 1; display: flex; flex-direction: column; gap: 2px; }
.device-tile-name { font-size: 14px; font-weight: 600; color: var(--no-text); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.device-tile-type { font-size: 12px; color: var(--no-text-muted); }
.device-tile-body { display: flex; flex-direction: column; gap: 5px; border-top: 1px solid var(--no-border-light); padding-top: 10px; }
.device-tile-row { display: flex; align-items: center; justify-content: space-between; gap: 10px; font-size: 12px; }
.device-tile-row span { color: var(--no-text-muted); }
.device-tile-row b { color: var(--no-text); font-weight: 500; overflow-wrap: anywhere; text-align: right; }
.device-tile-reading { border-top: 1px solid var(--no-border-light); padding-top: 10px; display: flex; flex-direction: column; gap: 3px; }
.device-tile-reading .reading-label { font-size: 11px; color: var(--no-text-muted); }
.device-tile-reading .reading-value { font-size: 14px; font-weight: 600; color: var(--no-text); overflow-wrap: anywhere; }
.device-tile-reading .reading-time { font-size: 11px; color: var(--no-text-muted); }
.device-tile-reading.no-data .reading-none { font-size: 12px; color: var(--no-text-muted); }
.device-row .chan-name { min-width: 0; flex: 1 1 22%; }
.device-row .chan-sub { flex: 0 1 16%; min-width: 0; }
.device-row .chan-badge { flex: 0 0 auto; }
.device-card-actions { display: flex; align-items: center; gap: 8px; }

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

/* 系统日志 / 通道终端 */
.log-card, .terminal-card { overflow: hidden; }
.log-card .card-head, .terminal-card .card-head { border-bottom-color: var(--no-border-light); }
/* 内容区与 card-head 的 20px 水平对齐一致（card-head padding 16px 20px 12px） */
.log-card > :not(.card-head):not(.card-loading):not(.card-empty),
.terminal-card > :not(.card-head):not(.card-loading):not(.card-empty) {
  padding: 0 20px 16px;
}

/* 总线配置：双栏资源视图（对齐 designs/new-node-2.png） */
.bus-alert { min-height: 40px; display: flex; align-items: center; gap: 8px; padding: 0 14px; margin-bottom: 16px; color: var(--no-text-secondary); background: var(--no-warning-bg); border: 1px solid var(--no-warning-border); border-radius: 6px; font-size: 13px; }
.bus-alert > .el-icon { color: var(--no-warning); }
.bus-alert-refresh { margin-left: auto; flex: 0 0 auto; font-size: 12px; }
.bus-alert-offline { background: var(--no-chip-off-bg); border-color: var(--no-border); }
.bus-main-cols { display: flex; align-items: stretch; gap: 16px; }
.bus-col-left { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 16px; }
.bus-col-right { width: 468px; flex: 0 0 468px; display: flex; flex-direction: column; gap: 16px; }
.bus-resource-card { padding: 0 16px; overflow: hidden; }
.bus-subtabs { display: flex; height: 48px; overflow-x: auto; scrollbar-width: none; border-bottom: 1px solid var(--no-border-light); }
.bus-subtabs::-webkit-scrollbar { display: none; }
.bus-subtab { height: 48px; padding: 0 16px; display: inline-flex; align-items: center; gap: 6px; border: 0; border-bottom: 2px solid transparent; background: transparent; color: var(--no-text-secondary); font-size: 14px; line-height: 20px; white-space: nowrap; cursor: pointer; font-family: inherit; font-weight: 400; }
.bus-subtab:hover, .bus-subtab.active { color: var(--no-primary); }
.bus-subtab.active { border-bottom-color: var(--no-primary); font-weight: 600; }
.bus-desc { margin: 12px 0 16px; color: var(--no-text-secondary); font-size: 12px; line-height: 18px; }
.bus-loading { padding: 16px 4px; }
.bus-stat-row { display: grid; grid-template-columns: repeat(6, minmax(100px, 1fr)); gap: 8px; margin-bottom: 16px; }
.bus-stat-item { min-width: 0; display: flex; align-items: center; gap: 8px; }
.bus-stat-item > span:last-child { min-width: 0; display: flex; flex-direction: column; }
.bus-stat-icon { width: 32px; height: 32px; flex: 0 0 32px; display: flex; align-items: center; justify-content: center; border-radius: 6px; }
.bus-stat-blue { color: var(--no-primary); background: var(--no-bg-active); }
.bus-stat-green { color: var(--no-success-text); background: var(--no-success-bg); }
.bus-stat-muted { color: var(--no-text-muted); background: var(--no-chip-off-bg); }
.bus-stat-warning { color: var(--no-warning-text); background: var(--no-warning-bg); }
.bus-stat-label { color: var(--no-text-secondary); font-size: 12px; line-height: 18px; white-space: nowrap; }
.bus-stat-value { color: var(--no-text); font-size: 16px; font-weight: 600; line-height: 22px; }
.bus-empty { padding: 12px 0; }
.bus-table-wrap { overflow-x: auto; }
.bus-table { width: 100%; min-width: 0; border-collapse: collapse; table-layout: fixed; font-size: 13px; line-height: 20px; }
.bus-table th { height: 42px; padding: 0 6px; color: var(--no-text-secondary); text-align: left; font-size: 12px; font-weight: 500; line-height: 18px; white-space: nowrap; border-bottom: 1px solid var(--no-border); }
/* 2026-10-02：表体原先用 --no-border-light(#F0F2F5) 画行分隔线，在白底卡片上几乎看不见，
   整块表格读起来是"一片白"。表头加浅底色带 + 正文改用 --no-border，让"表头/表行/行间"
   三层结构各自可辨（--no-border-light 仍用于卡片内其它更轻的分隔，不动它）。 */
.bus-resource-card .bus-table thead th { background: var(--no-bg-subtle, #FAFBFC); }
.bus-table td { height: 44px; padding: 0 6px; color: var(--no-text); font-size: 13px; font-weight: 400; line-height: 20px; border-bottom: 1px solid var(--no-border); white-space: nowrap; }
/* 最后一行不再画线：线由卡片底部承担，避免出现"半截线" */
.bus-table tbody tr:last-child td { border-bottom: 0; }
/* 「关键参数」列放的是长度不定的标签（"1MHz" / "5000000 baud" / "SDA8 / SCL9"…）。
   单元格默认 white-space:nowrap，标签一超标就**溢出到相邻列** —— 实测 92px 标签挤进
   89px 单元格（2026-10-02 在 UART TAB 复现）。改为允许在此列内换行：宽度不再依赖
   对"最长文案"的猜测，任何长度的参数都不会越界。 */
.bus-table td:nth-child(4) { white-space: normal; }
.bus-table td:nth-child(4) .bus-tag { margin: 2px 3px 2px 0; }
.bus-table th:nth-child(1), .bus-table td:nth-child(1) { width: 28px; }
/* 2026-10-02 重新配比：把宽度让给真正需要空间的列。
   「DMA 绑定」要放一条选择器、「操作」要放"查看 + 建通道"两个入口，
   而「引脚」「状态」内容很短（SDA8 / SCL9、可用），不需要原来的份额。 */
.bus-table th:nth-child(2), .bus-table td:nth-child(2) { width: 9%; }
.bus-table th:nth-child(3), .bus-table td:nth-child(3) { width: 13%; }
.bus-table th:nth-child(4), .bus-table td:nth-child(4) { width: 12%; }
.bus-table th:nth-child(5), .bus-table td:nth-child(5) { width: 8%; }
.bus-table th:nth-child(6), .bus-table td:nth-child(6) { width: 9%; }
.bus-table th:nth-child(7), .bus-table td:nth-child(7) { width: 17%; }
.bus-table th:nth-child(8), .bus-table td:nth-child(8) { width: 21%; }
.bus-table tbody tr { cursor: pointer; transition: background .15s; }
.bus-table tbody tr:hover { background: var(--no-bg-hover); }
.bus-table tbody tr.selected { background: var(--no-bg-active); }
.bus-table tbody tr.disabled { opacity: .58; }
.bus-select-col { width: 28px; }
.bus-radio { display: block; width: 12px; height: 12px; border: 2px solid var(--no-border); border-radius: 50%; box-sizing: border-box; }
.bus-radio.checked { border-color: var(--no-primary); box-shadow: inset 0 0 0 2px var(--card-bg, #fff); background: var(--no-primary); }
.bus-resource-name, .link-btn { display: inline-flex; align-items: center; gap: 4px; padding: 0; border: 0; background: transparent; color: var(--no-primary); font-family: inherit; font-size: 13px; font-weight: 500; line-height: 20px; cursor: pointer; }
.bus-resource-name:hover, .link-btn:hover:not(:disabled) { color: var(--no-primary-hover); }
.link-btn:disabled { color: var(--no-text-faint); cursor: not-allowed; }
.bus-pins, .mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
.bus-tag { display: inline-flex; align-items: center; min-height: 20px; padding: 0 7px; margin-right: 4px; border-radius: 4px; font-size: 12px; font-weight: 500; line-height: 18px; }
.bus-tag-blue { color: var(--no-primary); background: var(--no-bg-active); }
.bus-tag-green { color: var(--no-success-text); background: var(--no-success-bg); }
.bus-tag-gray { color: var(--no-text-muted); background: var(--no-chip-off-bg); }
/* OTA 问题态标签：红=失败/超时，橙=需要重试。色值直接引用页面级语义 token
   （--no-danger / --no-warning-text / --no-warning-bg 在 html.dark 块里已另给暗色档），
   因此亮暗两套自动跟随主题，无需再定义新 token。 */
.bus-tag-red { color: var(--no-danger); background: color-mix(in srgb, var(--no-danger) 12%, transparent); }
.bus-tag-orange { color: var(--no-warning-text); background: var(--no-warning-bg); }
.dma-na { color: var(--no-text-muted); }
.bus-row-actions { display: flex; align-items: center; gap: 12px; }
.bus-pagination { min-height: 48px; display: flex; align-items: center; gap: 10px; color: var(--no-text-secondary); font-size: 12px; }
.bus-page-controls { margin-left: auto; display: flex; align-items: center; gap: 6px; }
.bus-page-btn, .bus-page-current { height: 26px; padding: 0 8px; border: 1px solid var(--no-border); border-radius: 5px; background: var(--card-bg, #fff); color: var(--no-text-secondary); font: inherit; }
.bus-page-btn:not(:disabled) { cursor: pointer; }
.bus-page-btn:disabled { opacity: .5; cursor: not-allowed; }
.bus-page-current { display: inline-flex; align-items: center; color: var(--text-on-fill); border-color: var(--no-primary); background: var(--no-primary); }
/* A：已创建通道列表（与资源表并列，两个层级必须视觉可分） */
.bus-channels-card { padding: 0 16px; }
.bus-channels-head { min-height: 52px; display: flex; align-items: center; gap: 8px; flex-wrap: wrap; border-bottom: 1px solid var(--no-border-light); }
.bus-channels-head > b { color: var(--no-text); font-size: 16px; font-weight: 600; line-height: 24px; }
.bus-channels-hint { color: var(--no-text-muted); font-size: 12px; line-height: 18px; }
.bus-channels-head .link-btn { margin-left: auto; font-size: 12px; line-height: 18px; }
.bus-channels-empty { padding: 14px 0; color: var(--no-text-secondary); font-size: 13px; line-height: 20px; }
.bus-channels-list { padding: 4px 0; }
.bus-channel-item { min-height: 44px; display: flex; align-items: center; gap: 10px; border-bottom: 1px solid var(--no-border-light); font-size: 13px; line-height: 20px; }
.bus-channel-item:last-child { border-bottom: 0; }
.bus-channel-name { color: var(--no-text); font-weight: 500; }
.bus-channel-hw { color: var(--no-text-secondary); }
.bus-channel-res { color: var(--no-text-muted); font-size: 12px; }
.bus-channel-actions { margin-left: auto; }
.channel-state-tag { display: inline-flex; align-items: center; min-height: 20px; padding: 0 7px; border-radius: 4px; font-size: 12px; font-weight: 500; line-height: 18px; }
.channel-state-tag.state-on { color: var(--no-success-text); background: var(--no-success-bg); }
.channel-state-tag.state-off { color: var(--no-text-muted); background: var(--no-chip-off-bg); }
/* B3：无可兼容 DMA 时的显式说明（不留空白控件） */
.dma-none { color: var(--no-text-muted); font-size: 12px; line-height: 18px; white-space: nowrap; }
/* 选择器宽度必须跟着列走：原先写死 200px，而「DMA 绑定」列在 812px 表宽下只有 ~81px，
   控件直接溢出并盖住右侧「操作」列（2026-10-02 实测该单元格 scrollWidth=206 > clientWidth=81）。 */
.dma-select { width: 100%; min-width: 0; }
.bus-tool-layout { display: grid; grid-template-columns: minmax(0, 2fr) minmax(228px, 1fr); gap: 16px; }
/* 只剩一组工具时让它占满整行，避免右侧留一片空白 */
.bus-tool-layout.single-group { grid-template-columns: minmax(0, 1fr); }
/* 单组时工具卡是"整行宽"，两个卡片并排更均衡 */
.bus-tool-layout.single-group .bus-tool-cards { grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); }
.bus-tool-group { min-width: 0; padding: 14px 16px 16px; }
.bus-tool-group-head { min-height: 24px; display: flex; align-items: center; gap: 8px; margin-bottom: 12px; }
.bus-tool-group-title { color: var(--no-text); font-size: 16px; font-weight: 600; line-height: 24px; }
.bus-tool-group-title em { margin-left: 6px; padding: 1px 5px; border-radius: 4px; color: var(--no-warning-text); background: var(--no-warning-bg); font-size: 11px; font-style: normal; font-weight: 500; line-height: 16px; }
.bus-tool-group-hint { margin-left: auto; color: var(--no-text-secondary); font-size: 12px; line-height: 18px; }
.bus-tool-cards { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
.bus-tool-card { min-height: 112px; padding: 12px; border: 1px solid var(--no-border-light); border-radius: 6px; display: flex; flex-direction: column; }
.bus-uart-tools .bus-tool-card { height: calc(100% - 36px); box-sizing: border-box; }
.bus-tool-head { display: flex; align-items: center; gap: 8px; color: var(--no-text); font-size: 14px; font-weight: 600; line-height: 20px; }
.bus-tool-icon { width: 24px; height: 24px; display: inline-flex; align-items: center; justify-content: center; border-radius: 50%; }
.bus-tool-blue { color: var(--no-primary); background: var(--no-bg-active); }
.bus-tool-green { color: var(--no-success-text); background: var(--no-success-bg); }
.bus-tool-orange { color: var(--no-warning-text); background: var(--no-warning-bg); }
.bus-tool-card p { margin: 8px 0; color: var(--no-text-secondary); font-size: 13px; line-height: 20px; }
.bus-tool-foot { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; margin-top: auto; }
.bus-tool-hint { font-size: 12px; line-height: 18px; color: var(--no-text-muted); flex: 1 1 100%; }
/* 选中态必须与「未选中」在视觉上可分：它决定这次提交的目标集合是 1 条还是 N 条。 */
.bus-tool-hint.baud-picked { color: var(--no-accent); font-weight: 500; }
.baud-target { font-size: 13px; color: var(--no-text); margin-bottom: 10px; }
.baud-pick-hint { font-size: 13px; line-height: 20px; color: var(--no-text-muted); margin-bottom: 10px; }
.baud-field { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.baud-field > label { font-size: 13px; color: var(--no-text-secondary); }
.baud-range { font-size: 12px; color: var(--no-text-muted); }
.baud-note { margin: 12px 0 8px; font-size: 12px; line-height: 18px; color: var(--no-text-muted); }
.btn-sm { height: 28px; padding: 0 11px; font-size: 12px; line-height: 18px; }
.scan-found { color: var(--no-success-text); font-size: 12px; }
.scan-empty { color: var(--no-text-muted); font-size: 12px; }
.bus-detail-card, .bus-create-card { padding: 0 16px; }
.bus-detail-head, .bus-create-head { min-height: 52px; display: flex; align-items: center; gap: 8px; border-bottom: 1px solid var(--no-border-light); }
.bus-detail-head > b, .bus-create-head > b { color: var(--no-text); font-size: 16px; font-weight: 600; line-height: 24px; }
.bus-detail-head .link-btn { margin-left: auto; font-size: 12px; line-height: 18px; }
.bus-detail-list { padding: 8px 0; }
.bus-detail-list > div { min-height: 32px; display: flex; align-items: center; justify-content: space-between; gap: 12px; border-bottom: 1px solid var(--no-border-light); font-size: 13px; line-height: 20px; }
.bus-detail-list > div:last-child { border-bottom: 0; }
.bus-detail-list span { color: var(--no-text-secondary); }
.bus-detail-list b { color: var(--no-text); font-weight: 500; text-align: right; overflow-wrap: anywhere; }
.bus-detail-list .dma-bound { color: var(--no-success-text); }
/* 详情行内的就地操作（查看通道 / DMA 选择器）靠右对齐，与只读值同一列 */
.bus-detail-list .bus-detail-action { margin-left: 8px; font-size: 12px; }
/* flex:1 + min-width:0 才能让选择器真正吃满剩余宽度；
   只写 width:100% 时父级 <b> 是 inline-flex 收缩包裹，100% 无处可依，会退到 min-width，
   于是长文案（如 "GDMA_CH0（#0 · UART · I2C）"）被截断成 "GDMA_CH0（#0 · UART · I…"。 */
.bus-detail-list .bus-detail-edit { flex: 1; min-width: 0; display: inline-flex; justify-content: flex-end; }
.bus-detail-list .bus-detail-edit .dma-select { width: 100%; min-width: 0; }
/* 通道行内联波特率编辑 */
.baud-inline-input { width: 120px; }
.baud-inline-error { color: var(--no-danger); font-size: 12px; }
.bus-create-head b { flex: 1; }
.bus-close { padding: 4px; border: 0; background: transparent; color: var(--no-text-muted); cursor: pointer; }
.bus-close:hover { color: var(--no-primary); }
.bus-create-card { flex: 1; min-height: 342px; display: flex; flex-direction: column; }
.bus-create-summary { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; padding: 14px 0 12px; border-bottom: 1px solid var(--no-border-light); }
.bus-create-resource-label { color: var(--no-text-secondary); font-size: 12px; line-height: 18px; }
.bus-create-fields { padding: 16px 0; }
.bus-create-section-title { margin-bottom: 10px; color: var(--no-text-secondary); font-size: 14px; font-weight: 600; line-height: 20px; }
.bus-create-field-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px 12px; }
.bus-create-field { min-height: 52px; padding: 8px 10px; border: 1px solid var(--no-border-light); border-radius: 6px; display: flex; flex-direction: column; gap: 4px; }
.bus-create-field span { color: var(--no-text-secondary); font-size: 12px; line-height: 18px; }
.bus-create-field b { color: var(--no-text); font-size: 13px; font-weight: 500; line-height: 20px; }
.bus-create-actions { margin-top: auto; padding: 14px 0 16px; border-top: 1px solid var(--no-border-light); display: flex; justify-content: flex-end; gap: 12px; }

/* 卡片行 */
.card-row { display: flex; gap: 16px; margin-bottom: 16px; }
.row-1 .info-card { flex: 5; }
.row-1 .metrics-card { flex: 3.2; }
.row-1 .events-card { flex: 3.2; min-width: 0; }
.row-2 > .card { flex: 1; min-width: 0; }

.card-head {
  display: flex; align-items: center; justify-content: space-between;
  padding: 16px 20px 12px; border-bottom: 1px solid var(--no-border-light);
}
.card-title { color: var(--no-text); font-size: 16px; font-weight: 600; line-height: 24px; }
.card-link { border: 0; background: transparent; padding: 0; font-family: inherit; color: var(--no-primary); font-size: 13px; font-weight: 500; line-height: 20px; cursor: pointer; }
.card-link:hover { text-decoration: underline; }
.card-link:focus-visible { outline: 2px solid var(--no-primary); outline-offset: 2px; border-radius: 4px; }
.card-loading { padding: 12px 20px; }
.card-empty { padding: 32px 20px; text-align: center; color: var(--no-text-secondary); font-size: 13px; line-height: 20px; }

/* 设备基本信息 */
.info-grid { display: flex; gap: 32px; padding: 12px 20px 16px; }
.info-col { flex: 1; min-width: 0; }
.info-row { display: flex; align-items: center; height: 36px; font-size: 13px; border-bottom: 1px solid var(--no-border-light); }
.info-col .info-row:last-child { border-bottom: none; }
/* ⚠ 2026-10-09（用户要求："DMA 分不到降级为提示"）：
   配置告警 —— 用 warning 色而非 danger 色：配置是成功的，只是降级了。 */
.config-warnings { margin-top: 8px; display: flex; flex-direction: column; gap: 6px; }
.config-warning-row {
  display: flex; align-items: flex-start; gap: 6px;
  padding: 6px 8px; border-radius: 4px; font-size: 12px; line-height: 1.5;
  background: var(--el-color-warning-light-9);
  border: 1px solid var(--el-color-warning-light-5);
  color: var(--el-color-warning-dark-2);
}
.config-warning-icon { flex: none; margin-top: 2px; color: var(--el-color-warning); }
.config-warning-text { flex: 1; word-break: break-word; }
.info-label { width: 92px; flex-shrink: 0; color: var(--no-text-muted); font-size: 12px; }
.info-val { color: var(--no-text); display: flex; align-items: center; gap: 6px; min-width: 0; }
.info-val.mono { font-family: ui-monospace, monospace; }
.info-val.dim { color: var(--no-text-muted); }
.mini-edit { display: inline-flex; align-items: center; border: 0; background: transparent; padding: 0; font: inherit; color: var(--no-text-muted); cursor: pointer; }
.mini-edit:hover { color: var(--no-primary); }
.mini-edit:focus-visible, .copy-icon:focus-visible { outline: 2px solid var(--no-primary); outline-offset: 2px; border-radius: 4px; }
.qbar { display: inline-block; width: 64px; height: 6px; border-radius: 3px; background: var(--no-border-light); overflow: hidden; flex-shrink: 0; }
.qbar-fill { display: block; height: 100%; border-radius: 3px; }
.q-num, .q-good { font-weight: 600; white-space: nowrap; }
.q-good { font-size: 12px; }
.latency { color: var(--no-success-text); font-weight: 600; }

/* 实时指标 */
.metrics-updated { font-size: 12px; color: var(--no-text-muted); display: flex; align-items: center; gap: 6px; }
.metrics-offline-tag {
  margin-left: 8px; font-size: 11px; font-weight: 400; color: var(--no-text-muted);
  background: var(--no-chip-off-bg); border-radius: 4px; padding: 1px 6px;
}
.metrics-offline .metric-val { color: var(--no-text-muted); }
.mini-refresh { cursor: pointer; }
.mini-refresh:hover { color: var(--no-primary); }
.metric-list { padding: 12px 20px 16px; }
.metric-row { display: flex; align-items: center; height: 44px; gap: 8px; }
.metric-icon { width: 24px; height: 24px; border-radius: 50%; flex-shrink: 0; display: flex; align-items: center; justify-content: center; }
.metric-name { font-size: 13px; color: var(--no-text-secondary); flex: 1; white-space: nowrap; }
.metric-val { font-size: 13px; font-weight: 500; white-space: nowrap; }
.metric-val.dim { color: var(--no-text-muted); font-weight: 400; }
.metric-unit { font-size: 12px; color: var(--no-text-muted); font-weight: 400; }

/* 时间线 */
.timeline { padding: 12px 20px 16px; position: relative; }
.timeline::before { content: ''; position: absolute; left: 27px; top: 20px; bottom: 24px; width: 2px; background: var(--no-border); }
.tl-row { display: flex; align-items: center; height: 40px; gap: 12px; position: relative; }
.tl-icon {
  width: 16px; height: 16px; border-radius: 50%; background: var(--card-bg, #fff); border: 1.5px solid;
  display: flex; align-items: center; justify-content: center; flex-shrink: 0; z-index: 1;
}
.tl-ok { border-color: var(--no-success); color: var(--no-success); }
.tl-bad { border-color: var(--no-danger); color: var(--no-danger); }
.tl-text { flex: 1; font-size: 13px; color: var(--no-text-secondary); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.tl-time { font-size: 12px; color: var(--no-text-muted); font-variant-numeric: tabular-nums; white-space: nowrap; }

/* 通道健康 */
.chips { display: flex; gap: 8px; padding: 14px 20px 10px; }
.chip { flex: 1; border-radius: 6px; padding: 8px 12px; display: flex; flex-direction: column; gap: 2px; }
.chip-total { background: var(--no-chip-off-bg); }
.chip-ok { background: var(--no-success-bg); }
.chip-warn { background: var(--no-warning-bg); }
.chip-off { background: var(--no-chip-off-bg); }
.chip-label { font-size: 12px; color: var(--no-text-secondary); display: flex; align-items: center; gap: 6px; }
.chip-dot { width: 6px; height: 6px; border-radius: 50%; display: inline-block; }
.chip-num { font-size: 18px; font-weight: 600; line-height: 26px; }
.dim-num { color: var(--no-text-muted); }
.chan-list { padding: 0 20px 12px; }
.chan-row {
  display: flex; align-items: center; height: 44px; gap: 10px; cursor: pointer;
  border-bottom: 1px solid var(--no-border-light); padding: 0 4px; transition: background .15s;
}
.chan-row:last-child { border-bottom: none; }
.chan-row:hover { background: var(--no-bg-hover); }
.chan-row:focus-visible { outline: 2px solid var(--no-primary); outline-offset: -2px; border-radius: 6px; }
/* D2：列表被截断时的显式提示（不能把"看到 6 行"读成"只有 6 个通道"）。 */
.chan-more { padding: 8px 4px 0; font-size: 12px; line-height: 18px; color: var(--no-text-muted); }
/* D5：DMA 空态可能是"设备未上报"，文案较长，给它可读的行高。 */
.dma-empty { line-height: 20px; }
.chan-icon {
  width: 28px; height: 28px; border-radius: 6px; background: var(--no-bg-active); color: var(--no-primary);
  display: flex; align-items: center; justify-content: center; flex-shrink: 0;
}
.chan-name { flex: 1; font-size: 13px; font-weight: 500; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.chan-badge { font-size: 12px; height: 22px; line-height: 22px; padding: 0 10px; border-radius: 11px; }
.cb-ok { color: var(--no-success-text); background: var(--no-success-bg); }
.cb-warn { color: var(--no-warning-text); background: var(--no-warning-bg); }
.cb-off { color: var(--no-text-muted); background: var(--no-chip-off-bg); }
.chan-arrow { color: var(--no-dot-off); }
.chan-edit { flex-shrink: 0; }
.channel-manager-edit-hint { padding: 0 20px 8px; font-size: 12px; color: var(--no-text-muted); }

/* 弹窗 */
.form-row { margin-bottom: 16px; }
.form-label { display: block; font-size: 12px; color: var(--no-text-muted); margin-bottom: 6px; }
.all-events { max-height: 400px; overflow-y: auto; }
.ae-row { display: flex; align-items: center; gap: 12px; height: 38px; border-bottom: 1px solid var(--no-border-light); }
.ae-text { flex: 1; font-size: 13px; color: var(--no-text-secondary); }
.ae-time { font-size: 12px; color: var(--no-text-muted); font-variant-numeric: tabular-nums; }

.no-error { padding: 40px 20px; }

/* ── 响应式（沿用 demo 三档断点） ── */
@media (max-width: 1200px) {
  .row-1 { flex-direction: column; }
  .row-1 > .card { width: 100%; }
  .stat-strip { flex-wrap: wrap; gap: 12px; }
  .stat-item { flex: 1 1 30%; }
}
/* 右栏固定为 468px，六项资源统计最少需要约 640px 内容宽；
   在有桌面侧栏的中等视口继续双栏会把左栏压缩并被卡片裁切。 */
@media (max-width: 1440px) {
  .bus-main-cols { flex-direction: column; }
  .bus-col-left { width: 100%; flex: 0 0 auto; }
  .bus-col-right { width: 100%; flex: 0 0 auto; }
  .bus-create-card { min-height: 0; }
}
@media (max-width: 900px) {
  .row-2 { flex-direction: column; }
  .info-grid { flex-direction: column; gap: 0; }
}
@media (max-width: 768px) {
  /* ── 面包屑：移动端从「整条隐藏」改为「压缩为两段」 ──
     改前 .no-breadcrumb{display:none} 与 MainLayout 顶栏面包屑同时消失，
     移动端只剩标题，层级位置感丢失（审计 F10 / §4.1.2 MUST）。
     这里必须用 flex 而非保留 EP 的 float:left —— float 布局下容器不参与
     overflow 计算，横向溢出会顶破页面（documentElement.scrollWidth > clientWidth）。 */
  .no-breadcrumb {
    display: flex;
    align-items: center;
    flex-wrap: nowrap;
    min-width: 0;
    margin-bottom: 10px;
    font-size: 12px;
    line-height: 18px;
    /* 兜底：极端长实体名时容器内滚动，而不是把页面撑出横向滚动条 */
    overflow-x: auto;
    overscroll-behavior-x: contain;
    scrollbar-width: none;
  }
  .no-breadcrumb::-webkit-scrollbar { display: none; }
  /* 只保留「上一级 / 当前」两段：根级「首页」在 390px 下信息价值最低，优先裁掉 */
  .no-breadcrumb :deep(.el-breadcrumb__item:first-child) { display: none; }
  .no-breadcrumb :deep(.el-breadcrumb__item) { flex: 0 0 auto; }
  /* 当前实体名可长可短：允许它收缩并省略，避免把胶囊标签撑成逐字竖排（§4.2.5） */
  .no-breadcrumb :deep(.el-breadcrumb__item:last-child) { flex: 0 1 auto; min-width: 6em; }
  .no-breadcrumb :deep(.el-breadcrumb__item:last-child .el-breadcrumb__inner) {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    word-break: keep-all;
  }
  /* 上一级是真实链接（:to）：它就是移动端唯一的「退回上一层」入口，
     必须满足 §4.4.5 的 44px 触控热区。改前这里写的是 min-height:20px —— 注释声称
     44px、实测 getBoundingClientRect().height 只有 20px（390px 实测 48x20），
     是「注释与实现相矛盾」的假达标。改为 44px 后热区由 flex 居中撑开，
     面包屑自身高度 20px→44px，仍在 390px 的预算内（docOverflowX 保持 0）。 */
  .no-breadcrumb :deep(.el-breadcrumb__inner.is-link) { min-height: 44px; display: inline-flex; align-items: center; }
  .no-breadcrumb :deep(.el-breadcrumb__separator) { margin: 0 6px; }
  .page-header { flex-direction: column; gap: 12px; }
  .ph-title-row { gap: 8px; }
  .ph-title { font-size: 18px; line-height: 26px; }
  .ph-actions { width: 100%; display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; }
  .ph-actions > span { min-width: 0; }
  .ph-actions .btn { width: 100%; min-width: 0; justify-content: center; padding: 0 8px; }
  .stat-item { flex: 1 1 45%; }
  .tab-bar { overflow-x: auto; overscroll-behavior-x: contain; scrollbar-width: none; gap: 20px; padding: 0 12px; }
  .tab-bar::-webkit-scrollbar { display: none; }
  .tab-item { flex: 0 0 auto; }
  /* 移动端无悬停，统计值允许换行显示完整时间戳（默认 nowrap+ellipsis 在窄列截断） */
  .stat-value { white-space: normal; overflow: visible; text-overflow: unset; font-size: 13px; }
  .bus-alert { align-items: flex-start; padding: 10px 12px; }
  .bus-alert-refresh { margin-top: 0; }
  .bus-stat-row { grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px 8px; }
  .bus-table { min-width: 680px; table-layout: auto; }
  .bus-table-wrap { margin: 0 -16px; padding: 0 16px; }
  .bus-tool-layout, .bus-tool-cards { grid-template-columns: 1fr; }
  .bus-uart-tools .bus-tool-card { height: auto; }
  .bus-create-field-grid { grid-template-columns: 1fr; }
  .bus-create-actions { flex-wrap: wrap; }
  .bus-create-actions .btn { flex: 1; justify-content: center; }
  /* 新 TAB 移动端：DMA 单列；设备行纵向紧凑；宽表容器内滚动不溢出页面 */
  .dma-grid { grid-template-columns: 1fr; }
  .device-row { flex-wrap: wrap; height: auto; min-height: 44px; padding: 8px 4px; gap: 6px 10px; }
  .device-row .chan-name { flex: 1 1 100%; }
  .device-row .chan-sub { flex: 1 1 40%; }
  .device-card-actions { flex-wrap: wrap; }
  .ota-table-wrap { margin: 0 -16px; padding: 0 16px; }

  /* M2：通道健康卡的「编辑/删除」在 390px 实测 42x20px（.link-btn 基样式
     padding:0 + line-height:20px），两个维度都不满足 §4.4.5 的 44x44px 可点区域。
     只改移动端：桌面 1440px 实测同样是 42x20，但那是 §4.2.3 有意保留的紧凑密度（鼠标精度高），
     故桌面必须保持原样，规则不得外溢到 1440 块。
     为何两个维度都要抬：规范说的是**实际可点击区域** 44x44，只加 min-height 仍不达标。
     justify-content: center 让文字在加宽后保持居中（否则会靠左、看起来像坏了）。
     为何不会撑破行：.chan-row 是 height:44px 固定行高，本规则只把**行内子元素**抬到 44px
     （与行高相等，不产生纵向溢出）。
     横向预算（Lead 独立实测，2026-09-24，390×844）：
       .chan-row 可用宽 302px；行内子项 icon28 + 名称71 + 徽章44 + 编辑44 + 删除44 + 箭头13 = 244
       加 5 个 gap×10 = 50 ⇒ 合计 **294px ≤ 302px**，余 8px，故 rowScrollW == clientW == 302 无溢出。
       （注：名称列实测 71px 会随通道名长短浮动；余量仅 8px，若将来行内再增子项需重新核算。）
     chan-del 未设 flex-shrink（chan-edit 有），故一并显式声明，避免两键互相挤压。 */
  .chan-row .link-btn.chan-edit,
  .chan-row .link-btn.chan-del {
    min-width: 44px;
    min-height: 44px;
    justify-content: center;
    flex-shrink: 0;
  }
}
</style>
