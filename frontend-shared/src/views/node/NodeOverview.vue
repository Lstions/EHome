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
      <div class="tab-bar card" :ref="setTabBarRef" :class="tabBarScrollClass" @scroll.passive="updateTabScrollState">
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
        <BusConfigPanel
          :node-online="nodeOnline"
          :node-offline="nodeOffline"
          :capabilities="capabilities"
          :channels="channels"
          :dma-channels="dmaChannels"
          :dma-toggling="dmaToggling"
          :active-bus-type="activeBusType"
          :selected-resource-id="selectedResourceId"
          :bus-page="busPage"
          :bus-page-size="busPageSize"
          :bus-loading="busLoading"
          :bus-load-error="busLoadError"
          :resource-querying="resourceQuerying"
          :i2c-scanning="i2cScanning"
          :scan-result="scanResult"
          :baud-tool-selection="baudToolSelection"
          :baud-tool-selection-text="baudToolSelectionText"
          :baud-tool-available="baudToolAvailable"
          @refresh-resources="requestBusResourceRefresh"
          @retry-load="fetchBusData"
          @select-bus-type="selectBusType"
          @select-resource="selectedResourceId = $event"
          @change-dma="changeResourceDma"
          @toggle-dma="toggleResourceDma"
          @scan="scanSelectedI2C"
          @navigate-channels="navigateToNodeChannels"
          @open-channel-manager="openChannelManager"
          @bus-page-change="busPage = $event"
          @open-baud-tool="openBaudToolInline"
        />
        <BusCreatedChannelsCard
          :channels="channels"
          :channels-loading="channelsLoading"
          :node-offline="nodeOffline"
          :node-serial="nodeSerial"
          :capabilities="capabilities"
          :active-bus-type="activeBusType"
          :baud-max="baudMax"
          :refresh-channels="fetchChannels"
          @edit-channel="editChannel"
          @navigate-channels="navigateToNodeChannels"
        />
      </template>

      <!-- 外设控制：GPIO/PWM 直控（PeriphCmd 0x1B / PeriphRsp 0x1C，不走通道）。
           改前该能力只存在于不可达组件（PeripheralControl → GPIOResourceList/PWMResourceList，
           仅由死文件 NodeDetail.vue 经 ChannelPanel.vue 消费），用户完全看不到；
           FR-I1/I2/I3 已标 ✅ 但生产无入口 —— 本 TAB 是能力接线，不是新功能。
           写后回填：本页订阅 WS periph_result，经 registerPending* 登记请求后按 request_id
           代际校验回填（与 ChannelPanel 同一套语义，见 onPeriphResult）。 -->
      <template v-else-if="activeTab === '外设控制'">
        <PeripheralControl
          :ref="setPeripheralControlRef"
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
      <DmaChannelsCard
        v-else-if="activeTab === 'DMA 通道'"
        :dma-channels="dmaChannels"
        :dma-loading="dmaLoading"
        :capabilities="capabilities"
      />

      <!-- 关联设备 -->
      <NodeDevicesCard
        v-else-if="activeTab === '关联设备'"
        :devices="devices"
        :devices-loading="devicesLoading"
        :node-offline="nodeOffline"
        :node-id="node?.node_id"
        :channels="channels"
        @view-device="viewDevice"
        @refresh-devices="fetchDevices"
        @create-device="showQuickCreate = true"
      />

      <!-- OTA 历史 -->
      <NodeOtaHistoryCard
        v-else-if="activeTab === 'OTA 历史'"
        :ota-history="otaHistory"
        :ota-history-loading="otaHistoryLoading"
        :node-offline="nodeOffline"
        @refresh="fetchOTAHistory"
        @cancel="handleCancelOTA"
      />

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
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import {
  ArrowRight, Clock, Connection, CopyDocument, Cpu, Delete, Document, EditPen, Link, Odometer,
  Refresh, RefreshRight, Select, Share, SwitchButton, Timer, UploadFilled, WarningFilled,
} from '@element-plus/icons-vue'
import { nodeApi, type OTARecord } from '@/api/node'
import { channelApi, type Channel } from '@/api/channel'
import OTAForm from '@/components/forms/OTAForm.vue'
import ChannelManager from '@/components/channel/ChannelManager.vue'
import ChannelTerminal from '@/components/channel/ChannelTerminal.vue'
import LogPanel from '@/components/node/LogPanel.vue'
import PeripheralControl from '@/components/periph/PeripheralControl.vue'
import QuickCreateDeviceDialog from '@/components/node/QuickCreateDeviceDialog.vue'
import { useDmaStore } from '@/stores/dma'
import { useEdgeDeviceStore } from '@/stores/edgeDevice'
import { UNKNOWN, formatTime } from '@/utils/format'
import { getSessionGeneration, assertSessionGeneration } from '@/utils/sessionCache'
import { logger } from '@/utils/logger'
import feedback from '@/utils/feedback'
import { channelBadgeClass, channelName, channelStatusText } from './composables/channelDisplay'
import type { BusResource } from './composables/busResourceHelpers'
import { useNodeOverviewData } from './composables/useNodeOverviewData'
import { useBusResources } from './composables/useBusResources'
import { useBusConfig } from './composables/useBusConfig'
import { useBaudReconfigure } from './composables/useBaudReconfigure'
import { useNodeTabs } from './composables/useNodeTabs'
import { useNodeHeaderActions } from './composables/useNodeHeaderActions'
import { useNodeDeviceOps } from './composables/useNodeDeviceOps'
import { usePeripheralAck } from './composables/usePeripheralAck'
import BusConfigPanel from './components/BusConfigPanel.vue'
import BusCreatedChannelsCard from './components/BusCreatedChannelsCard.vue'
import DmaChannelsCard from './components/DmaChannelsCard.vue'
import NodeDevicesCard from './components/NodeDevicesCard.vue'
import NodeOtaHistoryCard from './components/NodeOtaHistoryCard.vue'

