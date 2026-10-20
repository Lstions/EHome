<template>
  <!--
    BMS 设备详情页（2026-10-17 由评审 demo 转正）。

    · 数据全部来自真实后端：设备详情 / 实时流 / 受控操作目录 / 操作历史 / 轮询指令配置。
    · 路由：edge-device/:id 经 EdgeDeviceDetailRouter 按 device_type 分派到本页。
    · 滚动由 MainLayout 的 .el-main.main-content 负责，本页不自建滚动容器。
    · 四条用户诉求（保护明细默认展开 / 流式布局 / 实时流不抢滚动 / 轮询上限）的落点见各区段注释。
  -->
  <div class="bms-detail-page">
    <!-- 页头：设备身份（真实详情） -->
    <header class="page-head">
      <!-- 面包屑的「边缘设备管理」可点击返回列表（旧版由 DeviceHeader 的返回按钮提供；
           本页改为面包屑承载，但必须真的能点 —— .crumb-link 有 cursor:pointer 样式，
           若无 @click 就是**假可点**，比没有更糟）。 -->
      <div class="crumb">
        <el-icon :size="14"><HomeFilled /></el-icon>
        <span class="sep">/</span>
        <button type="button" class="crumb-link" @click="goBackToList">边缘设备管理</button>
        <span class="sep">/</span>
        <span class="crumb-current">BMS 设备详情</span>
      </div>
      <div class="head-main">
        <div class="dev-ident">
          <div class="dev-name-line">
            <h1 class="dev-name">{{ deviceName }}</h1>
            <el-tag :type="statusTag.type" size="small" effect="plain">{{ statusTag.text }}</el-tag>
            <el-tag type="info" size="small" effect="plain">{{ deviceTypeLabel }} · {{ cellCountLabel }}</el-tag>
          </div>
          <div class="dev-sub">
            <template v-if="isMobileViewport">
              <!--
                移动端紧凑版：与桌面版**信息等价**，只是去掉重复的"节点/通道/设备"字样，
                用 # 与 · 分隔。393px 下桌面长文案会折成 3~4 行（51px+），紧凑版压到 2 行。
                保留全部 5 项：节点序列号、总线、设备号、固件、轮询启用数。
              -->
              <span>{{ nodeLabel }}</span>
              <span>·</span><span>{{ channelLabel }}</span>
              <span>·</span><span>#{{ deviceId }}</span>
              <span>·</span><span>FW {{ firmwareLabel }}</span>
              <span>·</span><span>轮询 <b>{{ enabledCount }}<template v-if="hasPollLimit">/{{ pollLimitValue }}</template></b></span>
            </template>
            <template v-else>
              节点 {{ nodeLabel }} · 通道 {{ channelLabel }} · 设备 #{{ deviceId }} · 节点固件 {{ firmwareLabel }}
              <template v-if="hasPollLimit"> · 轮询已启用 <b>{{ enabledCount }}/{{ pollLimitValue }}</b> 条</template>
              <template v-else> · 轮询已启用 <b>{{ enabledCount }}</b> 条</template>
            </template>
          </div>
        </div>
        <!-- 设备操作（2026-10-17 转正时补回）：评审 demo 是视觉稿，没有这些入口；
             生产页必须有 —— 否则用户在本页无法同步到 HA / 编辑 / 删除设备，
             而旧版 BmsDetailPage 是通过 DeviceHeader 提供它们的，属功能回退。 -->
        <div class="head-actions">
          <!--
            移动端只保留「刷新」一个主操作，其余收进 ⋯ 溢出菜单：
            实测 393px 下 4 个按钮各占 44px 触控高，会折成 2~3 行，
            页头从 221px 涨到约 300px，还把设备身份信息挤到看不见。
            桌面端（≥769px）保持原来的 4 按钮横排 —— 契约测试锁定该结构。
          -->
          <template v-if="isMobileViewport">
            <el-button size="small" type="primary" :icon="Refresh" :loading="refreshing" @click="refreshAll">刷新</el-button>
            <el-dropdown trigger="click" @command="onHeadMoreCommand">
              <el-button size="small" :icon="MoreFilled" circle aria-label="更多设备操作" />
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item command="sync" :icon="Upload" :disabled="syncingHA">同步到 HA</el-dropdown-item>
                  <el-dropdown-item command="edit" :icon="Edit">编辑</el-dropdown-item>
                  <el-dropdown-item command="delete" :icon="Delete" divided>
                    <span style="color: var(--el-color-danger);">删除</span>
                  </el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
          </template>
          <template v-else>
            <el-button size="small" :loading="syncingHA" :icon="Upload" @click="onSyncToHA">同步到 HA</el-button>
            <el-button size="small" :icon="Edit" @click="onEditDevice">编辑</el-button>
            <el-button size="small" type="danger" text :icon="Delete" :aria-label="`删除 ${deviceName}`" @click="onDeleteDevice">删除</el-button>
            <el-button size="small" :icon="Refresh" :loading="refreshing" @click="refreshAll">刷新</el-button>
          </template>
        </div>
      </div>
    </header>

    <main class="page-body">
      <!-- 设备详情加载失败：常驻页面，不随 toast 消失 -->
      <el-alert v-if="detailError" type="error" :closable="false" show-icon class="inline-alert page-error">
        <template #title>设备详情加载失败（下方数据可能不完整）</template>
        <div class="alert-detail">{{ detailError }}</div>
      </el-alert>

      <!-- 指标卡 -->
      <section class="row metrics">
        <MetricStatCard label="SOC 剩余电量" :value="fmt(soc, 1)" unit="%" tone="success" :progress="socProgress" sub-text="健康区间 20% – 90%">
          <template #icon><el-icon :size="22"><PieChart /></el-icon></template>
        </MetricStatCard>
        <MetricStatCard label="剩余容量" :value="fmt(remainingCapacity, 2)" unit="Ah" tone="primary" :sub-text="capacitySubText">
          <template #icon><el-icon :size="22"><Odometer /></el-icon></template>
        </MetricStatCard>
        <MetricStatCard label="总电压" :value="fmt(totalVoltage, 2)" unit="V" tone="primary" :sub-text="voltageSubText">
          <template #icon><el-icon :size="22"><DataLine /></el-icon></template>
        </MetricStatCard>
        <MetricStatCard label="电流" :value="fmt(currentDisplay, 2)" unit="A" tone="primary" :sub-text="currentHint">
          <template #icon><el-icon :size="22"><Lightning /></el-icon></template>
        </MetricStatCard>
      </section>

      <!-- 诉求①：MOS 充电/放电直接开关 + 诉求④：保护状态 -->
      <section class="row mos-protect">
        <!-- 诉求① 落点：MOS 直控卡（页首主位，不再埋进受控操作清单） -->
        <div class="card mos-card">
          <div class="card-head">
            <span class="card-title">MOS 直控</span>
            <span class="card-hint">像电灯开关一样直接操作</span>
            <span class="head-right mono-badge">{{ hasFetStatus ? 'fet_status = 0x' + fetHex : 'fet_status = ' + DASH }}</span>
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
                  <span v-if="m.hwOff" class="hw-off-note"><el-icon :size="12"><WarningFilled /></el-icon>可能被硬件保护关断</span>
                </div>
                <div class="mos-desc">{{ m.desc }}</div>
              </div>
              <el-switch
                class="mos-switch"
                :model-value="m.on"
                :loading="!!m.phase"
                :disabled="!!m.phase || !canControlMos"
                :aria-label="m.name + '开关'"
                @change="(v: string | number | boolean) => requestMosChange(m.key, !!v)"
              />
            </div>
            <el-alert v-if="m.error" type="error" :closable="false" show-icon class="inline-alert">
              <template #title>{{ m.name }}操作失败（回读校验不一致）</template>
              <div class="alert-detail">{{ m.error }}</div>
            </el-alert>
          </div>

          <p v-if="!canControlMos" class="card-footnote mos-manual-note">
            当前不可控制：{{ hasFetStatus ? '设备离线（状态 ' + statusTag.text + '），指令无法下发' : '尚无 fet_status 数据' }}。
          </p>

          <p class="card-footnote">
            <b>关断</b>是正常工况而非故障，因此用中性色；只有「保护已触发」才显示告警色。
          </p>
        </div>

        <!-- 诉求④ 落点：保护状态（正常态紧凑一行 / 有保护位时展开 12 项明细） -->
        <div class="card protect-card">
          <div class="card-head">
            <span class="card-title">保护状态</span>
            <el-tag v-if="activeProtections.length" type="danger" size="small">
              {{ activeProtections.length }} 项保护已触发
            </el-tag>
            <el-tag v-else-if="!hasProtectionData" type="info" size="small" effect="plain">暂无数据</el-tag>
            <el-tag v-else type="success" size="small" effect="plain">
              <el-icon :size="12"><CircleCheckFilled /></el-icon> 全部正常
            </el-tag>
            <span class="head-right mono-badge">{{ hasProtectionData ? 'protection_status = 0x' + protHex : 'protection_status = ' + DASH }}</span>
          </div>

          <!-- 无保护命中：紧凑一行。
               ⚠ 2026-10-16 用户要求：**删除收起/展开，明细默认展开**。
               原来正常态只给一行摘要 + 「展开明细」按钮，要用户多点一次才能看到
               12 位明细；而这页的主要用途就是核对保护位，默认藏起来反而妨碍阅读。
               现在 12 项明细恒定可见（见下方 .protect-grid），少一层交互。 -->
          <div
            v-if="!activeProtections.length"
            class="protect-compact"
            :class="{ 'is-all-normal': hasProtectionData }"
            role="status"
          >
            <el-icon :size="18" :color="hasProtectionData ? 'var(--el-color-success)' : 'var(--el-text-color-secondary)'"><CircleCheckFilled /></el-icon>
            <span class="protect-compact-text">{{ hasProtectionData ? '12 项保护均未触发（bit0 – bit11 全 0）' : '暂无 protection_status 数据' }}</span>
          </div>

          <!-- 有保护命中：结论 + 明细 -->
          <template v-else>
            <el-alert type="error" :closable="false" show-icon class="inline-alert">
              <template #title>{{ activeProtections.map(p => p.label).join('、') }} 已触发</template>
              <div class="alert-detail">
                protection_status = 0x{{ protHex }}。硬件保护可能已覆盖对应 MOS：请先排除故障，再到左上方尝试开关。
              </div>
            </el-alert>
          </template>

          <!-- 12 位明细：**恒定展开**（用户要求删除收起/展开）。
               不再依赖 protectExpanded 状态，因此也不会有"默认收起"的信息缺口。 -->
          <div class="protect-grid">
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

      <!-- 电芯电压分布：图表卡是固定高度的"异类"，独占一行 ——
           与内容驱动卡片同行时，矮卡下方必然留出页面底色（padding 无法吸收固定高度差）。 -->
      <section class="row cell-chart-row">
        <div class="card cell-card">
          <div class="card-head">
            <span class="card-title">电芯电压分布</span>
            <span class="card-hint">{{ cells.length }} 串 · 单位 V</span>
            <span v-if="cells.length" class="head-right cell-summary">
              最高 <b class="txt-warning">#{{ maxCell.index }} {{ maxCell.voltage.toFixed(3) }}V</b>
              · 最低 <b class="txt-secondary">#{{ minCell.index }} {{ minCell.voltage.toFixed(3) }}V</b>
              · 压差 <b :class="diff > 0.1 ? 'txt-warning' : 'txt-secondary'">{{ diff.toFixed(3) }}V</b>
            </span>
            <span v-else class="head-right cell-summary">暂无单体电压数据</span>
          </div>
          <div class="cell-chart-host">
            <BmsCellVoltageChart
              v-if="cellVoltages.length"
              :voltages="cellVoltages"
              :cell-count="cellVoltages.length"
              height="300px"
            />
            <div v-else class="cell-chart-empty">暂无单体电压数据</div>
          </div>
          <div class="cell-legend">
            <span class="lg"><i class="swatch swatch-normal"></i>正常</span>
            <span class="lg"><i class="swatch swatch-high"></i>偏高 &gt; 3.600V</span>
            <span class="lg"><i class="swatch swatch-danger"></i>偏低 &lt; 3.000V</span>
            <span class="cell-thresholds">参考线 3.000V / 3.600V</span>
          </div>
          <p class="card-footnote">点击柱体可查看精确值。</p>
        </div>
      </section>

      <!-- 温度探头：内容驱动卡，与 MOS/保护等卡同属流式（auto-fill 列数） -->
      <section class="row temp-row">
        <div class="card temp-card">
          <div class="card-head">
            <span class="card-title">温度探头</span>
            <span class="card-hint">{{ temps.length }} 路</span>
          </div>
          <div class="temp-list">
            <div v-for="t in temps" :key="t.index" class="temp-item">
              <span class="temp-label">探头{{ t.index }}</span>
              <span class="temp-value" :class="'is-' + t.level">{{ t.value.toFixed(1) }} ℃</span>
              <el-tag :type="t.level === 'success' ? 'success' : t.level === 'warning' ? 'warning' : 'danger'" size="small" effect="plain">
                {{ t.text }}
              </el-tag>
            </div>
            <div v-if="!temps.length" class="stream-empty">暂无温度数据</div>
          </div>
          <p class="card-footnote">&gt;45℃ 偏高 · &gt;60℃ 过高</p>
        </div>
      </section>

      <!-- 图表：业务逻辑参考旧 BMS 页面（BmsCellVoltageHistoryChart / HistoryChartSection）重建，
           复用 LineChart / TimeRangeSelector / useHistoryData / useTimeRange / computeAdaptiveYAxisRange。
           容器高度固定，避免内容变化撑变页面高度而破坏 overflow-anchor:none 防跳动。 -->
      <BmsChartsPanel :device-id="deviceId" :cell-count="16" :online="deviceOnline" />

      <!-- 诉求②：实时数据流不抢滚动 + 诉求③：轮询指令上限 -->
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
              <div v-if="!streamRows.length" class="stream-empty">暂无实时数据；设备下一次上报后会自动出现在这里</div>
            </div>
            <transition name="fade">
              <button v-if="!following" type="button" class="back-latest" @click="scrollToLatest">
                <el-icon :size="14"><Bottom /></el-icon>
                回到最新<span v-if="pendingCount">（{{ pendingCount }} 条）</span>
              </button>
            </transition>
          </div>

          <p v-if="snapshotError" class="card-footnote txt-danger">最新快照加载失败：{{ snapshotError }}</p>
          <p class="card-footnote">
            数据来自设备实时上报（新的在前）。向上滚动即暂停跟随，新数据只累积计数；滚回底部或点「回到最新」恢复。
          </p>
        </div>

        <!-- 诉求③ 落点：轮询指令配置（上限由节点上报，超限点名要禁用哪几条） -->
        <div class="card poll-card">
          <div class="card-head">
            <span class="card-title">轮询指令配置</span>
            <el-tag v-if="hasPollLimit" :type="pollBadgeType" size="small">已启用 {{ enabledCount }}/{{ pollLimitValue }}</el-tag>
            <el-tag v-else type="success" size="small">已启用 {{ enabledCount }} 条</el-tag>
          </div>
          <p class="card-note">
            每条指令可单独设间隔，<b>0 = 禁用</b>。
            <template v-if="hasPollLimit">
              固件静态上限为同时最多 <b>{{ pollLimitValue }} 条</b>；超过会被后端整单拒绝（HTTP 400）且不落库 —— 所以这里在保存前就把约束摆出来。
            </template>
            <template v-else>
              本节点未上报轮询条数上限，保存前不显示容量提示（后端仍会在超限时整单拒绝）。
            </template>
          </p>

          <el-alert v-if="pollLoadError" type="error" :closable="false" show-icon class="inline-alert">
            <template #title>轮询指令加载失败</template>
            <div class="alert-detail">
              {{ pollLoadError }}
              <el-button size="small" text type="primary" @click="loadPollCommands">重试</el-button>
            </div>
          </el-alert>

          <el-alert v-if="overLimit" type="warning" :closable="false" show-icon class="inline-alert">
            <template #title>已启用 {{ enabledCount }} 条，超过上限 {{ pollLimitValue }} 条，保存会被拒绝</template>
            <div class="alert-detail">请先禁用 {{ enabledCount - pollLimitValue }} 条：<b>{{ excessNames }}</b>。</div>
          </el-alert>
          <el-alert v-else-if="hasPollLimit && enabledCount === pollLimitValue" type="info" :closable="false" class="inline-alert">
            <template #title>已占满 {{ pollLimitValue }}/{{ pollLimitValue }} 条轮询额度</template>
            <div class="alert-detail">再启用任何一条都会超限；如需调整，请先禁用一条。</div>
          </el-alert>

          <div class="poll-list">
            <div v-for="c in pollCommands" :key="c.id" class="poll-row" :class="{ 'is-over': isExcess(c) }">
              <div class="poll-main">
                <div class="poll-name-line">
                  <span class="poll-name">{{ c.name }}</span>
                  <el-tag v-if="isExcess(c)" type="warning" size="small" effect="dark">超限</el-tag>
                  <el-tag v-else-if="!isPollEnabled(c)" type="info" size="small" effect="plain">已禁用</el-tag>
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
                  :disabled="!isPollEnabled(c)"
                  class="poll-interval"
                  :aria-label="c.name + '轮询间隔（毫秒，0 = 禁用）'"
                  @change="() => onIntervalChange(c)"
                />
                <span class="poll-unit">ms</span>
                <el-switch
                  :model-value="isPollEnabled(c)"
                  size="small"
                  :aria-label="c.name + '启用轮询'"
                  @change="(v: string | number | boolean) => togglePoll(c, !!v)"
                />
                <el-button v-if="isExcess(c)" size="small" @click="togglePoll(c, false)">禁用</el-button>
              </div>
            </div>
            <div v-if="!pollCommands.length && !pollLoadError" class="stream-empty">暂无轮询指令</div>
          </div>

          <!-- 保存被拒：常驻页面（toast 会消失） -->
          <el-alert v-if="pollError" type="error" :closable="false" show-icon class="inline-alert">
            <template #title>保存失败，改动未生效（下方仍是设备当前配置）</template>
            <div class="alert-detail">{{ pollError }}</div>
          </el-alert>

          <div class="poll-foot">
            <el-button type="primary" size="small" :disabled="overLimit || !pollDirty || pollBusy" @click="savePoll">保存</el-button>
            <span class="poll-sync" :class="pollSaveState === 'synced' ? 'is-ok' : overLimit ? 'is-warn' : ''">{{ pollHint }}</span>
          </div>
        </div>
      </section>

      <!-- 受控操作（目录来自后端，按用途分组） + 操作历史 -->
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

          <el-alert v-if="catalogError" type="error" :closable="false" show-icon class="inline-alert">
            <template #title>受控操作目录加载失败</template>
            <div class="alert-detail">
              {{ catalogError }}
              <el-button size="small" text type="primary" @click="loadOperations">重试</el-button>
            </div>
          </el-alert>
          <div v-else-if="catalogLoading" class="stream-empty">正在加载受控操作目录…</div>

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
                      <el-tag :type="op.risky ? 'danger' : 'info'" size="small" effect="plain">{{ op.risky ? '风险' : '普通' }}</el-tag>
                      <el-tag v-if="!op.available" type="warning" size="small" effect="plain">暂不可用</el-tag>
                      <el-tag v-if="op.needsParams" type="info" size="small" effect="plain">需参数</el-tag>
                      <el-tag v-if="op.movedTo" type="primary" size="small">已移至上方直控</el-tag>
                    </div>
                    <div v-if="op.note" class="op-desc">{{ op.note }}</div>
                    <div v-if="!op.available && op.reason" class="op-desc op-desc-warn">不可用原因：{{ op.reason }}</div>
                    <!-- 不再把人支到生产页：本页现在就能填（2026-10-16 用户要求） -->
                    <div v-else-if="op.needsParams" class="op-desc">该操作需要参数，点「填写参数」按后端 Schema 逐项填写后提交。</div>
                  </div>
                  <div class="op-right">
                    <span v-if="opState(op.id).phase !== 'idle'" class="op-state">
                      <i class="dot" :class="stateDot(opState(op.id).phase)"></i>{{ opState(op.id).text }}
                    </span>
                    <el-button v-if="op.movedTo" size="small" @click="scrollToMos">去上方开关</el-button>
                    <el-button
                      v-else
                      size="small"
                      :type="op.risky ? 'warning' : 'primary'"
                      :plain="op.risky"
                      :loading="opBusy(op.id)"
                      :disabled="opBusy(op.id) || !op.available"
                      @click="onOpClick(op)"
                    >
                      {{ !op.available ? '暂不可用' : op.needsParams ? '填写参数' : op.risky ? '确认后执行' : '执行' }}
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
            <span class="card-hint">审计线索 · 最近 {{ historyRows.length }} 条</span>
          </div>
          <div class="history-list">
            <div v-for="h in historyRows" :key="h.id" class="history-item">
              <div class="h-top">
                <span class="h-type">{{ h.type }}</span>
                <el-tag :type="resultTag(h.result)" size="small" effect="plain">{{ resultText(h.result) }}</el-tag>
              </div>
              <div class="h-content">{{ h.content }}</div>
              <div class="h-meta">{{ h.time }}</div>
            </div>
            <div v-if="!historyRows.length" class="history-empty">暂无操作记录</div>
          </div>
        </div>
      </section>
    </main>

    <!-- 近期身份验证（后端 10 分钟窗口，缺失即 403 recent_auth_required） -->
    <el-dialog v-model="reauthVisible" title="验证当前身份" width="420px" :close-on-click-modal="false" append-to-body>
      <p class="dialog-note">该操作属于高风险动作，需要重新验证当前账户密码；验证通过后会自动继续执行。</p>
      <el-input v-model="reauthPassword" type="password" show-password placeholder="请输入当前账户密码" aria-label="当前账户密码" />
      <template #footer>
        <el-button @click="cancelReauth">取消</el-button>
        <el-button type="primary" :loading="reauthBusy" :disabled="!reauthPassword" @click="doReauth">验证并继续</el-button>
      </template>
    </el-dialog>

    <!-- 参数化操作表单：字段由后端 definition.input_schema 生成，与生产页同一组件。
         参数与标定组（30/33 参数的内阻与保护参数表）靠它的双列布局 + 数字感知排序排版。 -->
    <ActionForm
      v-model:visible="opFormVisible"
      :definition="opFormTarget?.definition ?? null"
      :can-read="opFormCanRead"
      :reading="opFormReading"
      :prefill="opFormPrefill"
      @read="readOpFormValues"
      @submit="submitOpForm"
    />

    <!-- 编辑设备名（旧版由 DeviceHeader 内联对话框提供，转正后补回） -->
    <el-dialog v-model="editVisible" title="编辑设备" width="420px">
      <el-form label-width="72px" @submit.prevent>
        <el-form-item label="设备名称">
          <el-input v-model="editName" maxlength="64" show-word-limit placeholder="请输入设备名称" @keyup.enter="submitEditDevice" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="editVisible = false">取消</el-button>
        <el-button type="primary" :loading="editSaving" @click="submitEditDevice">保存</el-button>
      </template>
    </el-dialog>

    <!-- 删除设备（复用共享对话框，与列表页同一交互：可选是否连带删除历史数据） -->
    <DeviceDeleteDialog
      v-model:visible="deleteVisible"
      :device="device"
      :submitting="deleting"
      @confirm="submitDeleteDevice"
    />
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, watch, onMounted, onUnmounted, nextTick } from 'vue'
import type { Component } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  HomeFilled, PieChart, Odometer, DataLine, Lightning, Open, Lock,
  CircleCheckFilled, WarningFilled, Bottom, ArrowRight, InfoFilled,
  Timer, Operation, SetUp, Tools, Document, MagicStick, Refresh,
  Upload, Edit, Delete, MoreFilled,
} from '@element-plus/icons-vue'
import MetricStatCard from '@/components/common/MetricStatCard.vue'
// 参数化操作表单：按后端 input_schema 生成字段（boolean/enum/integer/string），
// 复用生产页同一组件，避免"demo 自己做一套表单"再次造成风格与校验漂移。
import ActionForm from '@/views/edge-device/shared/ActionForm.vue'
import BmsChartsPanel from './BmsChartsPanel.vue'
// 电芯电压分布柱状图：直接复用旧 BMS 页的图表组件（业务逻辑不重写、不漂移）
import BmsCellVoltageChart from '@/views/edge-device/bms/BmsCellVoltageChart.vue'
import { feedback, extractErrorMessage } from '@/utils/feedback'
import { edgeDeviceApi, type EdgeDevice, type CommandTemplateWithInterval } from '@/api/edgeDevice'
import { haApi } from '@/api/homeassistant'
import { assertSessionGeneration, getSessionGeneration } from '@/utils/sessionCache'
import DeviceDeleteDialog from '@/components/device/DeviceDeleteDialog.vue'
import { useRealtimeData } from '@/composables/useRealtimeData'
import { useWebSocketStore } from '@/stores/websocket'
import { useDeviceOperationStore } from '@/stores/deviceOperation'
import { useEdgeDeviceStore } from '@/stores/edgeDevice'
import { useControlAction } from '@/composables/useControlAction'
import type { ActionDefinition, DeviceOperation, EffectiveAction, OperationStatus } from '@/api/deviceOperation'
import { deviceOperationApi } from '@/api/deviceOperation'
import type { DataItem } from '@/types/realtime'

