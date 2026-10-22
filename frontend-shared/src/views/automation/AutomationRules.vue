<template>
  <div class="automation-rules-page">
    <PageHeader
      title="自动化策略"
      subtitle="传感器数据、时间窗口或日出日落触发设备动作或通知"
    />

    <!-- 规则表格 -->
    <section class="card rules-card">
      <div class="card-head">
        <span class="card-title">规则<el-tag v-if="rules.length" size="small" class="count-tag">{{ rules.length }}</el-tag></span>
        <el-button type="primary" :icon="Plus" data-test="create-rule" @click="openCreate">创建规则</el-button>
      </div>
      <!--
        2026-10-21 第二轮截图精读（P1 信息损失）：393 下这张 7 列表格自然宽 970px，
        视口内只剩「名称 + 触发器」两列，且名称被 show-overflow-tooltip 截成
        「客厅高温自动开…」—— 触屏没有 hover，tooltip 不可达，横滑也只是多看一列。
        修法：窄屏（≤768px）改**卡片列表**（名称全显示 + 触发器类型徽标 + 触发条件/动作摘要
        + 冷却/熔断 + 启用开关 + 操作按钮，顶部虚线之外无信息损失）；桌面（>768px）表格
        与固定列逐像素不变。
      -->
      <!-- 桌面：宽表（结构 / 固定列 / 横滑提示与改前一致） -->
      <div v-if="!isMobile" class="mobile-table-wrapper">
        <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
      <el-table :data="rules" v-loading="rulesLoading" data-test="rules-table">
        <el-table-column prop="name" label="名称" min-width="140" show-overflow-tooltip />
        <el-table-column label="触发器" min-width="180">
          <template #default="{ row }">
            <div class="trigger-cell">
              <el-tag size="small" class="trigger-type-tag">{{ triggerTypeText(row.trigger_type) }}</el-tag>
              <span class="mono trigger-summary">{{ triggerSummary(asRule(row)) }}</span>
            </div>
          </template>
        </el-table-column>
        <!-- R2: 动作列由"单动作摘要"改为"多动作摘要 + 风险计数"。风险计数让用户在
             列表页就能发现风险规则，不必逐条打开编辑器。计数读的是后端下发的
             requires_confirmation（唯一判定点），前端不自己算风险等级。 -->
        <el-table-column label="动作" min-width="180">
          <template #default="{ row }">
            <div class="action-cell">
              <el-tag size="small" :type="row.action_type === 'device_action' ? 'warning' : 'info'">
                {{ row.action_type === 'device_action' ? '设备动作' : '通知' }} ×{{ ruleActions(asRule(row)).length }}
              </el-tag>
              <el-tag v-if="ruleRiskCount(asRule(row))" type="danger" size="small" data-test="rule-risk-count">
                <span aria-hidden="true">⚠</span> 含 {{ ruleRiskCount(asRule(row)) }} 个风险动作
              </el-tag>
              <span class="mono action-summary">{{ actionSummary(asRule(row)) }}</span>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="冷却/熔断" width="120">
          <template #default="{ row }">
            <span class="mono">{{ row.cooldown_sec }}s / {{ row.max_daily_exec === 0 ? '不限' : row.max_daily_exec }}</span>
          </template>
        </el-table-column>
        <el-table-column label="确认制" width="90">
          <template #default="{ row }">
            <el-tag v-if="row.require_confirmed" type="warning" size="small">需确认</el-tag>
            <span v-else class="text-muted">-</span>
          </template>
        </el-table-column>
        <el-table-column label="启用" width="80">
          <template #default="{ row }">
            <!-- 可访问名带**规则名**：表格里每行都有一个一模一样的开关，
                 没有名字时屏幕阅读器只读「开关」，用户无法分辨改的是哪条规则。 -->
            <el-switch
              :model-value="row.enabled"
              :aria-label="`${asRule(row).name} 启用`"
              data-test="rule-enabled"
              @change="(v: string | number | boolean) => onToggle(asRule(row), v === true)"
            />
          </template>
        </el-table-column>
        <!-- F26 裁决 D2：该列在窄屏**取消固定**（原 width="180" fixed="right"）。
             实测（390px，48 点 elementFromPoint 网格）：固定列 180px 压在「启用」列上，
             行内 el-switch 命中自身 0/48、命中固定列 48/48 —— 开关 100% 不可点；
             桌面 1440px 命中 48/48（完全正常）。根因是粘性列的绘制顺序恒在普通列之上
             （见 theme.css「固定列」小节），与列宽无关，故不适用收窄列宽那条路：
             若按 EdgeDeviceList 范式改成图标按钮，桌面操作列会从 180px 降到 96px，
             直接改变本页桌面视觉密度（任务书禁止）。
             改法：宽度与内容不变，只在窄屏（<768px，与 useResponsive 的 BREAKPOINTS.md
             同源）把 fixed 置 false。EP 的 table-column 对 fixed 注册了 watch 并触发
             scheduleLayout（element-plus/es/components/table/src/table-column/watcher-helper.mjs），
             运行期翻转会重算固定列集合；组件挂载时 isMobile 已是正确值，
             桌面渲染路径与改前逐字节等价。 -->
        <el-table-column label="操作" width="180" :fixed="isMobile ? false : 'right'">
          <template #default="{ row }">
            <el-button link type="warning" size="small" :loading="triggeringId === row.id" data-test="trigger-rule" @click="onTrigger(asRule(row))">触发</el-button>
            <el-button link type="primary" size="small" @click="openEdit(asRule(row))">编辑</el-button>
            <el-button link type="danger" size="small" @click="onDelete(asRule(row))">删除</el-button>
          </template>
        </el-table-column>
        <template #empty>暂无规则，点击右上角创建</template>
      </el-table>
      </div>
      <!-- 窄屏：规则卡片列表（每张卡承载桌面 7 列的全部信息，无省略号截断） -->
      <div v-else class="rule-cards" data-test="rules-cards">
        <p v-if="rulesLoading" class="cards-empty">正在加载规则…</p>
        <template v-else>
        <article v-for="row in rules" :key="row.id" class="rule-card" data-test="rule-card">
          <div class="rule-card__head">
            <h3 class="rule-card__name">{{ asRule(row).name }}</h3>
            <el-switch
              :model-value="row.enabled"
              :aria-label="`${asRule(row).name} 启用`"
              data-test="rule-enabled"
              @change="(v: string | number | boolean) => onToggle(asRule(row), v === true)"
            />
          </div>
          <div class="rule-card__badges">
            <el-tag size="small" class="trigger-type-tag">{{ triggerTypeText(row.trigger_type) }}</el-tag>
            <el-tag size="small" :type="row.action_type === 'device_action' ? 'warning' : 'info'">
              {{ row.action_type === 'device_action' ? '设备动作' : '通知' }} ×{{ ruleActions(asRule(row)).length }}
            </el-tag>
            <el-tag v-if="ruleRiskCount(asRule(row))" type="danger" size="small" data-test="rule-risk-count">
              <span aria-hidden="true">⚠</span> 含 {{ ruleRiskCount(asRule(row)) }} 个风险动作
            </el-tag>
            <el-tag v-if="row.require_confirmed" type="warning" size="small">需确认</el-tag>
          </div>
          <dl class="rule-card__meta">
            <div>
              <dt>触发条件</dt>
              <dd class="mono">{{ triggerSummary(asRule(row)) }}</dd>
            </div>
            <div>
              <dt>动作</dt>
              <dd class="mono">{{ actionSummary(asRule(row)) }}</dd>
            </div>
            <div>
              <dt>冷却/熔断</dt>
              <dd class="mono">{{ row.cooldown_sec }}s / {{ row.max_daily_exec === 0 ? '不限' : row.max_daily_exec }}</dd>
            </div>
          </dl>
          <div class="rule-card__ops">
            <el-button type="warning" size="small" plain :loading="triggeringId === row.id" data-test="trigger-rule" @click="onTrigger(asRule(row))">触发</el-button>
            <el-button type="primary" size="small" plain @click="openEdit(asRule(row))">编辑</el-button>
            <el-button type="danger" size="small" plain @click="onDelete(asRule(row))">删除</el-button>
          </div>
        </article>
        <p v-if="rules.length === 0" class="cards-empty">暂无规则，点击右上角创建</p>
        </template>
      </div>
    </section>

    <!-- 触发历史 -->
    <section class="card events-card">
      <div class="card-head">
        <span class="card-title">触发历史<el-tag v-if="events.length" size="small" class="count-tag">{{ events.length }}</el-tag></span>
        <div class="event-filters">
          <el-select v-model="eventFilterRuleId" placeholder="按规则过滤" clearable size="small" style="width: 140px" data-test="filter-rule" @change="fetchEvents">
            <el-option v-for="r in rules" :key="r.id" :label="r.name" :value="r.id" />
          </el-select>
          <el-select v-model="eventFilterResult" placeholder="按结果过滤" clearable size="small" style="width: 140px" data-test="filter-result" @change="fetchEvents">
            <el-option v-for="opt in resultOptions" :key="opt.value" :label="opt.label" :value="opt.value" />
          </el-select>
          <el-button link size="small" data-test="refresh-events" @click="fetchEvents">刷新</el-button>
        </div>
      </div>
      <!--
        2026-10-21 第二轮截图精读（P1 遮挡）：393 下 910px 自然宽的 8 列表格里，右侧
        fixed="right" 的「操作」列（100px，占视口 31%）压住「规则」列文字
        （实测 td 192~312 被固定列覆盖 59px、「结果」覆盖 41px）；上一轮无数据时把它判为
        待确认 P2，本轮用注入数据复测确认确有遮挡 + 横滑信息损失。
        修法：窄屏（≤768px）改**卡片列表**（时间 + 规则名 + 结果 + 触发值/来源/命令 ID/详情 +
        待确认操作），不再渲染表格、不再存在固定列；桌面（>768px）表格与 fixed 逐像素不变。
      -->
      <!-- 桌面：宽表（结构 / fixed 操作列 / 横滑提示与改前一致） -->
      <div v-if="!isMobile" class="mobile-table-wrapper">
        <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
      <el-table :data="events" v-loading="eventsLoading" data-test="events-table">
        <el-table-column label="时间" min-width="160">
          <template #default="{ row }">{{ formatTime(row.triggered_at) }}</template>
        </el-table-column>
        <el-table-column label="规则" width="120">
          <template #default="{ row }">
            <span class="mono" data-test="event-rule-name">{{ ruleName(row.rule_id) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="结果" width="110">
          <template #default="{ row }">
            <el-tag :type="resultTagType(row.result)" size="small">{{ resultText(row.result) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="触发值" width="100">
          <template #default="{ row }">
            <span v-if="row.trigger_value !== undefined" class="mono">{{ formatValue(row.trigger_value) }}</span>
            <span v-else class="text-muted">-</span>
          </template>
        </el-table-column>
        <el-table-column label="来源" width="70">
          <template #default="{ row }">
            <el-tag v-if="row.trigger_source === 'manual'" type="warning" size="small">手动</el-tag>
            <span v-else class="text-muted">自动</span>
          </template>
        </el-table-column>
        <el-table-column label="命令 ID" width="110">
          <template #default="{ row }">
            <el-button v-if="row.command_id" link type="primary" size="small" @click="goCommand(row.command_id)">
              {{ row.command_id }}
            </el-button>
            <span v-else class="text-muted">-</span>
          </template>
        </el-table-column>
        <el-table-column prop="detail" label="详情" min-width="140" show-overflow-tooltip />
        <!-- 2026-10-21 审计复核结论：本列 width=100 < 窄屏表格盒（280~310px），
             F26 契约与实测探针都判定**不遮挡**（48/48 命中自身），故保持 fixed="right"。
             C-ops 审计曾把它标为「待确认 P2」（当时无数据、只看到白色条带），
             本轮按 F26 既有裁决回退 —— 不用未验证的猜测改动已通过的契约。 -->
        <el-table-column label="操作" width="100" fixed="right">
          <template #default="{ row }">
            <el-button
              v-if="row.result === 'pending_confirm'"
              link
              type="warning"
              size="small"
              data-test="confirm-event"
              @click="onConfirmEvent(asEvent(row))"
            >
              确认执行
            </el-button>
          </template>
        </el-table-column>
        <!-- F3：区分"没配策略"与"策略没触发/被抑制" —— 后者常见成因是 cooldown 或日熔断，
             只说「暂无触发事件」会让用户以为页面坏了（审计 R2-C2 口径）。 -->
        <template #empty>
          <div class="table-empty">
            <p>{{ rules.length === 0 ? '尚未创建自动化策略' : '暂无触发事件' }}</p>
            <p class="table-empty-hint">
              {{ rules.length === 0
                ? '在上方「自动化策略」中创建规则后，命中的事件会出现在这里。'
                : '策略已就绪但尚未产生事件：确认条件可达，并留意冷却期（cooldown）与日熔断会抑制触发。' }}
            </p>
          </div>
        </template>
      </el-table>
      </div>
      <!-- 窄屏：触发事件卡片列表（时间 / 规则名 / 结果 / 触发值 / 来源 / 命令 ID / 详情 / 待确认操作） -->
      <div v-else class="event-cards" data-test="events-cards">
        <p v-if="eventsLoading" class="cards-empty">正在加载触发事件…</p>
        <template v-else>
          <article v-for="row in events" :key="row.id" class="event-card" data-test="event-card">
            <div class="event-card__head">
              <el-tag :type="resultTagType(row.result)" size="small">{{ resultText(row.result) }}</el-tag>
              <span class="mono event-card__time">{{ formatTime(row.triggered_at) }}</span>
              <el-tag v-if="row.trigger_source === 'manual'" type="warning" size="small">手动</el-tag>
              <span v-else class="event-card__source">自动</span>
            </div>
            <p class="event-card__rule" data-test="event-rule-name">{{ ruleName(row.rule_id) }}</p>
            <dl class="event-card__meta">
              <div>
                <dt>触发值</dt>
                <dd class="mono">{{ row.trigger_value !== undefined ? formatValue(row.trigger_value) : '-' }}</dd>
              </div>
              <div v-if="row.command_id">
                <dt>命令 ID</dt>
                <dd class="mono event-card__cmd">
                  <span class="event-card__cmd-id">{{ row.command_id }}</span>
                  <el-button link type="primary" size="small" @click="goCommand(row.command_id)">查看</el-button>
                </dd>
              </div>
              <div v-if="row.detail">
                <dt>详情</dt>
                <dd>{{ row.detail }}</dd>
              </div>
            </dl>
            <div v-if="row.result === 'pending_confirm'" class="event-card__ops">
              <el-button type="warning" size="small" plain data-test="confirm-event" @click="onConfirmEvent(asEvent(row))">确认执行</el-button>
            </div>
          </article>
          <!-- F3：卡片列表沿用与表格 #empty 完全相同的"没配策略 / 没触发"区分口径 -->
          <div v-if="events.length === 0" class="cards-empty">
            <p>{{ rules.length === 0 ? '尚未创建自动化策略' : '暂无触发事件' }}</p>
            <p class="cards-empty__hint">
              {{ rules.length === 0
                ? '在上方「自动化策略」中创建规则后，命中的事件会出现在这里。'
                : '策略已就绪但尚未产生事件：确认条件可达，并留意冷却期（cooldown）与日熔断会抑制触发。' }}
            </p>
          </div>
        </template>
      </div>
      <!-- 分页 (真分页: 表格数据来自接口当前页, 不是本地全量切片)。
           改前本表一次性渲染后端 Limit(500) 的全部行 (实测 502 行/11902 节点,
           真实事件 >1006 条被静默截断), fixed 列为每行各注入一份 inline style。 -->
      <!-- 2026-10-21 审计 C-ops P2：total=0 时分页器仍渲染（实测 321×76「共 0 条 / 20条/页 / 1」），
           空列表被两个空控件（空态 + 分页）拉长。total>0 才渲染。 -->
      <div v-if="eventsTotal > 0" class="events-pagination">
        <el-pagination
          v-model:current-page="eventsPage"
          v-model:page-size="eventsPageSize"
          :total="eventsTotal"
          :page-sizes="[20, 50, 100]"
          layout="total, sizes, prev, pager, next, jumper"
          data-test="events-pagination"
          @current-change="() => fetchEvents()"
          @size-change="onEventsPageSizeChange"
        />
      </div>
    </section>

    <!-- 创建/编辑对话框 -->
    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑规则' : '创建规则'" width="640px" data-test="rule-dialog">
      <el-form :model="form" label-width="110px" data-test="rule-form">
        <el-form-item label="名称" required>
          <el-input v-model="form.name" placeholder="如：高温自动开窗" data-test="field-name" />
        </el-form-item>

        <el-form-item label="触发器类型" required>
          <!-- 下拉值域与后端 models.AutomationTriggerType **不同**：后端只有 sun_event
               一个类型（日出/日落由 trigger_sun_kind 区分），而设计稿 R4 与用户需求都要求
               下拉里直接看到「日出」「日落」两项。故这里绑 triggerSelection 这个
               computed 代理（读时把 sun_event 展开为 kind，写时把 sunrise/sunset 折叠回
               trigger_type='sun_event' + trigger_sun_kind），提交时由 buildPayload() 映射。
               为什么不塞一个 "sun_event" 项再另设二级下拉：那会让下拉文案与用户脑中的
               "日落前 30 分钟" 隔一层，而这句话本身就是需求原文（设计 D-2）。 -->
          <el-select v-model="triggerSelection" data-test="field-trigger-type" @change="onTriggerTypeChange">
            <el-option label="传感器阈值" value="sensor_threshold" />
            <el-option label="时间窗口" value="time_window" />
            <el-option label="日出" value="sunrise" />
            <el-option label="日落" value="sunset" />
            <el-option label="事件驱动（暂不支持创建）" value="event" disabled />
          </el-select>
        </el-form-item>

        <!-- sensor_threshold 字段组 -->
        <template v-if="form.trigger_type === 'sensor_threshold'">
          <!-- 数据来源（方案 v0.3 §4.3）：①某台边缘设备（存量语义，NULL 同义）②来源组（主备/复合）。
               来源组是跨逻辑设备的"一个数据来源"对象，规则只绑 source_group_id；目标设备锚点由后端保存期双写。 -->
          <el-form-item label="数据来源">
            <el-select v-model="form.source_kind" data-test="field-source-kind" @change="onSourceKindChange">
              <el-option label="边缘设备" value="edge_device" />
              <el-option label="来源组（主备 / 复合）" value="source_group" />
            </el-select>
          </el-form-item>
          <template v-if="form.source_kind === 'source_group'">
            <el-form-item label="来源组" required>
              <el-select v-model="form.source_group_id" filterable data-test="field-source-group" @change="onSourceGroupChange">
                <el-option v-for="g in sourceGroupOptions" :key="g.id" :label="groupOptionLabel(g)" :value="g.id" />
              </el-select>
            </el-form-item>
            <el-form-item v-if="selectedSourceGroup" label="组语义">
              <span class="hint" data-test="source-group-summary">{{ groupSummary(selectedSourceGroup) }}</span>
            </el-form-item>
            <el-form-item label="冷却提示">
              <span class="hint" data-test="cooldown-hint">来源组规则的冷却时间须 ≥ 成员采集间隔（后端保存期强制）；复合组按 combine 合成，缺员处置见组配置。</span>
            </el-form-item>
          </template>
          <el-form-item v-else label="边缘设备" required>
            <el-select v-model="form.trigger_edge_device_id" filterable data-test="field-trigger-device">
              <el-option v-for="d in devices" :key="d.id" :label="d.name" :value="d.id" />
            </el-select>
          </el-form-item>
          <el-form-item label="传感器" required>
            <el-input
              v-model="form.trigger_sensor_name"
              :disabled="form.source_kind === 'source_group'"
              :placeholder="form.source_kind === 'source_group' ? '由来源组的数据类别决定' : '如：temperature'"
              data-test="field-sensor"
            />
          </el-form-item>
          <el-form-item label="条件" required>
            <div class="cond-row">
              <el-select v-model="form.trigger_comparator" data-test="field-comparator" style="width: 100px">
                <el-option v-for="c in comparators" :key="c.value" :label="c.label" :value="c.value" />
              </el-select>
              <el-input-number v-model="form.trigger_threshold" :precision="3" data-test="field-threshold" />
            </div>
          </el-form-item>
          <el-form-item label="持续时长">
            <el-input-number v-model="form.trigger_duration_sec" :min="0" :step="10" data-test="field-duration" />
            <span class="hint">秒，0 = 立即触发</span>
          </el-form-item>
        </template>

        <!-- time_window 字段组 -->
        <template v-if="form.trigger_type === 'time_window'">
          <el-form-item label="开始时间" required>
            <el-input v-model="form.trigger_window_start" placeholder="HH:MM，如 08:00" data-test="field-window-start" />
          </el-form-item>
          <el-form-item label="结束时间" required>
            <el-input v-model="form.trigger_window_end" placeholder="HH:MM，如 18:00" data-test="field-window-end" />
          </el-form-item>
          <el-form-item label="触发沿" required>
            <el-select v-model="form.trigger_window_edge" data-test="field-window-edge">
              <el-option label="进入窗口" value="enter" />
              <el-option label="离开窗口" value="exit" />
              <el-option label="窗口内持续" value="inside" />
            </el-select>
          </el-form-item>
        </template>

        <!-- sun_event 字段组 (设计稿 R6 / §6 D-1 D-2) -->
        <template v-if="isSunTrigger">
          <el-form-item label="日出 / 日落" required>
            <el-select v-model="form.trigger_sun_kind" data-test="field-sun-kind">
              <el-option v-for="opt in SUN_KIND_OPTIONS" :key="opt.value" :label="opt.label" :value="opt.value" />
            </el-select>
          </el-form-item>
          <el-form-item label="偏移分钟" required>
            <div class="sun-offset-row">
              <el-input-number
                v-model="form.trigger_sun_offset_min"
                :min="SUN_OFFSET_MIN"
                :max="SUN_OFFSET_MAX"
                :step="5"
                data-test="field-sun-offset"
                aria-label="相对日出/日落的偏移分钟，负值为之前"
              />
              <!-- 实时自然语言回显（设计 D-2）：用户需求原话就是"日落前 30 分钟"，
                   让界面直接说出这句话，用户不必在脑内把"方向 + 绝对值"翻译一遍。 -->
              <span class="hint" data-test="offset-sentence">{{ sunOffsetText(form.trigger_sun_kind, form.trigger_sun_offset_min) }}</span>
            </div>
            <div class="hint-block">范围 -720 ~ 720 分钟；负值 = 日出/日落之前，正值 = 之后，0 = 正点</div>
          </el-form-item>
          <!-- 坐标/时区继承状态（用户裁决 O-3: 节点级权威 + 规则级可空覆盖）。
               为什么必须在界面上说清"留空=继承"以及"节点没配会被后端拒绝保存"：
               后端按 O-7 fail-closed，用户在节点页配坐标前保存日出规则会拿到 400 ——
               把这层因果提前说出来，用户才不会以为页面坏了。 -->
          <el-form-item label="坐标与时区">
            <div class="sun-location" data-test="sun-location">
              <div class="sun-location__state" :data-state="sunLocationState(form)" data-test="sun-location-state">
                {{ sunLocationText(form) }}
              </div>
              <div class="sun-location__fields">
                <el-input v-model="sunLatitudeText" placeholder="纬度，如 31.23" data-test="field-latitude" aria-label="规则级纬度（留空 = 继承节点）" />
                <el-input v-model="sunLongitudeText" placeholder="经度，如 121.47" data-test="field-longitude" aria-label="规则级经度（留空 = 继承节点）" />
                <el-input v-model="form.timezone" placeholder="时区，如 Asia/Shanghai" data-test="field-timezone" aria-label="规则级 IANA 时区（留空 = 继承节点）" />
              </div>
            </div>
          </el-form-item>
        </template>

        <el-form-item label="附加条件">
          <el-input
            v-model="form.conditions_json"
            type="textarea"
            :rows="2"
            placeholder='JSON 数组，如 [{"sensor":"humidity","comparator":"lt","threshold":30}]'
            data-test="field-conditions"
          />
        </el-form-item>

        <!-- R7: 动作区由「单动作字段组」升级为**多动作卡片列表**。
             为什么用纵向卡片而不是表格（设计 D-3）：① 卡片天然容纳长度不定的
             「风险原因」文本；② 上移/下移是**顺序语义**操作，纵向列表的视觉顺序
             与执行顺序一致；③ 复用既有 DeviceControlPanel 的卡片语言
             （圆角 8px + --el-border-color-lighter 边框），不引入新视觉。 -->
        <el-form-item label="动作" required>
          <div class="action-toolbar">
            <span class="text-muted" data-test="action-count">
              共 {{ form.actions.length }} 个动作{{ riskyActions.length
                ? `，其中 ${riskyActions.length} 个风险动作（保存前需逐项确认）`
                : '，无风险动作（保存时不会弹出二次确认）' }}
            </span>
            <el-button type="primary" size="small" data-test="add-action" @click="addAction">+ 添加动作</el-button>
          </div>
          <ul class="action-list" data-test="action-list">
            <li
              v-for="(action, index) in form.actions"
              :key="action.uid"
              class="action-card"
              :class="{ 'is-risk': !!riskBadgeOf(action) }"
              :data-risk="riskBadgeOf(action) ? '1' : '0'"
              data-test="action-card"
            >
              <div class="action-card__head">
                <span class="action-card__index" aria-hidden="true">{{ index + 1 }}</span>
                <span class="action-card__name">{{ actionTitle(action) }}</span>
                <!-- 风险徽标：**图标 + 文字**，不只用颜色（设计 D-6 四重编码）。
                     红绿色盲用户看不到红色徽标，文字与图标是唯一能读出"风险"的路径。
                     第三种状态（目录未加载 / 动作不在目录里）**不渲染任何等级徽标** ——
                     把未知显示成「普通」等于替后端断言"它不危险"，见 riskPresentation 注释。 -->
                <el-tag v-if="riskBadgeOf(action)" type="danger" size="small" class="risk-badge" data-test="risk-badge">
                  <span aria-hidden="true">{{ riskBadgeOf(action)!.icon }}</span> {{ riskBadgeOf(action)!.label }}
                </el-tag>
                <el-tag v-else-if="cardRiskState(action) === 'normal'" type="info" size="small" class="normal-badge" data-test="normal-badge">
                  <span aria-hidden="true">{{ NORMAL_BADGE.icon }}</span> {{ NORMAL_BADGE.label }}
                </el-tag>
                <el-tag v-else type="info" size="small" effect="plain" class="unknown-badge" data-test="unknown-badge">
                  等级未知
                </el-tag>
                <span class="action-card__ops">
                  <el-button
                    link size="small" :disabled="index === 0" data-test="move-up"
                    :aria-label="`上移第 ${index + 1} 个动作`" @click="moveCard(action.uid, -1)"
                  >↑ 上移</el-button>
                  <el-button
                    link size="small" :disabled="index === form.actions.length - 1" data-test="move-down"
                    :aria-label="`下移第 ${index + 1} 个动作`" @click="moveCard(action.uid, 1)"
                  >↓ 下移</el-button>
                  <el-button
                    link type="primary" size="small" data-test="edit-action-params"
                    :aria-label="`编辑第 ${index + 1} 个动作的参数`" @click="openParams(action.uid)"
                  >编辑参数</el-button>
                  <el-button
                    link type="danger" size="small" data-test="remove-action"
                    :aria-label="`删除第 ${index + 1} 个动作`" @click="removeAction(action.uid)"
                  >删除</el-button>
                </span>
              </div>
              <dl class="action-meta">
                <div>
                  <dt>动作类型</dt>
                  <dd>
                    <el-select v-model="action.action_type" size="small" :aria-label="`第 ${index + 1} 个动作的类型`" :data-test="`action-type-${index}`" @change="onCardTypeChange(action)">
                      <el-option label="设备动作" value="device_action" />
                      <el-option label="通知" value="notification" />
                    </el-select>
                  </dd>
                </div>
                <div v-if="action.action_type === 'device_action'">
                  <dt>目标设备</dt>
                  <dd>
                    <el-select v-model="action.action_device_id" filterable size="small" :aria-label="`第 ${index + 1} 个动作的目标设备`" :data-test="`action-device-${index}`" @change="onCardDeviceChange">
                      <el-option v-for="d in devices" :key="d.id" :label="d.name" :value="d.id" />
                    </el-select>
                  </dd>
                </div>
                <div v-else>
                  <dt>通知级别</dt>
                  <dd>
                    <el-select v-model="action.action_level" size="small" :aria-label="`第 ${index + 1} 个动作的通知级别`" :data-test="`action-level-${index}`">
                      <el-option label="信息" value="info" />
                      <el-option label="警告" value="warning" />
                      <el-option label="严重" value="critical" />
                    </el-select>
                  </dd>
                </div>
                <div v-if="action.action_type === 'device_action'">
                  <dt>动作 ID</dt>
                  <dd>
                    <el-input v-model="action.action_id" size="small" placeholder="如 gpio_set" :aria-label="`第 ${index + 1} 个动作的 ID`" :data-test="`action-id-${index}`" />
                  </dd>
                </div>
                <div class="span2">
                  <dt>参数</dt>
                  <dd class="mono" data-test="action-params-summary">{{ paramsSummary(action.action_params_json) }}</dd>
                </div>
              </dl>
              <!-- 风险原因块：与徽标并存的第二条非颜色通道。后端 risk_reason 尚未实现
                   （设计稿 §9 O-2），此处**如实说明原因文案未下发**，不编造危险说明
                   —— 安全文案不得由前端想象（编造的"危险说明"比没有更危险）。 -->
              <div v-if="riskBadgeOf(action)" class="risk-reason" data-test="risk-reason">
                <span class="risk-reason__icon" aria-hidden="true">⚠</span>
                <span>
                  <span class="risk-reason__label">风险原因：</span>
                  {{ riskReasonOf(action) }}
                </span>
              </div>
            </li>
          </ul>
        </el-form-item>

        <!-- R9: 「需人工确认」是**规则级**开关（触发后是否要人确认），与动作级风险徽标
             语义不同，二者并存不合并（设计稿 §9 O-3）。
             提示文案把后端 V7 的组合条件说清楚：风险动作 + 关掉本开关时，保存必须
             同时带 risk_acknowledged（即下面的风险清单弹窗逐项确认）。 -->
        <el-form-item label="需人工确认">
          <el-switch v-model="form.require_confirmed" data-test="field-require-confirmed" />
          <span class="hint">
            开启则规则触发后只生成「待确认」通知，人工确认后才下发；关闭则触发即执行
          </span>
        </el-form-item>
        <div v-if="!form.require_confirmed && riskyActions.length" class="ack-hint" data-test="ack-hint">
          本规则含 {{ riskyActions.length }} 个风险动作且已关闭人工确认：保存时会要求逐项勾选确认
          （后端按规则级授权放行，授权在动作集合变化后自动失效并需重新确认）。
        </div>

        <el-form-item label="冷却时间">
          <el-input-number v-model="form.cooldown_sec" :min="0" :max="86400" :step="60" data-test="field-cooldown" />
          <span class="hint">秒，0-86400</span>
        </el-form-item>
        <el-form-item label="每日上限">
          <el-input-number v-model="form.max_daily_exec" :min="0" :max="1000" data-test="field-max-daily" />
          <span class="hint">0 = 不限，最大 1000</span>
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" data-test="field-enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" data-test="save-rule" @click="onSave">保存</el-button>
      </template>
    </el-dialog>

    <!-- R8: 动作参数编辑（沿用既有"参数 JSON 文本域"形态，只改承载位置：卡片 → 叠层弹窗）。
         为什么保留文本域而不是按 input_schema 生成表单：参数 schema 由后端 Catalog 驱动，
         改造前就是这个形态；本次只换承载位置，不顺手改参数录入形态（不过度设计）。 -->
    <el-dialog
      v-model="paramsDialogVisible"
      title="编辑动作参数"
      width="min(520px, 92vw)"
      data-test="edit-params-dialog"
    >
      <p class="hint-block">
        目标动作：<span class="mono">{{ editingParamsActionId || '-' }}</span>
      </p>
      <el-input
        v-model="paramsDraft"
        type="textarea"
        :rows="5"
        placeholder='JSON 对象，如 {"pin": 12, "value": 1}'
        data-test="params-json"
        aria-label="动作参数 JSON"
        @input="onParamsDraftChange"
      />
      <p v-if="paramsDraftError" class="params-error" data-test="params-error">{{ paramsDraftError }}</p>
      <template #footer>
        <el-button @click="paramsDialogVisible = false">取消</el-button>
        <el-button type="primary" :disabled="!!paramsDraftError" data-test="params-done" @click="applyParams">完成</el-button>
      </template>
    </el-dialog>

    <!-- R11: 保存前风险清单二次确认（用户裁决 O-2 = 逐规则显式授权 + 保存时强确认）。
         为什么逐项勾选而不是一次总确认（设计 D-5）：一次总确认在 3 个以上风险动作时会
         退化成"闭眼点确定"；逐项勾选强制用户逐条读完 动作名 + 目标设备 + 参数。
         默认焦点在**安全侧**（取消）也沿用既有 feedback.confirmDanger 的裁决
         （utils/feedback.ts: 不得诱导性确认）。 -->
    <el-dialog
      v-model="riskDialogVisible"
      title="保存前确认"
      width="min(560px, 92vw)"
      data-test="save-risk-dialog"
      class="save-risk-dialog"
    >
      <el-alert
        type="error"
        :closable="false"
        show-icon
        :title="`本规则包含 ${riskDialogActions.length} 个风险动作`"
        data-test="risk-summary"
      >
        请核对下列动作的目标设备与参数，逐项勾选确认后才可保存。本弹窗只约束「保存这条规则」。
      </el-alert>
      <ul class="risk-list" data-test="risk-list">
        <li v-for="(item, index) in riskDialogActions" :key="item.seq" class="risk-item" data-test="risk-item">
          <div class="risk-item__name">
            <span aria-hidden="true">⚠</span>
            {{ item.action_name }}
            <el-tag type="danger" size="small" data-test="risk-item-badge">
              <span aria-hidden="true">⚠</span> {{ item.risk_label }}
            </el-tag>
          </div>
          <dl class="risk-item__meta">
            <div><dt>目标设备</dt><dd>{{ item.device_name }}</dd></div>
            <div><dt>动作 ID</dt><dd class="mono">{{ item.action_id }}</dd></div>
            <div><dt>参数</dt><dd class="mono">{{ item.params_summary }}</dd></div>
            <div><dt>风险原因</dt><dd data-test="risk-item-reason">{{ item.risk_reason }}</dd></div>
          </dl>
          <label class="risk-ack">
            <input
              type="checkbox"
              :checked="ackedSeqs.includes(item.seq)"
              :data-test="`risk-ack-${index + 1}`"
              :aria-label="`我已了解「${item.action_name}」对「${item.device_name}」的影响，并确认将其加入本规则`"
              @change="toggleAck(item.seq)"
            />
            <span>我已了解「{{ item.action_name }}」对「{{ item.device_name }}」的影响，并确认将其加入本规则。</span>
          </label>
        </li>
      </ul>
      <template #footer>
        <span class="ack-progress" data-test="ack-progress" role="status">
          已确认 {{ ackedSeqs.length }} / {{ riskDialogActions.length }} 个风险动作
        </span>
        <el-button ref="riskCancelRef" data-test="risk-cancel" @click="cancelRiskSave">取消</el-button>
        <el-button
          type="danger"
          :disabled="!allRisksAcked"
          :loading="saving"
          data-test="confirm-save"
          @click="confirmRiskSave"
        >
          保存规则
        </el-button>
      </template>
    </el-dialog>

    <!-- 命令执行详情（F18：命令 ID 此前点了没反应）。
         只读：展示该次执行的终态与（若有）失败原因/人工裁决，不提供任何写操作
         —— 审计入口的职责是"看清楚"，写操作在设备页的命令面板里。 -->
    <el-dialog
      v-model="commandDetailOpen"
      title="命令执行详情"
      width="min(560px, 92vw)"
      data-test="command-detail-dialog"
    >
      <div v-if="commandDetailLoading" aria-busy="true"><el-skeleton :rows="4" animated /></div>
      <el-descriptions v-else-if="commandDetail" :column="1" border size="small">
        <el-descriptions-item label="命令 ID">
          <span class="mono">{{ commandDetail.command_id }}</span>
        </el-descriptions-item>
        <el-descriptions-item label="执行状态">
          <el-tag size="small" :type="commandDetail.status === 'SUCCEEDED' ? 'success' : commandDetail.status === 'FAILED' ? 'danger' : 'info'">
            {{ operationStatusText(commandDetail.status) }}
          </el-tag>
        </el-descriptions-item>
        <el-descriptions-item label="动作">
          {{ commandDetail.action_id }} <span class="text-muted">v{{ commandDetail.action_version }}</span>
        </el-descriptions-item>
        <el-descriptions-item label="设备">
          #{{ commandDetail.edge_device_id }} <span class="text-muted">({{ commandDetail.node_id }})</span>
        </el-descriptions-item>
        <el-descriptions-item v-if="commandDetail.final_reason" label="失败原因">
          {{ commandDetail.final_reason }}
        </el-descriptions-item>
        <el-descriptions-item v-if="commandDetail.manual_resolution" label="人工裁决">
          {{ commandDetail.manual_resolution.outcome }}：{{ commandDetail.manual_resolution.reason }}
        </el-descriptions-item>
        <el-descriptions-item label="创建时间">{{ commandDetail.created_at }}</el-descriptions-item>
        <el-descriptions-item v-if="commandDetail.completed_at" label="完成时间">{{ commandDetail.completed_at }}</el-descriptions-item>
      </el-descriptions>
      <template #footer>
        <el-button type="primary" @click="commandDetailOpen = false">关闭</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import feedback from '@/utils/feedback'
import { UNKNOWN } from '@/utils/format'
import { Plus } from '@element-plus/icons-vue'
import PageHeader from '@/components/common/PageHeader.vue'
import { useResponsive } from '@/composables/useResponsive'
import { useWebSocketStore } from '@/stores/websocket'
import { logger } from '@/utils/logger'
import { edgeDeviceApi, type EdgeDevice } from '@/api/edgeDevice'
import { sourceGroupApi, type SourceGroup } from '@/api/sourceGroup'
import { deviceOperationApi, type ActionDefinition, type DeviceOperation } from '@/api/deviceOperation'
import {
  automationApi,
  type AutomationRule,
  type AutomationEvent,
  type AutomationEventResult,
  type AutomationComparator,
  type AutomationTriggerType,
  type SunEventKind,
  type PrecheckResponse,
  type PrecheckRiskAction,
  type CreateAutomationRuleRequest,
  type UpdateAutomationRuleRequest,
} from '@/api/automation'
import {
  SUN_KIND_OPTIONS, SUN_OFFSET_MIN, SUN_OFFSET_MAX,
  sunOffsetText, sunLocationState, sunLocationText,
} from './sunEvent'
import {
  NORMAL_BADGE, emptyAction, moveAction, toSequencedActions, paramsSummary, riskBadge, riskPresentation, type ActionDraft,
} from './actionList'

/** el-table 作用域槽的 row 在 EP 类型里是内部 DefaultRow（未从包根导出），此处做一次命名类型的边界收窄（非 any）。 */
const asRule = (row: unknown) => row as AutomationRule
const asEvent = (row: unknown) => row as AutomationEvent

/** 窄屏（<768px，与 theme.css 的 @media (max-width:768px) 同断点）用于取消粘性操作列（F26）。 */
const { isMobile } = useResponsive()

const devices = ref<EdgeDevice[]>([])

// ── 规则 ──
const rules = ref<AutomationRule[]>([])
const rulesLoading = ref(false)

async function fetchRules() {
  rulesLoading.value = true
  try {
    rules.value = await automationApi.listRules()
  } catch {
    feedback.error('加载规则失败')
  } finally {
    rulesLoading.value = false
  }
}

// ── 事件 ──
const events = ref<AutomationEvent[]>([])
const eventsLoading = ref(false)
const eventsTotal = ref(0)
const eventsPage = ref(1)
const eventsPageSize = ref(20)
/** 筛选 (rule_id/result) 的初值, 用于判断筛选是否真的变化而重置页码。 */
const EVENT_FILTER_INITIAL = { ruleId: undefined as number | undefined, result: undefined as AutomationEventResult | undefined }
const lastEventFilter = ref({ ...EVENT_FILTER_INITIAL })
const eventFilterRuleId = ref<number | undefined>(EVENT_FILTER_INITIAL.ruleId)
const eventFilterResult = ref<AutomationEventResult | undefined>(EVENT_FILTER_INITIAL.result)

// F-8: 新结果枚举必须在**三处**映射齐全（resultOptions / resultText / resultTagType），
// 否则新结果码会以英文原串直接显示给用户（"字面量泄漏"）。三处同源维护：
// 新增枚举时若只改一处，tests 里的 F-8 门禁会红。
const resultOptions: Array<{ value: AutomationEventResult; label: string }> = [
  { value: 'executed', label: '已执行' },
  { value: 'pending_confirm', label: '待确认' },
  { value: 'suppressed_cooldown', label: '冷却抑制' },
  { value: 'suppressed_daily_limit', label: '达每日上限' },
  { value: 'condition_changed', label: '条件已变' },
  { value: 'failed_gate', label: '门禁失败' },
  { value: 'failed_dispatch', label: '下发失败' },
  { value: 'notification', label: '已通知' },
  { value: 'expired', label: '已过期' },
  { value: 'confirmation_required', label: '需确认（未授权）' },
  { value: 'partial_failure', label: '部分失败' },
  { value: 'suppressed_no_solar_event', label: '无日出日落事件' },
]

async function fetchEvents() {
  // §3.2.6 MUST: 会改变查询范围的输入 (rule_id/result 筛选) 变化时必须重置分页派生状态。
  // 本函数同时是分页控件与「刷新」按钮的入口 —— 后两者的页码变化属用户**主动翻页**,
  // 不能重置 (否则永远停在第一页)。故只在筛选值真的变了时才把 eventsPage 归 1。
  if (eventFilterRuleId.value !== lastEventFilter.value.ruleId
    || eventFilterResult.value !== lastEventFilter.value.result) {
    eventsPage.value = 1
    lastEventFilter.value = { ruleId: eventFilterRuleId.value, result: eventFilterResult.value }
  }
  eventsLoading.value = true
  try {
    const params: {
      rule_id?: number
      result?: AutomationEventResult
      page: number
      page_size: number
    } = { page: eventsPage.value, page_size: eventsPageSize.value }
    if (eventFilterRuleId.value) params.rule_id = eventFilterRuleId.value
    if (eventFilterResult.value) params.result = eventFilterResult.value
    const res = await automationApi.listEvents(params)
    events.value = res.items
    eventsTotal.value = res.total
  } catch {
    feedback.error('加载事件失败')
  } finally {
    eventsLoading.value = false
  }
}

/** 每页条数变化: 页码必须回到第 1 页 (原第 3 页在新页长下可能已越界)。 */
function onEventsPageSizeChange() {
  eventsPage.value = 1
  void fetchEvents()
}

// ── 对话框 ──
const dialogVisible = ref(false)
const saving = ref(false)
const editingId = ref<number | null>(null)

const comparators: Array<{ value: AutomationComparator; label: string }> = [
  { value: 'gt', label: '>' },
  { value: 'gte', label: '≥' },
  { value: 'lt', label: '<' },
  { value: 'lte', label: '≤' },
  { value: 'eq', label: '=' },
  { value: 'neq', label: '≠' },
]

/**
 * 表单模型。
 *
 * 两处与改造前的结构性差异：
 *   ① `trigger_type` 是**下拉代理值**（sunrise|sunset 代表 sun_event + kind）；
 *      提交前由 toTriggerPayload() 映射回后端契约，见该函数注释。
 *   ② 动作由单值字段组变成 `actions: ActionDraft[]`，提交前由 toSequencedActions()
 *      按下标重排 seq（后端 V1 要求 1..N 连续无重复）。
 * 以下字段改名自设计评审中的措辞，源码里保持与后端 JSON 同名，便于对照。
 */
function emptyForm() {
  return {
    name: '',
    /** 后端契约的触发器类型（sun_event 由 trigger_sun_kind 细分；下拉代理值见 triggerSelection） */
    trigger_type: 'sensor_threshold' as AutomationTriggerType,
    trigger_sun_kind: 'sunset' as SunEventKind,
    trigger_sun_offset_min: 0,
    trigger_sensor_name: '',
    trigger_comparator: 'gt' as AutomationComparator,
    trigger_threshold: 0,
    trigger_duration_sec: 0,
    trigger_window_start: '',
    trigger_window_end: '',
    trigger_window_edge: 'enter' as const,
    // 0 会在 el-select 里回显成「0」（看起来像已选中设备 0）；undefined 才是真空态
    trigger_edge_device_id: undefined as number | undefined,
    // 数据来源（方案 v0.3 §4.3）：edge_device=存量语义；source_group=从来源组取值
    source_kind: 'edge_device' as 'edge_device' | 'source_group',
    source_group_id: undefined as number | undefined,
    latitude: undefined as number | undefined,
    longitude: undefined as number | undefined,
    timezone: '' as string | undefined,
    conditions_json: '',
    actions: [emptyAction(1)] as ActionDraft[],
    cooldown_sec: 300,
    max_daily_exec: 0,
    require_confirmed: false,
    enabled: true,
  }
}

const form = reactive(emptyForm())

// ── 来源组（方案 v0.3 §4.3）──
// 只做为「下拉候选」加载；组语义（active 选择/combine/缺员）由后端 sourceref 负责，前端不重算。
const sourceGroups = ref<SourceGroup[]>([])
/** 编辑中的规则原本是否绑来源组：决定「切回边缘设备」时要不要显式清空来源列。 */
const editingSourceWasGroup = ref(false)
const sourceGroupOptions = computed(() => sourceGroups.value.filter(g => g.enabled !== false))
const selectedSourceGroup = computed(() => sourceGroups.value.find(g => g.id === form.source_group_id))
const COMBINE_LABELS: Record<string, string> = {
  first_available: '首个可用',
  avg: '平均',
  max: '最大',
  min: '最小',
  sum: '求和',
}
function groupOptionLabel(g: SourceGroup): string {
  return `${g.name}（${g.kind === 'primary_backup' ? '主备' : '复合'}）`
}
function groupSummary(g: SourceGroup): string {
  if (g.kind === 'primary_backup') return `主备组 · 类别 ${g.category} · active 成员由数据源状态机决定`
  const n = g.combine === 'first_available' ? 1 : (g.min_members || 1)
  const degrade = g.on_degrade === 'use_available' ? '可降级（事件标记 degraded）' : 'fail_closed'
  return `复合组 · ${COMBINE_LABELS[g.combine || ''] ?? g.combine ?? '—'} · 最少 ${n} 个成员 · 缺员 ${degrade}`
}
function onSourceKindChange(kind: 'edge_device' | 'source_group') {
  if (kind === 'edge_device') {
    form.source_group_id = undefined
    return
  }
  form.trigger_edge_device_id = undefined
  if (form.source_group_id) onSourceGroupChange()
}
/** 选组后把传感器名同步为组的数据类别（组已声明 category；用户不需要再手打物理名）。 */
function onSourceGroupChange() {
  const g = selectedSourceGroup.value
  if (g) form.trigger_sensor_name = g.category
}
async function loadSourceGroups() {
  try {
    const res = await sourceGroupApi.list({ page: 1, page_size: 200 })
    sourceGroups.value = Array.isArray(res.items) ? res.items : []
  } catch {
    // 只影响下拉候选；存量边缘设备规则与页面其它功能不受影响。
    sourceGroups.value = []
  }
}

/**
 * 规则级经/纬度用文本 ref 承载，而不是直接绑 model 上的 number。
 *
 * 为什么必须多这一层：`v-model.number` 在输入框被清空时给出**空串**，写进
 * 数字字段后 JSON.stringify 会把它序列化成 `""`（后端 float64 直接 400）。
 * 清空 = "改为继承节点" 是正常用户操作，不能因此报错。故文本层独立，
 * 只在填写了合法数字时同步到 model。
 */
const sunLatitudeText = ref('')
const sunLongitudeText = ref('')

/** 下拉当前值：sun_event 规则展开为 sunrise/sunset 供下拉显示，其余原样。 */
const triggerSelection = computed<AutomationTriggerType | SunEventKind>({
  get: () => {
    if (form.trigger_type === 'sun_event') return form.trigger_sun_kind
    return form.trigger_type
  },
  set: (value) => {
    if (value === 'sunrise' || value === 'sunset') {
      form.trigger_type = 'sun_event'
      form.trigger_sun_kind = value
    } else {
      form.trigger_type = value as AutomationTriggerType
    }
  },
})

const isSunTrigger = computed(() => form.trigger_type === 'sun_event')

/** 数字文本 → 有限数字或 undefined（空串/非法一律视为"未填"，即继承节点）。 */
function parseCoordinate(text: string): number | undefined {
  const trimmed = text.trim()
  if (!trimmed) return undefined
  const value = Number(trimmed)
  return Number.isFinite(value) ? value : undefined
}

function syncCoordinates() {
  form.latitude = parseCoordinate(sunLatitudeText.value)
  form.longitude = parseCoordinate(sunLongitudeText.value)
}

function uidSeed(): number {
  return Date.now() + Math.floor(Math.random() * 1000)
}

function openCreate() {
  editingId.value = null
  editingSourceWasGroup.value = false
  Object.assign(form, emptyForm())
  sunLatitudeText.value = ''
  sunLongitudeText.value = ''
  dialogVisible.value = true
}

function openEdit(rule: AutomationRule) {
  editingId.value = rule.id
  editingSourceWasGroup.value = rule.source_kind === 'source_group'
  Object.assign(form, {
    name: rule.name,
    trigger_type: rule.trigger_type,
    trigger_sun_kind: rule.trigger_sun_kind ?? 'sunset',
    trigger_sun_offset_min: rule.trigger_sun_offset_min ?? 0,
    trigger_sensor_name: rule.trigger_sensor_name ?? '',
    trigger_comparator: rule.trigger_comparator ?? 'gt',
    trigger_threshold: rule.trigger_threshold ?? 0,
    trigger_duration_sec: rule.trigger_duration_sec ?? 0,
    trigger_window_start: rule.trigger_window_start ?? '',
    trigger_window_end: rule.trigger_window_end ?? '',
    trigger_window_edge: rule.trigger_window_edge ?? 'enter',
    trigger_edge_device_id: rule.trigger_edge_device_id ?? undefined,
    source_kind: (rule.source_kind === 'source_group' ? 'source_group' : 'edge_device') as 'edge_device' | 'source_group',
    source_group_id: rule.source_group_id ?? undefined,
    latitude: rule.latitude,
    longitude: rule.longitude,
    timezone: rule.timezone ?? '',
    conditions_json: rule.conditions_json ?? '',
    actions: ruleActions(rule).map((a, index) => ({
      uid: index + 1,
      action_type: a.action_type === 'notification' ? 'notification' : 'device_action',
      // undefined（而不是 0）= 「未选择」：0 会回显成设备 0，见 actionList.ActionDraft 注释
      action_device_id: a.action_device_id,
      action_id: a.action_id ?? '',
      action_params_json: a.action_params_json ?? '',
      action_level: (a.action_level as ActionDraft['action_level']) ?? 'warning',
    })) as ActionDraft[],
    cooldown_sec: rule.cooldown_sec,
    max_daily_exec: rule.max_daily_exec,
    require_confirmed: rule.require_confirmed,
    enabled: rule.enabled,
  })
  // 编辑既有规则时下拉必须显示真值（而不是空）—— 用文本层回显规则级覆盖值，
  // 未配置（undefined）保持空串以表达"继承节点"。
  sunLatitudeText.value = typeof rule.latitude === 'number' ? String(rule.latitude) : ''
  sunLongitudeText.value = typeof rule.longitude === 'number' ? String(rule.longitude) : ''
  dialogVisible.value = true
}

function onTriggerTypeChange() {
  // 切换触发器类型时清空互斥字段（既有行为保留），并同步清掉 sun_event 专属值。
  if (form.trigger_type === 'sensor_threshold') {
    form.trigger_window_start = ''
    form.trigger_window_end = ''
  } else if (form.trigger_type === 'time_window') {
    form.trigger_sensor_name = ''
    form.trigger_threshold = 0
    form.trigger_duration_sec = 0
  } else if (form.trigger_type !== 'sun_event') {
    form.trigger_sun_offset_min = 0
  }
}

// ── 多动作卡片 (R7) ───────────────────────────────────────────────────────

function addAction() {
  form.actions.push(emptyAction(uidSeed()))
}

function removeAction(uid: number) {
  // 至少保留一个动作：空动作集合后端会 400（"actions 不能为空"），
  // 让用户删到 0 再报错不如当场拦住。
  if (form.actions.length <= 1) {
    ElMessage.warning('规则至少需要一个动作')
    return
  }
  form.actions = form.actions.filter(a => a.uid !== uid)
}

function moveCard(uid: number, delta: number) {
  form.actions = moveAction(form.actions, uid, delta)
}

function onCardTypeChange(action: ActionDraft) {
  // 与既有单动作版的 onActionTypeChange 同口径：切到通知即清掉设备动作专属字段。
  if (action.action_type === 'notification') {
    // undefined = 未选择（el-select 显示「请选择」）。不能写 0：0 会被当成有效值
    // 回显成「0」，看起来像选中了编号 0 的设备。
    action.action_device_id = undefined
    action.action_id = ''
    action.action_params_json = ''
  } else {
    action.action_level = 'warning'
  }
}

/** 卡片标题：动作 ID 优先（用户填的），通知类回落到级别文案。 */
function actionTitle(action: ActionDraft): string {
  if (action.action_type !== 'device_action') return '通知'
  return action.action_id.trim() || '未命名动作'
}

/** 已选目标设备的展示名（找不到时回退 #id，不留空白）。 */
function deviceName(deviceId: number): string {
  const hit = devices.value.find(d => d.id === deviceId)
  return hit ? hit.name : `#${deviceId}`
}

/**
 * 卡片风险徽标。
 *
 * 判定源 = 后端下发的 `requires_confirmation`（`deviceaction.RiskLevel.RequiresConfirmation()`
 * 的对外派生，唯一判定点）。**不读 4 级 risk 字面量** —— 那是驱动出厂方言，
 * 前端自己比较它就会出现第二份判定（R2 防漂移门禁）。
 *
 * ⚠️ 为什么这里不用预检结果：卡片是"编辑中"的状态，动作可能刚被改动、尚未预检；
 * 预检失败（后端未实现/网络失败）时若靠它渲染徽标，风险动作会**静默变成普通动作**。
 * 故卡片徽标从已加载的设备目录（/edge-devices/:id/actions）推导，预检只用于
 * **保存前**（弹窗与放行判定），二者分工明确。
 */
function riskBadgeOf(action: ActionDraft) {
  return riskBadge(catalogDefinition(action))
}

/** 卡片的风险呈现态：risk / normal / unknown（未知时只显示"等级未知"，不猜）。 */
function cardRiskState(action: ActionDraft) {
  return riskPresentation(catalogDefinition(action))
}

/** 在已加载的设备目录里找该动作的定义（找不到返回 null，徽标不渲染：不猜风险）。 */
function catalogDefinition(action: ActionDraft) {
  if (action.action_type !== 'device_action' || !action.action_device_id || !action.action_id) return null
  const entry = actionCatalog.value.get(`${action.action_device_id}:${action.action_id}`)
  return entry ?? null
}

/**
 * 风险原因文案。
 *
 * 后端 Catalog 目前只下发 risk_level / requires_confirmation，**没有面向用户的
 * "为什么危险"字段**（设计稿 §9 O-2 记录：前端硬编码映射表会漂移，且驱动层的
 * description 是操作说明不是危险说明）。故此处如实说明"原因文案尚未下发"，
 * **不编造危险说明** —— 编造的安全文案比没有更危险（用户会据此做判断）。
 * 后端补上 risk_reason 后，这里改为直接消费（弹窗已按该字段设计）。
 */
function riskReasonOf(action: ActionDraft): string {
  const def = catalogDefinition(action)
  if (def?.description) {
    return `后端未下发风险原因，以下是该动作的操作说明（非危险说明）：${def.description}`
  }
  return '后端未下发风险原因文案，请自行确认该动作对目标设备的影响'
}

/** 规则动作的显示辅助（列表页与编辑回填共用）。 */
function ruleActions(rule: AutomationRule) {
  if (rule.actions && rule.actions.length > 0) return rule.actions
  // 兼容期回落：后端尚未回填 actions 子表时用单动作镜像列合成 seq=1。
  if (rule.action_type === 'device_action' && rule.action_id) {
    return [{ seq: 1, action_type: rule.action_type, action_id: rule.action_id, action_device_id: rule.action_device_id }]
  }
  if (rule.action_type === 'notification') {
    return [{ seq: 1, action_type: rule.action_type, action_level: rule.action_level }]
  }
  return []
}

/** 列表页「含 N 个风险动作」计数：读后端下发的 requires_confirmation。 */
function ruleRiskCount(rule: AutomationRule): number {
  return ruleActions(rule).filter(a => riskBadge(a)).length
}

/**
 * 编辑中的风险动作卡片。
 *
 * ⚠️ 这是**界面提示**用的计数（"其中 N 个风险动作（保存前需逐项确认）"），
 * 它读的是已加载的设备目录。真正决定"要不要弹风险清单、能不能保存"的判定
 * 走**后端预检端点**（onSave 里），因为风险等级的唯一事实源在后端。
 * 目录尚未加载完时此值为 0 —— 提示文案会少说一句，但不会漏掉二次确认。
 */
const riskyActions = computed(() => form.actions.filter(a => riskBadgeOf(a)))

function validate(): string | null {
  if (!form.name.trim()) return '请填写规则名称'
  if (form.trigger_type === 'sensor_threshold') {
    if (form.source_kind === 'source_group') {
      if (!form.source_group_id) return '请选择来源组'
    } else if (!form.trigger_edge_device_id) {
      return '请选择边缘设备'
    }
    if (!form.trigger_sensor_name.trim()) return '请填写传感器名称'
  } else if (form.trigger_type === 'time_window') {
    if (!form.trigger_window_start.trim()) return '请填写开始时间'
    if (!form.trigger_window_end.trim()) return '请填写结束时间'
  } else if (form.trigger_type === 'sun_event') {
    // V2 的界面侧前置校验：日出/日落必须有 kind，且坐标/时区三项要"全有或全无"。
    // 全无 = 继承节点（后端会按节点值解析，节点也没有则 400）；部分填写是无意义组合，
    // 后端必拒，前端提前拦下来说清原因（见 sunLocationText）。
    if (!form.trigger_sun_kind) return '请选择日出或日落'
    if (sunLocationState(form) === 'partial') return '纬度、经度、时区必须同时填写，或全部留空以继承节点'
    if (form.trigger_sun_offset_min < SUN_OFFSET_MIN || form.trigger_sun_offset_min > SUN_OFFSET_MAX) {
      return `偏移分钟必须在 ${SUN_OFFSET_MIN} ~ ${SUN_OFFSET_MAX} 之间`
    }
  }
  if (form.actions.length === 0) return '请至少添加一个动作'
  for (const [index, action] of form.actions.entries()) {
    const where = `第 ${index + 1} 个动作`
    if (action.action_type === 'device_action') {
      if (!action.action_device_id) return `${where}：请选择目标设备`
      if (!action.action_id.trim()) return `${where}：请填写动作 ID`
      if (action.action_params_json.trim()) {
        try {
          JSON.parse(action.action_params_json)
        } catch {
          return `${where}：动作参数 JSON 格式错误`
        }
      }
    } else if (!action.action_level) {
      return `${where}：请选择通知级别`
    }
  }
  // JSON 校验
  if (form.conditions_json.trim()) {
    try {
      const parsed = JSON.parse(form.conditions_json)
      if (!Array.isArray(parsed)) return '附加条件必须是 JSON 数组'
    } catch {
      return '附加条件 JSON 格式错误'
    }
  }
  if (form.cooldown_sec < 0 || form.cooldown_sec > 86400) return '冷却时间必须在 0-86400 秒之间'
  if (form.max_daily_exec < 0 || form.max_daily_exec > 1000) return '每日上限必须在 0-1000 之间'
  return null
}

/**
 * 组装提交体。
 *
 * 三处必须显式处理的映射（都会导致后端 400，且都不是"格式小问题"）：
 *   ① 下拉代理值 sunrise/sunset → trigger_type='sun_event' + trigger_sun_kind；
 *   ② 动作卡片 → actions 数组，seq 按**下标**重排（后端 V1: 1..N 连续无重复）；
 *   ③ 坐标只在整个"全有或全无"组合下提交，否则省略（= 继承节点）。
 */
function buildPayload(riskAcknowledged?: boolean): CreateAutomationRuleRequest {
  syncCoordinates()
  const payload: CreateAutomationRuleRequest = {
    name: form.name.trim(),
    trigger_type: form.trigger_type,
    conditions_json: form.conditions_json.trim() || undefined,
    actions: toSequencedActions(form.actions),
    cooldown_sec: form.cooldown_sec,
    max_daily_exec: form.max_daily_exec,
    require_confirmed: form.require_confirmed,
    enabled: form.enabled,
  }
  if (riskAcknowledged !== undefined) payload.risk_acknowledged = riskAcknowledged
  if (form.trigger_type === 'sensor_threshold') {
    payload.trigger_sensor_name = form.trigger_sensor_name.trim()
    payload.trigger_comparator = form.trigger_comparator
    payload.trigger_threshold = form.trigger_threshold
    payload.trigger_duration_sec = form.trigger_duration_sec
    if (form.source_kind === 'source_group') {
      // 来源组：**不传** trigger_edge_device_id —— 锚点由后端按契约 #7 双写
      //（旧二进制降级为「只看主成员」而不是「任意设备触发」）。
      payload.source_kind = 'source_group'
      payload.source_group_id = form.source_group_id
    } else {
      payload.trigger_edge_device_id = form.trigger_edge_device_id
      // 编辑「来源组规则」后切回边缘设备：必须显式清空来源列（后端按指针区分 NULL）。
      if (editingSourceWasGroup.value) {
        payload.source_kind = 'edge_device'
        payload.source_group_id = null
      }
    }
  } else if (form.trigger_type === 'time_window') {
    payload.trigger_window_start = form.trigger_window_start.trim()
    payload.trigger_window_end = form.trigger_window_end.trim()
    payload.trigger_window_edge = form.trigger_window_edge
  } else if (form.trigger_type === 'sun_event') {
    // 后端 fail-closed 的三组字段（方案 §4.1.1）。空串/undefined 一律**省略**，
    // 而不是发空串 —— 后端用指针区分"未提供"（继承节点）与"提供了空值"。
    payload.trigger_type = 'sun_event'
    payload.trigger_sun_kind = form.trigger_sun_kind
    payload.trigger_sun_offset_min = form.trigger_sun_offset_min
    if (sunLocationState(form) === 'override') {
      payload.latitude = form.latitude
      payload.longitude = form.longitude
      payload.timezone = form.timezone?.trim()
    }
  }
  return payload
}

// ── 保存前风险清单二次确认 (R11 / F-4, 用户裁决 O-2) ──────────────────────

interface RiskCheckItem {
  seq: number
  action_id: string
  action_name: string
  device_name: string
  /** 后端下发的展示标签（缺失时用 2 级契约的固定文案） */
  risk_label: string
  risk_reason: string
  params_summary: string
}

const riskDialogVisible = ref(false)
const riskDialogActions = ref<RiskCheckItem[]>([])
const ackedSeqs = ref<number[]>([])
/** 保存成功后真的要执行的提交（避免把已确认的风险清单状态带进下一次保存）。 */
const pendingSubmit = ref<CreateAutomationRuleRequest | null>(null)
const riskCancelRef = ref<{ $el?: HTMLElement } | null>(null)

const allRisksAcked = computed(
  () => riskDialogActions.value.length > 0 && ackedSeqs.value.length === riskDialogActions.value.length,
)

function toggleAck(seq: number) {
  ackedSeqs.value = ackedSeqs.value.includes(seq)
    ? ackedSeqs.value.filter(s => s !== seq)
    : [...ackedSeqs.value, seq]
}

function cancelRiskSave() {
  riskDialogVisible.value = false
  ackedSeqs.value = []
  pendingSubmit.value = null
}

/** 弹窗内某个动作的风险原因：后端 risk_reason 优先，其次回落到卡片同款说明。 */
function riskItemReason(actionId: string, deviceId: number, backendReason?: string): string {
  if (backendReason && backendReason.trim()) return backendReason
  const draft = form.actions.find(a => a.action_id === actionId && a.action_device_id === deviceId)
  return draft ? riskReasonOf(draft) : '后端未下发风险原因文案，请自行确认该动作对目标设备的影响'
}

/**
 * 把**预检响应**转换成风险清单条目。
 *
 * 清单必须列全 动作名 / 目标设备 / 动作 ID / 参数 / 风险原因（需求 §1.3"列出风险操作"）。
 * 动作名只能从本地设备目录取 —— 预检响应只有 `action_device_name`（设备名），
 * 没有动作名字段，把设备名当动作名会让用户确认错对象（安全文案错误）。
 */
function buildRiskItems(risks: PrecheckRiskAction[]): RiskCheckItem[] {
  return risks.map(item => ({
    seq: item.seq,
    action_id: item.action_id,
    action_name: actionNameOf(item.action_id, item.action_device_id),
    device_name: item.action_device_name || deviceName(item.action_device_id),
    risk_label: item.risk_label || '风险',
    risk_reason: riskItemReason(item.action_id, item.action_device_id, item.risk_reason),
    // 摘要优先级: 后端给的摘要 → 后端回带的参数 JSON → 本地卡片里的参数 JSON。
    // 不能写成 `paramsSummary(item.action_params_json) || ...`：paramsSummary(undefined)
    // 返回 '无参数'（真值），会把本地兜底整段短路掉，风险清单里就看不到用户实际填的参数。
    params_summary: item.params_summary
      || (item.action_params_json ? paramsSummary(item.action_params_json) : '')
      || paramsSummary(form.actions.find(a => a.action_id === item.action_id)?.action_params_json),
  }))
}

function openRiskDialog(risks: PrecheckRiskAction[], payload: CreateAutomationRuleRequest) {
  riskDialogActions.value = buildRiskItems(risks)
  ackedSeqs.value = []
  pendingSubmit.value = payload
  riskDialogVisible.value = true
}

/**
 * 保存入口：先探后端预检端点，再按结果分派。
 *
 * ## 为什么必须有"端点不可用"的降级分支（而不是一律 fail-closed）
 *
 * 方案 §4.1.2 的 `POST /automation-rules/precheck` 是**新增端点**，B4 前端不得假定
 * 它已随本批上线：实测后端 routes 里**只有** GET/POST ""、GET/:id、PUT/:id、
 * DELETE/:id、PATCH /:id/enabled、POST /:id/trigger —— **没有 precheck**。
 * 若在端点缺失时一律 fail-closed，用户将**完全无法保存任何规则**（含普通规则），
 * 这比"风险清单拿不到"严重得多。
 *
 * ## 降级到什么？（关键：不能降级成"当作没有风险动作"）
 *
 * 后端**已经**通过规则详情下发动作集合（`actions` 数组），并已在动作目录
 * （`GET /edge-devices/:id/actions`）里下发风险等级（`risk_level` /
 * `requires_confirmation`）。二者合起来足以判定"这条规则是否含风险动作" ——
 * 判定源仍是**后端下发的字段**，前端没有第二份风险等级表（不违反 R2/F-7）。
 *
 * 因此降级语义是"用后端下发的动作集合自行拼装清单"，而**不是**"跳过确认"：
 *   · 判定源 = requires_confirmation（后端字段，与后端确认门同一判定点）；
 *   · 风险原因 = 后端 Catalog 的 description，并**明确标注它不是危险说明**
 *     （后端未实现 risk_reason，见设计稿 §9 O-2；编造危险说明比没有更危险）；
 *   · 设备名 = 本地设备列表（后端未给时的等价来源）。
 *
 * ⚠️ 唯一的真实缺口：目录**尚未加载**的设备上的动作无法判定等级。此时**不得**
 * 静默放行 —— 必须显式告知用户"无法判定风险，请重试/刷新"并阻止保存，
 * 而不是把未知当普通（那正是 F-3/F-6 要防的"猜风险"）。
 */
async function onSave() {
  // 绕过护栏（F-4 反向验证的落点）：风险清单弹窗已打开时，**任何**再次触发的保存
  // 都必须被拒 —— 否则用户可以在未勾选的情况下重复点「保存」把风险确认绕过去。
  // 这条判定放在函数入口而不是只靠按钮 disabled：disabled 是 UI 属性，
  // 键盘/程序化触发/未来的快捷键都可能绕过它。
  if (riskDialogVisible.value) return
  const err = validate()
  if (err) {
    ElMessage.warning(err)
    return
  }
  saving.value = true
  try {
    const payload = buildPayload()
    // 普通动作**无**二次确认，直接落库。此处刻意**不带** risk_acknowledged 字段
    // （既不 true 也不 false）：带 false 会在每次保存普通规则时清掉规则级授权，而
    // "未授权的普通规则被改成含风险动作"必然触发后端 B1 的授权失效（动作集合变化即
    // 清空）并强制重新确认 —— 故 false 不带来额外安全性，只带来无谓的状态翻转；
    // 带 true 则是无依据的授权。省略 = "本次保存不改变授权语义"，与后端 *bool 可空语义一致。
    const submitPlain = async () => { await submitRule(payload) }

    // ① 后端预检端点（方案 §4.1.2，**尚未实现**）。
    const precheck = await automationApi.precheckRule(payload)
    if (precheck !== null) {
      const blocking = precheck.blocking_errors ?? []
      if (blocking.length > 0) {
        ElMessage.warning(blocking[0])
        return
      }
      const risks = precheck.risk_actions ?? []
      if (risks.length === 0) {
        await submitPlain()
        return
      }
      openRiskDialog(risks, payload)
      await focusRiskCancel()
      return
    }

    // ② 降级：端点不可用（未实现/网络失败）。用后端已下发的动作目录判定，
    //    **绝不**把"无法判定"当成"没有风险动作"（见 onSave 顶部注释）。
    const fallback = riskActionsFromCatalog()
    if (fallback === null) {
      feedback.error('无法判定本规则是否包含风险动作（设备动作目录尚未加载），已阻止保存：请稍后重试')
      return
    }
    if (fallback.length === 0) {
      await submitPlain()
      return
    }
    openRiskDialog(fallback, payload)
    await focusRiskCancel()
  } catch {
    feedback.error('保存失败')
  } finally {
    saving.value = false
  }
}

/**
 * 降级路径：从**后端已下发的动作目录**里挑出风险动作，拼成预检同形的清单。
 *
 * 返回 `null` 表示**无法判定**（有 device_action 的目录尚未加载）——调用方必须
 * 据此阻止保存，而不是当作"无风险动作"。
 */
function riskActionsFromCatalog(): PrecheckRiskAction[] | null {
  const risks: NonNullable<PrecheckResponse['risk_actions']> = []
  for (const [index, action] of form.actions.entries()) {
    if (action.action_type !== 'device_action' || !action.action_device_id || !action.action_id) continue
    const def = catalogDefinition(action)
    if (!def) return null // 目录未加载 ⇒ 无法判定，不得猜
    if (!riskBadge(def)) continue
    risks.push({
      seq: index + 1,
      action_id: action.action_id,
      action_device_id: action.action_device_id,
      action_device_name: deviceName(action.action_device_id),
      risk_level: def.risk_level,
      risk_label: '风险',
      // 刻意**不填** risk_reason：后端未实现该字段，而 Catalog 的 description 是
      // **操作说明**不是**危险说明**（例如 set_mos_policy 的 description 写的是
      // "一次提交充放电两个关闭位；bounded 写+读回 fet_status 对账"，直接当风险原因
      // 用户读不懂 —— 设计稿 §9 O-2 记录的正是这一点）。留空后 riskItemReason 会
      // 回落到 riskReasonOf()，那里会**显式标注"非危险说明"**并说明后端未下发原因。
      // 编造一条听起来像危险说明的文案，比如实说"未下发"更危险。
      params_summary: paramsSummary(action.action_params_json),
    })
  }
  return risks
}

/** 默认焦点落在**安全侧**（取消），沿用 utils/feedback.ts 的既有裁决（不得诱导性确认）。 */
async function focusRiskCancel() {
  await nextTick()
  const el = riskCancelRef.value?.$el
  if (el && typeof el.focus === 'function') el.focus()
}

/**
 * 动作的人类可读名。
 *
 * 预检响应里只有 `action_device_name`（**设备**名），没有动作名（方案 §4.1.2 的字段表）。
 * 故动作名只能从已加载的设备动作目录里取；目录里也没有时回落到 action_id ——
 * 宁可显示 ID 也不能把设备名当成动作名（那会让用户确认错东西，是安全文案错误）。
 */
function actionNameOf(actionId: string, deviceId: number): string {
  const def = catalogDefinition({ uid: 0, action_type: 'device_action', action_device_id: deviceId, action_id: actionId, action_params_json: '', action_level: 'warning' })
  return def?.name || actionId
}

/** 逐项勾满后才可调用（按钮 disabled 的同时在函数里再判一次：绕过 UI 也必须被拒）。 */
async function confirmRiskSave() {
  if (!allRisksAcked.value) return
  const payload = pendingSubmit.value
  if (!payload) return
  saving.value = true
  try {
    await submitRule({ ...payload, risk_acknowledged: true })
    riskDialogVisible.value = false
    ackedSeqs.value = []
    pendingSubmit.value = null
  } catch {
    feedback.error('保存失败')
  } finally {
    saving.value = false
  }
}

async function submitRule(payload: CreateAutomationRuleRequest) {
  if (editingId.value) {
    await automationApi.updateRule(editingId.value, payload as UpdateAutomationRuleRequest)
    ElMessage.success('规则已更新')
  } else {
    await automationApi.createRule(payload)
    ElMessage.success('规则已创建')
  }
  dialogVisible.value = false
  void fetchRules()
  void fetchEvents()
}

async function onToggle(rule: AutomationRule, enabled: boolean) {
  try {
    await automationApi.setRuleEnabled(rule.id, enabled)
    rule.enabled = enabled
  } catch {
    feedback.error('切换失败')
  }
}

async function onDelete(rule: AutomationRule) {
  // 删除规则不可恢复：走统一危险确认（danger 确认键 + 焦点不落在破坏性按钮上）。
  const confirmed = await feedback.confirmDanger(`删除规则「${rule.name}」？`, {
    title: '确认删除',
    confirmText: '删除',
    cancelText: '取消',
  })
  if (!confirmed) return
  try {
    await automationApi.deleteRule(rule.id)
    ElMessage.success('已删除')
    void fetchRules()
    void fetchEvents()
  } catch {
    /* 取消或失败静默 */
  }
}

async function onConfirmEvent(event: AutomationEvent) {
  // 确认制事件的「确认执行」会放开人工确认门、直接下发设备动作（不可撤销），
  // 属破坏性操作，故走 danger 确认 + 安全侧焦点。
  const confirmed = await feedback.confirmDanger('确认执行该高风险动作？', {
    title: '确认执行',
    confirmText: '确定',
    cancelText: '取消',
  })
  if (!confirmed) return
  try {
    await automationApi.confirmEvent(event.id)
    ElMessage.success('已确认执行')
    void fetchEvents()
  } catch {
    /* 取消或失败静默 */
  }
}

// ── 手动触发 ──
const triggeringId = ref<number | null>(null)

async function onTrigger(rule: AutomationRule) {
  // 手动触发是**非破坏性**操作 ⇒ 走 feedback.confirm (普通确认)。
  // 不得改走 confirmDanger: §3.4.3 的 danger 类目只用于破坏性操作 (本仓既有结构性裁决,
  // 见 listPageConventions.spec.ts)。confirm 返回 boolean, 取消即 return。
  const ok = await feedback.confirm(
    `手动触发规则「${rule.name}」？将跳过条件评估与确认制直接执行动作 (cooldown/日熔断仍生效)。`,
    { title: '手动触发', confirmText: '触发', cancelText: '取消' },
  )
  if (!ok) return // 用户取消
  triggeringId.value = rule.id
  try {
    const ev = await automationApi.triggerRule(rule.id)
    const msg = resultText(ev.result)
    if (ev.result === 'executed' || ev.result === 'notification') {
      ElMessage.success(`已触发: ${msg}`)
    } else if (ev.result === 'suppressed_cooldown') {
      ElMessage.warning(`触发被抑制: ${msg} (${ev.detail ?? ''})`)
    } else if (ev.result === 'suppressed_daily_limit') {
      ElMessage.warning(`触发被抑制: ${msg}`)
    } else {
      ElMessage.info(`触发结果: ${msg}${ev.detail ? ' — ' + ev.detail : ''}`)
    }
    void fetchEvents()
  } catch (e: unknown) {
    feedback.handleError(e, '触发失败')
  } finally {
    triggeringId.value = null
  }
}

/**
 * 命令 ID 回链（F18：此前是 console.log 占位 —— 点了没反应，属"静默失败"）。
 *
 * 本仓**没有**"命令详情页"路由，但有真实可查的端点
 * `GET /api/v1/device-operations/:command_id`（handler_device_operation.go:113）。
 * 因此这里不再"跳转"，而是**就地查询并展示**该次执行的终态 —— 用户点这个 ID 的
 * 真实意图是"这条命令后来怎么样了"，而不是"换一个页面"。
 *
 * 为什么用只读弹窗而不是引导用户去设备详情页：审计行只带 command_id，
 * 不带 edge_device_id 之外的定位信息；把用户丢到另一个列表再自己找，等于没回链。
 */
const commandDetail = ref<DeviceOperation | null>(null)
const commandDetailLoading = ref(false)
const commandDetailOpen = ref(false)

async function goCommand(commandId: string) {
  commandDetailOpen.value = true
  commandDetailLoading.value = true
  commandDetail.value = null
  try {
    commandDetail.value = await deviceOperationApi.get(commandId)
  } catch (e: unknown) {
    // 查不到（例如执行记录已被保留期清理）必须**显式告知**，不能留一个空弹窗。
    //
    // 必须用 handleErrorWithContext 而不是 handleError：后者的第二参数是**兜底**，
    // 只在拿不到 message 时才生效；而后端**总是**带 message（envelope.Error 必写 Message），
    // 实测结果是用户只看到 "operation not found"，**不知道是哪个操作失败了**
    // （见 utils/feedback.ts:114-131 的记录）。这里"查命令记录"这个上下文对用户有信息量，
    // 所以显式拼进消息。
    feedback.handleErrorWithContext(e, '未能读取该命令的执行记录（可能已被保留期清理）')
    commandDetailOpen.value = false
  } finally {
    commandDetailLoading.value = false
  }
}

/** 执行状态 → 中文（与后端 OperationStatus 枚举一一对应，不给未定义的兜底文案）。 */
function operationStatusText(status: string): string {
  const map: Record<string, string> = {
    QUEUED: '排队中',
    DISPATCHED: '已下发',
    DEVICE_ACCEPTED: '设备已受理',
    VERIFYING: '核验中',
    SUCCEEDED: '成功',
    FAILED: '失败',
    CANCELLED: '已取消',
    UNKNOWN: '结果未知',
  }
  return map[status] ?? status
}

// ── 动作参数编辑弹窗 (R8) ──
const paramsDialogVisible = ref(false)
const paramsEditingUid = ref<number | null>(null)
const paramsDraft = ref('')
const paramsDraftError = ref('')
const editingParamsActionId = computed(
  () => form.actions.find(a => a.uid === paramsEditingUid.value)?.action_id ?? '',
)

function openParams(uid: number) {
  const action = form.actions.find(a => a.uid === uid)
  if (!action) return
  paramsEditingUid.value = uid
  paramsDraft.value = action.action_params_json
  paramsDraftError.value = validateParamsJson(paramsDraft.value)
  paramsDialogVisible.value = true
}

/** 空值 = 无参数（合法）；非空必须是 JSON **对象**（数组/标量过不了后端 CanonicalizeParams）。 */
function validateParamsJson(text: string): string {
  if (!text.trim()) return ''
  try {
    const parsed = JSON.parse(text) as unknown
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return '动作参数必须是 JSON 对象'
    return ''
  } catch {
    return '动作参数 JSON 格式错误'
  }
}

function onParamsDraftChange() {
  paramsDraftError.value = validateParamsJson(paramsDraft.value)
}

function applyParams() {
  paramsDraftError.value = validateParamsJson(paramsDraft.value)
  if (paramsDraftError.value) return
  const action = form.actions.find(a => a.uid === paramsEditingUid.value)
  if (action) action.action_params_json = paramsDraft.value.trim()
  paramsDialogVisible.value = false
}

// ── 显示辅助 ──
/** 事件只持久化 rule_id：回链规则名展示，规则已被删除时回退 #id（不留空白）。 */
function ruleName(ruleId: number): string {
  const hit = rules.value.find(r => r.id === ruleId)
  return hit ? hit.name : `#${ruleId}`
}
function triggerTypeText(t: AutomationTriggerType): string {
  if (t === 'sensor_threshold') return '传感器阈值'
  if (t === 'time_window') return '时间窗口'
  if (t === 'sun_event') return '日出/日落'
  return '事件驱动'
}
/** 触发器摘要：日出/日落与表单里的实时文案**同源**（sunOffsetText），避免两套措辞。 */
function triggerSummary(rule: AutomationRule): string {
  if (rule.trigger_type === 'sensor_threshold') {
    const cmp = comparators.find(c => c.value === rule.trigger_comparator)?.label ?? rule.trigger_comparator
    const base = `${rule.trigger_sensor_name} ${cmp} ${rule.trigger_threshold}`
    // 来源组规则：在触发摘要里显式标注来源组，避免用户把它误读成"某台设备"。
    return rule.source_kind === 'source_group' ? `来源组 #${rule.source_group_id} · ${base}` : base
  }
  if (rule.trigger_type === 'time_window') {
    const edge = rule.trigger_window_edge === 'enter' ? '进入' : rule.trigger_window_edge === 'exit' ? '离开' : '持续'
    return `${rule.trigger_window_start}-${rule.trigger_window_end} ${edge}`
  }
  if (rule.trigger_type === 'sun_event') {
    return sunOffsetText(rule.trigger_sun_kind, rule.trigger_sun_offset_min)
  }
  return UNKNOWN
}
/** 动作列摘要：多动作下把所有动作 ID 串起来（单值摘要已无法表达）。 */
function actionSummary(rule: AutomationRule): string {
  const actions = ruleActions(rule)
  if (actions.length === 0) return UNKNOWN
  return actions
    .map(a => a.action_type === 'notification'
      ? `通知(${a.action_level === 'critical' ? '严重' : a.action_level === 'warning' ? '警告' : '信息'})`
      : (a.action_id ?? UNKNOWN))
    .join(', ')
}
function resultText(r: AutomationEventResult): string {
  const map: Record<AutomationEventResult, string> = {
    executed: '已执行',
    pending_confirm: '待确认',
    suppressed_cooldown: '冷却抑制',
    suppressed_daily_limit: '达每日上限',
    condition_changed: '条件已变',
    failed_gate: '门禁失败',
    failed_dispatch: '下发失败',
    notification: '已通知',
    expired: '已过期',
    confirmation_required: '需确认（未授权）',
    partial_failure: '部分失败',
    suppressed_no_solar_event: '无日出日落事件',
  }
  return map[r] ?? r
}
function resultTagType(r: AutomationEventResult): 'success' | 'warning' | 'danger' | 'info' {
  if (r === 'executed' || r === 'notification') return 'success'
  if (r === 'pending_confirm' || r === 'confirmation_required') return 'warning'
  // partial_failure 是**危险状态**（设备已被改了一半，需人工介入），必须比 failed_* 更显眼，
  // 故与 expired/failed_* 同为 danger。
  if (r.startsWith('failed_') || r === 'expired' || r === 'partial_failure') return 'danger'
  return 'info'
}
function formatValue(v: number): string {
  return Number.isFinite(v) ? String(Math.round(v * 1000) / 1000) : UNKNOWN
}
function formatTime(t?: string | null): string {
  if (!t) return UNKNOWN
  return new Date(t).toLocaleString()
}

/**
 * 设备 ID:动作 ID → ActionDefinition 的索引。
 *
 * 为什么需要它：动作卡片要显示"目标设备 / 动作名 / 风险徽标"，而这些信息只存在于
 * 各设备自己的受控操作目录里（GET /edge-devices/:id/actions）。逐卡片各发一次请求
 * 会变成 N+1，故页面挂载时一次性把**用到的**设备目录取回来建索引。
 *
 * 为什么风险徽标走目录而不走预检：见 riskBadgeOf 的注释（预检失败时风险动作会静默
 * 降级为普通动作）。目录失败时索引为空 ⇒ 徽标不渲染（不猜风险），保存前的预检
 * 仍会把风险动作拦下来，故不会漏确认。
 */
const actionCatalog = ref(new Map<string, ActionDefinition>())
/** 已请求过的设备 ID（含失败）：避免同一设备重复打接口。 */
const catalogRequested = ref(new Set<number>())

/**
 * 取一批设备的受控操作目录并建索引（幂等：已请求过的设备跳过）。
 *
 * 为什么需要"编辑中新增的设备"也走这条路：徽标只在**已加载目录**的设备上渲染。
 * 若只加载既有规则用到的设备，用户在新建规则里选一台从未配过规则的设备时，
 * 风险动作会**没有徽标**（看起来像普通动作）—— 这是安全信息缺失，不是视觉问题。
 */
async function ensureActionCatalog(deviceIds: Iterable<number>) {
  const pending = [...new Set(deviceIds)].filter(id => id > 0 && !catalogRequested.value.has(id))
  if (pending.length === 0) return
  const requested = new Set(catalogRequested.value)
  for (const id of pending) requested.add(id)
  catalogRequested.value = requested
  const next = new Map(actionCatalog.value)
  await Promise.all(pending.map(async (id) => {
    try {
      const items = await deviceOperationApi.actions(id)
      for (const item of items) {
        const def = item.definition
        next.set(`${id}:${def.id}`, def)
      }
    } catch {
      /* 单个设备目录失败不阻塞页面：该卡片不渲染风险徽标，保存前的预检仍会拦风险动作 */
    }
  }))
  actionCatalog.value = next
}

/** 卡片切换目标设备时按需加载该设备的目录（否则风险徽标会缺失）。 */
function onCardDeviceChange(deviceId: number) {
  void ensureActionCatalog([deviceId])
}

async function loadActionCatalog() {
  const ids: number[] = []
  for (const rule of rules.value) {
    for (const action of ruleActions(rule)) {
      if (action.action_type === 'device_action' && action.action_device_id) ids.push(action.action_device_id)
    }
  }
  await ensureActionCatalog(ids)
}

onMounted(async () => {
  await Promise.all([fetchRules(), fetchEvents()])
  try {
    const list = await edgeDeviceApi.getList()
    devices.value = list.items
  } catch {
    /* 设备下拉加载失败不阻塞页面 */
  }
  await loadSourceGroups()
  await loadActionCatalog()
})

// ── G5：人工确认事件到达时自动刷新，不要求用户手动翻页/重进 ──
//
// 改前：后端 `automation/planner.go` 在「待人工确认 / 日熔断 / 系统执行者不可用」时
// 已通过 WS 推送（`automation_pending_confirm` 等），但本页**没有任何订阅** ——
// 事件到达时页面看不到新事件，用户只能手动点「刷新」或翻页才可能发现待确认项，
// 而此时确认时限可能已经过去（策略卡在 pending_confirm）。
// MainLayout 只负责弹提示与跳转（`MainLayout.vue:495-508`），不刷新本页数据。
//
// 订阅的事件名是**后端实际推送的字面量**（`planner.go:474/525/550`），
// 它们尚未收进 `events/events.ts` 的 WS_EVENT 枚举（枚举缺失是既有债，
// 这里先用字面量并注释出处，避免为 3 个事件扩枚举引发更大范围的改动）。
//
// B4 新增两个事件名的订阅（方案 §4.3）：
//   · `automation_confirmation_required` —— C5 消除的**前端闭环**：自动路径上的风险动作
//     被确认门拦住时，前端必须给出用户可见的提示，而不是只往事件表里写一行
//     （改造前该结果落 failed_dispatch，与"传输失败"在界面上无法区分，用户看不到
//     任何可行动信号）。这里**显式提示 + 带规则名**，并把事件表刷出来。
//   · `automation_partial_failure` —— 多动作执行到第 k 个失败、剩余已中止。这是
//     危险状态（设备已被改了一半），必须显式提示"需要人工介入"，不能等用户翻事件表。
const wsStore = useWebSocketStore()
const AUTOMATION_PUSH_EVENTS = [
  'automation_pending_confirm',
  'automation_daily_limit',
  'automation_system_actor_unavailable',
  'automation_confirmation_required',
  'automation_partial_failure',
] as const
let unsubscribeAutomationPushes: Array<() => void> = []

/** WS 载荷可能嵌一层 notification 实体，detail 里才有规则名/动作名。 */
function pushDetail(message: { payload?: { detail?: Record<string, unknown> } } & Record<string, unknown>): Record<string, unknown> {
  const payload = (message.payload ?? {}) as Record<string, unknown>
  const detail = (payload.detail ?? {}) as Record<string, unknown>
  return { ...payload, ...detail }
}

function onAutomationPush(name: string, message: unknown) {
  const detail = pushDetail(message as { payload?: { detail?: Record<string, unknown> } })
  if (name === 'automation_confirmation_required') {
    const rule = detail.rule_name ?? detail.rule_id ?? ''
    const action = detail.action_id ?? ''
    ElMessage.warning(
      `规则「${rule}」的风险动作 ${action} 未获授权，已被确认门拦下：请在本页「触发历史」中对相应事件执行确认，或在编辑规则时勾选风险确认。`,
    )
  } else if (name === 'automation_partial_failure') {
    const rule = detail.rule_name ?? detail.rule_id ?? ''
    const seq = detail.failed_seq ?? ''
    const failed = detail.failed_action_id ?? ''
    // 必须走 feedback（I-1 收敛门禁：禁止裸 ElMessage.error）。这是**错误**级提示，
    // 且带上下文（哪条规则、第几个动作、哪个动作），用户据此才能去人工核查。
    feedback.error(
      `规则「${rule}」第 ${seq} 个动作 ${failed} 执行失败，剩余动作已中止 —— 设备可能处于部分变更状态，请立即人工核查。`,
    )
  }
  // 只刷事件表（确认动作就是针对事件行的）；规则表结构不受推送影响。
  // 失败不打扰用户：这是被动刷新，页面上仍有手动「刷新」按钮兜底。
  void fetchEvents().catch((err: unknown) => {
    logger.warn('自动化推送触发的刷新失败', { error: String(err), event: name })
  })
}

onMounted(() => {
  try {
    unsubscribeAutomationPushes = AUTOMATION_PUSH_EVENTS.map(name => wsStore.subscribe(name, message => onAutomationPush(name, message)))
  } catch (err: unknown) {
    // 订阅失败不得阻塞页面（无 WS 时页面仍可手动刷新）。
    logger.warn('自动化推送订阅失败', { error: String(err) })
  }
})

onUnmounted(() => {
  for (const off of unsubscribeAutomationPushes) off()
  unsubscribeAutomationPushes = []
})
</script>

<style scoped>
.automation-rules-page { display: flex; flex-direction: column; gap: 16px; }
.card { background: var(--el-bg-color); border-radius: 8px; padding: 16px; }
/* 2026-10-21 审计 C-ops P1：「触发历史」卡片头改前不可换行 —— 标题被右侧 332px 的
   筛选条挤到 15×84（逐字竖排），筛选条右界还溢出卡片 14px（卡片右 365 / 筛选右 379）。
   修复：卡片头允许换行（筛选条整体落到第二行）、标题单行不压缩、筛选条自身也换行。 */
.card-head { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; row-gap: 8px; margin-bottom: 12px; }
.card-title { font-size: 15px; font-weight: 600; white-space: nowrap; flex: 0 0 auto; }
.count-tag { margin-left: 8px; }
.mono { font-family: monospace; }
/* 2026-10-21 审计 C-ops P2：「条件」行无宽度约束 ⇒ 393 弹窗 .el-dialog__body
   scrollWidth 368 > clientWidth 361（横向溢出 7px），＋按钮贴到弹窗右内边。
   与 /alerts 同口径：比较符定宽 52px、阈值框弹性保底 176px（± 钮各 44px ⇒ 内层 input ≈88px
   ≥84px），更窄视口下允许换行而不是溢出。 */
.cond-row { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.cond-row .el-select { flex: 0 0 52px; min-width: 52px; }
.cond-row .el-input-number { flex: 1 1 auto; min-width: 176px; max-width: 100%; }
.hint { margin-left: 8px; color: var(--el-text-color-secondary); font-size: 12px; }
.text-muted { color: var(--el-text-color-secondary); }
.trigger-cell, .action-cell { display: flex; align-items: center; gap: 6px; }
.trigger-type-tag { flex-shrink: 0; }
.trigger-summary, .action-summary { font-size: 12px; }
.event-filters { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; justify-content: flex-end; min-width: 0; }
.events-pagination { display: flex; justify-content: flex-end; margin-top: 12px; flex-wrap: wrap; }
/* 分页控件换行（窄屏真实裁切修复，与 LogicalDeviceList.vue 同根因）。
   .events-pagination 的 flex-wrap 只管多个 item 之间；内层 el-pagination 自身
   white-space:nowrap + display:flex 且宽 676.94px，在 768px（.events-pagination 宽 488px）
   就已把「共 N 条」推出容器左界 153px —— 实测 el-pagination x=47.06 < 容器 x=236，
   祖先链 scrollWidth === clientWidth（不可回滚）⇒ 真实裁切。
   同仓范式：firmware/FirmwareManage.vue 的 .firmware-manage :deep(.el-pagination)。 */
.events-pagination :deep(.el-pagination) { flex-wrap: wrap; }
/* F3：表格内的空态（说明原因 + 下一步），与全页 EmptyState 保持同一种信息密度。 */
.table-empty { color: var(--el-text-color-secondary); }
.table-empty p { margin: 0; }
.table-empty-hint { margin-top: 4px; font-size: 12px; color: var(--el-text-color-placeholder); }

/* ============================================================================
   日出/日落字段组 (设计稿 §5 R6 / §6 D-2)
   ========================================================================== */
.sun-offset-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
/* 提示行与输入框同排时 margin-left:8px（.hint 既有口径），此处不需要额外偏移 */
.sun-offset-row .hint { margin-left: 0; }
.hint-block { margin-top: 4px; font-size: 12px; color: var(--el-text-color-secondary); line-height: 1.6; }
.sun-location { width: 100%; }
.sun-location__state {
  font-size: 12px;
  line-height: 1.6;
  color: var(--el-text-color-secondary);
  margin-bottom: 6px;
}
/* 部分填写是**错误状态**（后端必拒），用 warning 色提前告警；仅靠颜色不够，
   文案本身已说明"必须同时填写"（见 sunLocationText）。 */
.sun-location__state[data-state="partial"] { color: var(--el-color-warning); font-weight: 600; }
.sun-location__fields { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 8px; }

/* ============================================================================
   多动作卡片列表 (设计稿 §5 R7-R8 / §6 D-3 D-6)
   ========================================================================== */
.action-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 10px;
  flex-wrap: wrap;
}
.action-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 10px; }
.action-card {
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
  padding: 12px;
  background: var(--el-bg-color);
}
/* 风险卡片的**第一条非颜色通道**：左侧红边 + 浅红底（徽标与原因块是另外两条）。
   四重编码见设计 D-6：图标 / 文字 / 徽标色 / 卡片边。 */
.action-card.is-risk {
  border-color: var(--el-color-danger-light-5);
  border-left: 3px solid var(--el-color-danger);
  background: var(--el-color-danger-light-9);
}
.action-card__head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.action-card__index {
  display: inline-flex; align-items: center; justify-content: center;
  width: 20px; height: 20px; border-radius: 50%;
  background: var(--el-color-primary-light-9); color: var(--el-color-primary);
  font-size: 12px; font-weight: 600; flex: 0 0 auto;
}
.action-card__name { font-size: 14px; font-weight: 600; color: var(--el-text-color-primary); }
.action-card__ops { margin-left: auto; display: flex; align-items: center; gap: 4px; flex-wrap: wrap; }
.risk-badge { font-weight: 600; }
.normal-badge, .unknown-badge { flex: 0 0 auto; }
.action-meta { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 6px 16px; margin: 10px 0 0; }
.action-meta > div { display: flex; gap: 6px; min-width: 0; align-items: center; }
.action-meta > div.span2 { grid-column: 1 / -1; }
.action-meta dt { flex: 0 0 auto; color: var(--el-text-color-secondary); font-size: 12px; }
.action-meta dd { margin: 0; font-size: 12px; color: var(--el-text-color-regular); overflow-wrap: anywhere; flex: 1; min-width: 0; }
.action-meta dd :deep(.el-select) { width: 100%; }
/* 风险原因块：第二条非颜色通道（与徽标并存的文本块）。 */
.risk-reason {
  display: flex; gap: 8px; align-items: flex-start; margin-top: 10px;
  padding: 8px 10px; border-left: 3px solid var(--el-color-danger);
  border-radius: 4px; background: var(--el-bg-color);
  font-size: 12px; color: var(--el-text-color-regular); line-height: 1.6;
}
.risk-reason__icon { color: var(--el-color-danger); font-weight: 700; flex: 0 0 auto; }
.risk-reason__label { font-weight: 600; color: var(--el-color-danger); }
.ack-hint {
  margin: -6px 0 12px 110px;
  padding: 6px 10px;
  border: 1px dashed var(--el-color-warning-light-5);
  border-radius: 4px;
  background: var(--el-color-warning-light-9);
  font-size: 12px;
  color: var(--el-text-color-regular);
  line-height: 1.6;
}
.params-error { margin: 6px 0 0; font-size: 12px; color: var(--el-color-danger); }

/* ============================================================================
   保存前风险清单弹窗 (设计稿 §5 R11 / §6 D-5)
   ========================================================================== */
.risk-list { list-style: none; margin: 12px 0 0; padding: 0; display: flex; flex-direction: column; gap: 10px; }
.risk-item {
  border: 1px solid var(--el-color-danger-light-5);
  border-radius: 8px;
  padding: 10px 12px;
  background: var(--el-bg-color);
}
.risk-item__name {
  display: flex; align-items: center; gap: 6px; flex-wrap: wrap;
  font-weight: 600; color: var(--el-text-color-primary); font-size: 14px;
}
.risk-item__meta { display: grid; grid-template-columns: 1fr; gap: 2px; margin: 8px 0 0; }
.risk-item__meta > div { display: flex; gap: 6px; font-size: 12px; min-width: 0; }
.risk-item__meta dt { flex: 0 0 60px; color: var(--el-text-color-secondary); }
.risk-item__meta dd { margin: 0; color: var(--el-text-color-regular); overflow-wrap: anywhere; }
.risk-ack { display: flex; gap: 8px; align-items: flex-start; margin-top: 10px; font-size: 13px; color: var(--el-text-color-regular); cursor: pointer; line-height: 1.6; }
.risk-ack input { width: 16px; height: 16px; margin: 3px 0 0; flex: 0 0 auto; accent-color: var(--el-color-danger); }
.risk-ack input:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 1px; }
.ack-progress { margin-right: auto; font-size: 12px; color: var(--el-text-color-secondary); }

/* ============================================================================
   窄屏：768px 主断点（与 theme.css / useResponsive 同源）
   ========================================================================== */
@media (max-width: 768px) {
  .action-meta { grid-template-columns: 1fr; }
  .sun-location__fields { grid-template-columns: 1fr; }
  .ack-hint { margin-left: 0; }

  /* ---- 窄屏卡片列表（2026-10-21 第二轮：393 宽表只剩 2 列 + 名称被省略号截断）----
     卡片长度不受列宽限制：名称/详情/命令 ID 一律换行全显，不用 ellipsis 隐藏数据。
     视觉语言沿用 Dashboard .latest-card / DataPanel .data-card（浅描边 + 8px 圆角 + 双列 meta）。 */
  .rule-cards, .event-cards { display: flex; flex-direction: column; gap: 10px; }
  .rule-card, .event-card {
    border: 1px solid var(--el-border-color-lighter);
    border-radius: var(--radius-md, 8px);
    padding: 10px 12px;
    background: var(--el-bg-color);
    display: flex;
    flex-direction: column;
    gap: 8px;
    min-width: 0;
  }
  .rule-card__head { display: flex; align-items: flex-start; justify-content: space-between; gap: 8px; }
  /* 卡片标题：完整名称换行显示（被修掉的正是「客厅高温自动开…」这类省略号截断） */
  .rule-card__name {
    margin: 0;
    font-size: 15px;
    font-weight: 600;
    line-height: 1.4;
    color: var(--el-text-color-primary);
    overflow-wrap: anywhere;
    min-width: 0;
  }
  .rule-card__head :deep(.el-switch) { flex: 0 0 auto; min-height: 44px; }
  .rule-card__badges { display: flex; flex-wrap: wrap; gap: 6px; }
  .rule-card__meta, .event-card__meta { display: flex; flex-direction: column; gap: 4px; margin: 0; min-width: 0; }
  .rule-card__meta > div, .event-card__meta > div { display: flex; gap: 8px; font-size: 13px; line-height: 1.5; min-width: 0; }
  .rule-card__meta dt, .event-card__meta dt { flex: 0 0 auto; color: var(--el-text-color-secondary); }
  .rule-card__meta dd, .event-card__meta dd { margin: 0; min-width: 0; color: var(--el-text-color-regular); overflow-wrap: anywhere; }
  .rule-card__ops, .event-card__ops { display: flex; flex-wrap: wrap; gap: 8px; }
  .rule-card__ops :deep(.el-button), .event-card__ops :deep(.el-button) { margin-left: 0; min-height: 44px; padding-left: 14px; padding-right: 14px; }
  .event-card__head { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
  .event-card__time { font-size: 13px; color: var(--el-text-color-regular); }
  .event-card__source { font-size: 12px; color: var(--el-text-color-secondary); }
  .event-card__rule {
    margin: 0;
    font-size: 15px;
    font-weight: 600;
    line-height: 1.4;
    color: var(--el-text-color-primary);
    overflow-wrap: anywhere;
  }
  .event-card__meta dd:last-child, .event-card__cmd { overflow-wrap: anywhere; }
  .event-card__cmd { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
  .event-card__cmd-id { word-break: break-all; }
  .event-card__cmd :deep(.el-button) { margin-left: 0; min-height: 44px; }
  /* 空态/加载态必须可见且有解释（卡片列表下不渲染表格 #empty 插槽） */
  .cards-empty {
    margin: 0;
    padding: 16px 8px;
    text-align: center;
    font-size: 13px;
    line-height: 1.6;
    color: var(--el-text-color-secondary);
  }
  .cards-empty p { margin: 0; }
  .cards-empty__hint { margin-top: 4px; font-size: 12px; color: var(--el-text-color-placeholder); }
}

/* ============================================================================
   窄屏：480px 加强档 (F-10；设计稿 §7)
   为什么弹窗要变底部抽屉：640px 宽的规则编辑器在 390px 视口下即使 92vw 也只有
   359px 可用宽，表单标签 + 控件会被压到逐字换行；而保存前确认弹窗内容长度不可控
   （风险动作越多越长），全屏抽屉能让内容区独立滚动且 footer 常驻可达。
   ========================================================================== */
@media (max-width: 480px) {
  .card { padding: 12px; }
  /* 动作卡操作区换行到第二行、右对齐，并补足触控高度（theme.css 的 36px/44px 口径）。 */
  .action-card__head { align-items: flex-start; }
  .action-card__name { flex: 1 1 100%; }
  .action-card__ops { margin-left: 0; width: 100%; justify-content: flex-end; gap: 2px; }
  .action-card__ops :deep(.el-button) { min-height: 36px; }
  /* dt/dd 上下堆叠：避免"风险原因"这类长标签挤压正文。 */
  .action-meta > div { flex-direction: column; gap: 0; align-items: stretch; }
  .risk-item__meta > div { flex-direction: column; gap: 0; }
  .risk-item__meta dt { flex: none; }
  /* 弹窗改为底部抽屉：满宽、贴底、顶部圆角、内容区独立滚动。
     ⚠ 2026-10-21 实测：只把 .el-dialog 的 margin 归零是**不够**的 —— 外层
     .el-overlay-dialog 计算样式是 display:block（不是 flex），于是 92vh 高的弹窗
     顶对齐：dialog y=0 / bottom=783，视口 851 时底部留下 68px 灰带、顶部圆角出现在
     屏幕最上方 —— 视觉上是个"顶抽屉"。把 overlay 容器本身改为 flex 且底部对齐，
     抽屉才真正贴底（改后 dialog bottom=851）。 */
  :deep(.el-overlay-dialog) {
    display: flex;
    align-items: flex-end;
    justify-content: center;
  }
  :deep(.el-dialog) {
    width: 100% !important;
    max-width: 100% !important;
    margin: 0 !important;
    border-radius: 12px 12px 0 0 !important;
    max-height: 92vh;
    display: flex;
    flex-direction: column;
  }
  :deep(.el-dialog__body) { overflow: auto; }
  :deep(.el-dialog__footer) { display: flex; flex-wrap: wrap; gap: 8px; }
  :deep(.el-dialog__footer .el-button) { flex: 1 1 auto; min-height: 44px; }
  .ack-progress { flex: 1 1 100%; margin: 0 0 4px; }
}
</style>
