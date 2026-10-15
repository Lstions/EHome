<template>
  <!--
    BmsDemoPage2.vue — BMS 设备详情页「重设计」评审 demo（2026-10-15 重设计 / 2026-10-15 接真实后端）

    · 数据全部来自真实后端：设备详情 / 实时流 / 受控操作目录 / 操作历史 / 轮询指令配置。
    · 仅开发环境路由可见（/dev/bms-demo2?device=<id>），带 DEV 水印。
    · 视觉与交互设计保持不变，本轮只替换数据层。
    · 本轮四条用户诉求的落点见各区段标题注释。
  -->
  <div class="bms-demo2">
    <div class="dev-watermark">DEV · BMS 详情页重设计评审（已接入真实后端设备数据，非生产页面）</div>

    <!-- 页头：设备身份（真实详情） -->
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
            <h1 class="dev-name">{{ deviceName }}</h1>
            <el-tag :type="statusTag.type" size="small" effect="plain">{{ statusTag.text }}</el-tag>
            <el-tag type="info" size="small" effect="plain">{{ deviceTypeLabel }} · {{ cellCountLabel }}</el-tag>
          </div>
          <div class="dev-sub">
            节点 {{ nodeLabel }} · 通道 {{ channelLabel }} · 设备 #{{ deviceId }} · 节点固件 {{ firmwareLabel }}
            <template v-if="hasPollLimit"> · 轮询已启用 <b>{{ enabledCount }}/{{ pollLimitValue }}</b> 条</template>
            <template v-else> · 轮询已启用 <b>{{ enabledCount }}</b> 条</template>
          </div>
        </div>
        <div class="head-actions">
          <el-button size="small" :icon="Refresh" :loading="refreshing" @click="refreshAll">刷新</el-button>
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
            着色规则：<b>导通</b>用成功色、<b>关断</b>用中性色 —— 关断是正常工况，不是故障；只有「保护已触发」才用告警色。
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

          <!-- 无保护命中：紧凑一行 -->
          <div v-if="!activeProtections.length" class="protect-compact" role="status">
            <el-icon :size="18" :color="hasProtectionData ? 'var(--el-color-success)' : 'var(--el-text-color-secondary)'"><CircleCheckFilled /></el-icon>
            <span class="protect-compact-text">{{ hasProtectionData ? '12 项保护均未触发（bit0 – bit11 全 0）' : '暂无 protection_status 数据' }}</span>
            <button type="button" class="link-btn" @click="protectExpanded = !protectExpanded">
              {{ protectExpanded ? '收起明细' : '展开明细' }}
            </button>
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
            <span class="card-hint">{{ cells.length }} 串 · 单位 V</span>
            <span v-if="cells.length" class="head-right cell-summary">
              最高 <b class="txt-warning">#{{ maxCell.index }} {{ maxCell.voltage.toFixed(3) }}V</b>
              · 最低 <b class="txt-secondary">#{{ minCell.index }} {{ minCell.voltage.toFixed(3) }}V</b>
              · 压差 <b :class="diff > 0.1 ? 'txt-warning' : 'txt-secondary'">{{ diff.toFixed(3) }}V</b>
            </span>
            <span v-else class="head-right cell-summary">暂无单体电压数据</span>
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
          <p class="card-footnote">判定：&gt;45℃ 偏高（告警）· &gt;60℃ 过高（危险）。正常温区用成功色，不用危险色。</p>
        </div>
      </section>

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
            数据来自设备 WebSocket 上报（新的在前）。验证方式：点「高频注入」让列表每秒新增 1 条（本地压测行，非设备数据）
            → 用鼠标/触控<b>向上滚</b>，列表应停在原处、新数据只累积计数而不把视口拉回；点「回到最新」或滚回底部即恢复跟随。
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
                    <div v-else-if="op.needsParams" class="op-desc op-desc-warn">该操作需要参数，请到设备详情页的「受控操作」面板填写后执行。</div>
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
                      :disabled="opBusy(op.id) || !op.available || op.needsParams"
                      @click="runOp(op)"
                    >
                      {{ !op.available ? '暂不可用' : op.needsParams ? '需参数' : op.risky ? '确认后执行' : '执行' }}
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
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, watch, onMounted, onUnmounted, nextTick } from 'vue'
import type { Component } from 'vue'
import { useRoute } from 'vue-router'
import {
  HomeFilled, PieChart, Odometer, DataLine, Lightning, Open, Lock,
  CircleCheckFilled, WarningFilled, Bottom, ArrowRight, InfoFilled,
  Timer, Operation, SetUp, Tools, Document, MagicStick, Refresh,
} from '@element-plus/icons-vue'
import MetricStatCard from '@/components/common/MetricStatCard.vue'
import { feedback, extractErrorMessage } from '@/utils/feedback'
import { edgeDeviceApi, type EdgeDevice, type CommandTemplateWithInterval } from '@/api/edgeDevice'
import { useRealtimeData } from '@/composables/useRealtimeData'
import { useWebSocketStore } from '@/stores/websocket'
import { useDeviceOperationStore } from '@/stores/deviceOperation'
import { useControlAction } from '@/composables/useControlAction'
import type { DeviceOperation, EffectiveAction, OperationStatus } from '@/api/deviceOperation'
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
const deviceId = computed(() => Number(route.query.device ?? 1) || 1)
const device = ref<EdgeDevice | null>(null)
const detailError = ref('')
const refreshing = ref(false)

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

// ── 电芯柱状图（相对高度；只标最高/最低） ──
const cellDomain = computed(() => {
  if (!cells.value.length) return { lo: 0, hi: 1 }
  const vs = cells.value.map((c) => c.voltage)
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

// ════════════ 诉求①：MOS 直控（真机 fet_status + set_mos_policy） ════════════
const opStore = useDeviceOperationStore()
const historyOps = computed<DeviceOperation[]>(() => opStore.histories.get(deviceId.value) ?? [])
const catalog = computed<EffectiveAction[]>(() => opStore.catalogs.get(deviceId.value) ?? [])

const mosPhase = reactive<Record<'charge' | 'discharge', string>>({ charge: '', discharge: '' })
const mosError = reactive<Record<'charge' | 'discharge', string>>({ charge: '', discharge: '' })

const hasFetStatus = computed(() => num(latestData.value?.fet_status) !== null)
const fetStatus = computed(() => num(latestData.value?.fet_status) ?? 0)
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

async function runOp(op: OpDef) {
  if (op.movedTo || op.needsParams || !op.available) return
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
  const ok = await dispatch(op.id, {}, op.requiresConfirmation)
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
  protectExpanded.value = false
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

watch(deviceId, () => {
  resetDeviceScopedState()
  void loadDetail()
  void loadPollCommands()
  void loadLatestSnapshot()
  void loadOperations()
})

onMounted(() => {
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