// ════════════ 通用小工具 ════════════
const DASH = '—'
function num(v: unknown): number | null {
  return typeof v === 'number' && Number.isFinite(v) ? v : null
}
function fmt(v: number | null | undefined, digits: number): string {
  return v === null || v === undefined || !Number.isFinite(v) ? DASH : v.toFixed(digits)
}
const pad2 = (n: number) => String(n).padStart(2, '0')
function nowTime(): string {
  const t = new Date()
  return pad2(t.getHours()) + ':' + pad2(t.getMinutes()) + ':' + pad2(t.getSeconds())
}
function formatClock(iso: string): string {
  const d = iso ? new Date(iso) : new Date()
  if (Number.isNaN(d.getTime())) return nowTime()
  return pad2(d.getHours()) + ':' + pad2(d.getMinutes()) + ':' + pad2(d.getSeconds())
}
function formatStamp(iso?: string | null): string {
  if (!iso) return DASH
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return DASH
  return d.getFullYear() + '-' + pad2(d.getMonth() + 1) + '-' + pad2(d.getDate()) + ' '
    + pad2(d.getHours()) + ':' + pad2(d.getMinutes()) + ':' + pad2(d.getSeconds())
}
function hexOf(n: number): string {
  return Math.round(n).toString(16).toUpperCase().padStart(2, '0')
}

