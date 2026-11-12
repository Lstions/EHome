<template>
  <div class="automation-rules-page">
    <PageHeader
      title="自动化策略"
      subtitle="传感器数据、时间窗口或日出日落触发设备动作或通知"
    />

    <!-- 规则列表（桌面 7 列宽表 / 窄屏卡片）：片段已搬到 components/RuleListSection.vue。
         数据加载与动作（fetchRules / onToggle / onDelete / onTrigger）仍留在本页，
         子组件只接 props + 抛语义化事件（create / edit / delete / toggle / trigger）。 -->
    <RuleListSection
      :rules="rules"
      :loading="rulesLoading"
      :is-mobile="isMobile"
      :triggering-id="triggeringId"
      @create="openCreate"
      @edit="openEdit"
      @delete="onDelete"
      @toggle="onToggle"
      @trigger="onTrigger"
    />

    <!-- 触发历史（筛选条 / 桌面 8 列宽表 / 窄屏卡片 / 空态）：片段已搬到 components/EventHistorySection.vue。
         数据加载、筛选值、页码与 fetchEvents 仍留在本页。
         ⚠ 分页控件**故意留在页面**（经 #pagination 插槽注入到 section 内部）：
         listPageConventions 门禁按「路由级页面的模板里有 <el-pagination>」判定列表页，
         把控件搬进子组件会让本页退出该门禁的分母（门禁失去对象）。插槽位置与搬走前一致，
         最终 DOM 不变；.events-pagination 样式也因此留在本页。 -->
    <EventHistorySection
      :events="events"
      :loading="eventsLoading"
      :is-mobile="isMobile"
      :rules="rules"
      v-model:filter-rule-id="eventFilterRuleId"
      v-model:filter-result="eventFilterResult"
      @refresh="fetchEvents"
      @filter-change="fetchEvents"
      @confirm-event="onConfirmEvent"
      @go-command="goCommand"
    >
      <!-- 分页 (真分页: 表格数据来自接口当前页, 不是本地全量切片)。
           改前本表一次性渲染后端 Limit(500) 的全部行 (实测 502 行/11902 节点,
           真实事件 >1006 条被静默截断), fixed 列为每行各注入一份 inline style。 -->
      <!-- 2026-10-21 审计 C-ops P2：total=0 时分页器仍渲染（实测 321×76「共 0 条 / 20条/页 / 1」），
           空列表被两个空控件（空态 + 分页）拉长。total>0 才渲染。 -->
      <template #pagination>
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
      </template>
    </EventHistorySection>

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
               来源组是跨设备的"一个数据来源"对象，规则只绑 source_group_id；目标设备锚点由后端保存期双写。 -->
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
          <!-- 传感器字段：按数据来源分成两个独立分支，而不是"一个文本框加 disabled"。
               为什么必须换掉裸文本框（文档 §3.1/§4.1）：后端接受的是**字段选择器契约**
               （字面量 temperature_1 / 字段族 cell_voltage_*:max / 语义别名 @soc / 派生量），
               手打物理名拼错一个字母 = 保存成功 + 永不触发 + 界面无痕；而文本框连后两类
               形态都表达不出来。
               为什么来源组分支**不放**选择器：字段清单由组的 category 决定，选择器没有
               设备可查目录，只会显示「请先选择边缘设备」—— 对一个已经选了来源组的用户
               是纯误导（不是"不可用"，而是"问错了问题"）。 -->
          <el-form-item v-if="form.source_kind === 'source_group'" label="传感器" required>
            <el-input
              v-model="form.trigger_sensor_name"
              disabled
              placeholder="由来源组的数据类别决定"
              data-test="field-sensor-readonly"
            />
            <div class="hint-block" data-test="sensor-source-group-hint">
              来源组的字段由所选组的数据类别决定（见上方「组语义」），保存时后端按组解析；
              需要逐字段选择或字段族聚合时，请改用「边缘设备」来源。
            </div>
          </el-form-item>
          <el-form-item v-else label="传感器" required>
            <SensorFieldPicker
              v-model="form.trigger_sensor_name"
              :device-id="form.trigger_edge_device_id"
              @change-field="onSensorFieldChange"
            />
          </el-form-item>
          <el-form-item label="条件" required>
            <div class="cond-row">
              <el-select v-model="form.trigger_comparator" data-test="field-comparator" style="width: 100px">
                <el-option v-for="c in comparators" :key="c.value" :label="c.label" :value="c.value" />
              </el-select>
              <el-input-number
                v-model="form.trigger_threshold"
                :precision="thresholdPrecision"
                data-test="field-threshold"
              />
              <!-- 单位只从字段目录取（change-field 给的 SensorField.unit），未知时不渲染。
                   为什么**不**复用 utils/sensor.ts 的 sensorUnitMap：那张表是第二份词表，
                   与 /sensor-fields 的 meta 并行存在；后端改单位或新增字段时它不会跟着变，
                   界面就会显示一个与契约不符的单位（文档 §4.1：字段目录才是权威）。
                   为什么不写成"单位映射表 + 目录兜底"：两份数据源必然漂移，且漂移是静默的。 -->
              <span v-if="thresholdUnit" class="hint threshold-unit" data-test="field-threshold-unit">{{ thresholdUnit }}</span>
            </div>
            <!-- 2026-11-12 保存前试算（文档 §4.4）：用近 24h 真实数据回答"这条规则保存后会不会响"。
                 为什么是按钮、不是输入即请求：后端要扫帧表（24h 窗口在生产可达数十万帧），
                 每敲一位数字都打一次 = 把填表单变成压测；用户又常常先填完整再回头改，
                 自动请求只会产出一串互相覆盖的结论。
                 为什么只给边缘设备：来源组没有单一设备可扫（active 成员由数据源状态机决定），
                 拿组里任一成员试算，回答的是另一条规则的问题，不是这条。
                 为什么"不会触发"只提示、不阻止保存：试算描述的是"最近 24h 有没有数据满足"，
                 不是"规则是否合法"——阈值先填、设备后接入是正常流程。保存校验（validate /
                 onSave）**完全不读 previewResult**，这一行永远只是提示。 -->
            <template v-if="form.source_kind === 'edge_device'">
              <div class="preview-row">
                <el-button
                  size="small"
                  :loading="previewLoading"
                  :disabled="previewDisabled"
                  :title="previewHint"
                  data-test="preview-button"
                  @click="runPreview"
                >用近 24h 数据试算</el-button>
                <span class="hint">只读预演，不改动规则，也不影响保存</span>
              </div>
              <!-- 一句人话就是结论；原文 frames / trace 不往界面上堆（排障看接口）。
                   tone=warning 只用于"试算不可用"，不是规则校验错误。 -->
              <div
                v-if="previewResult"
                class="preview-result"
                :class="{ 'preview-result--warning': previewResult.tone === 'warning' }"
                data-test="preview-result"
              >{{ previewResult.text }}</div>
            </template>
            <div v-else class="hint-block" data-test="preview-unavailable">
              来源组规则不支持试算（组内成员由数据源状态机决定）
            </div>
          </el-form-item>
          <el-form-item label="持续时长">
            <el-input-number v-model="form.trigger_duration_sec" :min="0" :step="10" data-test="field-duration" />
            <span class="hint">秒，0 = 立即触发</span>
          </el-form-item>
        </template>

        <!-- time_window 字段组：时间窗改用**时间选择器**（文档 §3.5 / §4.5）。
             为什么这一步能消灭一整类静默失效：后端保存期只校验"非空"，运行期用
             time.Parse("15:04") 解析，`8:00`（不补零）解析失败时只打一条 warn 就 return
             —— 规则永久失效、不落审计、不产生事件、界面没有任何痕迹。
             选择器把「格式合法」变成控件的输出约束，用户选不出 8:00 这种值。
             为什么是 el-time-picker 而不是 el-time-select：后者按固定步长生成候选
             （常见 15/30 分钟一档），08:07 这类真实需要的时刻根本选不出来。 -->
        <template v-if="form.trigger_type === 'time_window'">
          <el-form-item label="开始时间" required>
            <el-time-picker
              v-model="form.trigger_window_start"
              format="HH:mm"
              value-format="HH:mm"
              :clearable="false"
              placeholder="选择开始时间"
              data-test="field-window-start"
            />
            <!-- 存量规则被规范化时必须说出来：改写用户数据却不说，等于让他在别处
                 看到一个"从没填过"的时间（见 normalizeTimeWindow）。 -->
            <div v-if="windowStartFixNote" class="hint-block" data-test="window-start-fixed">
              {{ windowStartFixNote }}
            </div>
          </el-form-item>
          <el-form-item label="结束时间" required>
            <el-time-picker
              v-model="form.trigger_window_end"
              format="HH:mm"
              value-format="HH:mm"
              :clearable="false"
              placeholder="选择结束时间"
              data-test="field-window-end"
            />
            <div v-if="windowEndFixNote" class="hint-block" data-test="window-end-fixed">
              {{ windowEndFixNote }}
            </div>
            <!-- start > end 在后端是**受支持的跨日窗口**（22:00 → 次日 06:00），不是错误。
                 不明确说出来，用户既不敢这么填，也看不懂列表里 "22:00-06:00 进入" 的含义。 -->
            <div v-if="isOvernightWindow" class="hint-block" data-test="window-overnight">
              跨越零点：{{ form.trigger_window_start }} 到次日 {{ form.trigger_window_end }}
            </div>
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
                <!-- 时区：文本 → 下拉。手输 "Asia/Shangai"（少一个 h）后端按 O-7 fail-closed
                     拒绝保存，运行期解析失败则规则不触发 —— 而 IANA 全表是**浏览器内置**的
                     （Intl.supportedValuesOf('timeZone')），没有任何理由让人背拼写。
                     首项显式给出「继承节点（留空）」：空值是一个有语义的选项，不是"没选"。 -->
                <el-select
                  v-model="form.timezone"
                  filterable
                  clearable
                  :allow-create="timezoneAllowCreate"
                  placeholder="选择时区（留空 = 继承节点）"
                  aria-label="规则级 IANA 时区（留空 = 继承节点）"
                  data-test="field-timezone"
                  @change="onTimezoneChange"
                >
                  <el-option label="继承节点（留空）" value="" />
                  <el-option v-for="tz in timezoneOptions" :key="tz" :label="tz" :value="tz" />
                </el-select>
              </div>
              <!-- 与改造前同一条因果：留空 = 继承节点；节点也没配时后端会拒绝保存。
                   不让用户以为"留空 = 一个安全的默认值"。 -->
              <div class="hint-block" data-test="timezone-hint">
                留空 = 继承节点时区；节点也没有配置时，后端会拒绝保存日出/日落规则（fail-closed）。
              </div>
            </div>
          </el-form-item>
        </template>

        <!-- 附加条件（文档 §4.3）：JSON 文本域 → 结构化条件行。
             为什么条件行必须复用**同一个** SensorFieldPicker（而不是给条件另设一个普通下拉/文本框）：
             后端 conditionsSatisfied 逐条走的也是 evalSelector（evaluator.go:1018-1033）——
             与上面的触发字段是**同一个 selector 契约**（字面量 / 字段族 cell_voltage_*:max / @别名）。
             条件行若退回手打物理名，同一张表单里就会出现两套"字段是什么"的答案，其中一套
             拼错一个字母 = 保存成功 + 条件永不满足 + 规则静默不触发（文档 §3.1 整类缺陷）。
             为什么只在 sensor_threshold 下渲染：conditionsSatisfied 只在传感器阈值路径被调用
             （evalRule / evalRuleWithSource / evalCompositeRule），time_window / sun_event 的
             conditions_json 是**惰性数据**。给它编辑入口等于向用户承诺"日出规则也有附加条件"，
             而它永远不会被求值。但存量值仍**保留**并说明（见下方 v-else 分支）——
             清空别人的数据比多显示一句解释糟得多。 -->
        <el-form-item v-if="form.trigger_type === 'sensor_threshold'" label="附加条件">
          <div class="conditions" data-test="conditions-editor">
            <div class="conditions__head">
              <span class="hint-block conditions__hint">全部满足才触发（后端逐条 AND 求值，任一不满足即不触发）</span>
              <!-- 源码模式默认关闭（文档 §4.3）：JSON 是迁移期/排障入口，不是日常录入方式 -->
              <el-button
                link
                type="primary"
                size="small"
                data-test="conditions-raw-toggle"
                @click="toggleConditionsRaw"
              >
                {{ conditionsRawMode ? '切回条件行编辑' : 'JSON 源码' }}
              </el-button>
            </div>

            <!-- 非法存量数据（历史手写 / 被别的工具改坏）：原文原样保留，并说清为什么改不动它。
                 为什么不是"解析失败就清空"：那是**丢用户数据**，而且丢得没有任何痕迹 ——
                 用户下次打开看到的是一张干净的空表单，会以为这些条件本来就不存在。 -->
            <div
              v-if="conditionsRawMode && conditionsRawInvalid"
              class="hint-block conditions__warn"
              data-test="conditions-raw-invalid"
            >
              现有内容无法解析为条件数组，已保留原文；修正后才能切回条件行编辑
            </div>

            <el-input
              v-if="conditionsRawMode"
              v-model="form.conditions_json"
              type="textarea"
              :rows="2"
              placeholder='JSON 数组，如 [{"sensor_name":"humidity","comparator":"lt","threshold":30}]'
              data-test="field-conditions"
            />
            <template v-else>
              <ul v-if="conditionRows.length" class="condition-list" data-test="condition-list">
                <li
                  v-for="(row, index) in conditionRows"
                  :key="row.uid"
                  class="condition-row"
                  data-test="condition-row"
                >
                  <span class="condition-row__index" aria-hidden="true">{{ index + 1 }}</span>
                  <!-- 来源组（source_kind=source_group）没有设备字段目录可查：选择器在这种情形只会
                       显示「请先选择边缘设备」，对已选组的用户是答非所问。故改用物理名输入框，
                       并在下方说明求值口径（组内成员按同一物理名求值）。 -->
                  <el-input
                    v-if="form.source_kind === 'source_group'"
                    v-model="row.selector"
                    class="condition-row__field"
                    placeholder="物理字段名，如 temperature_2"
                    :aria-label="`第 ${index + 1} 个条件的字段物理名`"
                    data-test="condition-selector-input"
                  />
                  <SensorFieldPicker
                    v-else
                    v-model="row.selector"
                    class="condition-row__field"
                    :device-id="form.trigger_edge_device_id"
                  />
                  <el-select
                    v-model="row.comparator"
                    class="condition-row__cmp"
                    :aria-label="`第 ${index + 1} 个条件的比较符`"
                    :data-test="`condition-comparator-${index}`"
                  >
                    <el-option v-for="c in comparators" :key="c.value" :label="c.label" :value="c.value" />
                  </el-select>
                  <el-input-number
                    v-model="row.threshold"
                    class="condition-row__threshold"
                    :aria-label="`第 ${index + 1} 个条件的阈值`"
                    :data-test="`condition-threshold-${index}`"
                  />
                  <el-button
                    link
                    type="danger"
                    size="small"
                    class="condition-row__remove"
                    :aria-label="`删除第 ${index + 1} 个条件`"
                    :data-test="`remove-condition-${index}`"
                    @click="removeCondition(row.uid)"
                  >✕</el-button>
                </li>
              </ul>
              <p v-else class="hint-block" data-test="conditions-empty">
                暂无附加条件：规则只按上方的触发条件判定。
              </p>
              <div class="conditions__foot">
                <el-button link type="primary" size="small" data-test="add-condition" @click="addCondition">+ 添加条件</el-button>
                <span
                  v-if="form.source_kind === 'source_group'"
                  class="hint"
                  data-test="conditions-source-group-hint"
                >
                  来源组没有设备字段目录：组内成员按同一物理名求值。
                </span>
              </div>
            </template>
          </div>
        </el-form-item>
        <!-- 非传感器阈值触发 + 存量带条件：说明它不会被求值，但**不清空**（清空是丢数据）。
             为什么保留而不是隐藏：用户编辑这条规则时看不到任何相关控件，却在保存后
             仍带着一段他看不见的配置 —— 说一句比"看不见"安全。 -->
        <el-form-item v-else-if="form.conditions_json.trim()" label="附加条件">
          <span class="hint-block" data-test="conditions-inactive-hint">
            该规则存有附加条件，但只有「传感器阈值」触发会使用它们（当前触发类型不会求值，保存时按原值保留）。
          </span>
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
                    <!-- 传整张卡片（而不只是 id）：切换设备后要判定"已选动作在新目录里还在不在"，
                         并可能清空它（见 onCardDeviceChange） -->
                    <el-select v-model="action.action_device_id" filterable size="small" :aria-label="`第 ${index + 1} 个动作的目标设备`" :data-test="`action-device-${index}`" @change="onCardDeviceChange(action)">
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
                    <!-- 动作 ID：从**裸输入框**升级为「目录下拉 + 允许手输」。
                         为什么这是缺陷而不是特色：动作目录（actionCatalog）早就为了渲染风险徽标
                         加载在内存里了，却让用户凭记忆背 action_id —— 填一个目录里不存在的动作
                         要等到保存时被后端 400 才知道，而界面从头到尾没告诉过他有哪些可选。
                         为什么仍保留 allow-create 手输：目录可能取不到（设备离线 / 驱动未声明）
                         或设备换型，此时阻断建规则比让用户先填一个"待后端校验"的 ID 更糟
                         （与既有降级口径一致：目录拿不到不阻断建规则）。 -->
                    <el-select
                      v-model="action.action_id"
                      filterable
                      allow-create
                      default-first-option
                      size="small"
                      :placeholder="actionIdPlaceholder(action)"
                      :aria-label="`第 ${index + 1} 个动作的 ID`"
                      :data-test="`action-id-select-${index}`"
                      @change="onCardActionIdChange(action)"
                    >
                      <el-option
                        v-for="option in actionOptions(action)"
                        :key="option.value"
                        :label="option.label"
                        :value="option.value"
                      />
                    </el-select>
                    <!-- 目录提示：三种互斥状态各自说清，绝不把"没取到"说成"没有"。
                         ① 空态（取回失败或确实没有动作）：仍可手输，明说可能是设备离线/驱动未声明；
                         ② 全部不可用：目录**有**动作但一台都下发不了 —— 与①分开，否则用户会去查驱动；
                         ③ 不在目录中（存量规则/设备换型）：只提示，**不清空**用户已有的动作与参数。 -->
                    <p
                      v-if="catalogEmpty(action)"
                      class="hint-block"
                      :data-test="`action-id-empty-hint-${index}`"
                    >
                      该设备暂无可用动作（可能是设备离线或驱动未声明）
                    </p>
                    <p
                      v-else-if="catalogAllUnavailable(action)"
                      class="hint-block"
                      :data-test="`action-id-unavailable-hint-${index}`"
                    >
                      该设备的 {{ actionOptions(action).length }} 个动作当前均不可用，可先配置，执行结果由后端判定
                    </p>
                    <p
                      v-else-if="catalogMissing(action)"
                      class="hint-block action-card__hint--warn"
                      :data-test="`action-id-missing-hint-${index}`"
                    >
                      动作不在当前设备目录中（存量规则或设备换型）：保留原值，保存时由后端校验
                    </p>
                  </dd>
                </div>
                <div class="span2">
                  <dt>参数</dt>
                  <dd>
                    <span class="mono" data-test="action-params-summary">{{ paramsSummary(action.action_params_json) }}</span>
                    <!-- 表单之外的「高级」出口：schema 之外的方言键在表单里没有位置，
                         而且表单在不完整时禁提交，用户有时就是要改原文里的一个字符。
                         只在**确实有 schema**（默认走表单）时出现，否则「编辑参数」本身就是 JSON。 -->
                    <el-button
                      v-if="hasParamSchema(action)"
                      link
                      type="primary"
                      size="small"
                      class="action-card__json-link"
                      :data-test="`action-params-form-link-${index}`"
                      :aria-label="`第 ${index + 1} 个动作改用 JSON 编辑参数`"
                      @click="openJsonParams(action)"
                    >手写 JSON</el-button>
                  </dd>
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

        <!-- 冷却 / 每日上限（文档 §4.5「数字框旁加快捷 chips」）。
             chips 只是把值写回 v-model（不改控件类型）：现场最常用的就是这几个档位，
             手敲 300/1800 时要先换算，换算错就是"冷却比想象中短/长一个量级"。
             为什么不换成下拉：0-86400 的连续值域里，任何枚举都会缺档，数字框才是兜底。 -->
        <el-form-item label="冷却时间">
          <div class="number-field">
            <div class="number-field__main">
              <el-input-number v-model="form.cooldown_sec" :min="0" :max="86400" :step="60" data-test="field-cooldown" />
              <span class="hint">秒，0-86400</span>
            </div>
            <div class="chip-row" data-test="cooldown-chips">
              <button
                v-for="chip in COOLDOWN_CHIPS"
                :key="chip.value"
                type="button"
                class="chip"
                :class="{ 'is-active': form.cooldown_sec === chip.value }"
                :aria-pressed="form.cooldown_sec === chip.value ? 'true' : 'false'"
                :aria-label="`冷却时间设为 ${chip.text}`"
                :data-test="`cooldown-chip-${chip.value}`"
                @click="form.cooldown_sec = chip.value"
              >{{ chip.label }}</button>
            </div>
          </div>
        </el-form-item>
        <el-form-item label="每日上限">
          <div class="number-field">
            <div class="number-field__main">
              <el-input-number v-model="form.max_daily_exec" :min="0" :max="1000" data-test="field-max-daily" />
              <span class="hint">0 = 不限，最大 1000</span>
            </div>
            <div class="chip-row" data-test="daily-limit-chips">
              <button
                v-for="chip in DAILY_LIMIT_CHIPS"
                :key="chip.value"
                type="button"
                class="chip"
                :class="{ 'is-active': form.max_daily_exec === chip.value }"
                :aria-pressed="form.max_daily_exec === chip.value ? 'true' : 'false'"
                :aria-label="`每日上限设为 ${chip.text}`"
                :data-test="`daily-limit-chip-${chip.value}`"
                @click="form.max_daily_exec = chip.value"
              >{{ chip.label }}</button>
            </div>
          </div>
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" data-test="field-enabled" />
        </el-form-item>
      </el-form>
      <!-- 保存前诊断（§3.3）：位置**紧跟 footer 之上** —— 用户刚点完「保存」，视线就在这里，
           放在表单中部会被误读成"某个字段的报错"。默认不渲染，只在本轮 precheck
           返回 blocking_errors / warnings 时出现，且每次 onSave 整体重算（不累积上次残留）。 -->
      <div
        v-if="saveBlockingErrors.length || saveWarnings.length"
        class="save-diagnostics"
        data-test="save-diagnostics"
      >
        <!-- 阻断：硬校验，修正前不允许保存。必须逐条列出全部条目 ——
             改前是 ElMessage.warning(blocking[0])，多条错误要来回保存 N 次。
             role="alert" 让读屏器在诊断区出现时立即播报，避免焦点仍在按钮上而漏读。 -->
        <el-alert
          v-if="saveBlockingErrors.length"
          type="error"
          role="alert"
          :closable="false"
          show-icon
          :title="`保存被拒绝（${saveBlockingErrors.length} 项）`"
          data-test="save-blocking-errors"
        >
          <p class="save-diagnostics__hint">这些是硬校验，修正后才能保存</p>
          <ul class="save-diagnostics__list">
            <li v-for="(message, index) in saveBlockingErrors" :key="`blocking-${index}`">{{ message }}</li>
          </ul>
        </el-alert>
        <!-- 风险提示：后端判定"不阻塞保存，但保存后可能永不触发"。第一次点击只展示，
             主按钮改语义为「我已了解，仍然保存」；第二次点击（同一份内容）才落库。 -->
        <el-alert
          v-if="saveWarnings.length"
          type="warning"
          role="alert"
          :closable="false"
          show-icon
          :title="`以下问题可能导致规则永不触发（${saveWarnings.length} 项）`"
          data-test="save-warnings"
        >
          <ul class="save-diagnostics__list">
            <li v-for="(message, index) in saveWarnings" :key="`warning-${index}`">{{ message }}</li>
          </ul>
        </el-alert>
      </div>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <!-- data-acked 是"风险提示已展示过一次"的可测标记（既有测试仍用 data-test="save-rule"，
             故不换 data-test 值，只加语义属性）。 -->
        <el-button
          type="primary"
          :loading="saving"
          data-test="save-rule"
          :data-acked="warningsAcknowledged ? 'true' : 'false'"
          @click="onSave"
        >
          {{ warningsAcknowledged ? '我已了解，仍然保存' : '保存' }}
        </el-button>
      </template>
    </el-dialog>

    <!-- R8: 动作参数编辑（JSON 文本域 = 「高级」入口）。
         为什么还要保留它：有 input_schema 的动作默认走表单，但 ① 没有 schema 的动作
         （老驱动 / 方言参数）根本没有可生成的字段，JSON 是唯一形态；② 即使有 schema，
         表单项不完整时会禁用提交，用户有时需要直接改原文。两者互转见卡片上的「手写 JSON」
         与下面的「改用表单」。 -->
    <el-dialog
      v-model="paramsDialogVisible"
      title="编辑动作参数"
      width="min(520px, 92vw)"
      data-test="edit-params-dialog"
    >
      <p class="hint-block">
        目标动作：<span class="mono">{{ editingParamsActionId || '-' }}</span>
        <!-- JSON → 表单：把当前文本里**能读出**的对象作为 prefill 带过去（不丢用户刚写的内容），
             不写回模型 —— 用户还没点「完成」，切换编辑器不该悄悄改数据。 -->
        <el-button
          v-if="paramsEditingSchema"
          link
          type="primary"
          size="small"
          :data-test="`action-params-json-link-${paramsEditingIndex}`"
          :aria-label="`第 ${paramsEditingIndex + 1} 个动作改用表单编辑参数`"
          @click="switchParamsToForm"
        >改用表单</el-button>
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

    <!-- 参数表单（schema 驱动）：与 BMS 页 / 设备控制面板同一个组件、同一份 definition。
         prefill 传"已存的参数对象"（编辑既有动作时把值带出来）；提交回来的是一个对象，
         写回 action_params_json 仍然保持 JSON **字符串**（全仓既有的落库形态）。 -->
    <ActionForm
      :visible="paramsFormVisible"
      :definition="paramsFormDefinition"
      :prefill="paramsFormPrefill"
      @update:visible="paramsFormVisible = $event"
      @submit="applyFormParams"
    />

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
         —— 审计入口的职责是"看清楚"，写操作在设备页的命令面板里。
         模板已搬到 components/CommandDetailDialog.vue：本页只保留查询（goCommand）与三份状态。 -->
    <CommandDetailDialog
      v-model="commandDetailOpen"
      :loading="commandDetailLoading"
      :detail="commandDetail"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import feedback from '@/utils/feedback'
