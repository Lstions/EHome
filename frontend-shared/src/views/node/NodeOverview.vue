<template>
  <!-- 节点总览（生产页）：骨架复刻 designs/new-node-1.png，数据全部来自真实 API -->
  <!-- 与 demo(/dev/new-node-demo) 的差异：MainLayout 提供导航 chrome；无 mock 数据； -->
  <!-- 位置/备注/时区/搜索/通知卡已裁剪（后端模型无对应字段）；指标卡仅保留真实指标。 -->
  <div class="node-overview-page">
    <!-- 面包屑 -->
    <el-breadcrumb separator="/" class="no-breadcrumb">
      <el-breadcrumb-item :to="{ path: '/dashboard' }">首页</el-breadcrumb-item>
      <el-breadcrumb-item :to="{ path: '/node' }">节点管理</el-breadcrumb-item>
      <el-breadcrumb-item>{{ pageTitle }}</el-breadcrumb-item>
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
            <el-icon :size="16" class="ph-edit" @click="renameVisible = true"><EditPen /></el-icon>
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
            <el-icon :size="13" class="copy-icon" @click="copyId"><CopyDocument /></el-icon>
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
            <div class="stat-value">{{ node.model || '-' }}</div>
          </div>
        </div>
        <div class="stat-sep"></div>
        <div class="stat-item">
          <div class="stat-icon"><el-icon :size="16"><Document /></el-icon></div>
          <div class="stat-text">
            <div class="stat-label">固件版本</div>
            <div class="stat-value">{{ node.firmware_version || '-' }}</div>
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
            <div class="stat-value">{{ sessionDuration }}</div>
          </div>
        </div>
        <div class="stat-sep"></div>
        <div class="stat-item">
          <div class="stat-icon"><el-icon :size="16"><Share /></el-icon></div>
          <div class="stat-text">
            <div class="stat-label">协议版本</div>
            <div class="stat-value">{{ node.protocol_version || '-' }}</div>
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
          @click="activateTab(tab.label)"
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
              <div class="info-row"><span class="info-label">节点名称</span><span class="info-val">{{ node.name || '-' }} <el-icon :size="12" class="mini-edit" @click="renameVisible = true"><EditPen /></el-icon></span></div>
              <div class="info-row"><span class="info-label">设备 ID</span><span class="info-val mono">{{ node.node_id }} <el-icon :size="12" class="mini-edit" @click="copyId"><CopyDocument /></el-icon></span></div>
              <div class="info-row"><span class="info-label">型号</span><span class="info-val">{{ node.model || '-' }}</span></div>
              <div class="info-row"><span class="info-label">固件版本</span><span class="info-val">{{ node.firmware_version || '-' }}</span></div>
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
              <span class="metric-icon" style="background: rgba(34,197,94,.1); color: #16A34A">
                <el-icon :size="13"><Connection /></el-icon>
              </span>
              <span class="metric-name">WiFi 信号强度</span>
              <span class="metric-val" v-if="node.wifi_rssi !== 0">{{ node.wifi_rssi }}<span class="metric-unit"> dBm</span></span>
              <span class="metric-val dim" v-else>—</span>
            </div>
            <div class="metric-row">
              <span class="metric-icon" style="background: rgba(139,92,246,.1); color: #8B5CF6">
                <el-icon :size="13"><Odometer /></el-icon>
              </span>
              <span class="metric-name">空闲堆内存</span>
              <span class="metric-val" v-if="node.free_heap_bytes > 0">{{ freeHeapText }}<span class="metric-unit"> KB</span></span>
              <span class="metric-val dim" v-else>—</span>
            </div>
            <div class="metric-row">
              <span class="metric-icon" style="background: rgba(46,107,255,.1); color: #2E6BFF">
                <el-icon :size="13"><Timer /></el-icon>
              </span>
              <span class="metric-name">通信延迟</span>
              <span class="metric-val" v-if="latencyMs > 0">{{ latencyMs }}<span class="metric-unit"> ms</span></span>
              <span class="metric-val dim" v-else>—</span>
            </div>
            <div class="metric-row">
              <span class="metric-icon" style="background: rgba(245,158,11,.1); color: #D97706">
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
            <span class="card-link" @click="eventsVisible = true">查看全部</span>
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
            <span class="card-link" @click="goToDetail">查看全部</span>
          </div>
          <div v-if="channelsLoading" class="card-loading"><el-skeleton :rows="3" animated /></div>
          <template v-else>
            <div class="chips">
              <div class="chip chip-total"><span class="chip-label"><i class="chip-dot" style="background:#8A93A3"></i>总数</span><span class="chip-num">{{ channelStats.total }}</span></div>
              <div class="chip" :class="channelStats.ok > 0 ? 'chip-ok' : 'chip-off'"><span class="chip-label"><i class="chip-dot" style="background:#22C55E"></i>正常</span><span class="chip-num">{{ channelStats.ok }}</span></div>
              <div class="chip" :class="channelStats.error > 0 ? 'chip-warn' : 'chip-off'"><span class="chip-label"><i class="chip-dot" style="background:#F59E0B"></i>异常</span><span class="chip-num" :class="{ 'dim-num': channelStats.error === 0 }">{{ channelStats.error }}</span></div>
              <div class="chip chip-off"><span class="chip-label"><i class="chip-dot" style="background:#9CA3AF"></i>其他</span><span class="chip-num dim-num">{{ channelStats.other }}</span></div>
            </div>
            <div v-if="channels.length === 0" class="card-empty">该节点暂无通道</div>
            <div v-else class="chan-list">
              <div v-for="ch in channels.slice(0, 6)" :key="ch.id" class="chan-row" @click="goToDetail">
                <span class="chan-icon"><el-icon :size="14"><Link /></el-icon></span>
                <span class="chan-name">{{ channelName(ch) }}</span>
                <span class="chan-badge" :class="channelBadgeClass(ch)">{{ channelStatusText(ch) }}</span>
                <el-icon :size="13" class="chan-arrow"><ArrowRight /></el-icon>
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
                          <el-switch
                            v-else
                            :model-value="resourceDmaBinding(resource)?.bound_to ? true : false"
                            size="small"
                            :disabled="nodeOffline || !canToggleResourceDma(resource)"
                            :loading="resourceDmaBinding(resource) ? Boolean(dmaStore.toggling[resourceDmaBinding(resource)!.dma_id]) : false"
                            @click.stop
                            @change="toggleResourceDma(resource, $event)"
                          />
                        </td>
                        <td>
                          <div class="bus-row-actions">
                            <button class="link-btn" type="button" @click.stop="selectedResourceId = resource.id"><el-icon :size="12"><View /></el-icon>查看</button>
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

            <div class="bus-tool-layout">
              <section class="card bus-tool-group bus-i2c-tools">
                <div class="bus-tool-group-head">
                  <span class="bus-tool-group-title">I2C 总线工具</span>
                  <span class="bus-tool-group-hint">仅展示后端已支持的操作</span>
                </div>
                <div class="bus-tool-cards">
                  <section class="bus-tool-card">
                    <div class="bus-tool-head"><span class="bus-tool-icon bus-tool-blue"><el-icon :size="15"><Search /></el-icon></span><b>地址扫描</b></div>
                    <p>扫描所选 I2C 资源上的从设备地址</p>
                    <div class="bus-tool-foot">
                      <button class="btn btn-primary btn-sm" :disabled="nodeOffline || activeBusType !== 'i2c' || !selectedResource" @click="scanSelectedI2C">{{ i2cScanning ? '扫描中…' : '开始扫描' }}</button>
                      <span v-if="scanResult !== null" :class="scanResult.length ? 'scan-found' : 'scan-empty'">{{ scanResult.length ? `发现 ${scanResult.length} 个设备` : '未发现设备' }}</span>
                    </div>
                  </section>
                  <section class="bus-tool-card">
                    <div class="bus-tool-head"><span class="bus-tool-icon bus-tool-green"><el-icon :size="15"><RefreshRight /></el-icon></span><b>资源刷新</b></div>
                    <p>请求节点重新上报当前总线资源状态</p>
                    <div class="bus-tool-foot"><button class="btn btn-plain btn-sm" :disabled="nodeOffline || resourceQuerying" @click="requestBusResourceRefresh">{{ resourceQuerying ? '查询中…' : '查询资源' }}</button></div>
                  </section>
                </div>
              </section>
              <section class="card bus-tool-group bus-uart-tools">
                <div class="bus-tool-group-head">
                  <span class="bus-tool-group-title">快速操作 <em>UART 专属</em></span>
                </div>
                <section class="bus-tool-card">
                  <div class="bus-tool-head"><span class="bus-tool-icon bus-tool-orange"><el-icon :size="15"><Tools /></el-icon></span><b>修改波特率</b></div>
                  <p>服务端尚未实现安全重配置下发，仅提供状态说明。</p>
                  <div class="bus-tool-foot"><button class="btn btn-plain btn-sm" :disabled="nodeOffline || activeBusType !== 'uart'" @click="openBaudTool">查看限制</button></div>
                </section>
              </section>
            </div>
          </div>

          <aside class="bus-col-right">
            <section class="card bus-detail-card">
              <div class="bus-detail-head"><b>资源详情</b><span class="bus-tag bus-tag-gray">设备上报</span><button class="link-btn" type="button" :disabled="nodeOffline || activeBusType !== 'i2c' || !selectedResource" @click="scanSelectedI2C"><el-icon :size="12"><Search /></el-icon>地址扫描</button></div>
              <div v-if="selectedResource" class="bus-detail-list">
                <div><span>资源名称</span><b class="mono">{{ selectedResource.id }}</b></div>
                <div><span>引脚</span><b class="mono">{{ resourcePins(selectedResource) }}</b></div>
                <div><span>工作模式</span><b>{{ resourceMode(selectedResource) }}</b></div>
                <div><span>关键参数</span><b>{{ resourceParameters(selectedResource).join(' · ') || '—' }}</b></div>
                <div><span>已挂载通道</span><b>{{ resourceMountedChannels(selectedResource).length }}</b></div>
                <div><span>DMA 绑定</span><b :class="{ 'dma-bound': resourceDmaBinding(selectedResource)?.bound_to }">{{ resourceDmaBinding(selectedResource)?.bound_to || '未绑定' }}</b></div>
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
                <button class="btn btn-primary" :disabled="nodeOffline || !selectedResource || selectedResource.enabled === false || !busSupportsChannels" @click="openChannelManager(selectedResource)">{{ busSupportsChannels ? '新建通道' : '此资源不支持通道' }}</button>
              </div>
            </section>
          </aside>
        </div>
      </template>

      <!-- 其他 tab 占位 -->
      <div v-else class="card tab-placeholder">
        <el-icon :size="40" color="#C9D2DE"><Files /></el-icon>
        <div class="tp-title">「{{ activeTab }}」设计稿未包含</div>
        <div class="tp-sub">当前仅实现「基本信息」页;总线配置见 designs/new-node-2.png</div>
        <button class="btn btn-plain" @click="activeTab = '基本信息'">返回基本信息</button>
      </div>
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
    <ChannelManager
      v-model="channelManagerVisible"
      :collector-id="nodeSerial"
      :capabilities="capabilities"
      :preset-hardware-type="activeBusType"
      :preset-hardware-id="selectedResourceId"
      :collector-status="node?.status"
      @refresh="handleChannelManagerRefresh"
    />

    <!-- 后端重配置端点当前为 stub，不能对用户伪报下发成功。 -->
    <el-dialog v-model="baudToolVisible" title="批量修改波特率" width="440px">
      <el-alert type="warning" :closable="false" title="服务端尚未实现通道重配置下发，无法安全执行此操作。" />
      <template #footer><button class="btn btn-plain" @click="baudToolVisible = false">关闭</button></template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import {
  ArrowRight, Clock, Cloudy, Connection, CopyDocument, Cpu, DataLine, Document,
  EditPen, Files, Grid, House, InfoFilled, Link, Lock, MagicStick, Odometer, Plus, Refresh,
  RefreshRight, Search, Select, Share, SwitchButton, Timer, Tools, UploadFilled, UserFilled,
  View, WarningFilled,
} from '@element-plus/icons-vue'
import { nodeApi, type Capabilities, type DmaChannelInfo, type Node } from '@/api/node'
import { channelApi, type Channel } from '@/api/channel'
import client from '@/api/client'
import OTAForm from '@/components/forms/OTAForm.vue'
import ChannelManager from '@/components/channel/ChannelManager.vue'
import { useWebSocketStore, type WebSocketMessage } from '@/stores/websocket'
import { useDmaStore } from '@/stores/dma'
import { WS_EVENT } from '@/events/events'
import { getSessionGeneration, assertSessionGeneration } from '@/utils/sessionCache'
import { isDmaRebindable } from '@/utils/dmaState'
import { formatTime } from '@/utils/format'
import { logger } from '@/utils/logger'

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