// ════════════ 路由 / 设备详情（edgeDeviceApi.getDetail） ════════════
const route = useRoute()
const router = useRouter()
const edgeDeviceStore = useEdgeDeviceStore()
// 生产路由是 edge-device/:id（params），不是 demo 的 ?device= 查询串。
const deviceId = computed(() => Number(route.params.id) || 0)
const device = ref<EdgeDevice | null>(null)
const detailError = ref('')
const refreshing = ref(false)

/**
 * 视口是否窄屏（≤768px）。
 *
 * 为什么用 JS 判定而不是纯 CSS 媒体查询：
 *   页头在窄屏要把「同步到HA / 编辑 / 删除」收进 ⋯ 溢出菜单（少 3 个 DOM 按钮），
 *   这是**结构变化**，CSS 只能隐藏不能减少节点。用 `display:none` 藏起来的按钮
 *   仍会被读屏与 Tab 顺序命中，属于可访问性缺陷。
 *   JS 判定后按结构渲染是干净的（与 DeviceHeader.vue 既有做法一致）。
 *
 * SSR/测试环境没有 window.matchMedia 时按桌面处理（宽屏），保证契约测试稳定。
 */
const MOBILE_MAX = 768
const isMobileViewport = ref(
  typeof window !== 'undefined' && typeof window.matchMedia === 'function'
    ? window.matchMedia(`(max-width: ${MOBILE_MAX}px)`).matches
    : false,
)
let mobileMql: MediaQueryList | null = null
const onMobileChange = (e: MediaQueryListEvent) => { isMobileViewport.value = e.matches }

/** 页头 ⋯ 菜单的命令分发（移动端专用；桌面端各按钮直连原 handler） */
const onHeadMoreCommand = (command: string) => {
  if (command === 'sync') onSyncToHA()
  else if (command === 'edit') onEditDevice()
  else if (command === 'delete') onDeleteDevice()
}

const deviceName = computed(() => device.value?.name || DASH)
const deviceTypeLabel = computed(() => device.value?.device_type || DASH)
const firmwareLabel = computed(() => device.value?.node?.firmware_version || DASH)
const nodeLabel = computed(() => {
  const n = device.value?.node
  if (n?.name) return n.name
  const id = device.value?.node_id
  return id === undefined || id === null || id === '' ? DASH : String(id)
})
// ⚠ 通道名只能取 channel_hardware_id（总线名）；hardware_id 是设备从站地址，语义不同。
const channelLabel = computed(() => device.value?.channel_hardware_id || DASH)
const deviceOnline = computed(() => {
  const s = device.value?.status
  return s === 'active' || s === 'online'
})
type TagType = 'primary' | 'success' | 'info' | 'warning' | 'danger'
const DEVICE_STATUS_MAP: Record<string, { type: TagType; text: string }> = {
  active: { type: 'success', text: '在线' },
  online: { type: 'success', text: '在线' },
  offline: { type: 'info', text: '离线' },
  warning: { type: 'warning', text: '告警' },
  error: { type: 'danger', text: '故障' },
  disabled: { type: 'info', text: '已停用' },
  pending: { type: 'info', text: '待激活' },
  initializing: { type: 'info', text: '初始化中' },
  unknown: { type: 'info', text: '状态未知' },
}
const statusTag = computed(() => DEVICE_STATUS_MAP[device.value?.status ?? 'unknown'] ?? DEVICE_STATUS_MAP.unknown)

async function loadDetail() {
  try {
    device.value = await edgeDeviceApi.getDetail(deviceId.value)
    detailError.value = ''
  } catch (e) {
    detailError.value = extractErrorMessage(e, '设备详情加载失败')
  }
}

// ════════════ 实时数据（useRealtimeData） ════════════
const REALTIME_MAX_ITEMS = 200
// 本页是独立 DEV 路由（不在 MainLayout 之下，而 WS 连接由 MainLayout 建立）。
// 不在这里补一次 connect()，useRealtimeData 就永远收不到推送 —— 实时流会只剩快照兜底。
const wsStore = useWebSocketStore()
const {
  dataItems, latestData, clear: clearRealtime,
} = useRealtimeData({ deviceId, maxItems: REALTIME_MAX_ITEMS })

/** 挂载时用最新快照兜底（WS 尚无推送时不至于整页空白），口径同 useDeviceData。 */
async function loadLatestSnapshot() {
  snapshotError.value = ''
  try {
    const response = await edgeDeviceApi.getLatestData(deviceId.value)
    if (!response) return
    let parsed: Record<string, any> = response.data || {}
    if (response.data_json) {
      try {
        const dj = JSON.parse(response.data_json)
        if (dj.sensors && Array.isArray(dj.sensors)) {
          parsed = {}
          for (const s of dj.sensors) {
            // StringValue 优先：字符串型读数的真值在 StringValue 里，Value 恒为 0。
            const sv = s?.StringValue
            parsed[s.Name] = (typeof sv === 'string' && sv.trim() !== '') ? sv : s.Value
          }
        }
      } catch { /* 解析失败时退回 data 字段 */ }
    }
    latestData.value = { ...(latestData.value ?? {}), ...parsed }
    const item: DataItem = {
      id: 'latest-' + Date.now(),
      timestamp: response.created_at || new Date().toISOString(),
      data: parsed,
      rawData: response.raw_data,
      isRealtime: false,
    }
    dataItems.value = [item, ...dataItems.value].slice(0, REALTIME_MAX_ITEMS)
  } catch (e) {
    // 快照只是兜底：失败不弹 toast，但原因常驻页面（WS 仍会带来数据）。
    snapshotError.value = extractErrorMessage(e, '最新快照加载失败')
  }
}
const snapshotError = ref('')

// ── 字段口径：单体电压 cell_voltage_N / 温度 temp_N（或 temperature_N） ──
const cellCount = computed(() => {
  const declared = num(latestData.value?.cell_count)
  if (declared && declared > 0) return Math.min(32, Math.floor(declared))
  let highest = 0
  for (let i = 1; i <= 32; i++) {
    if (num(latestData.value?.['cell_voltage_' + i] ?? latestData.value?.['cell_v_' + i]) !== null) highest = i
  }
  return highest
})
const cellCountLabel = computed(() => (cellCount.value > 0 ? cellCount.value + 'S' : DASH))

interface CellVoltage { index: number; voltage: number }
const cells = computed<CellVoltage[]>(() => {
  const out: CellVoltage[] = []
  for (let i = 1; i <= cellCount.value; i++) {
    const v = num(latestData.value?.['cell_voltage_' + i] ?? latestData.value?.['cell_v_' + i])
    if (v !== null) out.push({ index: i, voltage: v })
  }
  return out
})
const cellSum = computed(() => cells.value.reduce((a, b) => a + b.voltage, 0))
const totalVoltage = computed(() => {
  const declared = num(latestData.value?.total_voltage ?? latestData.value?.voltage)
  if (declared !== null) return declared
  return cells.value.length ? cellSum.value : null
})
const avgCell = computed(() => (totalVoltage.value !== null && cells.value.length ? totalVoltage.value / cells.value.length : null))
const maxCell = computed<CellVoltage>(() =>
  cells.value.length ? cells.value.reduce((a, b) => (b.voltage > a.voltage ? b : a)) : { index: 0, voltage: 0 })
const minCell = computed<CellVoltage>(() =>
  cells.value.length ? cells.value.reduce((a, b) => (b.voltage < a.voltage ? b : a)) : { index: 0, voltage: 0 })
const diff = computed(() => maxCell.value.voltage - minCell.value.voltage)
/** 供旧页 BmsCellVoltageChart 使用的纯数值序列 */
const cellVoltages = computed(() => cells.value.map((c) => c.voltage))
const soc = computed(() => num(latestData.value?.rsoc ?? latestData.value?.soc))
const socProgress = computed(() => (soc.value === null ? undefined : Math.max(0, Math.min(100, Math.round(soc.value)))))
const nominalCapacity = computed(() => num(latestData.value?.nominal_capacity))
const remainingCapacity = computed(() => {
  const declared = num(latestData.value?.remaining_capacity)
  if (declared !== null) return declared
  if (soc.value !== null && nominalCapacity.value !== null) return (soc.value / 100) * nominalCapacity.value
  return null
})
const cycleCount = computed(() => num(latestData.value?.cycle_count))
const currentDisplay = computed(() => num(latestData.value?.current))
const currentHint = computed(() => {
  const c = currentDisplay.value
  if (c === null) return '暂无电流数据'
  if (c < 0) return '放电中（电流为负）'
  if (c > 0) return '充电中（电流为正）'
  return '无电流'
})
const voltageSubText = computed(() => {
  if (cells.value.length) return cells.value.length + ' 串 · 平均 ' + fmt(avgCell.value, 3) + 'V'
  if (cellCount.value) return cellCount.value + ' 串 · 单体电压未上报'
  return DASH
})
const capacitySubText = computed(() => '/ ' + fmt(nominalCapacity.value, 0) + ' Ah 标称')

// ── 温度探头 ──
interface TempProbe { index: number; value: number; level: 'success' | 'warning' | 'danger'; text: string }
function tempLevel(v: number): 'success' | 'warning' | 'danger' {
  return v > 60 ? 'danger' : v > 45 ? 'warning' : 'success'
}
function tempText(v: number): string {
  return v > 60 ? '过高' : v > 45 ? '偏高' : '正常'
}
const temps = computed<TempProbe[]>(() => {
  const out: TempProbe[] = []
  for (let i = 1; i <= 8; i++) {
    const v = num(latestData.value?.['temp_' + i] ?? latestData.value?.['temperature_' + i])
    if (v !== null) out.push({ index: i, value: v, level: tempLevel(v), text: tempText(v) })
  }
  return out
})

// 电芯电压分布改由旧页 BmsCellVoltageChart 渲染（自适应 Y 轴 + 上下限标注 + 标签降级），
// 页面不再自带一套手绘柱状图。

// ════════════ 诉求①：MOS 直控（真机 fet_status + set_mos_policy） ════════════
const opStore = useDeviceOperationStore()
const historyOps = computed<DeviceOperation[]>(() => opStore.histories.get(deviceId.value) ?? [])
const catalog = computed<EffectiveAction[]>(() => opStore.catalogs.get(deviceId.value) ?? [])

const mosPhase = reactive<Record<'charge' | 'discharge', string>>({ charge: '', discharge: '' })
const mosError = reactive<Record<'charge' | 'discharge', string>>({ charge: '', discharge: '' })

/**
 * 最近一次**真正携带** fet_status 的值（2026-10-15 实测缺陷修复）。
 *
 * 根因：latestData 是"**任意指令的最新一帧**"，不是"0x03 基本信息帧"。
 *   设备按各指令自己的间隔轮流上报，实测 60 次采样的帧分布：
 *     0x03 读取基本信息 45%（唯一含 fet_status）
 *     0xAA 保护次数 32% / 0x05 硬件版本 8% / 0x0F 综合 8% / 0x04 单体 7%（均不含）
 *   ⇒ 约 **47% 的时间**当前帧根本没有 fet_status。
 *
 * 改前把"本帧没这个字段"当成"MOS 关断"（effectiveFet 变 null ⇒ chargeOn=false），
 * 于是开关每几秒自己翻转，并在设备实际充电时显示「充电 MOS 关断」——
 * 这是**危险的信息错误**：用户会据此判断电池没在充电。
 *
 * 正确语义：该帧对 MOS **没有信息** ⇒ 保留上一次已知值，而不是解释为 false。
 * 启用 5 条轮询后非 0x03 帧占比升高，把该缺陷从偶发放大成显性。
 *
 * （另有一个模拟器侧缺陷已同时修复：0x0F 帧曾硬编码 fet_status=0，
 *   而 0x0F 是 0x03 的超集、同样会被解析 ⇒ 两条指令对同一字段给出矛盾取值。）
 */
const lastFet = ref<number | null>(null)
watch(() => latestData.value?.fet_status, (v) => {
  const n = num(v)
  if (n !== null) lastFet.value = n
}, { immediate: true })
// 设备切换时清空，避免把上一台设备的 MOS 状态当成新设备的
watch(deviceId, () => { lastFet.value = null })

/** fetStatus：优先本帧，其次最近一次携带该字段的帧（保证展示的是**已知**状态而非默认值） */
const fetStatus = computed(() => num(latestData.value?.fet_status) ?? lastFet.value ?? 0)
const hasFetStatus = computed(() => num(latestData.value?.fet_status) !== null || lastFet.value !== null)
const fetHex = computed(() => hexOf(fetStatus.value))