import PageHeader from '@/components/common/PageHeader.vue'
// 字段选择的全部逻辑（目录请求、类型门禁、字段族/别名、降级链、保存前 resolve 校验）
// 都在这个共享组件里，本页只负责"接哪台设备""选中的字段对象要不要用来渲染单位"。
import SensorFieldPicker from '@/components/sensor/SensorFieldPicker.vue'
// 参数表单与设备控制面板/BMS 页是**同一个组件**：字段/类型/枚举/范围/必填全部来自后端
// definition.input_schema。本页不另写一套 —— 第二份实现必然漂移
//（设备页能提交的参数在自动化页被拦，或反之），而这正是本次要修掉的"同一系统两套录入"。
import ActionForm from '@/views/edge-device/shared/ActionForm.vue'
import { sensorFieldApi, type SensorField, type SensorFieldPreview, type SensorFieldPreviewFrame } from '@/api/sensorField'
import { ApiError } from '@/api/client'
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
// ── 列表侧拆分（2026-11-12 第一轮，纯搬移）────────────────────────────────────
//   · ./automationFormat：触发器/动作/结果/时间等**纯展示函数**，本页与两个 section 共用；
//     comparators 与 resultText/ruleActions 在本页仍有消费方（表单下拉、手动触发结果提示、
//     编辑回填与动作目录），其余只被 section 消费。
//   · ./components/*：规则区、触发历史区、命令执行详情。数据加载与动作（fetchRules /
//     fetchEvents / onToggle / onDelete / onTrigger / onConfirmEvent / goCommand / WS 订阅）
//     全部留在本页，子组件只接 props + 抛语义化事件。
import { comparators, resultText, ruleActions } from './automationFormat'
import RuleListSection from './components/RuleListSection.vue'
import EventHistorySection from './components/EventHistorySection.vue'
import CommandDetailDialog from './components/CommandDetailDialog.vue'

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