// ── 防竞态序列号（NodeDetail 模式） ──
let detailSequence = 0
let channelsSequence = 0
let eventsSequence = 0
let capabilitiesSequence = 0
let componentOperationGeneration = 0

// ── 页面状态 ──
const loading = ref(false)
const refreshing = ref(false)
const node = ref<Node | null>(null)
const channels = ref<Channel[]>([])
const channelsLoading = ref(false)
const nodeEvents = ref<NodeEvent[]>([])
const eventsLoading = ref(false)

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
const selectedResourceId = ref('')
const busPage = ref(1)
const busPageSize = 10
const i2cScanning = ref(false)
const scanResult = ref<string[] | null>(null)
const resourceQuerying = ref(false)
const channelManagerVisible = ref(false)
const baudToolVisible = ref(false)
let pendingResourceRefreshTimeout: ReturnType<typeof setTimeout> | null = null
let resolveResourceRefreshWait: (() => void) | null = null

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

function activateTab(label: string) {
  activeTab.value = label
  if (label === '总线配置' && node.value && !busLoading.value && !busDataLoaded.value) {
    void fetchBusData()
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

const busTabs: Array<{ type: BusType; label: string; icon: any; description: string }> = [
  { type: 'i2c', label: 'I2C', icon: Cpu, description: 'I2C 总线用于连接低速外设，支持多主多从通信' },
  { type: 'uart', label: 'UART', icon: Connection, description: 'UART 串口用于异步串行通信' },
  { type: 'spi', label: 'SPI', icon: MagicStick, description: 'SPI 总线用于高速同步串行通信' },
  { type: 'adc', label: 'ADC', icon: DataLine, description: 'ADC 通道用于采集模拟量输入' },
  { type: 'gpio', label: 'GPIO', icon: Share, description: 'GPIO 是引脚直控资源，不属于通道协议总线' },
  { type: 'pwm', label: 'PWM', icon: Tools, description: 'PWM 资源用于占空比控制输出' },
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
  if (q >= 80) return '#16A34A'
  if (q >= 60) return '#2E6BFF'
  if (q >= 40) return '#D97706'
  return '#EF4444'
})

const lastOnlineText = computed(() => {
  const t = node.value?.last_online_time
  if (!t) return '—'
  return formatTime(t)
})

// 在线时长：从 last_online_time 到现在（每秒走字），离线显示 '-'
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
  const s = node.value?.config_sync_state
  return { in_sync: '已同步', syncing: '同步中', lag: '落后', error: '错误', unknown: '未知' }[s as string] || '未知'
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
function channelName(ch: Channel): string {
  return ch.name || `${(ch.hardware_type || '').toUpperCase()} ${ch.hardware_id || ''}`.trim() || `通道 #${ch.id}`
}
const channelStats = computed(() => {
  const total = channels.value.length
  const ok = channels.value.filter(c => channelStatusText(c) === '正常').length
  const error = channels.value.filter(c => channelStatusText(c) === '异常').length
  return { total, ok, error, other: total - ok - error }
})

function busTypeMask(type: BusType): number {
  return { uart: 1, i2c: 2, spi: 4 }[type] || 0
}

function resourceMountedChannels(resource: BusResource): Channel[] {
  return channels.value.filter(channel => (
    String(channel.hardware_type || '').toLowerCase() === activeBusType.value
    && hardwareResourceMatchesChannel(resource, channel)
  ))
}

function canonicalHardwareId(type: BusType, rawId: unknown): number | null {
  const raw = String(rawId || '').trim()
  if (/^\d+$/.test(raw)) return Number(raw)
  const index = Number(raw.replace(/\D/g, ''))
  if (!Number.isFinite(index)) return null
  const base = { i2c: 1, spi: 10, uart: 20, gpio: 30, adc: 40 }[type]
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
    const baud = Number(resource.baud_rate || resource.max_baud)
    if (baud > 0) values.push(`${baud} baud`)
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
    if (activeTab.value === '总线配置') void fetchBusData()
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
    ElMessage.error(`资源刷新请求失败: ${err.message || '未知错误'}`)
  } finally {
    if (operation === componentOperationGeneration && route.params.id === id) resourceQuerying.value = false
  }
}