/**
 * 操作回执带来的**权威** fet_status（时序差桥接）。
 * 周期快照最长落后约 8s，若只等快照，会出现「报成功了开关却没动」。成功回执里带着
 * 写后回读的 fet_status，用它立即桥接；一旦周期帧也报出同一个值即撤掉桥接（自愈）。
 */
const verifiedFet = ref<number | null>(null)
watch(() => latestData.value?.fet_status, (live) => {
  if (verifiedFet.value !== null && num(live) === verifiedFet.value) verifiedFet.value = null
})
/** 优先操作回执的权威值；否则用 fetStatus（已含 lastFet 兜底，不会因缺字段而塌陷） */
const effectiveFet = computed(() => verifiedFet.value ?? (hasFetStatus.value ? fetStatus.value : null))
const chargeOn = computed(() => effectiveFet.value !== null && (effectiveFet.value & 1) !== 0)
const dischargeOn = computed(() => effectiveFet.value !== null && (effectiveFet.value & 2) !== 0)
function fetTextOf(fet: number): string {
  const c = (fet & 1) !== 0
  const d = (fet & 2) !== 0
  if (c && d) return '充放导通'
  if (c) return '仅充电导通'
  if (d) return '仅放电导通'
  return '全部关断'
}
const canControlMos = computed(() => hasFetStatus.value && deviceOnline.value)

const mosRows = computed(() => [
  {
    key: 'charge' as const,
    name: '充电 MOS',
    on: chargeOn.value,
    phase: mosPhase.charge,
    error: mosError.charge,
    hwOff: hasFetStatus.value && !chargeOn.value && protectionActive(0),
    desc: chargeOn.value ? '允许充电 · fet_status bit0 = 1' : '禁止充电 · fet_status bit0 = 0',
  },
  {
    key: 'discharge' as const,
    name: '放电 MOS',
    on: dischargeOn.value,
    phase: mosPhase.discharge,
    error: mosError.discharge,
    hwOff: hasFetStatus.value && !dischargeOn.value && (protectionActive(9) || protectionActive(10)),
    desc: dischargeOn.value ? '允许放电 · fet_status bit1 = 1' : '禁止放电 · fet_status bit1 = 0',
  },
])

/**
 * ⚠ 极性换算（唯一一处，模板/参数里禁止手写取反）。
 *
 * 协议参数字段是 charge_software_closed / discharge_software_closed —— 语义为
 * 「**软件关闭**位」；fet_status 的位语义是「**导通** = 1」，两者**反相**：
 * 要让 MOS 导通，必须把 software_closed 置 **false**。
 */
function swClosed(conducting: boolean): boolean {
  return !conducting
}

let mosKind: 'charge' | 'discharge' | null = null
let activeCommandId = ''

const ca = useControlAction(() => deviceId.value)
const { lastError, reauthVisible, reauthPassword, reauthBusy, dispatch, retryAfterReauth, cancelReauth: cancelReauthAction, clearError } = ca

type ReauthTarget = { type: 'mos'; which: 'charge' | 'discharge' } | { type: 'op'; id: string } | null
let reauthTarget: ReauthTarget = null

async function requestMosChange(which: 'charge' | 'discharge', target: boolean) {
  if (mosPhase[which] !== '' || !canControlMos.value) return
  const currentOn = which === 'charge' ? chargeOn.value : dischargeOn.value
  if (currentOn === target) return
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
  mosPhase[which] = '下发中'
  mosError[which] = ''
  mosKind = which
  clearError()
  // 两个位一起提交（协议要求两个字段都必填）：未操作的那个保持当前实际状态。
  const ok = await dispatch('set_mos_policy', {
    charge_software_closed: swClosed(which === 'charge' ? target : chargeOn.value),
    discharge_software_closed: swClosed(which === 'discharge' ? target : dischargeOn.value),
    priority: 'user',
  }, true)
  if (ok) {
    mosPhase[which] = '已发送'
    reauthTarget = null
    trackCommand('set_mos_policy')
  } else if (reauthVisible.value) {
    reauthTarget = { type: 'mos', which }
  } else {
    mosPhase[which] = ''
    mosError[which] = lastError.value || 'MOS 指令未能提交'
    feedback.error(name + '指令未能提交：' + (lastError.value || '未知原因'))
  }
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
const hasProtectionData = computed(() => num(latestData.value?.protection_status) !== null)
const protectionStatus = computed(() => num(latestData.value?.protection_status) ?? 0)
const protHex = computed(() => hexOf(protectionStatus.value))
// （2026-10-16 已删除 protectExpanded：用户要求保护明细默认展开、不留收起/展开交互）
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
  id: string
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
let injectTimer: ReturnType<typeof setInterval> | null = null
const seenItemIds = new Set<string>()

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
  // 需求：清空走 composable 的 clear()。
  clearRealtime()
  seenItemIds.clear()
  streamRows.value = []
  pendingCount.value = 0
  following.value = true
  // clear() 会同时清空 latestData（指标卡会短暂空白），这里立即用最新快照补回。
  void loadLatestSnapshot()
}

const LEVEL_TO_STATUS: Record<'success' | 'warning' | 'danger', StreamRow['status']> = {
  success: 'normal', warning: 'warning', danger: 'danger',
}

/** 把一条真实上报（DataItem）摊平成数据流行；只展示有真实值且对现场有意义的字段。 */
function rowsForItem(item: DataItem): StreamRow[] {
  const d = item.data ?? {}
  const t = formatClock(item.timestamp)
  const rows: StreamRow[] = []
  const push = (label: string, value: string, unit: string, status: StreamRow['status'], statusText: string) => {
    streamSeq++
    rows.push({ id: 's' + streamSeq, time: t, item: label, value, unit, status, statusText })
  }
  const tv = num(d.total_voltage)
  if (tv !== null) push('总电压', tv.toFixed(2), 'V', 'normal', '正常')
  const cur = num(d.current)
  if (cur !== null) push('电流', cur.toFixed(2), 'A', 'normal', cur < 0 ? '放电' : cur > 0 ? '充电' : '待机')
  const s = num(d.rsoc ?? d.soc)
  if (s !== null) push('SOC', s.toFixed(1), '%', 'normal', '正常')
  const fet = num(d.fet_status)
  if (fet !== null) push('MOS 状态', '0x' + hexOf(fet), '', 'normal', fetTextOf(fet))
  const ps = num(d.protection_status)
  if (ps !== null && ps > 0) {
    push('保护状态', '0x' + hexOf(ps), '', 'danger', protectionSummary(ps))
  }
  const itemCells: number[] = []
  for (let i = 1; i <= 32; i++) {
    const v = num(d['cell_voltage_' + i] ?? d['cell_v_' + i])
    if (v !== null) itemCells.push(v)
  }
  if (itemCells.length) {
    const mx = Math.max(...itemCells)
    const mn = Math.min(...itemCells)
    push('单体最高 #' + (itemCells.indexOf(mx) + 1), mx.toFixed(3), 'V', mx > 3.6 ? 'danger' : 'normal', mx > 3.6 ? '越限' : '正常')
    push('单体最低 #' + (itemCells.indexOf(mn) + 1), mn.toFixed(3), 'V', mn < 2.5 ? 'danger' : 'normal', mn < 2.5 ? '越限' : '正常')
  }
  for (let i = 1; i <= 4; i++) {
    const v = num(d['temp_' + i] ?? d['temperature_' + i])
    if (v !== null) push('温度 探头' + i, v.toFixed(1), '℃', LEVEL_TO_STATUS[tempLevel(v)], tempText(v))
  }
  return rows
}

function protectionSummary(mask: number): string {
  let n = 0
  for (let b = 0; b < 12; b++) if ((mask & (1 << b)) !== 0) n++
  return n + ' 项触发'
}

function syncStreamFromItems(items: DataItem[]) {
  if (!items.length) return
  const unseen = items.filter((it) => !seenItemIds.has(it.id))
  if (!unseen.length) return
  for (const it of unseen) seenItemIds.add(it.id)
  // dataItems 新的在前；按时间正序追加，保证「最新的在底部」。
  const rows: StreamRow[] = []
  for (const it of [...unseen].reverse()) rows.push(...rowsForItem(it))
  if (rows.length) appendRows(rows)
}
watch(dataItems, (items) => syncStreamFromItems(items))