/**
 * 冷却 / 每日上限的快捷值（文档 §4.5）。
 *
 * 只是把值写回 v-model（`form.cooldown_sec = chip.value`），**不改控件类型** ——
 * 数字框仍在，0-86400 的连续值域里任何枚举都会缺档，chips 只覆盖现场最常用的几档。
 * 单位在 label 里用人类可读的写法（5s/1m/1h），写回的一律是**秒**（后端契约字段名就是 _sec）。
 *
 * text 只用于 aria-label：读屏用户听到的是"冷却时间设为 5 分钟"，不是 "5m"。
 */
const COOLDOWN_CHIPS: Array<{ value: number; label: string; text: string }> = [
  { value: 0, label: '0', text: '0 秒（不冷却）' },
  { value: 5, label: '5s', text: '5 秒' },
  { value: 30, label: '30s', text: '30 秒' },
  { value: 60, label: '1m', text: '1 分钟' },
  { value: 300, label: '5m', text: '5 分钟' },
  { value: 1800, label: '30m', text: '30 分钟' },
  { value: 3600, label: '1h', text: '1 小时' },
]

/** 每日上限快捷值：0 = 不限（后端语义），其余为"每天最多 N 次"。 */
const DAILY_LIMIT_CHIPS: Array<{ value: number; label: string; text: string }> = [
  { value: 0, label: '0', text: '0 次（不限）' },
  { value: 1, label: '1', text: '1 次' },
  { value: 3, label: '3', text: '3 次' },
  { value: 10, label: '10', text: '10 次' },
  { value: 50, label: '50', text: '50 次' },
]

// ── 保存前诊断（§3.3：后端 precheck 已经产出 warnings，前端此前全页 0 引用）──
//
// 诊断区**默认不渲染**，只在本轮 onSave 拿到后端判定后出现；位置紧跟 footer 上方
// （用户刚点完保存，视线就在那里），不塞进表单中部 —— 那里会让用户误以为是某字段的报错。
//
// 为什么 blocking/warnings 要逐条列全而不是只弹一条 toast（改前行为）：
//   toast 3 秒消失、只显示 blocking[0]，用户改一条、再保存、再看第二条，往返 N 次；
//   而警告类（warnings）改前根本没显示 —— 那正是"选择器写错一个字母、保存成功、
//   规则永不触发、没有任何报错"的静默失效路径。

/** 硬校验清单（blocking_errors）。非空 = 本次保存被拒，诊断区逐条列出。 */
const saveBlockingErrors = ref<string[]>([])

/** 风险提示（warnings）。非空 = 保存后**可能**永不触发，需要用户二次确认。 */
const saveWarnings = ref<string[]>([])

/**
 * 已向用户展示并确认过的 warnings 内容指纹（warnings.join('|')）。null = 尚未确认。
 *
 * 为什么用**内容指纹**而不是一个 boolean 开关：
 * 确认的对象是"这一份警告"，不是"这个页面从此免检"。两次点击之间用户可能改了字段名/
 * 设备/条件，warnings 随之变化；若用 boolean，第一次确认后随便怎么改都会被静默放行，
 * 改出一个新的解析不到的选择器也直接保存成功 —— 那恰好是 warnings 要防的那类静默失效。
 *
 * 保存成功 / 重新打开对话框 / 切换规则时必须清空（见 resetSaveDiagnostics），
 * 否则上一份规则的确认会被下一份规则复用。
 */
const ackedWarningFingerprint = ref<string | null>(null)

/** 当前展示的 warnings 指纹（与 ackedWarningFingerprint 同源、同算法）。 */
const saveWarningFingerprint = computed(() => saveWarnings.value.join('|'))

/**
 * 主按钮是否处于"已确认过这一份 warnings"的语义（文案改为「我已了解，仍然保存」）。
 *
 * 判据 = 诊断区正展示 warnings，且该指纹已由**拦截分支**记过确认。诊断区在保存成功 /
 * 重开对话框时清空，所以它精确对应"用户已看到提示、还没点第二次"这一个状态。
 *
 * ⚠ 名字是"已确认"（warningsAcknowledged），不是"需要确认"：为真表示**已经**确认过，
 * 所以按钮文案才是「我已了解，仍然保存」（再点一次就落库）。旧名 saveWarningsNeedAck
 * 读起来恰好相反，后维护者照名字写"if (needAck) return"会把逻辑写反。
 *
 * 注意：阻断与 warnings 可能同时存在，那一轮走的是 blocking 分支（不写确认），
 * 因此本值为 false，按钮保持普通「保存」—— 硬校验没修正前，不应诱导用户去点"仍然保存"。
 */
const warningsAcknowledged = computed(() =>
  saveWarnings.value.length > 0
  && ackedWarningFingerprint.value === saveWarningFingerprint.value,
)

/** 清空诊断与确认指纹（保存成功 / 重新打开对话框 / 切换规则时调用）。 */
function resetSaveDiagnostics() {
  saveBlockingErrors.value = []
  saveWarnings.value = []
  ackedWarningFingerprint.value = null
}

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

// ── 传感器字段：阈值单位与小数位的唯一来源（文档 §4.1 / §4.2）──

/**
 * 当前选中的字段对象；**只有**选择器给出「具体字面量字段」时才有值。
 *
 * 族（`cell_voltage_*:max`）、@别名、手输、来源组分支一律 null：它们没有单一单位/小数位
 * （族是多个字段的聚合，成员单位可能不同），此时阈值旁不显示单位（§4.2「未知时不显示」）。
 * 编辑存量规则时的回填由组件负责（目录就绪后 emit 一次 change-field），本页不重复拉目录。
 */
const sensorField = ref<SensorField | null>(null)

/** 选择器给的字段对象变化：只用于渲染单位/小数位，不参与提交（提交体没有单位字段）。 */
function onSensorFieldChange(field: SensorField | null) {
  sensorField.value = field
}

/** 阈值单位：来自字段目录，空串 = 未知 ⇒ 不渲染（不猜、不用 sensorUnitMap 兜底）。 */
const thresholdUnit = computed(() => (sensorField.value?.unit ?? '').trim())

/**
 * 阈值小数位：字段目录的 precision 优先，≤0 回落到 3 位。
 *
 * 生产 meta.precision 全为 0（后端尚未回填 ⇒ 0 = 未知），回落 3 与改造前写死的
 * `:precision="3"` 完全一致 —— 不会因为"这次拿不到精度"就把已有规则的值显示成另一种形态。
 */
const thresholdPrecision = computed(() => {
  const precision = sensorField.value?.precision ?? 0
  return precision > 0 ? precision : 3
})

// ── 保存前试算（文档 §4.4）：用近 24h 真实数据回答"保存后会不会响" ──
//
// 为什么是按钮而不是输入即试算：后端要扫帧表（24h 窗口在生产是数十万帧量级），
// 每敲一位数字就打一次请求，等于把"填表单"变成"压测"；而且用户常常先填完整、
// 再回头改条件，自动请求只会产出一串互相覆盖、永远慢一步的结论。
//
// 为什么任何相关输入变化都必须丢弃旧结论：结论是**某一次具体输入**的答案，
// 留着它等于给用户看"别人的答案"（例如把 45°C 算出的"不会触发"挂到 30°C 的规则上），
// 比没有结论更危险。
//
// 为什么"不会触发"绝不阻止保存：试算回答的是"最近 24h 的观测里它响不响"，
// 不是"这条规则是否合法"。阈值先填、设备后接入是正常流程；把"暂时没数据"升级成
// "保存失败"，与后端对 unbound 只给 warning 的软校验口径相反，也会逼用户为了保存
// 而删掉一条本来正确的规则（见下方 data-test="preview-result" 的注释）。
//
// 为什么算不出最长连续就省略而不是估一个数：试算的全部价值在于"结论有证据"；
// 没有可用采样间隔（帧太少 / 时间戳解析不了）时，任何近似值都会让用户以为
// 后端看到了一个连续的满足区间 —— 宁可不写，也不编。