async function toggleResourceDma(resource: BusResource, enabled: boolean | string | number) {
  if (nodeOffline.value) return
  const id = route.params.id as string
  const serial = nodeSerial.value
  const desired = Boolean(enabled)
  const bound = resourceDmaBinding(resource)
  const candidate = bound || resourceDmaCandidates(resource).find(dma => isDmaRebindable(dma.state, dma.bound_to))
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
    ElMessage.error(`DMA 配置保存失败: ${err.message || '未知错误'}`)
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
    ElMessage.error(`地址扫描失败: ${err.message || '未知错误'}`)
  } finally {
    if (operation === componentOperationGeneration && route.params.id === id) i2cScanning.value = false
  }
}

function diagnoseSelectedBus() {
  // 服务端没有独立诊断端点；触发真实资源查询，避免 demo 式伪成功。
  void requestBusResourceRefresh()
}

function openBaudTool() {
  baudToolVisible.value = true
}

function openChannelManager(resource: BusResource) {
  if (nodeOffline.value || resource.enabled === false || !busSupportsChannels.value) return
  selectedResourceId.value = resource.id
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
    ElMessage.error('配置同步失败: ' + (err.message || '未知错误'))
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
    ElMessage.error('发送 Ping 失败: ' + (err.message || '未知错误'))
  }
}