/** 「高频注入」：本地压测行，每秒推 1 条，用于人工验证滚动不被抢（不代表设备数据）。 */
function injectOne() {
  const t = nowTime()
  const slot = streamSeq % 4
  let row: Omit<StreamRow, 'id' | 'time'>
  if (slot === 0 && cells.value.length) {
    const c = cells.value[Math.floor(Math.random() * cells.value.length)]
    row = { item: '单体电压 #' + c.index, value: c.voltage.toFixed(3), unit: 'V', status: c.voltage > 3.6 ? 'danger' : 'normal', statusText: c.voltage > 3.6 ? '越限' : '正常' }
  } else if (slot === 1 && temps.value.length) {
    const tp = temps.value[Math.floor(Math.random() * temps.value.length)]
    row = { item: '温度 探头' + tp.index, value: tp.value.toFixed(1), unit: '℃', status: LEVEL_TO_STATUS[tp.level], statusText: tp.text }
  } else if (slot === 2 && remainingCapacity.value !== null) {
    row = { item: '剩余容量', value: remainingCapacity.value.toFixed(2), unit: 'Ah', status: 'normal', statusText: '正常' }
  } else {
    row = { item: '循环次数', value: cycleCount.value !== null ? String(cycleCount.value) : DASH, unit: '次', status: 'normal', statusText: '正常' }
  }
  streamSeq++
  appendRows([{ id: 'i' + streamSeq, time: t, ...row }])
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

// ════════════ 诉求③：轮询指令配置（上限由节点上报） ════════════
interface PollCommand { id: string; name: string; desc: string; interval: number; defaultInterval: number }
const pollCommands = ref<PollCommand[]>([])
const pollBaseline = ref('[]')
const pollSaveState = ref<'idle' | 'saving' | 'synced'>('idle')
const pollError = ref('')
const pollLoadError = ref('')
const pollBusy = computed(() => pollSaveState.value === 'saving')

const isPollEnabled = (c: PollCommand) => c.interval > 0
const enabledCount = computed(() => pollCommands.value.filter(isPollEnabled).length)
/** 上限来自设备详情（后端随 GET /edge-devices/:id 下发），缺失时不显示任何上限提示。 */
const pollLimit = computed(() => num(device.value?.max_polling_commands))
const pollLimitValue = computed(() => pollLimit.value ?? 0)
const hasPollLimit = computed(() => pollLimit.value !== null && pollLimit.value > 0)
const overLimit = computed(() => hasPollLimit.value && enabledCount.value > (pollLimit.value as number))
const excessIds = computed(() => {
  if (!overLimit.value) return new Set<string>()
  return new Set(pollCommands.value.filter(isPollEnabled).slice(pollLimit.value as number).map((c) => c.id))
})
const excessNames = computed(() =>
  pollCommands.value.filter((c) => excessIds.value.has(c.id)).map((c) => '「' + c.name + '」').join('、'))
function isExcess(c: PollCommand): boolean {
  return isPollEnabled(c) && excessIds.value.has(c.id)
}
const pollBadgeType = computed<TagType>(() => {
  if (!hasPollLimit.value) return 'success'
  if (overLimit.value) return 'danger'
  return enabledCount.value === pollLimit.value ? 'warning' : 'success'
})

function snapshotPoll(): string {
  return JSON.stringify(pollCommands.value.map((c) => [c.id, c.interval]))
}
const pollDirty = computed(() => snapshotPoll() !== pollBaseline.value)
const pollHint = computed(() => {
  if (overLimit.value) return '超限 ' + enabledCount.value + '/' + pollLimit.value + ' 条，修正后才能保存'
  switch (pollSaveState.value) {
    case 'saving': return '保存中…'
    case 'synced': return '已保存'
    default: return pollDirty.value ? '有未保存改动' : '当前配置与设备一致'
  }
})

async function loadPollCommands() {
  pollLoadError.value = ''
  pollError.value = ''
  try {
    const cmds = await edgeDeviceApi.getCommandIntervals(deviceId.value)
    pollCommands.value = cmds.filter((c: CommandTemplateWithInterval) => c.schedulable).map((c: CommandTemplateWithInterval) => ({
      id: c.id,
      name: c.name,
      desc: c.description || '',
      interval: c.current_interval_ms,
      defaultInterval: c.interval_ms > 0 ? c.interval_ms : 5000,
    }))
    pollBaseline.value = snapshotPoll()
    pollSaveState.value = 'idle'
  } catch (e) {
    pollCommands.value = []
    pollLoadError.value = extractErrorMessage(e, '轮询指令加载失败')
  }
}

function togglePoll(c: PollCommand, on: boolean) {
  c.interval = on ? (c.defaultInterval || 5000) : 0
  markPollEdited()
}
function onIntervalChange(c: PollCommand) {
  c.interval = !Number.isFinite(c.interval) || c.interval <= 0 ? 0 : Math.min(Math.floor(c.interval), 60000)
  markPollEdited()
}
function markPollEdited() {
  pollError.value = ''
  if (pollSaveState.value === 'synced') pollSaveState.value = 'idle'
}
async function savePoll() {
  if (overLimit.value || !pollDirty.value || pollBusy.value) return
  pollSaveState.value = 'saving'
  pollError.value = ''
  try {
    const intervals: Record<string, number> = {}
    for (const c of pollCommands.value) intervals[c.id] = c.interval
    await edgeDeviceApi.updateCommandIntervals(deviceId.value, intervals)
    pollBaseline.value = snapshotPoll()
    pollSaveState.value = 'synced'
    feedback.success('轮询配置已保存')
  } catch (e) {
    pollSaveState.value = 'idle'
    // 拒绝原因必须**常驻页面**：toast 会消失，而"改完保存、刷新后没变化"正是因为它没被留住。
    pollError.value = extractErrorMessage(e, '轮询配置保存失败')
    feedback.handleError(e, '轮询配置保存失败')
  }
}

// ════════════ 受控操作（目录来自后端 catalogs） ════════════
interface OpDef {
  id: string
  name: string
  note: string
  risky: boolean
  requiresConfirmation: boolean
  available: boolean
  reason: string
  needsParams: boolean
  movedTo: boolean
  /** 后端动作定义，参数化表单据此生成字段（input_schema） */
  definition: ActionDefinition
}
interface OpGroup { id: string; name: string; hint: string; icon: Component; ops: OpDef[] }
type OpPhase = 'idle' | 'sent' | 'running' | 'verifying' | 'success' | 'failed' | 'unknown' | 'cancelled'
interface OpState { phase: OpPhase; text: string; reason: string }

/** 分组只是展示层归属（目录本身不含分组），成员按 action id 归属；未列出的归入「其他操作」。 */
const OP_GROUP_DEFS: Array<{ id: string; name: string; hint: string; icon: Component; ids: string[] }> = [
  {
    id: 'read', name: '读取信息', hint: '一次性读取，不改设备配置', icon: Document,
    ids: ['read_basic_info', 'read_cell_voltage', 'read_hardware_version', 'read_comprehensive', 'read_protection_count', 'read_custom_attributes', 'read_test_mos_status'],
  },
  {
    id: 'diag', name: '诊断与测试', hint: '不写入参数，用于现场排障', icon: Tools,
    ids: ['find_car', 'test_charge_mos', 'test_discharge_mos'],
  },
  {
    id: 'runtime', name: '运行控制', hint: '直接影响设备当前运行状态', icon: Operation,
    ids: ['set_mos_policy', 'force_balance', 'clear_alarm', 'auto_test_edv', 'bms_restart'],
  },
  {
    id: 'param', name: '参数与标定', hint: '读取 / 写入厂内参数，需进厂模式', icon: SetUp,
    ids: ['read_protection_parameters', 'read_system_parameters', 'write_protection_parameters', 'write_system_parameters', 'write_custom_attributes', 'write_internal_resistance', 'write_sn'],
  },
  {
    id: 'timer', name: '时间与上报设置', hint: '改变设备行为参数', icon: Timer,
    ids: ['set_static_correction_time', 'set_report_interval', 'set_charge_time_window', 'set_discharge_time_limit'],
  },
]

function toOp(action: EffectiveAction): OpDef {
  const def = action.definition
  const properties = Object.keys(def.input_schema?.properties ?? {})
  return {
    id: def.id,
    name: def.name,
    note: def.description || '',
    risky: def.risk_level === 'elevated',
    requiresConfirmation: def.requires_confirmation === true,
    available: action.available,
    reason: action.reason || '',
    needsParams: properties.length > 0,
    movedTo: def.id === 'set_mos_policy',
    definition: def,
  }
}

const opGroups = computed<OpGroup[]>(() => {
  if (!catalog.value.length) return []
  const byId = new Map(catalog.value.map((a) => [a.definition.id, a]))
  const claimed = new Set<string>()
  const groups: OpGroup[] = []
  for (const g of OP_GROUP_DEFS) {
    const ops = g.ids
      .map((id) => byId.get(id))
      .filter((a): a is EffectiveAction => !!a)
      .map(toOp)
    if (!ops.length) continue
    for (const o of ops) claimed.add(o.id)
    groups.push({ id: g.id, name: g.name, hint: g.hint, icon: g.icon, ops })
  }
  const rest = catalog.value.filter((a) => !claimed.has(a.definition.id)).map(toOp)
  if (rest.length) groups.push({ id: 'other', name: '其他操作', hint: '目录中未归类的操作', icon: Operation, ops: rest })
  return groups
})
const totalOps = computed(() => catalog.value.length)
const openGroups = reactive<Record<string, boolean>>({ runtime: true })
function toggleGroup(id: string) {
  openGroups[id] = !openGroups[id]
}
function riskCount(g: OpGroup): number {
  return g.ops.filter((o) => o.risky).length
}
function catalogName(actionId: string): string {
  const hit = catalog.value.find((a) => a.definition.id === actionId)
  return hit?.definition.name || actionId
}

const opStates = reactive<Record<string, OpState>>({})
// 预初始化：render 期间不写响应式状态（避免渲染中改 dep 触发递归更新）。
watch(catalog, (list) => {
  for (const a of list) {
    if (!opStates[a.definition.id]) opStates[a.definition.id] = { phase: 'idle', text: '', reason: '' }
  }
}, { immediate: true })
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
    case 'cancelled': return 'dot-orange'
    default: return 'dot-blue'
  }
}

/** 执行状态 → 中文（不暴露 QUEUED/VERIFYING 等内部枚举）。 */
const OPERATION_UI: Record<OperationStatus, { phase: OpPhase; text: string }> = {
  QUEUED: { phase: 'sent', text: '已发送' },
  DISPATCHED: { phase: 'running', text: '已下发' },
  DEVICE_ACCEPTED: { phase: 'running', text: '设备已收到' },
  VERIFYING: { phase: 'verifying', text: '校验中' },
  SUCCEEDED: { phase: 'success', text: '成功' },
  FAILED: { phase: 'failed', text: '失败' },
  UNKNOWN: { phase: 'unknown', text: '结果未知' },
  CANCELLED: { phase: 'cancelled', text: '已取消' },
}

const notified = new Set<string>()
function notifyOnce(op: DeviceOperation, fn: () => void) {
  const key = op.command_id + ':' + op.status
  if (notified.has(key)) return
  notified.add(key)
  fn()
}

const TERMINAL: OperationStatus[] = ['SUCCEEDED', 'FAILED', 'UNKNOWN', 'CANCELLED']

/** 把一条真实操作回执映射到页面状态（失败原因常驻，成功用权威回读桥接）。 */
function applyOperationToUi(op: DeviceOperation) {
  const ui = OPERATION_UI[op.status]
  const terminal = TERMINAL.includes(op.status)

  if (op.action_id === 'set_mos_policy') {
    const kind = mosKind
    if (kind) mosPhase[kind] = terminal ? '' : ui.text
    notifyOnce(op, () => {
      const label = kind === 'charge' ? '充电 MOS' : '放电 MOS'
      if (op.status === 'SUCCEEDED') {
        if (kind) mosError[kind] = ''
        // 用回执里的权威 fet_status 立即桥接，抹掉"成功但开关没动"的窗口。
        const verified = (op.verified_result ?? []).find((v) => v.name === 'fet_status')
        if (typeof verified?.value === 'number') verifiedFet.value = verified.value
        feedback.success(label + '操作成功（已回读校验）')
      } else if (op.status === 'FAILED') {
        const reason = readableFinalReason(op.final_reason) || '设备未接受或回读不一致'
        if (kind) mosError[kind] = reason
        feedback.error(label + '操作失败：' + reason)
      } else if (op.status === 'UNKNOWN') {
        if (kind) mosError[kind] = '操作结果未知；请先核对当前 MOS 状态，勿重复提交。'
        feedback.warning(label + '操作结果未知，请核对当前状态')
      } else if (op.status === 'CANCELLED') {
        if (kind) mosError[kind] = '操作已取消'
        feedback.warning(label + '操作已取消')
      }
    })
    return
  }

  const st = opState(op.action_id)
  if (op.status === 'SUCCEEDED') {
    setPhase(op.action_id, 'success', '成功')
    st.reason = ''
  } else if (op.status === 'FAILED') {
    setPhase(op.action_id, 'failed', '失败')
    st.reason = readableFinalReason(op.final_reason) || '设备未接受或回读校验不一致'
  } else if (op.status === 'UNKNOWN') {
    setPhase(op.action_id, 'unknown', '结果未知')
    st.reason = '观察类操作无法以 ACK 判定成功；请等待设备重新上线后在设备详情页确认。'
  } else if (op.status === 'CANCELLED') {
    setPhase(op.action_id, 'cancelled', '已取消')
    st.reason = '操作已取消'
  } else {
    setPhase(op.action_id, ui.phase, ui.text)
  }
  notifyOnce(op, () => {
    const name = catalogName(op.action_id)
    if (op.status === 'SUCCEEDED') feedback.success('「' + name + '」执行成功')
    else if (op.status === 'FAILED') feedback.error('「' + name + '」失败：' + (readableFinalReason(op.final_reason) || '回读校验不一致'))
    else if (op.status === 'UNKNOWN') feedback.warning('「' + name + '」结果未知：请等待设备重新上线')
  })
}

/** 记录本次页面发起的命令，只跟踪它自己的回执（不把历史遗留操作当成本次结果）。 */
function trackCommand(actionId: string) {
  const op = historyOps.value.find((o) => o.action_id === actionId)
  if (!op) return
  activeCommandId = op.command_id
  applyOperationToUi(op)
}
watch(historyOps, (list) => {
  if (!activeCommandId) return
  const op = list.find((o) => o.command_id === activeCommandId)
  if (op) applyOperationToUi(op)
})

/**
 * 参数化操作表单（2026-10-16 用户要求："参数与标定功能应该参数化显示，可修改保存"）。
 *
 * 改前带参数的操作（参数与标定全组、诊断/运行/时间组共 12 项）按钮一律置灰，
 * 只给一句"请到设备详情页填写"—— 在评审页等于不可用。
 * 现在复用生产页同一份 ActionForm：字段直接由后端 definition.input_schema 生成
 * （boolean→开关、enum→下拉、integer→带上下限的数字框、string→文本框），
 * 必填与范围也在同一处校验，不再有第二套表单导致的规则漂移。
 *
 * 提交流程：填参数 →（若为高风险动作）极简确认 → dispatch(带参数)。
 * 风险动作后端强制单次令牌，仍由 useControlAction 内部处理重验证。
 */
const opFormVisible = ref(false)
const opFormTarget = ref<OpDef | null>(null)

function onOpClick(op: OpDef) {
  if (op.movedTo || !op.available) return
  if (op.needsParams) {
    opFormTarget.value = op
    // 每次打开都清掉上一次的读取结果，避免把 A 动作的值带进 B 动作的表单
    opFormPrefill.value = null
    opFormVisible.value = true
    return
  }
  void runOp(op)
}

function submitOpForm(params: Record<string, unknown>) {
  const op = opFormTarget.value
  opFormVisible.value = false
  if (!op) return
  void runOp(op, params)
}

/* ══════════ 读取现有值 → 回填表单（2026-10-16 用户要求） ══════════
   用户原话："参数应该先读取现有值，弹窗内取消最左边增加'读取'"。

   为什么必须有：写入类动作的参数表动辄 15/30/33 项（保护参数 / 系统参数 / 内阻表），
   空白手填既慢又极易填错；而这些值设备本来就有，读一次就能拿到。
   这里把「写入动作」配对到「读动作」——后端命名是一一对应的
   （write_protection_parameters ↔ read_protection_parameters 等），
   按 write_→read_ 推导并**要求该读动作在当前目录里存在且可用**，
   不存在就不显示「读取」按钮（避免给出一个点了没反应的按钮）。

   结果取回：读动作以 verified_result 返回 [{name, value}]，
   字段名与写入 schema 完全对应（实测 read_protection_parameters 回 23 项，
   cell_ov_protect / cell_uv_delay / chg_ot_protect 等与写表单同名）。 */

const opFormReading = ref(false)
const opFormPrefill = ref<Record<string, unknown> | null>(null)

/** 写入动作对应的读动作 id；没有可用读动作时返回空串 ⇒ 不显示「读取」按钮 */
function readActionFor(writeId: string): string {
  if (!writeId.startsWith('write_')) return ''
  const candidate = 'read_' + writeId.slice('write_'.length)
  const hit = catalog.value.find((a) => a.definition.id === candidate && a.available)
  return hit ? candidate : ''
}

const opFormCanRead = computed(() => !!opFormTarget.value && !!readActionFor(opFormTarget.value.id))

/** 轮询该读操作的终态并取回 verified_result（有界等待，失败返回 null） */
async function waitReadResult(readId: string): Promise<Record<string, unknown> | null> {
  for (let i = 0; i < 20; i++) {
    await new Promise((r) => setTimeout(r, 1000))
    const list = await deviceOperationApi.list(deviceId.value)
    const op = list.find((o) => o.action_id === readId)
    if (!op) continue
    if (['QUEUED', 'DISPATCHED', 'DEVICE_ACCEPTED', 'VERIFYING'].includes(op.status)) continue
    if (op.status !== 'SUCCEEDED') return null
    const out: Record<string, unknown> = {}
    for (const v of op.verified_result ?? []) {
      if (v.string_value !== undefined && v.string_value !== '') out[v.name] = v.string_value
      else out[v.name] = v.value
    }
    return Object.keys(out).length ? out : null
  }
  return null
}