/** 试算结论；null = 尚未试算，或已被输入变化作废。 */
const previewResult = ref<{ text: string; tone: 'info' | 'warning' } | null>(null)
const previewLoading = ref(false)

/** 请求序号：输入一变就 +1，迟到的旧响应据此丢弃（不得把上一份输入的答案贴回来）。 */
let previewSeq = 0

/** 清空结论并让在途响应失效（设备/字段/比较符/阈值/持续时长任一变化时调用）。 */
function clearPreviewResult() {
  previewSeq += 1
  previewResult.value = null
  previewLoading.value = false
}

/** 按钮缺什么：只有三项齐备才有可试算的输入（edge_device_id 是后端必填）。 */
const previewMissing = computed(() => {
  const missing: string[] = []
  if (!form.trigger_edge_device_id) missing.push('边缘设备')
  if (!form.trigger_sensor_name.trim()) missing.push('字段选择器')
  if (typeof form.trigger_threshold !== 'number' || !Number.isFinite(form.trigger_threshold)) missing.push('阈值')
  return missing
})

const previewDisabled = computed(() => previewLoading.value || previewMissing.value.length > 0)

/** 禁用时 title 必须说清"缺什么"（只给一个灰按钮，用户不知道该先填哪一项）。 */
const previewHint = computed(() => {
  if (previewMissing.value.length > 0) return `请先选择/填写：${previewMissing.value.join('、')}`
  return '用最近 24 小时已上报的数据逐帧重放这条规则；只读预演，不改动规则，也不影响保存'
})