function copyId() {
  const id = nodeSerial.value
  navigator.clipboard?.writeText(id).then(() => ElMessage.success('设备 ID 已复制')).catch(() => ElMessage.info(id))
}

async function saveRename() {
  const v = renameDraft.value.trim()
  if (!v) { ElMessage.error('名称不能为空'); return }
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
    ElMessage.error('保存失败: ' + (err.message || '未知错误'))
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
function goToDetail() { router.push(`/node/${nodeSerial.value}`) }

// ── 弹窗初始化 ──
watch(renameVisible, v => { if (v) renameDraft.value = node.value?.name || '' })

// ── 路由切换重置 ──
watch(() => route.params.id, () => {
  detailSequence++
  channelsSequence++
  eventsSequence++
  capabilitiesSequence++
  componentOperationGeneration++
  node.value = null
  channels.value = []
  nodeEvents.value = []
  capabilities.value = { buses: {} }
  busDataLoaded.value = false
  selectedResourceId.value = ''
  scanResult.value = null
  dmaStore.clearCache()
  pinging.value = false
  syncing.value = false
  if (pendingPingTimeout.value) { clearTimeout(pendingPingTimeout.value); pendingPingTimeout.value = null }
  void fetchDetail()
})

// ── 生命周期 ──
onMounted(() => {
  void fetchDetail()
  sessionTimer = setInterval(() => { nowTick.value = Date.now() }, 1000)

  // 节点状态更新（node_status 无延迟字段，仅状态/uptime）
  const unsubStatus = wsStore.subscribe(WS_EVENT.NODE_STATUS, (message: WebSocketMessage) => {
    if (message.payload?.node_id !== nodeSerial.value) return
    if (node.value && message.payload?.status) {
      node.value = {
        ...node.value,
        status: message.payload.status,
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

  unsubscribe = () => { unsubStatus(); unsubPing() }
})

onUnmounted(() => {
  componentOperationGeneration++
  detailSequence++
  channelsSequence++
  eventsSequence++
  capabilitiesSequence++
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
  --no-chip-off-bg: #F2F4F7;
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
  --no-success-bg: rgba(34, 197, 94, 0.15);
  --no-warning-bg: rgba(245, 158, 11, 0.15);
  --no-chip-off-bg: rgba(255, 255, 255, 0.06);
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
.dot-gray { background: #C9D2DE; }

/* ── 页头 ── */
.page-header { display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 16px; gap: 16px; }
.ph-title-row { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.ph-title { font-size: 20px; font-weight: 600; margin: 0; line-height: 28px; }
.ph-edit { color: var(--no-text-muted); cursor: pointer; }
.ph-edit:hover { color: var(--no-primary); }
.badge { font-size: 12px; padding: 2px 10px; border-radius: 11px; display: inline-flex; align-items: center; gap: 5px; }
.badge-green { background: var(--no-success-bg); color: var(--no-success-text); }
.badge-gray { background: var(--no-chip-off-bg); color: var(--no-text-muted); }
.badge .dot { width: 6px; height: 6px; }
.quality { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; color: var(--no-text-secondary); }
.q-val, .q-text { font-weight: 600; }
.ph-id { margin-top: 8px; font-size: 12px; color: var(--no-text-muted); display: flex; align-items: center; gap: 6px; }
.copy-icon { cursor: pointer; color: var(--no-text-muted); }
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
.btn-primary { background: var(--no-primary); color: #fff; }
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
.stat-sep { width: 1px; height: 40px; background: var(--no-border); flex-shrink: 0; }

/* Tab 栏 */
.tab-bar { display: flex; align-items: center; height: 48px; padding: 0 20px; gap: 24px; margin-bottom: 16px; border-bottom: 1px solid var(--no-border); border-radius: 8px 8px 0 0; }
.tab-item {
  display: flex; align-items: center; gap: 6px; height: 48px; padding: 0 4px;
  font-size: 14px; color: var(--no-text-secondary); cursor: pointer; position: relative;
}
.tab-item:hover { color: var(--no-primary); }
.tab-item.active { color: var(--no-primary); font-weight: 500; }
.tab-item.active::after {
  content: ''; position: absolute; left: 0; right: 0; bottom: -1px; height: 2px; background: var(--no-primary);
}

/* Tab 占位 */
.tab-placeholder { display: flex; flex-direction: column; align-items: center; justify-content: center; padding: 64px 20px; gap: 12px; }
.tp-title { font-size: 15px; font-weight: 600; color: var(--no-text-secondary); }
.tp-sub { font-size: 13px; color: var(--no-text-muted); }

/* 总线配置：双栏资源视图（对齐 designs/new-node-2.png） */
.bus-alert { min-height: 40px; display: flex; align-items: center; gap: 8px; padding: 0 14px; margin-bottom: 16px; color: var(--no-text-secondary); background: var(--no-warning-bg); border: 1px solid #FCD98C; border-radius: 6px; font-size: 13px; }
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
.bus-table td { height: 44px; padding: 0 6px; color: var(--no-text); font-size: 13px; font-weight: 400; line-height: 20px; border-bottom: 1px solid var(--no-border-light); white-space: nowrap; }
.bus-table th:nth-child(1), .bus-table td:nth-child(1) { width: 28px; }
.bus-table th:nth-child(2), .bus-table td:nth-child(2) { width: 10%; }
.bus-table th:nth-child(3), .bus-table td:nth-child(3) { width: 18%; }
.bus-table th:nth-child(4), .bus-table td:nth-child(4) { width: 12%; }
.bus-table th:nth-child(5), .bus-table td:nth-child(5) { width: 9%; }
.bus-table th:nth-child(6), .bus-table td:nth-child(6) { width: 13%; }
.bus-table th:nth-child(7), .bus-table td:nth-child(7) { width: 10%; }
.bus-table th:nth-child(8), .bus-table td:nth-child(8) { width: 22%; }
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
.dma-na { color: var(--no-text-muted); }
.bus-row-actions { display: flex; align-items: center; gap: 12px; }
.bus-pagination { min-height: 48px; display: flex; align-items: center; gap: 10px; color: var(--no-text-secondary); font-size: 12px; }
.bus-page-controls { margin-left: auto; display: flex; align-items: center; gap: 6px; }
.bus-page-btn, .bus-page-current { height: 26px; padding: 0 8px; border: 1px solid var(--no-border); border-radius: 5px; background: var(--card-bg, #fff); color: var(--no-text-secondary); font: inherit; }
.bus-page-btn:not(:disabled) { cursor: pointer; }
.bus-page-btn:disabled { opacity: .5; cursor: not-allowed; }
.bus-page-current { display: inline-flex; align-items: center; color: #fff; border-color: var(--no-primary); background: var(--no-primary); }
.bus-tool-layout { display: grid; grid-template-columns: minmax(0, 2fr) minmax(228px, 1fr); gap: 16px; }
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
.card-link { color: var(--no-primary); font-size: 13px; font-weight: 500; line-height: 20px; cursor: pointer; }
.card-link:hover { text-decoration: underline; }
.card-loading { padding: 12px 20px; }
.card-empty { padding: 32px 20px; text-align: center; color: var(--no-text-secondary); font-size: 13px; line-height: 20px; }

/* 设备基本信息 */
.info-grid { display: flex; gap: 32px; padding: 12px 20px 16px; }
.info-col { flex: 1; min-width: 0; }
.info-row { display: flex; align-items: center; height: 36px; font-size: 13px; border-bottom: 1px solid var(--no-border-light); }
.info-col .info-row:last-child { border-bottom: none; }
.info-label { width: 92px; flex-shrink: 0; color: var(--no-text-muted); font-size: 12px; }
.info-val { color: var(--no-text); display: flex; align-items: center; gap: 6px; min-width: 0; }
.info-val.mono { font-family: ui-monospace, monospace; }
.info-val.dim { color: var(--no-text-muted); }
.mini-edit { color: var(--no-text-muted); cursor: pointer; }
.mini-edit:hover { color: var(--no-primary); }
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
.chan-icon {
  width: 28px; height: 28px; border-radius: 6px; background: var(--no-bg-active); color: var(--no-primary);
  display: flex; align-items: center; justify-content: center; flex-shrink: 0;
}
.chan-name { flex: 1; font-size: 13px; font-weight: 500; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.chan-badge { font-size: 12px; height: 22px; line-height: 22px; padding: 0 10px; border-radius: 11px; }
.cb-ok { color: var(--no-success-text); background: var(--no-success-bg); }
.cb-warn { color: var(--no-warning-text); background: var(--no-warning-bg); }
.cb-off { color: var(--no-text-muted); background: var(--no-chip-off-bg); }
.chan-arrow { color: #C9D2DE; }

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
  .stat-sep { display: none; }
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
  .no-breadcrumb { display: none; }
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
}
</style>