async function readOpFormValues() {
  const op = opFormTarget.value
  if (!op || opFormReading.value) return
  const readId = readActionFor(op.id)
  if (!readId) return
  opFormReading.value = true
  opFormPrefill.value = null
  clearError()
  try {
    // 是否取确认令牌必须**按该读动作自己的 requires_confirmation**，不能写死 true：
    //   read_protection_parameters / read_sn 是 elevated ⇒ 需要令牌；
    //   read_internal_resistance 是单步 low-risk 读 ⇒ **不需要**令牌，此时若仍去
    //   申请，后端直接 409 "confirmation is not required for this action"，读取静默失败。
    // 读取不改设备，两种情况下都不额外弹本地确认框。
    const readAction = catalog.value.find((a) => a.definition.id === readId)
    const ok = await dispatch(readId, {}, readAction?.definition.requires_confirmation === true)
    if (!ok) {
      feedback.error('读取现有值失败：' + (lastError.value || '未知原因'))
      return
    }
    const values = await waitReadResult(readId)
    if (!values) {
      feedback.error('已下发读取，但未取回可用值（设备未响应或读取被拒）')
      return
    }
    opFormPrefill.value = values
    feedback.success('已读取设备现有值，共 ' + Object.keys(values).length + ' 项，可直接修改后提交')
  } catch (e) {
    feedback.error('读取现有值失败：' + String(e))
  } finally {
    opFormReading.value = false
  }
}

async function runOp(op: OpDef, params: Record<string, unknown> = {}) {
  // ⚠ 不再因 needsParams 直接 return —— 参数由 ActionForm 收集后传入（2026-10-16 用户要求
  // "参数与标定功能应该参数化显示，可修改保存"）。原实现把带参数的操作一律挡在门外，
  // 且 dispatch 传的是空对象 {}，即使绕过去也必然被后端 schema 拒绝。
  if (op.movedTo || !op.available) return
  const st = opState(op.id)
  st.reason = ''
  clearError()
  if (op.risky || op.requiresConfirmation) {
    // 后端对高风险动作强制单次确认令牌（useControlAction 内部走 confirm），这里再补一次极简确认。
    const ok = await feedback.confirmDanger(
      '确认执行「' + op.name + '」？该操作会影响设备运行参数，执行后自动回读校验。',
      { title: '受控操作 · ' + op.name, confirmText: '确认执行', cancelText: '取消' },
    )
    if (!ok) return
  }
  setPhase(op.id, 'sent', '已发送')
  const ok = await dispatch(op.id, params, op.requiresConfirmation)
  if (ok) {
    reauthTarget = null
    trackCommand(op.id)
  } else if (reauthVisible.value) {
    reauthTarget = { type: 'op', id: op.id }
  } else {
    setPhase(op.id, 'failed', '失败')
    st.reason = lastError.value || '操作未能提交'
    feedback.error('「' + op.name + '」未能提交：' + (lastError.value || '未知原因'))
  }
}

/** 重验证通过后自动继续执行（令牌单次且绑定 params，由 useControlAction 重新 confirm）。 */
async function doReauth() {
  const target = reauthTarget
  const ok = await retryAfterReauth()
  if (ok) {
    if (target?.type === 'mos') {
      mosPhase[target.which] = '已发送'
      trackCommand('set_mos_policy')
    } else if (target?.type === 'op') {
      setPhase(target.id, 'sent', '已发送')
      trackCommand(target.id)
    }
    reauthTarget = null
    return
  }
  if (!reauthVisible.value) {
    if (target?.type === 'mos') {
      mosPhase[target.which] = ''
      mosError[target.which] = lastError.value || 'MOS 指令未能提交'
    } else if (target?.type === 'op') {
      const st = opState(target.id)
      setPhase(target.id, 'failed', '失败')
      st.reason = lastError.value || '操作未能提交'
    }
    reauthTarget = null
  }
}

function cancelReauth() {
  cancelReauthAction()
  if (reauthTarget?.type === 'mos') {
    mosPhase[reauthTarget.which] = ''
    mosError[reauthTarget.which] = lastError.value || '已取消（未执行 MOS 操作）'
  } else if (reauthTarget?.type === 'op') {
    const st = opState(reauthTarget.id)
    setPhase(reauthTarget.id, 'cancelled', '已取消')
    st.reason = '已取消（未执行该操作）'
  }
  reauthTarget = null
}

function scrollToMos() {
  const el = document.querySelector('.mos-card')
  if (el) el.scrollIntoView({ behavior: 'smooth', block: 'start' })
}

// ════════════ 操作历史（审计线索，来自 store.histories） ════════════
type HistoryResult = 'success' | 'failed' | 'unknown' | 'cancelled'
interface HistoryRow { id: string; time: string; type: string; content: string; result: HistoryResult }
/** 后端 reason 里会出现 final_succeeded / final_failed 这类内部令牌，转成中文或交给回读摘要。 */
function readableFinalReason(reason?: string): string {
  if (!reason) return ''
  if (reason === 'final_failed') return '设备未接受或回读校验不一致'
  if (reason === 'final_succeeded') return ''
  return reason
}
function verifiedSummary(op: DeviceOperation): string {
  return (op.verified_result ?? [])
    .filter((v) => !v.name.endsWith('_ack'))
    .slice(0, 3)
    .map((v) => v.name + '=' + (v.string_value || v.value) + (v.unit || ''))
    .join(', ')
}
const historyRows = computed<HistoryRow[]>(() => {
  // store.apply 在 REST 合并时会 unshift，数组顺序不保证「新的在前」；显式按时间倒序，
  // 保证「最近 N 条」真的是最近的（否则 slice(0,30) 会留下最老的 30 条）。
  const ordered = [...historyOps.value].sort((a, b) => {
    const ta = Date.parse(a.updated_at || a.completed_at || a.created_at || '')
    const tb = Date.parse(b.updated_at || b.completed_at || b.created_at || '')
    return (Number.isNaN(tb) ? 0 : tb) - (Number.isNaN(ta) ? 0 : ta)
  })
  return ordered.slice(0, 30).map((op) => {
    const detail = op.manual_resolution?.reason || readableFinalReason(op.final_reason) || verifiedSummary(op)
    return {
      id: op.command_id,
      time: formatStamp(op.manual_resolution?.resolved_at || op.completed_at || op.updated_at || op.created_at),
      type: op.action_id === 'set_mos_policy' ? 'MOS 直控' : '受控操作',
      content: catalogName(op.action_id) + (detail ? ' — ' + detail : ''),
      result: op.status === 'SUCCEEDED' ? 'success' : op.status === 'FAILED' ? 'failed' : op.status === 'CANCELLED' ? 'cancelled' : 'unknown',
    }
  })
})
function resultTag(r: HistoryResult): TagType {
  return r === 'success' ? 'success' : r === 'failed' ? 'danger' : r === 'cancelled' ? 'info' : 'warning'
}
function resultText(r: HistoryResult): string {
  return r === 'success' ? '成功' : r === 'failed' ? '失败' : r === 'cancelled' ? '已取消' : '结果未知'
}

// ════════════ 受控操作目录加载 ════════════
const catalogReady = ref(false)
const catalogError = ref('')
const catalogLoading = computed(() => !catalogReady.value)
async function loadOperations() {
  catalogReady.value = false
  catalogError.value = ''
  try {
    await opStore.refresh(deviceId.value)
  } catch (e) {
    catalogError.value = extractErrorMessage(e, '受控操作目录加载失败')
  } finally {
    catalogReady.value = true
  }
}

// ════════════ 刷新 / 生命周期 ════════════
function resetDeviceScopedState() {
  stopInject()
  streamRows.value = []
  seenItemIds.clear()
  pendingCount.value = 0
  following.value = true
  snapshotError.value = ''
  mosPhase.charge = ''
  mosPhase.discharge = ''
  mosError.charge = ''
  mosError.discharge = ''
  mosKind = null
  activeCommandId = ''
  verifiedFet.value = null
  reauthTarget = null
  Object.keys(opStates).forEach((k) => {
    opStates[k] = { phase: 'idle', text: '', reason: '' }
  })
}

async function refreshAll() {
  if (refreshing.value) return
  refreshing.value = true
  try {
    await loadDetail()
    await Promise.all([loadPollCommands(), loadLatestSnapshot(), loadOperations()])
    feedback.success('已刷新设备数据')
  } catch (e) {
    feedback.handleError(e, '设备数据刷新失败')
  } finally {
    refreshing.value = false
  }
}

// ════════════ 设备级操作（同步到 HA / 编辑 / 删除 / 返回）════════════
// 2026-10-17 由 demo 转正时补回：demo 是视觉稿没有这些入口，但生产页必须有。

/** 返回列表。用 push 而不是 back()：详情页可能是直接打开/刷新进入的，
 *  此时 history 里没有上一页，back() 会退出站点。 */
function goBackToList() {
  router.push('/edge-device')
}

const syncingHA = ref(false)
/**
 * 同步到 Home Assistant。
 * 走与 useDeviceData.handleSyncToHA **同一个 API 与同一套会话代际校验**；
 * 本页没有引入该 composable（它自带一份 device/loading 状态，与本页既有状态重复），
 * 故在此复刻同样的三步：syncDevice → 校验代际 → 成功/失败反馈。
 */
async function onSyncToHA() {
  if (syncingHA.value || !deviceId.value) return
  const id = deviceId.value
  const generation = getSessionGeneration()
  syncingHA.value = true
  try {
    // syncDevice 返回 void，失败以异常表达（后端未配置 HA 时报错）——
    // 因此不猜测返回值结构，成功即成功、失败走 catch。
    await haApi.syncDevice(id)
    assertSessionGeneration(generation)
    if (deviceId.value !== id) return
    feedback.success('设备已同步到HomeAssistant')
  } catch (e) {
    if (deviceId.value !== id) return
    feedback.handleError(e, '同步到HomeAssistant失败')
  } finally {
    if (deviceId.value === id) syncingHA.value = false
  }
}

const editVisible = ref(false)
const editSaving = ref(false)
const editName = ref('')
function onEditDevice() {
  editName.value = deviceName.value
  editVisible.value = true
}
async function submitEditDevice() {
  if (editSaving.value || !deviceId.value) return
  const name = editName.value.trim()
  if (!name) {
    feedback.warning('设备名称不能为空')
    return
  }
  const id = deviceId.value
  editSaving.value = true
  try {
    await edgeDeviceApi.update(id, { name })
    // 身份变了 ⇒ 列表与详情缓存都要失效，否则返回列表仍是旧名字
    edgeDeviceStore.invalidateLists()
    edgeDeviceStore.invalidateDetail(id)
    feedback.success('设备信息已保存')
    editVisible.value = false
    await loadDetail()
  } catch (e) {
    feedback.handleError(e, '保存失败')
  } finally {
    editSaving.value = false
  }
}

const deleteVisible = ref(false)
const deleting = ref(false)
function onDeleteDevice() {
  deleteVisible.value = true
}
async function submitDeleteDevice(deleteData: boolean) {
  if (deleting.value || !deviceId.value) return
  const id = deviceId.value
  deleting.value = true
  try {
    await edgeDeviceStore.deleteDevice(id, { delete_data: deleteData })
    feedback.success('设备已删除')
    deleteVisible.value = false
    goBackToList()
  } catch (e) {
    feedback.handleError(e, '删除失败')
  } finally {
    deleting.value = false
  }
}

watch(deviceId, () => {
  resetDeviceScopedState()
  void loadDetail()
  void loadPollCommands()
  void loadLatestSnapshot()
  void loadOperations()
})

onMounted(() => {
  // 监听视口变化：横竖屏切换、桌面窗口缩放时页头要在「4 按钮」与「刷新+⋯」间切换
  if (typeof window !== 'undefined' && typeof window.matchMedia === 'function') {
    mobileMql = window.matchMedia(`(max-width: ${MOBILE_MAX}px)`)
    mobileMql.addEventListener('change', onMobileChange)
    isMobileViewport.value = mobileMql.matches
  }
  // connect() 幂等：已连接/连接中会直接返回，不会打断 MainLayout 建立的连接。
  wsStore.connect()
  void loadDetail()
  void loadPollCommands()
  void loadLatestSnapshot()
  void loadOperations()
  void nextTick(() => {
    const el = streamEl.value
    if (el) el.scrollTop = el.scrollHeight
  })
})

onUnmounted(() => {
  stopInject()
  // 必须解绑：不解绑会在路由切换后留下指向已销毁组件的监听，属于内存泄漏
  mobileMql?.removeEventListener('change', onMobileChange)
  mobileMql = null
})
</script>