/** 紧凑时长：45s / 4m10s / 1h2m（不写 "4.166m" 这种读不出来的数）。 */
function formatPreviewDuration(seconds: number): string {
  const total = Math.max(0, Math.round(seconds))
  if (total < 60) return `${total}s`
  if (total < 3600) {
    const minutes = Math.floor(total / 60)
    const rest = total % 60
    return rest > 0 ? `${minutes}m${rest}s` : `${minutes}m`
  }
  const hours = Math.floor(total / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  return minutes > 0 ? `${hours}h${minutes}m` : `${hours}h`
}

/** 数值展示：最多 3 位小数并去掉尾零（后端 float64 会给出 42.800000000000004）。 */
function formatPreviewValue(value: number): string {
  return String(Number(value.toFixed(3)))
}

/**
 * 时间戳 → 毫秒；无法解析返回 null（宁可省略结论，也不拿 NaN 去算连续段）。
 *
 * Go 的 time.Time 走 RFC3339Nano（小数位最多 9 位），而部分浏览器的 Date.parse
 * 只稳定接受 3 位；先裁到毫秒再解析，否则 Safari 上所有帧都会被判成"时间戳无效"，
 * 最长连续满足会被无谓地省略。
 */
function frameTimeMs(frame: SensorFieldPreviewFrame): number | null {
  const normalized = frame.timestamp.replace(/(\.\d{3})\d+/, '$1')
  const value = Date.parse(normalized)
  return Number.isFinite(value) ? value : null
}

/**
 * 中位采样间隔（毫秒）。
 *
 * 用**全部帧**的相邻间隔取中位数，而不是只取满足帧的间隔 —— 后者在"只有两帧满足、
 * 且相隔 3 小时"时会把中位数抬高，断段阈值随之失效，空档被误读成连续满足。
 */
function medianFrameIntervalMs(ordered: SensorFieldPreviewFrame[]): number | null {
  const times = ordered.map(frameTimeMs).filter((value): value is number => value !== null)
  if (times.length < 2) return null
  const gaps: number[] = []
  for (let index = 1; index < times.length; index += 1) {
    const gap = times[index] - times[index - 1]
    if (gap > 0) gaps.push(gap)
  }
  if (gaps.length === 0) return null
  gaps.sort((a, b) => a - b)
  const middle = Math.floor(gaps.length / 2)
  return gaps.length % 2 === 1 ? gaps[middle] : (gaps[middle - 1] + gaps[middle]) / 2
}

/**
 * 最长连续满足时长（毫秒）；null = 算不出来（此时 UI 省略这一段，不估算）。
 *
 * 判据与后端 duration_would_fire 同语义：连续段的**首帧 → 末帧**时间差；
 * 相邻帧间隔超过"中位采样间隔 × 3"即断段 —— 设备停报/离线留下的空档不能被当成
 * "一直满足"，否则一次 24h 试算会把两段 1 分钟的满足拼成一条几小时的假连续。
 * 单帧不构成"连续"区间（返回 0 也会被当成结论），故一律省略。
 */
function longestSatisfiedRunMs(ordered: SensorFieldPreviewFrame[], medianMs: number | null): number | null {
  if (medianMs === null) return null
  const gapLimit = medianMs * 3
  let runStart: number | null = null
  let lastTs: number | null = null
  let best = 0
  for (const frame of ordered) {
    const ts = frameTimeMs(frame)
    if (ts === null) return null
    if (frame.ok && frame.satisfied) {
      // 上一段已经结束（不满足帧）或中间空档过大 ⇒ 从这里重新起一段
      if (runStart === null || (lastTs !== null && ts - lastTs > gapLimit)) runStart = ts
      best = Math.max(best, ts - runStart)
    } else {
      runStart = null
    }
    lastTs = ts
  }
  return best > 0 ? best : null
}

/** 最近一帧 trace 里有效值的区间（N=0 时说明"阈值从未达到"用）；取不到返回 null。 */
function previewTraceRange(trace: SensorFieldPreview['aggregation_trace']): { min: number; max: number } | null {
  const values = trace
    .filter(item => item.valid && typeof item.value === 'number' && Number.isFinite(item.value))
    .map(item => item.value)
  if (values.length === 0) return null
  return { min: Math.min(...values), max: Math.max(...values) }
}

/**
 * 把后端证据翻译成一句人话。
 *
 * 口径固定为：满足 N/M 帧、最长连续满足 X（可省略）、持续时长 Y ⇒ 会/不会触发；
 * 之后按需追加"阈值从未达到"与"数据被截断"两句。不堆原始字段：用户要的是
 * "这条规则保存后会不会响"，不是 frames / aggregation_trace 的转储。
 */
function buildPreviewText(res: SensorFieldPreview): string {
  if (res.status !== 'resolved') return '该字段当前解析不到，无法试算'
  const frames = res.frames
  if (res.samples_considered === 0 || frames.length === 0) {
    return '近 24h 没有该字段的数据（设备可能未上报）'
  }
  // 后端按时间升序返回；这里再排一次，避免把顺序当成契约（连续段依赖顺序）。
  const ordered = [...frames].sort((a, b) => (frameTimeMs(a) ?? 0) - (frameTimeMs(b) ?? 0))
  const total = frames.length
  const satisfied = ordered.filter(frame => frame.ok && frame.satisfied).length
  const longestMs = longestSatisfiedRunMs(ordered, medianFrameIntervalMs(ordered))
  // duration_sec=0（立即触发）时后端恒给 duration_would_fire=false —— 那是"没有
  // 持续时长要求"，不是"不会触发"；只要有一帧满足，运行时就一定会触发。
  const wouldFire = res.duration_sec > 0 ? res.duration_would_fire : satisfied > 0
  const head = longestMs === null
    ? `近 24h 试算：满足 ${satisfied}/${total} 帧`
    : `近 24h 试算：满足 ${satisfied}/${total} 帧，最长连续满足 ${formatPreviewDuration(longestMs / 1000)}`
  const verdict = wouldFire ? '会触发' : '不会触发（连续时间未达标）'
  let text = `${head}；持续时长 ${formatPreviewDuration(res.duration_sec)} ⇒ ${verdict}。`
  if (satisfied === 0) {
    const range = previewTraceRange(res.aggregation_trace)
    if (range) {
      const unit = thresholdUnit.value ? ` ${thresholdUnit.value}` : ''
      text += `阈值从未达到（该字段区间 ${formatPreviewValue(range.min)}~${formatPreviewValue(range.max)}${unit}）。`
    }
  }
  if (res.truncated) text += `数据量过大，仅分析了前 ${total} 帧（结论可能偏乐观）。`
  return text
}

/**
 * 用户显式点了「用近 24h 数据试算」才发请求（输入变化走 watch 清结论，不发请求）。
 *
 * 失败不弹 ElMessage：用户正在填一条还没保存的规则，弹窗会打断输入；结论行里
 * 一句"试算暂不可用"足够说明，详情用 logger.warn 留痕给排障。
 */
async function runPreview() {
  if (previewDisabled.value) return
  const deviceId = form.trigger_edge_device_id
  const selector = form.trigger_sensor_name.trim()
  if (!deviceId || !selector) return
  previewSeq += 1
  const requestSeq = previewSeq
  previewResult.value = null
  previewLoading.value = true
  try {
    const res = await sensorFieldApi.preview({
      edge_device_id: deviceId,
      selector,
      comparator: form.trigger_comparator,
      threshold: form.trigger_threshold,
      window_hours: 24,
      duration_sec: form.trigger_duration_sec,
    })
    if (requestSeq !== previewSeq) return
    previewResult.value = { text: buildPreviewText(res), tone: 'info' }
  } catch (error: unknown) {
    if (requestSeq !== previewSeq) return
    if (error instanceof ApiError && error.status === 413) {
      // 413 = 帧预算/时间窗超限：不是配置错误，换一个更小的窗口才可能成功。
      previewResult.value = { text: '时间窗数据量超限，试算不可用（请缩小范围）', tone: 'warning' }
    } else {
      previewResult.value = { text: '试算暂不可用', tone: 'warning' }
      logger.warn('自动化规则试算失败', { error: String(error), selector, deviceId })
    }
  } finally {
    if (requestSeq === previewSeq) previewLoading.value = false
  }
}

// 设备 / 字段 / 比较符 / 阈值 / 持续时长 / 来源与触发类型任一变化 ⇒ 旧结论立即作废。
// 覆盖 source_kind 是为了"切到来源组再切回来"时不把来源组的旧结论带回边缘设备分支。
watch(
  () => [
    form.trigger_type,
    form.source_kind,
    form.trigger_edge_device_id,
    form.trigger_sensor_name,
    form.trigger_comparator,
    form.trigger_threshold,
    form.trigger_duration_sec,
  ],
  () => clearPreviewResult(),
)

// ── 时间窗：HH:mm 是运行期唯一能解析的形态（文档 §3.5）──

/** 后端运行期 time.Parse("15:04") 要求的形态：两位补零、冒号分隔。 */
const HHMM_PATTERN = /^\d{2}:\d{2}$/

/** 打开存量规则时改写过时间值留下的修复提示（空串 = 本轮无需修复）。 */
const windowStartFixNote = ref('')
const windowEndFixNote = ref('')

/**
 * 把存量时间窗值规范成 HH:mm。
 *
 * 为什么必须改写而不是原样回填：`8:00` 在后端运行期解析失败后只打一条 warn 就 return，
 * 规则永久不触发且界面无痕 —— 用户打开编辑框看到一个"看起来正常"的 8:00，会以为它是好的。
 * 改写用户数据必须**可见**，所以同时返回一句提示（渲染在该字段下方）。
 *
 * 无法解析时**保留原值**：静默丢弃用户填的东西比留一个坏值更糟；提示里让他重新选择。
 */
function normalizeTimeWindow(raw: string): { value: string; note: string } {
  const text = (raw ?? '').trim()
  if (!text) return { value: '', note: '' }
  const matched = /^(\d{1,2}):(\d{1,2})$/.exec(text)
  const hour = matched ? Number(matched[1]) : NaN
  const minute = matched ? Number(matched[2]) : NaN
  if (!matched || hour > 23 || minute > 59) {
    return {
      value: text,
      note: `原值「${text}」无法解析为合法的 HH:mm，已保留原值但运行期不会触发，请重新选择时间。`,
    }
  }
  const fixed = String(hour).padStart(2, '0') + ':' + String(minute).padStart(2, '0')
  if (fixed === text) return { value: text, note: '' }
  return {
    value: fixed,
    note: `已把原值 ${text} 规范为 ${fixed}（原格式运行期不会触发）。`,
  }
}

/**
 * start > end = 后端支持的**跨零点窗口**（22:00 → 次日 06:00），不是输入错误。
 *
 * 只在两端都是合法 HH:mm 时判定：字符串比较对补零的 HH:mm 与"比较时刻"等价，
 * 而非法值（例如存量 `8:00`）不该冒出一条"跨越零点"把真正的问题掩盖掉。
 */
const isOvernightWindow = computed(() => {
  const start = (form.trigger_window_start ?? '').trim()
  const end = (form.trigger_window_end ?? '').trim()
  if (!HHMM_PATTERN.test(start) || !HHMM_PATTERN.test(end)) return false
  return start > end
})

// ── 时区：浏览器内置的 IANA 全表（文档 §4.5）──

/**
 * `Intl.supportedValuesOf('timeZone')` 是现代浏览器的内置 IANA 全表。
 *
 * 本仓 tsconfig 的 lib 是 ES2022，可能还没有该方法的类型声明，故按"可选函数"取；
 * 取不到（老浏览器 / 被策略拦截）时退化到最小候选 + allow-create，功能不消失。
 */
const INTL_SUPPORTED_VALUES = Intl as unknown as { supportedValuesOf?: (key: string) => string[] }

/**
 * 降级候选：**只**在没有全表时使用。
 *
 * 故意只给最小集合：有全表时这条分支不生效，避免前端另存一份完整时区词表与
 * 浏览器/后端各说各话（与"不要复用 sensorUnitMap"是同一条理由）。
 */
const TIMEZONE_FALLBACK = ['Asia/Shanghai', 'UTC', 'America/New_York', 'Europe/London']

/** 浏览器给的 IANA 全表；空数组 = 拿不到（此时下拉必须保留手输）。 */
const timezoneVocabulary = (() => {
  try {
    const list = INTL_SUPPORTED_VALUES.supportedValuesOf?.('timeZone')
    if (Array.isArray(list)) return list.filter(item => typeof item === 'string' && item.length > 0)
  } catch {
    // 方法存在但抛错（策略/实现差异）：与"没有该方法"同样处理，只降级、不阻塞表单
  }
  return []
})()

/** 没有全表 ⇒ 允许手输（否则用户只能从 4 条候选里选，比改造前还差）。 */
const timezoneAllowCreate = timezoneVocabulary.length === 0

const timezoneOptions = computed<string[]>(() => {
  const base = timezoneVocabulary.length > 0 ? timezoneVocabulary : TIMEZONE_FALLBACK
  const current = (form.timezone ?? '').trim()
  // 编辑存量规则：当前值可能不在浏览器的 IANA 表里（历史值 / 浏览器版本差异）。
  // 不补一条，el-select 就显示空白，用户看不出这条规则绑的是哪个时区。
  return current && !base.includes(current) ? [current, ...base] : base
})

/**
 * 时区下拉变化：统一收敛成字符串。
 *
 * 为什么不裸 v-model：el-select 在「清空」时可能 emit undefined/null，而
 * `form.timezone` 的三态语义是「空串 = 继承节点」（sunLocationState 按 trim 判定）。
 * 让 undefined 漏进去，"继承"与"未填"会在状态判定里分叉。
 */
function onTimezoneChange(value: unknown) {
  form.timezone = typeof value === 'string' ? value : ''
}

// ── 来源组（方案 v0.3 §4.3）──
// 只做为「下拉候选」加载；组语义（active 选择/combine/缺员）由后端 sourceref 负责，前端不重算。
const sourceGroups = ref<SourceGroup[]>([])
/** 编辑中的规则原本是否绑来源组：决定「切回边缘设备」时要不要显式清空来源列。 */
const editingSourceWasGroup = ref(false)
const sourceGroupOptions = computed(() => sourceGroups.value.filter(g => g.enabled !== false))
const selectedSourceGroup = computed(() => sourceGroups.value.find(g => g.id === form.source_group_id))
const COMBINE_LABELS: Record<string, string> = {
  avg: '平均',
  sum: '求和',
}
function groupOptionLabel(g: SourceGroup): string {
  return `${g.name}（${g.kind === 'primary_backup' ? '主备' : '复合'}）`
}
function groupSummary(g: SourceGroup): string {
  if (g.kind === 'primary_backup') return `主备组 · 类别 ${g.category} · active 成员由数据源状态机决定`
  const n = g.min_members || 1
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

// ── 附加条件：结构化条件行（文档 §4.3）────────────────────────────────────────
//
// 序列化键与后端 models.AutomationCondition **逐字段同名**
// （backend/internal/models/automation.go:141-145）：
//   {"sensor_name":"temperature_2","comparator":"gt","threshold":45}
// 后端 conditionsSatisfied 对每条走同一个 evalSelector，全部满足才触发
//（任一不满足 / 无法判定 ⇒ 不触发，evaluator.go:1018-1033）。
// 因此界面顺序 = 提交数组顺序 = 求值顺序，二者必须一致（见 serializeConditionRows）。
//
// 与动作卡片同思路：v-for 的 key 用 uid 而不是下标。下标作 key 时删掉中间一行，
// 后面所有行的组件实例会被错位复用 —— 值"跳"到别的行、输入焦点跑掉，
// 而这一行的值还可能被写进另一行的 uid 上。

/** 条件行草稿。字段名直接沿用后端口径，不额外造一层 selector/threshold 的翻译表。 */
interface ConditionRow {
  /** v-for key（同 ActionDraft.uid） */
  uid: number
  /** 字段选择器原文，原样落到后端 sensor_name（与触发字段同一个 selector 契约） */
  selector: string
  comparator: AutomationComparator
  threshold: number
}

const conditionRows = ref<ConditionRow[]>([])

/**
 * 源码模式：默认关闭。只有两种情况打开 ——
 *   ① 用户手动点「JSON 源码」（迁移期录入 / 排障）；
 *   ② 存量 conditions_json 无法解析成条件数组（见 parseConditionRows），自动打开。
 * 打开时 textarea 绑定的是 form.conditions_json **原文**，本页一个字符都不改写它。
 */
const conditionsRawMode = ref(false)

const COMPARATOR_SET = new Set<string>(comparators.map(c => c.value))

/**
 * conditions_json → 条件行。返回 null = **无法安全还原**，调用方必须原样保留原文。
 *
 * 为什么校验这么严（缺键 / 未知比较符 / 字符串阈值一律判非法）：
 *   · 放宽 = 打开编辑框时静默改写用户数据的语义。例：历史手写的
 *     `{"sensor":"humidity"}`（旧 placeholder 的示例就是这种键名）会被读成
 *     sensor_name=""，用户什么都没动、保存一次，条件就从"湿度<30"变成"永不满足"；
 *   · 而"读不懂"是可逆的：切到源码模式，用户自己看得见、改得动、也能原样交回后端。
 */
function parseConditionRows(raw: string): ConditionRow[] | null {
  const text = (raw ?? '').trim()
  if (!text) return []
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch {
    return null
  }
  if (!Array.isArray(parsed)) return null
  const rows: ConditionRow[] = []
  for (const [index, item] of parsed.entries()) {
    if (!item || typeof item !== 'object' || Array.isArray(item)) return null
    const record = item as Record<string, unknown>
    const sensorName = record.sensor_name
    const comparator = record.comparator
    const threshold = record.threshold
    if (typeof sensorName !== 'string') return null
    if (typeof comparator !== 'string' || !COMPARATOR_SET.has(comparator)) return null
    if (typeof threshold !== 'number' || !Number.isFinite(threshold)) return null
    // uid = 下标+1：还原出来的行是一次性快照，后续新增行走 uidSeed()，两者不会撞号。
    rows.push({ uid: index + 1, selector: sensorName, comparator: comparator as AutomationComparator, threshold })
  }
  return rows
}

/**
 * 条件行 → conditions_json 原文。
 *
 * 零行 ⇒ 空串：后端 ParseConditions 把 "" 与 "[]" 都当"无条件"，空串是本仓既有口径
 * （buildPayload 的 `trim() || undefined` 依赖它）。
 * selector 统一 trim：后端按字面量比对字段名，尾部多一个空格就是一个"看起来对、
 * 永远不满足"的条件 —— 而它不会报错。
 */
function serializeConditionRows(rows: ConditionRow[]): string {
  if (rows.length === 0) return ''
  return JSON.stringify(rows.map(row => ({
    sensor_name: row.selector.trim(),
    comparator: row.comparator,
    threshold: row.threshold,
  })))
}

/**
 * 行 → form.conditions_json 的**唯一**出口。
 * 源码模式下不写：那时 textarea 里的原文才是唯一真相，行只是上一次的残影。
 */
function syncConditionsJson() {
  if (conditionsRawMode.value) return
  // 只有 sensor_threshold 会求值附加条件：其它触发类型的 conditions_json 是**惰性数据**，
  // 本页连"顺手规范化空格"都不做 —— 用户看不见的配置不该在他保存别的内容时被悄悄改写。
  if (form.trigger_type !== 'sensor_threshold') return
  form.conditions_json = serializeConditionRows(conditionRows.value)
}

// 任何行变化（新增 / 删除 / 改字段 / 改比较符 / 改阈值）都同步回提交体。
// 为什么用 deep watch 而不是在每个 @change 里手写同步：漏一处就是"界面改了、提交体没改"，
// 而这种不一致在保存前完全不可见（用户以为条件已经生效，实际提交的是旧条件）。
watch(conditionRows, syncConditionsJson, { deep: true })

/**
 * 源码模式下的原文是否**不可还原**（实时判定，而不是只记"打开那一刻是坏的"）。
 * 为什么实时：用户在源码里改到一半（比如删了个逗号）时，界面要立刻说明"现在切不回去"，
 * 而不是等他点了按钮才弹一句、还不知道是哪里不对。
 */
const conditionsRawInvalid = computed(() =>
  conditionsRawMode.value
  && form.conditions_json.trim() !== ''
  && parseConditionRows(form.conditions_json) === null,
)

/** 新增一行空条件：默认 gt / 0（与上方触发条件的默认值同口径）。空行由 validate() 拦。 */
function addCondition() {
  conditionRows.value.push({ uid: uidSeed(), selector: '', comparator: 'gt', threshold: 0 })
}

function removeCondition(uid: number) {
  conditionRows.value = conditionRows.value.filter(row => row.uid !== uid)
}

/**
 * 「JSON 源码」开关。
 *
 * 行 → 源码：行是唯一真相，conditions_json 已由上面的 watch 持续保持同步，直接切即可。
 * 源码 → 行：重新解析；**解析失败就拒绝关闭**并提示。关闭的同时把用户刚输入的内容换掉，
 * 比"停在源码模式"严重得多 —— 前者是数据丢失，后者只是多一个步骤。
 */
function toggleConditionsRaw() {
  if (!conditionsRawMode.value) {
    conditionsRawMode.value = true
    return
  }
  const rows = parseConditionRows(form.conditions_json)
  if (!rows) {
    ElMessage.warning('现有内容无法解析为条件数组，已保留原文；修正后才能切回条件行编辑')
    return
  }
  // 先落行、再退出源码模式：watch 回调排在本段同步代码之后，那时 conditionsRawMode
  // 已是 false，会顺手把（可能带空格/换行的）原文规范成与提交体同款的 JSON。
  conditionRows.value = rows
  conditionsRawMode.value = false
}

function uidSeed(): number {
  return Date.now() + Math.floor(Math.random() * 1000)
}

function openCreate() {
  editingId.value = null
  editingSourceWasGroup.value = false
  // 重开对话框 = 新的一次保存流程：清诊断与已确认指纹，避免上一份内容的确认被复用
  resetSaveDiagnostics()
  Object.assign(form, emptyForm())
  sunLatitudeText.value = ''
  sunLongitudeText.value = ''
  // 单位/精度与"时间窗已修复"提示都属于**上一条规则**的状态：不清掉就会挂到新表单上
  // （用户还没选字段就看到一个 °C，或者看到一条与当前内容无关的修复说明）。
  sensorField.value = null
  // 试算结论属于**上一条规则**：即使新表单的值恰好与旧值相同（watch 不触发），
  // 也必须显式清掉，否则用户会看到上一份输入的答案。
  clearPreviewResult()
  windowStartFixNote.value = ''
  windowEndFixNote.value = ''
  // 条件行与源码模式同样属于**上一条规则**的状态：不清就是新表单上挂着旧条件。
  // 先退出源码模式再清行：watch 会在本段同步代码之后跑，那时写回的是空串（零行 ⇒ ""）。
  conditionsRawMode.value = false
  conditionRows.value = []
  dialogVisible.value = true
}

function openEdit(rule: AutomationRule) {
  editingId.value = rule.id
  editingSourceWasGroup.value = rule.source_kind === 'source_group'
  // 时间窗存量值先规范化（见 normalizeTimeWindow）：`8:00` 这种形态在后端运行期
  // time.Parse("15:04") 失败后只打一条 warn 就 return —— 规则永久失效且界面无痕，
  // 而用户在编辑框里看到的仍是"看起来正常"的 8:00。规范化后必须留下可见的修复提示。
  const windowStart = normalizeTimeWindow(rule.trigger_window_start ?? '')
  const windowEnd = normalizeTimeWindow(rule.trigger_window_end ?? '')
  // 传感器字段对象在 openEdit 里**不主动拉目录**：
  // SensorFieldPicker 在目录就绪后会自己对当前 modelValue 回填一次 change-field
  // （组件契约，见该组件 refillFieldFromCatalog），这里只需先清空上一份的单位。
  sensorField.value = null
  // 切换规则 = 换了一份答案：试算结论不得跨规则复用（值相同也不会被 watch 清掉）。
  clearPreviewResult()
  Object.assign(form, {
    name: rule.name,
    trigger_type: rule.trigger_type,
    trigger_sun_kind: rule.trigger_sun_kind ?? 'sunset',
    trigger_sun_offset_min: rule.trigger_sun_offset_min ?? 0,
    trigger_sensor_name: rule.trigger_sensor_name ?? '',
    trigger_comparator: rule.trigger_comparator ?? 'gt',
    trigger_threshold: rule.trigger_threshold ?? 0,
    trigger_duration_sec: rule.trigger_duration_sec ?? 0,
    trigger_window_start: windowStart.value,
    trigger_window_end: windowEnd.value,
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
  // 附加条件：合法数组 ⇒ 还原成条件行；**读不懂就自动进源码模式并保留原文**
  //（历史手写 / 被别的工具改坏的数据不能被"解析失败"吞掉，见 parseConditionRows）。
  // 顺序有讲究：先定模式再落行 —— 源码模式下 syncConditionsJson 会直接返回，
  // 原文一个字符都不会被行数据覆盖。
  const parsedConditions = parseConditionRows(rule.conditions_json ?? '')
  conditionsRawMode.value = parsedConditions === null
  conditionRows.value = parsedConditions ?? []
  // 修复提示跟着这一条规则走：用户改完字段再切回来不该还看到"已把原值…"（那时值已是 08:00）
  windowStartFixNote.value = windowStart.note
  windowEndFixNote.value = windowEnd.note
  // 切换规则 = 另一份内容：上一份规则的 warnings 确认不得带过来
  resetSaveDiagnostics()
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
  // 单位与"已规范化"提示只对刚打开的那条规则有意义：换触发器类型后必然过时。
  sensorField.value = null
  windowStartFixNote.value = ''
  windowEndFixNote.value = ''
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

/**
 * 卡片标题：目录里的**动作名**优先，目录里没有才回落动作 ID。
 *
 * 为什么改：用户认的是「设置充放电 MOS 软件策略」，不是 set_mos_policy —— 目录本来就
 * 加载在内存里（风险徽标一直在用它），标题却还拿裸 ID 显示，等于让用户对照 ID 读界面。
 * 列表摘要 actionSummary 仍显示 action_id：摘要列宽有限，且要能一眼对上后端日志/事件记录，
 * 把长中文名塞进去会把"动作是什么"与"动作叫什么"搅成两套说法（见 actionSummary 注释）。
 */
function actionTitle(action: ActionDraft): string {
  if (action.action_type !== 'device_action') return '通知'
  const def = catalogDefinition(action)
  if (def?.name) return def.name
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

/**
 * 在已加载的设备目录里找该动作的定义（找不到返回 null，徽标不渲染：不猜风险）。
 *
 * 入参放宽到 null：参数弹窗/表单在"当前没有选中卡片"时也要能问一句"这个动作有没有 schema"，
 * 让调用方每次都先判空只会到处多一层 if（判定逻辑本身一个字没动）。
 */
function catalogDefinition(action: ActionDraft | null | undefined) {
  if (!action || action.action_type !== 'device_action' || !action.action_device_id || !action.action_id) return null
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
    // 格式兜底（控件之外的入口仍然存在：存量规则、浏览器自动填充、将来的快捷键）。
    // 后端保存期只校验"非空"，而运行期解析失败只打一条 warn —— 所以这一道必须在**保存前**，
    // 文案与时间选择器的 value-format 同一口径（HH:mm，两位补零）。
    if (!HHMM_PATTERN.test(form.trigger_window_start.trim())) return '时间格式必须为 HH:mm'
    if (!HHMM_PATTERN.test(form.trigger_window_end.trim())) return '时间格式必须为 HH:mm'
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
      let paramsObject: Record<string, unknown> = {}
      if (action.action_params_json.trim()) {
        try {
          const parsed = JSON.parse(action.action_params_json) as unknown
          // 只把**对象**交给必填校验：数组/标量原本就能过这一关（校验口径不变），
          // 到后端 CanonicalizeParams 才会被拒 —— 本次不顺手改这条既有判定。
          if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
            paramsObject = parsed as Record<string, unknown>
          }
        } catch {
          return `${where}：动作参数 JSON 格式错误`
        }
      }
      // 有 input_schema 时补一道 required：表单路径由 ActionForm 的"不完整即禁提交"兜住，
      // 但**手写 JSON**（卡片上的高级入口 / 存量规则 / 别的工具写入）可以整段删掉必填字段。
      // 缺字段后端必 400，这道校验是用户保存前能看到"到底缺哪个参数"的最后机会。
      const missingParams = requiredMissingText(paramsObject, catalogDefinition(action))
      if (missingParams) return `${where}：${missingParams}`
    } else if (!action.action_level) {
      return `${where}：请选择通知级别`
    }
  }
  // 附加条件：只有 sensor_threshold 会求值（后端 conditionsSatisfied 只在这条路径上被调用），
  // 所以也只有它需要在这里校验 —— 对一条日出规则拦下"它自己不会读的字段"，用户
  // 既不知道为什么要改、也只能先切触发器类型才能改。
  if (form.trigger_type === 'sensor_threshold') {
    if (!conditionsRawMode.value) {
      // 行模式：逐行校验，**空行必须报错**。
      // 为什么不静默丢弃："点了 + 没填完"与"我配了 3 个条件"在界面上一模一样（行还在），
      // 丢弃后保存成功、用户以为 3 个条件都生效，实际只有 2 个 —— 这是最难发现的配置错。
      for (const [index, row] of conditionRows.value.entries()) {
        const where = `第 ${index + 1} 个条件`
        if (!row.selector.trim()) return `${where}：请选择字段`
        // el-input-number 被清空时给 null/undefined（不是 NaN），两者都必须算"没填"。
        if (typeof row.threshold !== 'number' || !Number.isFinite(row.threshold)) {
          return `${where}：请填写阈值`
        }
      }
    } else if (form.conditions_json.trim()) {
      // 源码模式：保持改造前的校验口径（非法 JSON / 非数组在保存前拦下）。
      // 这段原文可能是用户手写的历史数据，"格式对不对"只能在这里判断。
      try {
        const parsed = JSON.parse(form.conditions_json)
        if (!Array.isArray(parsed)) return '附加条件必须是 JSON 数组'
      } catch {
        return '附加条件 JSON 格式错误'
      }
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
  // 行模式下提交前再同步一次（防御）：任何绕过 deep watch 的写入都不该让提交体与界面脱节。
  // 源码模式下本函数是空操作（syncConditionsJson 自己 guard），原文原样提交。
  syncConditionsJson()
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
        payload.source_group_id = undefined
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
  // 诊断区在每次保存时**重新计算**：先清掉上一轮的结果，避免"用户已经改掉的那条"
  // 继续留在界面上（残留提示比没有提示更误导）。
  saveBlockingErrors.value = []
  saveWarnings.value = []
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
      const warnings = precheck.warnings ?? []
      // 整体覆盖而不是累积：诊断区只反映**本轮**判定。
      saveBlockingErrors.value = blocking
      saveWarnings.value = warnings
      if (blocking.length > 0) {
        // 即时反馈仍是 toast + 首条（既有单测契约），**完整清单**交给诊断区逐条列出：
        // 改前这里是 `ElMessage.warning(blocking[0]); return`，多条错误要来回保存 N 次。
        ElMessage.warning(blocking[0])
        return
      }
      if (warnings.length > 0) {
        const fingerprint = warnings.join('|')
        if (ackedWarningFingerprint.value !== fingerprint) {
          // 第一次点击（同一份内容）：**不保存**，只把风险提示展示出来，主按钮改成
          // 「我已了解，仍然保存」；用户再点一次（同一份 warnings）才真正落库。
          //
          // 为什么 warnings 要二次确认，而不是像 blocking 那样硬拒、也不是直接保存：
          //   · 硬拒：后端把它判成 warning 就是因为**当前无法证明它错**（选择器语法合法，
          //     只是此刻解析不到，字段可能稍后才上报），拦死会让合法规则永远存不进去；
          //   · 直接放行：这是最坏的一种失效 —— 保存成功、规则永不触发、界面没有任何
          //     痕迹，用户不会有第二次机会看到这句提示。
          // 因此留一次"已看到并坚持保存"的选择，代价是一次点击，收益是把静默失效变成可见取舍。
          //
          // 确认记在**内容指纹**上（不是 boolean）：见 ackedWarningFingerprint 注释。
          ackedWarningFingerprint.value = fingerprint
          return
        }
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
  // 保存成功 = 本次流程结束：清空诊断与确认指纹。不清的话，下一份规则若恰好产出
  // 相同的 warnings 文本，会被上一份的确认静默放行（"下一份被静默放行"正是要防的）。
  resetSaveDiagnostics()
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

// ── 动作参数编辑 (R8)：schema 表单为主，JSON 文本域为「高级」 ──
const paramsDialogVisible = ref(false)
const paramsFormVisible = ref(false)
const paramsEditingUid = ref<number | null>(null)
const paramsDraft = ref('')
const paramsDraftError = ref('')
/** 表单初值：已存的参数对象（读不出/非法 JSON ⇒ null，表单从空白开始）。 */
const paramsFormPrefill = ref<Record<string, unknown> | null>(null)
const editingParamsAction = computed(
  () => form.actions.find(a => a.uid === paramsEditingUid.value) ?? null,
)
const editingParamsActionId = computed(() => editingParamsAction.value?.action_id ?? '')
/** 当前编辑的是第几张卡片（1 起用于文案；-1 = 动作已被删除，此时不出任何入口）。 */
const paramsEditingIndex = computed(
  () => form.actions.findIndex(a => a.uid === paramsEditingUid.value),
)
const paramsFormDefinition = computed(() => catalogDefinition(editingParamsAction.value))
/** JSON 弹窗里能否切回表单：只有目录真的给了字段（properties 非空）才有表单可开。 */
const paramsEditingSchema = computed(() => definitionHasSchema(paramsFormDefinition.value))

/**
 * 「编辑参数」按钮的唯一入口：按**有无 schema** 分流。
 *
 * 为什么默认走表单：后端 definition.input_schema 已经声明了字段/类型/枚举/范围，
 * 同一个系统里设备页能"选动作→填表单"，自动化页却要"背 JSON" —— 参数填错要等到保存
 * 甚至执行时才发现。JSON 只在没有 schema 时才是唯一形态（有 schema 时降级为「高级」）。
 */
function openParams(uid: number) {
  const action = form.actions.find(a => a.uid === uid)
  if (!action) return
  const def = catalogDefinition(action)
  if (definitionHasSchema(def)) openParamsForm(action, parsedParams(action.action_params_json))
  else openJsonParams(action)
}

/** 打开 JSON 文本域（无 schema 时的唯一入口 / 有 schema 时的高级入口）。 */
function openJsonParams(action: ActionDraft) {
  paramsEditingUid.value = action.uid
  paramsDraft.value = action.action_params_json
  // 当场就校验一遍：存量参数可能已经缺必填字段（别的工具写坏 / schema 后来加了字段）
  paramsDraftError.value = validateParamsJson(paramsDraft.value, catalogDefinition(action))
  paramsDialogVisible.value = true
}

/** 打开 schema 表单（prefill = 已存参数对象）。 */
function openParamsForm(action: ActionDraft, prefill: Record<string, unknown> | null) {
  paramsEditingUid.value = action.uid
  paramsFormPrefill.value = prefill
  paramsFormVisible.value = true
}

/** 参数 JSON 文本 → 对象（非法 JSON / 非对象 ⇒ null：调用方按"没有可回填的值"处理）。 */
function parsedParams(text: string): Record<string, unknown> | null {
  if (!text.trim()) return null
  try {
    const parsed = JSON.parse(text) as unknown
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return null
    return parsed as Record<string, unknown>
  } catch {
    return null
  }
}

/**
 * 空值 = 无参数（合法）；非空必须是 JSON **对象**（数组/标量过不了后端 CanonicalizeParams）。
 *
 * 有 schema 时**额外**校验 required：ActionForm 会禁用不完整表单，但 JSON 路径（本弹窗 /
 * 存量规则）可以整段删掉必填字段 —— 缺字段后端必 400，必须在保存前就说清缺什么。
 */
function validateParamsJson(text: string, def: ActionDefinition | null): string {
  if (!text.trim()) return requiredMissingText({}, def)
  try {
    const parsed = JSON.parse(text) as unknown
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return '动作参数必须是 JSON 对象'
    return requiredMissingText(parsed as Record<string, unknown>, def)
  } catch {
    return '动作参数 JSON 格式错误'
  }
}

/**
 * 缺了哪些**必填**参数（空数组 = 齐了）。
 *
 * 判定口径与 ActionForm 的 complete 完全对齐（undefined 与 '' 都算没填），否则会出现
 * "表单说齐了、保存又说不齐"两套答案。null 不算缺（ActionForm 也放行），因为方言参数里
 * null 可能是有意义的"清空该字段"。
 */
function requiredMissingText(params: Record<string, unknown>, def: ActionDefinition | null): string {
  const required = def?.input_schema?.required ?? []
  const missing = required.filter(name => params[name] === undefined || params[name] === '')
  return missing.length ? `缺少参数 ${missing.join('、')}` : ''
}

/** definition 是否声明了可生成的表单字段（空壳 schema 不算：打开一张空表单等于废掉 JSON 入口）。 */
function definitionHasSchema(def: ActionDefinition | null): boolean {
  const properties = def?.input_schema?.properties
  return !!properties && Object.keys(properties).length > 0
}

/** 该卡片是否有可生成的参数表单（见 definitionHasSchema）。 */
function hasParamSchema(action: ActionDraft): boolean {
  return definitionHasSchema(catalogDefinition(action))
}

/** 表单提交的参数对象 → 落库的 JSON 串（空对象写空串，与"无参数"的既有表示一致）。 */
function paramsToJson(params: Record<string, unknown>): string {
  const clean: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(params)) {
    // ActionForm 不会给 undefined，但这里是数据边界：undefined 序列化后会变成 null
    if (value !== undefined) clean[key] = value
  }
  return Object.keys(clean).length ? JSON.stringify(clean) : ''
}

function onParamsDraftChange() {
  paramsDraftError.value = validateParamsJson(paramsDraft.value, paramsFormDefinition.value)
}

function applyParams() {
  paramsDraftError.value = validateParamsJson(paramsDraft.value, paramsFormDefinition.value)
  if (paramsDraftError.value) return
  const action = editingParamsAction.value
  if (action) action.action_params_json = paramsDraft.value.trim()
  paramsDialogVisible.value = false
}

/**
 * 表单提交 → 写回 JSON 串。
 *
 * 为什么写回字符串而不是把模型改成对象：action_params_json 是全仓既有的落库形态
 * （后端 CanonicalizeParams 校验的也是"一个 JSON 对象串"）；改成对象会波及提交体、
 * 列表摘要 paramsSummary、预检参数摘要三处，属于本次改造之外的扩散。
 */
function applyFormParams(params: Record<string, unknown>) {
  const action = editingParamsAction.value
  if (action) action.action_params_json = paramsToJson(params)
  paramsFormVisible.value = false
}

/** JSON 弹窗 → 表单（「改用表单」）：把文本里能读出的对象作为 prefill 带过去。 */
function switchParamsToForm() {
  const action = editingParamsAction.value
  if (!action) return
  const prefill = parsedParams(paramsDraft.value)
  paramsDialogVisible.value = false
  openParamsForm(action, prefill)
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

/**
 * 目录条目的可用性（后端 EffectiveAction.available / reason）。
 *
 * 为什么单独一张表、而不塞进 actionCatalog 的值里：actionCatalog 是**风险徽标的输入**
 * （catalogDefinition → riskPresentation）。改它的值类型等于改风险判定的输入 —— 本次
 * 一个字都不动风险逻辑；可用性只服务下拉选项的「当前不可用」标注与空态提示文案。
 */
const actionAvailability = ref(new Map<string, { available: boolean; reason?: string }>())

/**
 * 设备目录的加载态。
 *
 * 为什么必须是三态而不是一个"请求过没"的 Set：空态提示只能说"没取到/没有动作"，
 * 不能把**请求失败**或**还在加载**说成"该设备没有动作"；而"动作不在目录中"
 * （存量规则提示）只在 ready 时才成立 —— failed 时无从判断，说了就是猜。
 */
type CatalogLoadState = 'loading' | 'ready' | 'failed'
const catalogState = ref(new Map<number, CatalogLoadState>())
/**
 * 进行中的目录请求（按设备）。
 *
 * 为什么除了"状态"还要留 Promise：卡片切换目标设备时要判定"已选动作还在不在新目录里"，
 * 而这个判定只有等目录**落定**才有依据。若新设备的目录恰好正在请求（例如页面挂载时
 * 已经在拉），只凭状态会走"拿不到就不断言"的保守分支，清空提示就被悄悄跳过了。
 */
const catalogInflight = new Map<number, Promise<void>>()

/**
 * 取一批设备的受控操作目录并建索引（幂等：已请求过的设备跳过）。
 *
 * 为什么需要"编辑中新增的设备"也走这条路：徽标只在**已加载目录**的设备上渲染。
 * 若只加载既有规则用到的设备，用户在新建规则里选一台从未配过规则的设备时，
 * 风险动作会**没有徽标**（看起来像普通动作）—— 这是安全信息缺失，不是视觉问题。
 */
async function ensureActionCatalog(deviceIds: Iterable<number>) {
  const ids = [...new Set(deviceIds)].filter(id => id > 0)
  const pending = ids.filter(id => !catalogState.value.has(id))
  // 别的调用已经发起、还没落定的请求：等它，别重复打接口，也别在"加载中"下断言
  const inflight = ids
    .map(id => catalogInflight.get(id))
    .filter((request): request is Promise<void> => request !== undefined)
  if (pending.length === 0) {
    await Promise.all(inflight)
    return
  }
  const states = new Map(catalogState.value)
  for (const id of pending) states.set(id, 'loading')
  catalogState.value = states
  const next = new Map(actionCatalog.value)
  const availability = new Map(actionAvailability.value)
  for (const id of pending) {
    const request = (async () => {
      try {
        const items = await deviceOperationApi.actions(id)
        for (const item of items) {
          const def = item.definition
          const key = `${id}:${def.id}`
          next.set(key, def)
          // available 缺省按"可用"处理（后端字段是 bool，老响应可能没有它）：
          // 没有依据时不要把动作标成不可用，否则用户会以为设备坏了。
          availability.set(key, { available: item.available !== false, reason: item.reason ?? def.availability_reason })
        }
        states.set(id, 'ready')
      } catch {
        /* 单个设备目录失败不阻塞页面：该卡片不渲染风险徽标，保存前的预检仍会拦风险动作。
           记为 failed 而不是 ready —— 否则空态提示会声称"该设备没有动作"，而事实是"没取到"。 */
        states.set(id, 'failed')
      }
    })()
    catalogInflight.set(id, request)
  }
  await Promise.all([...pending.map(id => catalogInflight.get(id)!), ...inflight])
  for (const id of pending) catalogInflight.delete(id)
  actionCatalog.value = next
  actionAvailability.value = availability
  // 重新赋值一份新 Map：Promise.all 里改的是同一个对象，不换引用不会触发依赖它的渲染
  catalogState.value = new Map(states)
}

/** 该卡片目标设备的目录加载态（未请求过 ⇒ unknown：按"还没准备好"处理，不下任何断言）。 */
function cardCatalogState(action: ActionDraft): CatalogLoadState | 'unknown' {
  const deviceId = action.action_device_id
  if (!deviceId) return 'unknown'
  return catalogState.value.get(deviceId) ?? 'unknown'
}

/** 动作下拉的一个候选（value = 动作 ID）。 */
interface ActionOption {
  value: string
  label: string
  available: boolean
  reason?: string
}

/**
 * 当前卡片目标设备下的动作候选。
 *
 * 为什么按设备过滤：action_id 由**该设备的驱动**声明，一台 BMS 与一台 ESP32 的动作集合
 * 不相交（键就是 <deviceId>:<actionId>）。把全设备的动作都列出来，只是给用户换一种方式填错。
 * 目录为空/未加载时返回空数组：下拉退化成 allow-create 手输，不阻断建规则（既有降级口径）。
 */
function actionOptions(action: ActionDraft): ActionOption[] {
  const deviceId = action.action_device_id
  if (!deviceId) return []
  const prefix = `${deviceId}:`
  const options: ActionOption[] = []
  for (const [key, def] of actionCatalog.value) {
    if (!key.startsWith(prefix)) continue
    const availability = actionAvailability.value.get(key)
    const available = availability ? availability.available : true
    const parts = [def.name || def.id, def.id]
    // 风险文字只是**选项文案**（让用户知道这个动作危险），真正判定仍是后端下发的
    // requires_confirmation（riskBadgeOf / 保存前预检）。这里绝不参与任何风险判定。
    const badge = riskBadge(def)
    if (badge) parts.push(`${badge.icon} ${badge.label}`)
    // 不可用的动作**照样列出**、只加后缀，不隐藏：动作确实存在，只是此刻下发不了
    // （电池未接入/驱动未就绪）。隐藏会让下拉看起来"这个动作不存在"，用户转而去查
    // 驱动声明；而是否可用是**运行期**状态，配置期不据此阻断（执行结果由后端判定）。
    if (!available) parts.push('当前不可用')
    options.push({ value: def.id, label: parts.join(' · '), available, reason: availability?.reason })
  }
  // 按显示名排序（数字感知："动作 2" 排在 "动作 10" 前面）：候选多时用户按名字找，不按 ID 找
  options.sort((a, b) => a.label.localeCompare(b.label, 'zh-Hans-CN', { numeric: true }))
  return options
}

/** 下拉 placeholder：有候选时提示可选可输，没有候选时只剩手输（不假装有目录）。 */
function actionIdPlaceholder(action: ActionDraft): string {
  return actionOptions(action).length ? '选择或输入动作 ID' : '输入动作 ID（如 gpio_set）'
}

/** 空态：目录确实取回（或明确失败）而该设备一个动作都没有 —— 明说"没有"，仍允许手输。 */
function catalogEmpty(action: ActionDraft): boolean {
  const state = cardCatalogState(action)
  return (state === 'ready' || state === 'failed') && actionOptions(action).length === 0
}

/**
 * 目录里**有**动作但全部 unavailable（电池未接入 / 驱动未就绪 / 设备离线）。
 *
 * 为什么单独一条提示而不是复用空态：把"有 3 个动作但都下发不了"说成"没有动作"，
 * 会让用户去查驱动声明，而实际该查的是设备连接状态。选择仍然放行（见 allow-create 注释）。
 */
function catalogAllUnavailable(action: ActionDraft): boolean {
  const options = actionOptions(action)
  return cardCatalogState(action) === 'ready' && options.length > 0 && options.every(o => !o.available)
}

/**
 * 存量规则 / 设备换型：目录已取回，但里面没有当前选中的动作。
 *
 * **不清空**用户的 action_id 与参数（清空就是静默丢数据），只给一行黄字提示，
 * 保存仍走既有后端校验 —— 与"目录拿不到不阻断建规则"的口径一致。
 */
function catalogMissing(action: ActionDraft): boolean {
  const deviceId = action.action_device_id
  if (!deviceId || cardCatalogState(action) !== 'ready') return false
  if (!action.action_id.trim()) return false
  return !actionCatalog.value.has(`${deviceId}:${action.action_id}`)
}

/**
 * 卡片切换目标设备。
 *
 * 目录按设备隔离：旧动作在新设备上可能根本不存在（action_id 是设备驱动声明的），
 * 继续带着它保存 = 一条到死都执行不了、用户还看不出原因的规则。故**目录就绪之后**
 * 再判定：不在新目录里就清空并说明一次（用户看得见变化，而不是被静默换掉）。
 * 目录没取到（设备离线 / 请求失败）时**不清空**：拿不到目录不等于动作不存在。
 */
async function onCardDeviceChange(action: ActionDraft) {
  const deviceId = action.action_device_id
  if (!deviceId) return
  await ensureActionCatalog([deviceId])
  // 等待期间用户可能又换了设备/动作：只在"仍然是这台设备"时判定
  if (action.action_device_id !== deviceId) return
  if (catalogState.value.get(deviceId) !== 'ready') return
  if (!action.action_id.trim()) return
  if (actionCatalog.value.has(`${deviceId}:${action.action_id}`)) return
  action.action_id = ''
  action.action_params_json = ''
  ElMessage.info('该动作不在新设备的动作目录中，已清空动作 ID 与参数，请重新选择')
}

/**
 * 动作 ID 变化（下拉选中 / allow-create 手输）。
 *
 * 为什么"选中就弹表单"：后端 definition.input_schema 已经声明了字段/类型/枚举/范围，
 * 却让用户去别处背 JSON，是同一系统里两套录入方式。有 schema 就当场进表单，用户不必
 * 先记住"这个动作有参数"再去找入口。
 * 为什么手输（目录里没有该 ID）时什么都不做：目录不可用是**已知降级**（设备离线/驱动
 * 未声明），此时没有 schema 可驱动，保留用户原样输入与既有参数，交给保存期后端校验。
 */
function onCardActionIdChange(action: ActionDraft) {
  const def = catalogDefinition(action)
  if (!def) return
  if (definitionHasSchema(def)) {
    // 不把旧参数带进来：它们属于上一个动作，字段名对不上时只会变成干扰（ActionForm
    // 只认自己 schema 里的字段，提交时也会把它们丢掉）。取消则原样保留旧参数。
    openParamsForm(action, null)
    return
  }
  // 无参数动作：schema 明说没有可填字段 ⇒ 清掉上一个动作留下的参数。
  // 不清的话后端会拿旧键去解析新动作（轻则忽略，重则 400），而界面上"参数"还显示着旧值。
  action.action_params_json = ''
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
.mono { font-family: monospace; }
/* 2026-10-21 审计 C-ops P2：「条件」行无宽度约束 ⇒ 393 弹窗 .el-dialog__body
   scrollWidth 368 > clientWidth 361（横向溢出 7px），＋按钮贴到弹窗右内边。
   与 /alerts 同口径：比较符定宽 52px、阈值框弹性保底 176px（± 钮各 44px ⇒ 内层 input ≈88px
   ≥84px），更窄视口下允许换行而不是溢出。 */
.cond-row { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.cond-row .el-select { flex: 0 0 52px; min-width: 52px; }
.cond-row .el-input-number { flex: 1 1 auto; min-width: 176px; max-width: 100%; }
/* 阈值单位：跟在数字框右侧的**文本**，不是输入框后缀（EP 的 suffix 只接受图标/字符串,
   且会随 precision 抖动）；不参与伸缩、不换行，用户读到的是完整的 "30 °C" 一句话。 */
.cond-row .threshold-unit { flex: 0 0 auto; margin-left: 4px; white-space: nowrap; }
/* 试算按钮与结论都在「条件」行的下一行：结论是**提示**，不占输入位，也不挤阈值。 */
.preview-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; width: 100%; margin-top: 8px; }
.preview-row .hint { margin-left: 0; }
/* 结论必须独占一行：与按钮同排时会被挤成一条窄缝，长句读不全 */
.preview-result { width: 100%; margin-top: 6px; font-size: 12px; line-height: 1.6; color: var(--el-text-color-regular); }
/* "试算不可用"（413 / 服务异常）用 warning 色，但仍是提示，不是表单校验错误 */
.preview-result--warning { color: var(--el-color-warning); }
.hint { margin-left: 8px; color: var(--el-text-color-secondary); font-size: 12px; }
.text-muted { color: var(--el-text-color-secondary); }
.events-pagination { display: flex; justify-content: flex-end; margin-top: 12px; flex-wrap: wrap; }
/* 分页控件换行（窄屏真实裁切修复，与 notification/NotificationChannels.vue 同根因；
   原参照页 LogicalDeviceList.vue 已于 2026-10-23 随逻辑设备概念移除）。
   .events-pagination 的 flex-wrap 只管多个 item 之间；内层 el-pagination 自身
   white-space:nowrap + display:flex 且宽 676.94px，在 768px（.events-pagination 宽 488px）
   就已把「共 N 条」推出容器左界 153px —— 实测 el-pagination x=47.06 < 容器 x=236，
   祖先链 scrollWidth === clientWidth（不可回滚）⇒ 真实裁切。
   同仓范式：firmware/FirmwareManage.vue 的 .firmware-manage :deep(.el-pagination)。 */
.events-pagination :deep(.el-pagination) { flex-wrap: wrap; }

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
/* 时区下拉在网格单元里必须撑满（EP 的 .el-select 是 inline-flex，默认按内容宽收缩），
   否则它与左边两个经纬度输入框不对齐。同 .action-meta dd :deep(.el-select) 的既有口径。 */
.sun-location__fields :deep(.el-select) { width: 100%; min-width: 0; }

/* ============================================================================
   附加条件：条件行（文档 §4.3）
   —— 一行放：序号 + 字段选择器（弹性） + 比较符（定宽） + 阈值（定宽） + 删除。
   640px 弹窗里 label 占 110px、内容区约 500px：定宽三件套 104+120+32 与 3 个 8px
   间距共约 280px，选择器仍有 ~190px 可用（与 .cond-row 的比较符定宽同口径，不另立一套）。
   ========================================================================== */
.conditions { width: 100%; display: flex; flex-direction: column; gap: 8px; min-width: 0; }
.conditions__head { display: flex; align-items: center; justify-content: space-between; gap: 8px; flex-wrap: wrap; }
.conditions__hint { margin-top: 0; flex: 1 1 auto; min-width: 0; }
/* 无法解析的存量原文是**警告**而不是错误：值本身还在，只是暂时改不动（不是数据坏了要删） */
.conditions__warn { color: var(--el-color-warning); }
.condition-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; }
.condition-row { display: flex; align-items: center; gap: 8px; min-width: 0; }
.condition-row__index {
  display: inline-flex; align-items: center; justify-content: center; flex: 0 0 auto;
  width: 20px; height: 20px; border-radius: 50%;
  background: var(--el-color-primary-light-9); color: var(--el-color-primary);
  font-size: 12px; font-weight: 600;
}
/* 选择器列必须能收缩：SensorFieldPicker 根是 width:100% 的纵向 flex，
   少了 min-width:0 它会把比较符/阈值挤出弹窗右侧。 */
.condition-row__field { flex: 1 1 auto; min-width: 0; }
.condition-row__field :deep(.el-select) { width: 100%; }
.condition-row__cmp { flex: 0 0 104px; }
.condition-row__threshold { flex: 0 0 120px; }
.condition-row__remove { flex: 0 0 auto; }
.conditions__foot { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }

/* ============================================================================
   冷却 / 每日上限快捷 chips（文档 §4.5）
   —— 视觉沿用页面既有的"浅描边 + 圆角"语言，不引入新的圆角/色板变量。
   ========================================================================== */
.number-field { width: 100%; display: flex; flex-direction: column; gap: 6px; }
.number-field__main { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.chip-row { display: flex; flex-wrap: wrap; gap: 6px; }
.chip {
  display: inline-flex; align-items: center; justify-content: center;
  min-height: 26px; padding: 0 10px;
  border: 1px solid var(--el-border-color);
  border-radius: 13px;
  background: var(--el-fill-color-blank);
  color: var(--el-text-color-regular);
  font-size: 12px; line-height: 1;
  cursor: pointer;
}
.chip:hover { border-color: var(--el-color-primary); color: var(--el-color-primary); }
.chip:focus-visible { outline: 2px solid var(--el-color-primary); outline-offset: 1px; }
.chip.is-active { border-color: var(--el-color-primary); color: var(--el-color-primary); background: var(--el-color-primary-light-9); }

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
/* 参数行：「手写 JSON」是表单之外的高级出口，与摘要同排但不抢主按钮的位置 */
.action-card__json-link { margin-left: 8px; }
/* 兼容态提示（存量规则 / 设备换型：动作不在当前目录里）。警告色小字 = 只是提示，
   不阻断保存 —— 判定权在后端，前端替它拦下来只会让合法规则存不进去。 */
.action-card__hint--warn { color: var(--el-color-warning); }
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
   保存前诊断区 (§3.3)：只做间距与列表排版，不引入新视觉语言 ——
   颜色/图标全部沿用 el-alert 的 error/warning 语义（红=硬阻断、黄=风险提示）。
   ========================================================================== */
.save-diagnostics { margin-top: 12px; display: flex; flex-direction: column; gap: 8px; }
.save-diagnostics__hint { margin: 0 0 4px; font-size: 12px; line-height: 1.6; }
.save-diagnostics__list { margin: 0; padding-left: 18px; font-size: 12px; line-height: 1.7; overflow-wrap: anywhere; }

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

  /* 条件行：窄屏换行/纵向堆叠，不新增断点（与页面既有 768px 同源）。
     行内四件套在 640px 弹窗里刚好放得下，但在 390px 视口下只剩 ~230px 内容宽，
     继续并排会把字段选择器压成一条竖线（与 .cond-row 的 flex-wrap 同口径处理）。 */
  .condition-row { flex-wrap: wrap; align-items: stretch; }
  .condition-row__field { flex: 1 1 100%; }
  .condition-row__cmp, .condition-row__threshold { flex: 1 1 96px; }
  .condition-row__remove { min-height: 36px; }
  /* 触控目标高度：chips 在粗指针设备上同样要够 36px（theme.css 的触控档） */
  .chip { min-height: 36px; padding: 0 12px; }

}

/* ============================================================================
   窄屏：480px 加强档 (F-10；设计稿 §7)
   为什么弹窗要变底部抽屉：640px 宽的规则编辑器在 390px 视口下即使 92vw 也只有
   359px 可用宽，表单标签 + 控件会被压到逐字换行；而保存前确认弹窗内容长度不可控
   （风险动作越多越长），全屏抽屉能让内容区独立滚动且 footer 常驻可达。
   ========================================================================== */
@media (max-width: 480px) {
  /* 列表两张卡片（RuleListSection / EventHistorySection 的根节点）在窄屏收紧内边距。
     子组件根节点会继承本页的 scope id（data-v-xxx），故这条规则仍然是活的命中，
     不是孤儿选择器 —— 挪进子组件反而会丢掉"页面级密度"这层语义。 */
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