/**
 * 节点总览（生产页）：页头 + 页签壳 + 「基本信息」与「外设/日志/终端」两组直接内容，
 * 以及四个自包含页签（总线配置 / DMA / 关联设备 / OTA 历史）的装配。
 *
 * 拆分边界（为什么是这样切）：
 *   · 状态与取数按**生命周期**归属：节点详情/通道/事件/设备/OTA 由同一个
 *     watch(route.params.id) 驱动，放在 useNodeOverviewData 里一起清空、一起递增代际；
 *     总线能力是独立一份数据（有自己的代际），放在 useBusResources。
 *   · 页面保留「编排」：页签切换触发哪几份补拉、创建通道对话框的 preset、
 *     删除通道/取消 OTA 这类需要二次确认的写动作、路由切换时的统一重置 ——
 *     这些是跨越多个 composable 的决策，放进任何一个 composable 都会让职责反转。
 *   · 页头、五格统计条、页签栏、「基本信息」与「通道健康」留在页面文件：
 *     它们由页面级 CSS token 与断点直接约束，且是门禁按页面源码检查的约定。
 */

const route = useRoute()
const router = useRouter()
const dmaStore = useDmaStore()
const edgeDeviceStore = useEdgeDeviceStore()

/**
 * 页面级操作代际：路由切换/卸载时自增，使在途写操作（DMA 绑定、地址扫描、
 * 测延迟、取消 OTA、资源刷新）的结果失效。做成普通 ref 而不是模块变量，
 * 保证每个挂载实例各有自己的一份（测试会并发挂载多个实例）。
 */
const opGeneration = ref(0)

// ── 页签栏 ──
const { tabs, activeTab, setTabBarRef, tabBarScrollClass, updateTabScrollState } = useNodeTabs()

// ── 数据层 ──
// fetchBusData / handlePing 由后续声明提供：这两个回调只在挂载后（fetchDetail 落地时）
// 才会被调用，因此用惰性闭包接上，避免让数据层反过来依赖总线与页头。
const data = useNodeOverviewData({
  activeTab: () => activeTab.value,
  fetchBusData: () => busResources.fetchBusData(),
  onOnlineWithoutLatency: () => header.handlePing(),
})
const {
  loading, refreshing, node, channels, channelsLoading, nodeEvents, eventsLoading,
  devices, devicesLoading, otaHistory, otaHistoryLoading,
  nodeSerial, nodeOnline, nodeOffline, pageTitle, latencyMs, qualityText, qualityColor,
  lastOnlineText, sessionDuration, uptimeText, freeHeapText, connectionTypeText, syncStateLabel,
  configWarnings, metricsUpdatedText, channelStats, occupiedHardwareIds, recentEvents, eventText,
  fetchDetail, fetchChannels, fetchDevices, fetchOTAHistory,
} = data

// ── 总线能力（独立代际）与总线选择/写动作 ──
const busResources = useBusResources({
  nodeSerial,
  nodeOffline,
  opGeneration,
  // 能力数据落地后选中态必须归位（原逻辑写在 fetchBusData 的赋值之后，逐字保留语义）。
  onCapabilitiesLoaded: () => bus.clampSelectionAfterCapabilities(),
})
const { capabilities, busLoading, busLoadError, busDataLoaded, resourceQuerying, fetchBusData, requestBusResourceRefresh } = busResources