<style scoped>
/* ══════════ 页面骨架 ══════════ */
/**
 * ⚠ 本页是**独立 DEV 路由**（不在 MainLayout 之下），因此必须自己提供滚动容器。
 *
 * 根因（2026-10-16 用户报障"页面内容显示不全，不能向下滚动"）：
 *   src/App.vue 的全局样式是**应用外壳**模式 ——
 *     html, body { width:100%; height:100%; overflow:hidden; }
 *     #app       { width:100%; height:100%; }
 *   即整页锁死为视口高度、**由 MainLayout 内部提供滚动区**。
 *   走 MainLayout 的页面因此都正常；而本页直接渲染进 #app、没有 MainLayout，
 *   就没有任何元素承担滚动 ⇒ 内容高 2113px 却滚不动（实测滚轮后 scrollY 恒为 0）。
 *
 * 修法：本页自己当滚动容器。**不能改全局 CSS** —— 那会破坏外壳模式，
 *   影响所有依赖 MainLayout 内部滚动的页面。
 *
 * 用 height:100% + overflow-y:auto（而不是 min-height:100vh）：
 *   前者让本页恰好占满 #app 并**在自己内部**滚动，滚动条位置/行为与生产页一致。
 */
/* 生产环境本页嵌在 MainLayout 的 .el-main.main-content 里，**由那一层负责滚动**
   （main-content 有 overflow-y:auto + height:calc(100vh - 60px) + padding:20px）。
   所以这里**不能**再写 height:100%/overflow-y:auto —— 那会造出第二个滚动容器，
   页面出现双滚动条、内层滚完外层还能滚。（评审 demo 当时是独立路由、
   没有 MainLayout 外壳，才需要自己滚。）
   保留 overflow-anchor:none：滚动锚定由浏览器布局引擎执行，不经过 JS 的 scrollTop
   setter、也不发 scroll/focus 事件，实测插桩时 0 次程序化滚动、scrollTop 纹丝不动；
   当锚点落在持续刷新的列表里、行又被 MAX_STREAM_ROWS 修剪时，锚点消失 ⇒
   浏览器重选锚点并调整滚动位置 ⇒ 整页跳到列表处（用户报障过）。
   禁用锚定对**最近的可滚动祖先**生效，在 main-content 上同样有效。 */
.bms-detail-page {
  overflow-anchor: none;
  color: var(--el-text-color-primary);
  font-size: 13px;
  padding-bottom: 24px;
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

/* 流式：不再用 1480px 上限把内容钉在中间，宽度自适应可用空间 */
.page-body { max-width: 100%; margin: 0 auto; padding: 16px 24px; }

/* ══════════ 流式布局（2026-10-16 用户要求） ══════════
   改前每行是**固定列数 grid**：列数与比例都写死
     .metrics    repeat(4, 1fr)
     .mos-protect 5fr / 7fr   …等
   问题：① 窄屏不重排，只把卡片越挤越窄，内容被压扁、出现横向溢出；
        ② 某张卡内容变多也只能在本列里挤，不能借用相邻空间；
        ③ 比例是"平均分配"，不反映卡片实际需要多宽。

   改成 flex + flex-wrap 的**流式**：
     · 每张卡用 flex-basis 声明"希望多宽"（按设计意图，大卡 520/600、小卡 320/380）；
     · min-width 是硬底线，保证不会被压到不可读；
     · 一行放不下就**自动换行**，窄屏自然堆叠成单列。
   这是流式与硬栅格的关键差别：宽度由内容需要决定，而不是由列数决定。

   ⚠ 关于 align-items（2026-10-20 修正，两次迭代才定稿）：

     · flex-start（原值）：矮卡不被拉高，但**同排底边参差** —— 实测 MOS 312 vs 保护 279
       差 34px、实时流 461 vs 轮询 446 差 16px，视觉上并排两块像被切断。
     · 全行 stretch：底边齐了，但「受控操作 740px」会把「操作历史 129px」拉成 740px，
       凭空多出 611px 空白 —— 比参差更糟（老注释警告的正是这个）。
     · **定稿**：按"同排两卡高度是否相近"分别处理：
         - 高度天然相近的并排行（指标卡 / MOS+保护 / 实时流+轮询）→ stretch，底边齐且
           多出的空白 ≤40px，可接受；
         - 高度悬殊的行（受控操作 740 vs 操作历史 129）→ 保持 flex-start，
           不为对齐制造 600px 空白。
       判据是**实测高度差**，不是"看起来应该并排"。 */
.row {
  display: flex;
  flex-wrap: wrap;
  gap: 16px;
  margin-bottom: 16px;
  align-items: flex-start;
}

/* 高度相近的并排行：同排等高，消除底边参差（差值实测均 ≤40px） */
.row.metrics,
.row.mos-protect,
.row.stream-poll {
  align-items: stretch;
}
.row > .card,
.row > .metric-card { flex-grow: 1; flex-shrink: 1; }
/* 同排等高后：卡片自身作为纵向 flex 容器，内容顶部对齐、空白留在底部间隙，
   避免"卡片被拉高但内容仍挤在顶部"的空旷感 */
.row > .card { display: flex; flex-direction: column; }
.row > .card > .card-head { flex-shrink: 0; }
/* 需要撑满剩余高度的内容区（列表/网格）显式声明，未声明者按内容高度、自然留白 */
.row > .card > .op-groups,
.row > .card > .poll-list,
.row > .card > .history-list,
.row > .card > .stream-box { flex: 1 1 auto; min-height: 0; }

/*
  四个指标卡：等权重自动折行。

  ⚠ flex-basis 与 min-width 的取值有实测依据（2026-10-20 二次复审修正）：
    v2 用 flex-basis:140 / min-width:0 → 在 **769~809px** 区间容器约 473px，
    140*3 + gap32 = 452 < 473 ⇒ 排成 3+1，每张仅 147px ⇒ 「8.34Ah」「51.71V」
    被省略号吃掉（实测 63>33 / 67>33）。769px 只比 768px 宽 1px 就崩，属悬崖式回归。
    修正：basis 回到 160、min-width 回到 150（放不下就折成 2 列而不是硬塞 3 列），
    窄屏 2 列由下方 @media (max-width:768px) 显式接管。 */
.metrics > .metric-card { flex: 1 1 160px; min-width: 150px; }

/* 各组按设计意图给大/小卡不同的基准宽度；窄屏自动堆叠 */
.mos-protect > .mos-card      { flex-basis: 380px; min-width: 320px; }
.mos-protect > .protect-card  { flex-basis: 520px; min-width: 340px; }
.cell-chart-row > .cell-card  { flex: 1 1 100%; }   /* 图表卡独占一行 */
.temp-row > .temp-card         { flex: 1 1 100%; }   /* 与 .op-groups 同宽，内部 auto-fill 决定列数 */
.stream-poll > .stream-card   { flex-basis: 520px; min-width: 380px; }
.stream-poll > .poll-card     { flex-basis: 380px; min-width: 340px; }
.ops-history > .ops-card      { flex-basis: 600px; min-width: 400px; }
.ops-history > .history-card  { flex-basis: 320px; min-width: 300px; }

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
.page-error { margin-bottom: 16px; }
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
.mos-manual-note { margin-top: 8px; }
.hw-off-note { display: inline-flex; align-items: center; gap: 4px; font-size: 11px; color: var(--el-color-warning); }

/* ══════════ 保护状态 ══════════ */
.protect-compact { display: flex; align-items: center; gap: 8px; padding: 6px 0; flex-wrap: wrap; }
.protect-compact-text { font-size: 13px; font-weight: 500; }
/* 流式：12 项明细按可用宽度自动决定列数（窄屏 2 列 / 宽屏 4 列以上），
   而不是写死 3 列 —— 写死会让窄屏每格过窄、宽屏留白过多。 */
.protect-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(150px, 1fr)); gap: 8px; margin-top: 8px; }
.protect-item {
  display: flex; align-items: center; gap: 6px; padding: 8px 10px;
  background: var(--el-fill-color-lighter); border: 1px solid transparent;
  border-radius: var(--radius-sm); min-width: 0;
}
.protect-item.active { background: var(--el-color-danger-light-9); border-color: var(--el-color-danger-light-5); }
.protect-name { font-size: 12px; flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.protect-bit { font-size: 11px; color: var(--el-text-color-secondary); font-family: 'SF Mono', Consolas, Monaco, monospace; }

/* ══════════ 电芯电压分布（图表复用旧页 BmsCellVoltageChart） ══════════ */
.cell-summary { font-size: 12px; color: var(--el-text-color-secondary); }
/* 固定高度：图表异步渲染不得改变卡片高度（本页依赖 overflow-anchor:none 防跳动）。
   图表卡独占一行（.cell-chart-row），不再有"同排矮卡"要迁就，故恢复到 300px 的可读高度。 */
.cell-chart-host { height: 300px; }
.cell-chart-empty { height: 300px; display: flex; align-items: center; justify-content: center; color: var(--el-text-color-secondary); font-size: 12px; }
.cell-legend { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; margin-top: 10px; font-size: 12px; color: var(--el-text-color-secondary); }
.lg { display: inline-flex; align-items: center; gap: 5px; }
.swatch { width: 10px; height: 10px; border-radius: 2px; display: inline-block; }
/* 与 BmsCellVoltageChart 口径一致：正常=成功色，>3.6V=告警色，<3.0V=危险色 */
.swatch-normal { background: var(--el-color-success); }
.swatch-high { background: var(--el-color-warning); }
.swatch-danger { background: var(--el-color-danger); }
.cell-thresholds { margin-left: auto; }

/* ══════════ 温度 ══════════ */
/* 流式：温度卡按宽度自适应列数（写死 2 列在窄屏会挤成两行文字）。 */
.temp-list { display: grid; grid-template-columns: repeat(auto-fill, minmax(150px, 1fr)); gap: 8px; }
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
  /* 列表行 churn 不得影响任何祖先滚动位置（见 .bms-detail-page 的同类说明）。
     锚点若落在本列表内，行被 MAX_STREAM_ROWS 裁剪时锚点会消失，
     浏览器随即重选锚点并调整**外层**滚动位置 ⇒ 整页跳到列表处。 */
  overflow-anchor: none;
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
  /* 白字保留是有依据的（FillContrastContract 要求就近说明）：
     底色 --el-color-primary 亮暗同值 #1f5ad8，**两端都是深色填充**，白字实测 5.98:1。
     不用 --text-on-fill：该 token 暗色下取 #1a1a1a（为"提亮后的 success/warning/danger
     浅底"设计），落在深蓝上只有 2.91 —— theme.css 亦明写它不适用于 primary 家族。 */
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
/*
  中文标签必须禁逐字断行（2026-10-20 移动端实测）：
  390px 下分组头只有 290px，图标/caret/计数/风险标签/提示 挤占后，
  「读取信息」被逐字拆成 4 行（49×38px，像竖排），分组头高 54~73px 参差。
  用 word-break: keep-all 保证中文词不被拆开，再用 flex-wrap 让整块换行。
*/
.op-group-name { font-weight: 600; word-break: keep-all; white-space: nowrap; }
.op-group-count { font-size: 12px; color: var(--el-text-color-secondary); white-space: nowrap; flex-shrink: 0; }
.op-group-hint { font-size: 12px; color: var(--el-text-color-secondary); margin-left: auto; word-break: keep-all; }
.op-list { display: flex; flex-direction: column; }
.op-item { display: flex; align-items: center; gap: 12px; padding: 10px 12px; border-top: 1px solid var(--el-border-color-lighter); flex-wrap: wrap; }
.op-info { flex: 1; min-width: 220px; }
.op-name-line { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.op-name { font-size: 13px; font-weight: 500; }
.op-desc { font-size: 12px; color: var(--el-text-color-secondary); margin-top: 3px; line-height: 1.6; }
.op-desc-warn { color: var(--el-color-warning); }
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
.history-empty { padding: 24px 12px; text-align: center; color: var(--el-text-color-secondary); font-size: 12px; }

/* ══════════ 身份重验证 ══════════ */
.dialog-note { margin: 0 0 12px; font-size: 13px; line-height: 1.7; color: var(--el-text-color-regular); }

/* ══════════ 响应式 ══════════ */
/* 流式布局下 .row 已是 flex —— 换行由 flex-basis / min-width 自动决定，
   下面这些 grid-template-columns 覆盖在 flex 容器上已**完全失效**
   （留着会让后人以为窄屏重排还靠它，属于误导性死代码），故改为收窄大卡基准宽度，
   让"大小卡并排"更早变成堆叠。 */
@media (max-width: 1200px) {
  .mos-protect > .protect-card,
  .stream-poll > .stream-card,
  .ops-history > .ops-card { flex-basis: 420px; }
}
/* 平板/窄桌面（769~1100）：指标卡固定 2 列。
   为什么需要单独一档：这个区间容器宽度刚好能塞下 3 张 160px 的卡，
   但每张只剩 ~147px，数值会被省略号吃掉（复审实测 N1）。宁可 2 列宽松，
   不要 3 列挤爆。 */
@media (max-width: 1100px) {
  .metrics > .metric-card { flex: 1 1 calc(50% - 8px); min-width: 0; }
  /*
    保护项在 1100 以下：2 列（原来是无条件单列）。

    原来单列的原因：2 列时 820/1024 会把「前端 IC 错误」截断 18~19px。
    但那是**旧实现只给 name 一段固定宽度 + nowrap 省略号**导致的。
    现在 name 允许折行（见下），2 列不会截断，且比单列省一半高度。
    3 列留给手机（≤768px，见该断点）。
  */
  .protect-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .protect-name { white-space: normal; overflow: visible; text-overflow: clip; }
}

@media (max-width: 768px) {
  /*
    页头压缩（2026-10-20，实测 221px → 目标 ≤112px）。

    原来的三行结构（面包屑 / 设备名+标签 / 详细副文本+4 按钮）在 393px 下
    实际渲染到 221~300px，占掉 1/3 屏，用户批评「顶部重复内容」。

    压缩手段（不动 DOM 语义，只改视觉与换行）：
      ① 面包屑收成一行小字（保留可点返回，只是不再占 10px margin + 20px 行高）；
      ② 设备副文本（节点/通道/设备#/固件/轮询数）从"长句"改为**单行可横向滑动的标签组**，
         既省高度又不丢信息；
      ③ 操作按钮已在模板层收敛为「刷新 + ⋯」（见 head-actions 的 isMobileViewport 分支）。
  */
  .page-head { padding: 10px 12px 12px; }
  .page-body { padding: 12px; }
  .head-actions { margin-left: 0; gap: 8px; }
  .head-main { align-items: flex-start; gap: 10px; flex-wrap: nowrap; }
  .dev-ident { flex: 1; min-width: 0; }

  .crumb { margin-bottom: 6px; font-size: 11px; gap: 6px; }
  /* 面包屑中间层级在手机上价值低（有头部返回），只留「当前页」语境 */
  .crumb .crumb-link { max-width: 92px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

  .dev-name { font-size: 17px; }
  .dev-name-line { gap: 6px; }

  /*
    副文本：**允许折行，不横滚、不省略**。

    走过的弯路：第一版改成 `white-space: nowrap + overflow-x: auto` 想省高度，
    实测 393px 下可视宽只有 206px，而内容宽 462px ⇒ 有 2/3 内容被裁在右侧，
    用户看不到「轮询已启用 1/32 条」这类关键信息，且**没有滚动提示**。
    对一行 12px 的次要文本，"要用户主动横滑"是不可发现交互。

    定稿：正常折行（最多 2 行，约 34px），信息完整可读。
    代价是比横滚多 17px 高度，但页头总高仍控制在 ~110px，达标。
    折行文案本身也精简：去掉冗余分隔符，改为「· 」紧凑风格。
  */
  .dev-sub {
    margin-top: 4px;
    line-height: 17px;
    font-size: 11px;
    /* 用 flex + gap 控制间距与折行：每个 span 是不可断的"词"，
       放不下就整词换行。这样序列号（F0F5BD02DD84）不会被拦腰截断。 */
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 0 4px;
    overflow-wrap: normal;
  }
  .dev-sub > span { white-space: nowrap; }
  .card { padding: 14px 12px; }
  .head-right, .stream-actions { margin-left: 0; }
  .cell-thresholds { margin-left: 0; }
  .page-body .el-button:not(.is-circle) { min-height: 44px; }
  .mos-switch, .op-group-head, .back-latest { min-height: 44px; }
  /* 分组头在窄屏允许换行：名称/计数/风险标签占第一行，提示自动掉到第二行，
     而不是把「读取信息」挤成竖排 */
  .op-group-head { flex-wrap: wrap; row-gap: 4px; align-items: center; }
  .op-group-hint { margin-left: 0; flex-basis: 100%; }
  .poll-row { align-items: flex-start; }
  .poll-controls { width: 100%; }
  .poll-interval { flex: 1; width: auto; }

  /* ── 指标卡：窄屏 2×2（此前是 1 列 × 4 行，白占 386px 高度） ──
     用 flex-basis 50% 而非 grid：与上面的 .row 流式模型保持一致。
     实测目标：区块 226 → ≤180px（4 张卡 2 行，每行约 82px）。 */
  .metrics > .metric-card { flex: 1 1 calc(50% - 8px); min-width: 0; }
  .row.metrics { gap: 8px; }

  /*
    指标卡内部再压一档：MetricStatCard 的移动端样式是按"卡片独占一行"设计的
    （padding 8px 4px + 图标 34px），2×2 之后每张只有 ~186px 宽，
    「SOC 剩余电量」「剩余容量」这类 5 字标签 + 数值 + 副文本会显得拥挤。
      ① 收紧左内距（图标靠边）；
      ② 图标 34→28px，给文字腾 6px；
      ③ 数值 20→18px、标签 13→12px，保证「51.76V」这类四位数不折行。
  */
  /*
    同一行两张卡必须等高：实测出现 83 / 76 两种高度（SOC 卡有 progress 多 6px，
    总电压卡只有一行副文本）。虽然 flex 的 align-items:stretch（继承自 .row.metrics）
    已经让底边对齐，但**卡片自身高度仍不同**，视觉上像"一张卡悬空"。
    这里给固定 min-height 让两张卡实体等高，内容少的居中。
  */
  .row.metrics :deep(.metric-card) { padding: 8px 6px; gap: 8px; min-height: 84px; }
  .row.metrics :deep(.metric-icon) { width: 28px; height: 28px; border-radius: 7px; }
  .row.metrics :deep(.metric-icon .el-icon) { font-size: 18px; }
  .row.metrics :deep(.metric-label) { font-size: 12px; }
  .row.metrics :deep(.metric-value) { font-size: 18px; }
  .row.metrics :deep(.metric-unit) { font-size: 12px; }
  .row.metrics :deep(.metric-sub) { font-size: 11px; line-height: 16px; }
  .row.metrics :deep(.metric-aux) { height: auto; min-height: 16px; margin-top: 2px; }

  /*
    极窄屏（≤360px）改回单列：2 列时每张仅 116px，
    「SOC 剩余电量」「充电中（电流为正）」这类标签/副文本会被压到折行或截断。
    360px 以下本身已是小屏设备，单列可读性优先于紧凑度。
  */
  @media (max-width: 360px) {
    .metrics > .metric-card { flex: 1 1 100%; }
  }

  /* ── 硬 min-width 在窄屏必须清零 ──
     根因（实测）：.ops-card { min-width: 400px }、.stream-card { min-width: 380px }
     等在 318px 的容器里**不能收缩**（min-width 是硬底线），于是卡片宽 400、
     整页 el-main scrollWidth 432 > clientWidth 382，页面可以横向拖动。
     桌面端这些 min-width 用于保证"卡片不会被压到不可读"，窄屏已经是单列堆叠，
     不需要它们 —— 清零后卡片跟随容器 100% 宽。 */
  .row > .card,
  .mos-protect > .mos-card,
  .mos-protect > .protect-card,
  .stream-poll > .stream-card,
  .stream-poll > .poll-card,
  .ops-history > .ops-card,
  .ops-history > .history-card {
    flex: 1 1 100%;
    min-width: 0;
  }

  /*
    保护网格：窄屏单列。
    实测：2 列时每项 ≤153px，name+tag+bit 挤不下，「短路保护」→「短路…」、
    「前端 IC 错误」→「前端 …」。保护名是关键信息，绝不能截断。
    单列断点从 768 提到 1100（复审：820px 与 1024px 下 2 列仍截「前端 IC 错误」
    18~19px）——1100px 以下统一单列，宽度充裕后再回 2 列。
  */
  /*
    ══════════ 保护状态：3 列紧凑网格（670px → 目标 ≤250px）══════════

    现状问题（实测 393px）：单列布局，12 项 × 44px 触控高 = 670px，
    占 0.77 屏；每项内部「名称 + 正常标签 + bitN」三段横排，
    而右侧还有大片空白（卡片 321px 但内容只用到 295px）。

    关键决策：**bitN 在移动端隐藏，名称必须保留**。
    理由：bitN 是给工程师对寄存器用的次要信息，名称才是用户判断"哪项保护"的依据；
    用户此前明确批评"短路保护被截成短路…"。两者冲突时保名称、舍 bitN。
    桌面端 bitN 保留（空间够）。

    3 列而非 2 列：实测每格 ~103px 恰好放得下 4 字名称（12px 字号），
    2 列会浪费宽度、1 列就是现状。
  */
  .protect-grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 4px;
    margin-top: 4px;
  }
  .protect-item {
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 1px;
    padding: 4px 3px;
    /* 名称 15 + gap 1 + 标签 16 + 上下内距 8 = 40px，仍高于 WCAG 的 24px 下限 */
    min-height: 40px;
  }
  .protect-name {
    /* 名称必须完整可读：允许折行，不用省略号 */
    white-space: normal;
    overflow: visible;
    text-overflow: clip;
    text-align: center;
    font-size: 12px;
    line-height: 15px;
    flex: none;
    width: 100%;
  }
  /* bitN 收起（移动端）；桌面端仍显示 */
  .protect-bit { display: none; }
  /* 「正常/已触发」标签收紧，不再单独占一行高度 */
  .protect-item :deep(.el-tag) {
    height: 16px;
    line-height: 14px;
    padding: 0 4px;
    font-size: 11px;
  }

  /*
    移动端把「12 项保护均未触发（bit0 – bit11 全 0）」这行摘要收掉 30px。

    为什么可以收：卡片头已有「全部正常」绿标签 + protection_status = 0x00 徽章，
    这句是同义重复，而它正是超出 250px 目标的那部分。

    ⚠ 只收「全正常」这一种（.is-all-normal），**「暂无 protection_status 数据」
      必须保留** —— 那是"采不到数据"的重要提示，藏掉等于让用户以为一切正常，
      属 fail-open。有保护触发时走另一分支，也不受影响。
  */
  .protect-compact.is-all-normal { display: none; }

  /*
    卡片头压缩 47 → ~28px：
    393px 下「保护状态 + 全部正常标签 + protection_status = 0x00 徽章」放不下，
    徽章被挤到第二行（monospace 长串换行占 20px）。

    ⚠ 隐藏的是**徽章**不是标签：
      · 「全部正常 / N 项保护已触发 / 暂无数据」标签保留（这是用户要看的结论）；
      · `protection_status = 0x00` 是给工程师对寄存器用的原始值，
        移动端属于"开发者文案"（用户明确批评过这类内容），且完整值在
        protectionBits 的 12 格里已逐位体现。桌面端保留。
  */
  .protect-card .card-head { margin-bottom: 6px; }
  .protect-card .card-head .mono-badge { display: none; }
  .protect-card .card-head { min-height: 0; padding-bottom: 0; }

  /*
    ══════════ MOS 直控：364px → 目标 ≤200px ══════════

    移动端收起三段**说明性长文本**，保留全部操作元素（2 个开关 + 状态标签）：
      ① .card-note（"充电/放电回路通断的软件策略 set_mos_policy。开关即时下发…"4 行）
         —— 解释机制，用户点一下开关就能理解，属"首次阅读才有价值"的内容；
      ② .card-footnote（"关断是正常工况而非故障…"）
         —— 解释配色决策，同理；
      ③ mono-badge（fet_status = 0x03）—— 原始寄存器值，工程师用，同保护卡处置。

    ⚠ 不收起：mos-name（充电/放电 MOS）、状态标签（导通/关断）、
      mos-desc（允许充电/允许放电）、开关本身、错误 alert、不可控提示。
      这些是操作与状态，一个都不能少。
  */
  .mos-card .card-note,
  .mos-card .card-footnote,
  .mos-card .card-head .mono-badge { display: none; }
  .mos-card .card-head { margin-bottom: 8px; }
  .mos-row { padding: 8px 0; }
  .mos-icon { width: 28px; height: 28px; }
  .mos-name { font-size: 14px; }
  .mos-desc { font-size: 11px; }

  /* ── 实时流：横向溢出治理（实测 el-main scrollWidth 432 > clientWidth 382） ──
     根因：.stream-row { min-width: 420px } 在 354px 容器里硬撑，把整页撑出横向滚动条。
     窄屏改为**两行网格**（上行 时间+项目，下行 值+单位+状态）：
       · 不删任何字段（单位/状态都是有效信息）；
       · 不设 min-width ⇒ 不再溢出；
       · 单行高度只增加约 16px，比重排整页代价低得多。
     grid-template-areas 让读屏顺序与视觉顺序保持一致。 */
  .stream-row {
    /* 列宽必须固定（不能用 auto）：auto 列每行独立计算宽度，
       实测单位列 left 在 286/283.3/270/空列 0 之间抖动 3~16px，
       整列看起来是锯齿。固定 40px 后各行对齐（复审 N5）。 */
    grid-template-columns: 58px minmax(0, 1fr) 68px 34px 46px;
    grid-template-areas:
      "time item  item  item  item"
      "time value unit  unit  status";
    min-width: 0;
    row-gap: 2px;
    padding: 8px 10px;
  }
  .stream-row .s-value { text-align: right; }
  .stream-row .s-time   { grid-area: time; align-self: start; }
  .stream-row .s-item   { grid-area: item; }
  .stream-row .s-value  { grid-area: value; text-align: left; }
  .stream-row .s-unit   { grid-area: unit; }
  .stream-row .s-status { grid-area: status; }
  .stream-scroll { overflow-x: hidden; }
}
@media (pointer: coarse) {
  .mos-switch, .op-group-head, .back-latest { min-height: 44px; }
  .page-body .el-button:not(.is-circle) { min-height: 44px; }
}
</style>