const bus = useBusConfig({ capabilities, channels, nodeSerial, nodeOffline, opGeneration })
const {
  activeBusType, selectedResourceId, busPage, busPageSize, i2cScanning, scanResult,
  selectedResource, busSupportsChannels, selectBusType, changeResourceDma, toggleResourceDma,
  scanSelectedI2C, resetForRouteChange: resetBusForRouteChange,
} = bus

// ── 页头动作 / 节点级设备操作 ──
const header = useNodeHeaderActions({ node, nodeSerial, nodeOffline, seq: data.seq, opGeneration, fetchDetail })
const {
  syncing, pinging, showOTADialog, renameVisible, renameDraft, renameSaving, eventsVisible,
  handleSyncConfig, handlePing, copyId, saveRename, handleOTASuccess,
} = header

const {
  deviceOpReady, deviceOpBusy, deviceOpInFlight, loadDeviceOpSupport, handleDeviceOp,
} = useNodeDeviceOps({ node, nodeSerial, nodeOffline })

// ── 波特率（按资源批量改；单条行内改在「已创建通道」卡内部） ──
const baud = useBaudReconfigure({
  channels, capabilities, activeBusType, selectedResource, nodeOffline, nodeSerial, fetchChannels,
})
const {
  baudToolVisible, baudTarget, baudSubmitting, baudToolError, baudToolSelection,
  baudToolSelectionText, baudToolAvailable, baudToolTargetText, baudMax,
  openBaudToolInline, confirmBaudReconfigure, resetForUnmount: resetBaudForUnmount,
} = baud

// ── 外设直控写后回填 ──
const periph = usePeripheralAck()
const { setPeripheralControlRef, registerPendingPeripheral, registerPendingPWM, onPeripheralConfigure } = periph

// ── 页面自身的弹窗/对话框状态 ──
const channelManagerVisible = ref(false)
// 通道健康卡「编辑」入口的数据源：打开 ChannelManager 时传给 initial-data（编辑而非新建）。
const channelManagerInitialData = ref<Channel | null>(null)
const showQuickCreate = ref(false)

/** 通道健康卡的预览条数（与模板 v-for 的 slice 上限共用同一常量，避免两处漂移）。 */
const CHANNEL_HEALTH_PREVIEW_LIMIT = 6

const dmaChannels = computed(() => dmaStore.mergedChannels)
const dmaLoading = computed(() => dmaStore.loading)
const dmaToggling = computed(() => dmaStore.toggling)

/**
 * 页签切换后的按需补拉。
 *
 * 为什么补拉清单留在页面而不是页签组件里：它是**页面级编排** ——
 * 一次切换可能同时触发总线能力、DMA、关联设备、OTA 四份数据，
 * 而这四份数据分属不同的 composable，只有页面知道它们的加载条件（首次/缓存/离线）。
 */
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

async function refreshAll() {
  refreshing.value = true
  try {
    await fetchDetail()
    if (activeTab.value === '总线配置') await fetchBusData()
    data.lastRefreshAt.value = Date.now()
  } finally {
    refreshing.value = false
  }
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

/**
 * D1 修复后的编辑入口：打开该行通道的配置对话框（initial-data ⇒ 编辑态）。
 * 注意与「新建通道」相反：这里不清 initial-data，它就是要编辑的那条通道。
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

/**
 * D1 修复：进入「该节点的通道视图」。
 *
 * 改前是 goToDetail() { router.push(`/node/${nodeSerial.value}`) } —— 它推的是**当前页**
 * （本页路由就是 /node/:id），于是点通道行、点「查看全部」都等于原地打转。
 * 目标必须是**已存在且可达**的页面：产品里通道的独立页是 /channel（通道管理，
 * 支持节点过滤 + 每行「编辑通道」入口）。因此这里跳 /channel 并带上 node 过滤。
 * 用 { name, query } 对象形态而非拼字符串：参数不会被手工拼接漏编码。
 */
function navigateToNodeChannels() {
  const serial = nodeSerial.value
  if (!serial) { router.push({ name: 'ChannelList' }); return }
  router.push({ name: 'ChannelList', query: { node: serial } })
}

// 说明：旧的函数名 goToDetail（推当前页）已删除。它既是缺陷本体，也无法被任何
// 合法目标复用 —— 保留一个"推当前页"的别名只会给回退留后门，故不再提供任何别名。

function goBack() { router.push('/node') }

function viewDevice(row: any) {
  router.push(`/edge-device/${row.id}`)
}

function handleDeviceCreated() {
  edgeDeviceStore.invalidateLists()
  void fetchDevices()
}

async function handleCancelOTA(record: OTARecord) {
  if (nodeOffline.value) return
  const id = route.params.id as string
  const operation = opGeneration.value
  // 会话代际在**确认之前**取：确认期间若发生重新登录/会话切换，操作必须被判为过期，
  // 而不是拿着新会话的凭据去取消一个旧会话里发起的升级。
  const sessionGeneration = getSessionGeneration()
  // 取消 OTA 会中断设备侧升级流程：按破坏性动作处理（danger 确认按钮 + 焦点不落在破坏性按钮上）。
  const confirmed = await feedback.confirmDanger('确定要取消此 OTA 升级吗？', {
    title: '提示',
    confirmText: '确定',
    cancelText: '取消',
  })
  if (!confirmed) return
  if (operation !== opGeneration.value || route.params.id !== id) return

  try {
    await nodeApi.cancelOTA(id, record.id)
    assertSessionGeneration(sessionGeneration)
    if (operation !== opGeneration.value || route.params.id !== id) return
    ElMessage.success('已取消 OTA 升级')
    void fetchOTAHistory()
  } catch {
    if (operation !== opGeneration.value || route.params.id !== id) return
    feedback.error('取消 OTA 失败')
  }
}

// ── 路由切换重置 ──
watch(() => route.params.id, () => {
  // 顺序即语义：先让旧节点的在途响应全部作废（各 composable 各自递增代际并清空数据），
  // 再发起新节点的详情请求；反过来会有一帧显示上一个节点的数据。
  data.resetForRouteChange()
  busResources.resetForRouteChange()
  resetBusForRouteChange()
  header.resetForRouteChange()
  periph.resetForRouteChange()
  opGeneration.value++
  dmaStore.clearCache()
  void fetchDetail()
})

// ── 生命周期 ──
onMounted(() => {
  void fetchDetail()
  // A-nodes P2：页签栏横滑渐隐的初始状态（节点详情异步加载，tabBarRef 挂载时再测一次）
  void nextTick(updateTabScrollState)
  window.addEventListener('resize', updateTabScrollState)
  // 探测服务端是否具备节点级设备操作能力；只影响两个按钮显不显示。
  void loadDeviceOpSupport()
  data.startClock()
  // 节点状态更新、延迟结果、外设回填三条订阅各自只认当前节点的序列号。
  data.startStatusSubscription()
  header.startPingSubscription()
  periph.startSubscription()
})

onUnmounted(() => {
  window.removeEventListener('resize', updateTabScrollState)
  // 代际先加一再清订阅：即使在途请求此刻落地，写回也已被判为过期。
  opGeneration.value++
  data.stopAll()
  busResources.stopAll()
  periph.stopAll()
  // 选中态只在「用户刚点了某一行」的上下文里有意义；留在组件外会让下次挂载
  // 带着上一次的通道/弹窗打开状态。
  resetBaudForUnmount()
  header.stopAll()
  dmaStore.clearCache()
})
</script>

<style scoped>
/* 页面自有样式：页面级色板 token、页头、统计条、页签栏、「基本信息」与「通道健康」卡片、
   以及弹窗。卡片基座/按钮/标签/通道行等跨页签共用的基元与全部窄屏规则在
   components/nodeOverview.shared.css（本文件末尾引入，且必须是最后一个 style 块）。 */
/* ── 页面级 CSS token：设计稿色板，不污染全局 theme.css ── */
.node-overview-page {
  --no-primary: #2E6BFF;
  --no-primary-hover: #1F56E0;
  --no-success: #22C55E;
  /* A-nodes P1（2026-10-21 对比度）：原 #16A34A 当**文字色**用时只有 3.02~3.30:1
     （正常 badge #16A34A on #E8F9EF=3.02、延迟 8 ms on 白=3.30），低于 WCAG AA 4.5。
     压深到 #15803D 后：on 白 5.02 / on 页面底 4.76 / on #E8F9EF 4.59，全部 ≥4.5。
     暗色块不变（html.dark 取 var(--color-success)=#85CE61，实测 7.6~8.73，不反向）。 */
  --no-success-text: #15803D;
  --no-success-bg: #E8F9EF;
  --no-warning: #F59E0B;
  /* A-nodes P1：原 #D97706 作为文字色只有 2.97（页头「46% 一般」on 页面底）、
     3.19（信息卡 46%/一般 on 白）、2.90（bus-tag-orange on #FEF3DE）。
     压深到 #B45309：页头 4.77 / 白卡 5.02 / #FEF3DE 4.56，全部 ≥4.5。
     暗色块不变（html.dark 取 var(--color-warning)=#EBB563，实测 7.83~8.99，不反向）。 */
  --no-warning-text: #B45309;
  --no-warning-bg: #FEF3DE;
  --no-danger: #EF4444;
  --no-text: #1F2329;
  /* 可读的三层文字：正文、字段/说明、仅作占位或禁用提示的弱文本。 */
  --no-text-secondary: #526072;
  --no-text-muted: #69778B;
  /* A-nodes P1（2026-10-21）：chip 里的数值「0」用 muted 色落在 --no-chip-off-bg 上只有
     4.13:1（<4.5）。muted 是三层文字层级的一环、被多处复用且被测试锁死取值，
     不应为一个数字整体压深 ⇒ 单独加一层"数值弱化"token（仍浅于 secondary，不干扰层级）。
     亮：#5A6675（on 白 5.85 / on 页底 5.55 / on chip 底 5.31）。
     暗：var(--text-color-regular)=#C0C6D0（on 卡片底 8.46，远高于 4.5，不反向）。 */
  --no-text-dim: #5A6675;
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
  /* A-nodes P1：chip 数值的亮色 token 的暗色档（亮 #5A6675 在深色底上不可读，
     故取主题 regular 文字色；卡片底 rgb(41,41,43) 实测 8.46）。 */
  --no-text-dim: var(--text-color-regular, #C0C6D0);
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

.no-loading { padding: 24px; background: var(--card-bg, #fff); border-radius: 8px; }
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
/* 系统日志 / 通道终端 */
.log-card, .terminal-card { overflow: hidden; }
.log-card .card-head, .terminal-card .card-head { border-bottom-color: var(--no-border-light); }
/* 内容区与 card-head 的 20px 水平对齐一致（card-head padding 16px 20px 12px） */
.log-card > :not(.card-head):not(.card-loading):not(.card-empty),
.terminal-card > :not(.card-head):not(.card-loading):not(.card-empty) {
  padding: 0 20px 16px;
}
.baud-target { font-size: 13px; color: var(--no-text); margin-bottom: 10px; }
.baud-pick-hint { font-size: 13px; line-height: 20px; color: var(--no-text-muted); margin-bottom: 10px; }
.baud-field { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.baud-field > label { font-size: 13px; color: var(--no-text-secondary); }
.baud-range { font-size: 12px; color: var(--no-text-muted); }
.baud-note { margin: 12px 0 8px; font-size: 12px; line-height: 18px; color: var(--no-text-muted); }
/* 卡片行 */
.card-row { display: flex; gap: 16px; margin-bottom: 16px; }
.row-1 .info-card { flex: 5; }
.row-1 .metrics-card { flex: 3.2; }
.row-1 .events-card { flex: 3.2; min-width: 0; }
.row-2 > .card { flex: 1; min-width: 0; }
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
/* A-nodes P2（2026-10-21）：改前 height:40px 固定 + .tl-text nowrap/ellipsis，
   1440 下「status_change（在线）」被截成「status_change（在…」，把在线/离线结论吃掉。
   现在：行高改为「至少 40px、按内容自适应」，文字允许换行 ⇒ 全量可见；
   短文案仍是 40px 行高，1440 事件卡高度不变（只有真正放不下的长文案才增高）。 */
.tl-row { display: flex; align-items: center; min-height: 40px; gap: 12px; position: relative; }
.tl-icon {
  width: 16px; height: 16px; border-radius: 50%; background: var(--card-bg, #fff); border: 1.5px solid;
  display: flex; align-items: center; justify-content: center; flex-shrink: 0; z-index: 1;
}
.tl-ok { border-color: var(--no-success); color: var(--no-success); }
.tl-bad { border-color: var(--no-danger); color: var(--no-danger); }
.tl-text { flex: 1; min-width: 0; font-size: 13px; color: var(--no-text-secondary); white-space: normal; overflow: visible; text-overflow: clip; overflow-wrap: anywhere; }
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
.dim-num { color: var(--no-text-dim); }
/* D2：列表被截断时的显式提示（不能把"看到 6 行"读成"只有 6 个通道"）。 */
.chan-more { padding: 8px 4px 0; font-size: 12px; line-height: 18px; color: var(--no-text-muted); }
/* D5：DMA 空态可能是"设备未上报"，文案较长，给它可读的行高。 */
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
@media (max-width: 900px) {
  .row-2 { flex-direction: column; }
  .info-grid { flex-direction: column; gap: 0; }
}
</style>

<style scoped src="./components/nodeOverview.shared.css"></style>
