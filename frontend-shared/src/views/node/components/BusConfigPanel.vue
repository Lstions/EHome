<template>
        <div class="bus-alert" :class="{ 'bus-alert-offline': nodeOffline }">
          <el-icon :size="16"><WarningFilled /></el-icon>
          <span><b>仅在线可编辑：</b>{{ nodeOnline ? '当前设备在线，您可以查看和修改配置' : '当前设备离线，只能查看已上报的资源配置' }}</span>
          <button class="link-btn bus-alert-refresh" type="button" :disabled="nodeOffline || resourceQuerying" @click="requestRefresh"><el-icon :size="12"><RefreshRight /></el-icon>{{ resourceQuerying ? '查询中…' : '查询资源' }}</button>
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
                  <button class="btn btn-plain" @click="retryLoad">重试</button>
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
                        @click="selectResource(resource.id)"
                      >
                        <td><span class="bus-radio" :class="{ checked: selectedResourceId === resource.id }"></span></td>
                        <td><button class="bus-resource-name" type="button" @click.stop="selectResource(resource.id)">{{ resource.id }}</button></td>
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
                              placeholder="未绑定"
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
                            :loading="resourceDmaBinding(resource) ? Boolean(dmaToggling[resourceDmaBinding(resource)!.dma_id]) : false"
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
                            <button class="link-btn" type="button" :data-view-resource="resource.id" @click.stop="selectResource(resource.id)"><el-icon :size="12"><View /></el-icon>查看</button>
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
                    <button class="bus-page-btn" type="button" :disabled="busPage <= 1" @click="setBusPage(busPage - 1)">上一页</button>
                    <span class="bus-page-current">{{ busPage }}</span>
                    <button class="bus-page-btn" type="button" :disabled="busPage >= busTotalPages" @click="setBusPage(busPage + 1)">下一页</button>
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
                    <div class="bus-tool-foot"><button class="btn btn-plain btn-sm" :disabled="nodeOffline || resourceQuerying" @click="requestRefresh">{{ resourceQuerying ? '查询中…' : '查询资源' }}</button></div>
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
                      class="dma-select dma-select-detail"
                      size="small"
                      :data-dma-detail-select="selectedResource.id"
                      :aria-label="`${selectedResource.id} 绑定的 DMA 资源`"
                      :model-value="dmaSelectionFor(selectedResource)"
                      :disabled="nodeOffline || !canToggleResourceDma(selectedResource)"
                      placeholder="未绑定"
                      @change="(value: any) => changeResourceDma(selectedResource!, String(value))"
                    >
                      <el-option label="不使用 DMA" value="" />
                      <!-- 与资源表同一套文案分档（2026-10-15）：已绑定本资源的那条用紧凑
                           文案作**显示值**（详情行只需回答"绑了哪条"），其余候选保留下拉里的
                           全量文案（带兼容总线）便于比较。两处语言一致才不会出现
                           "表格写 GDMA_CH0、详情写 GDMA_CH0（#0 · UART · I2C）"的分叉。 -->
                      <el-option
                        v-for="dma in resourceDmaCandidates(selectedResource)"
                        :key="dma.dma_id"
                        :label="isDmaBoundToResource(dma, selectedResource) ? dmaBoundValueLabel(dma) : dmaOptionLabel(dma)"
                        :value="String(dma.dma_id)"
                      />
                    </el-select>
                  </b>
                  <!-- 无候选/不可编辑时的只读值：走与 DMA 卡同一套 dmaBoundToText，
                       不把内部键 "uart/uart0" 原样露出（同一资源在别处写作 "uart0（UART）"）。 -->
                  <b v-else :class="{ 'dma-bound': resourceDmaBinding(selectedResource)?.bound_to }">{{ dmaBoundToText(resourceDmaBinding(selectedResource)?.bound_to) }}</b>
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
                <button class="btn btn-plain" :disabled="!selectedResource" @click="selectResource('')">取消选择</button>
                <button class="btn btn-primary" :disabled="nodeOffline || !selectedResource || selectedResource.enabled === false || !busSupportsChannels" @click="selectedResource ? openChannelManager(selectedResource) : undefined">{{ busSupportsChannels ? '新建通道' : '此资源不支持通道' }}</button>
              </div>
            </section>
          </aside>
        </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import {
  Connection, Cpu, DataLine, InfoFilled, Link, Lock, MagicStick, Plus, RefreshRight, Search, Select,
  Share, Timer, Tools, View, WarningFilled,
} from '@element-plus/icons-vue'
import type { Capabilities, DmaChannelInfo } from '@/api/node'
import type { Channel } from '@/api/channel'
import { getHardwareLabel } from '@/utils/hardwareTag'
import { isDmaRebindable } from '@/utils/dmaState'
import {
  type BusType, type BusResource, resourcesOf, resourcePins as pinsOf, resourceParameters as parametersOf,
  resourceMode as modeOf, resourceMountedChannels as mountedChannelsOf, dmaCandidatesOf, resourceDmaBindingOf,
  dmaSelectionOf, isDmaBoundToResource as isDmaBoundToResourceOf, canToggleResourceDma as canToggleResourceDmaOf,
  resourceDmaUnavailableText as dmaUnavailableTextOf, dmaOptionLabel, dmaBoundValueLabel, dmaBoundToText,
} from '../composables/busResourceHelpers'

/**
 * 「总线配置」页签：资源表 + 资源详情 + 创建通道说明 + 总线工具卡。
 *
 * 为什么整块成组件：它只读「设备上报的资源」与「页面当前的选择」，写操作（DMA 绑定/
 * 地址扫描/选中态变化）全部以事件抛回页面 —— 页面的选中态还要供创建通道对话框的 preset
 * 与路由切换重置使用，组件内不留任何跨页签可见的状态。只传原始数据而不传派生结果：
 * 资源视角的派生与「已创建通道」卡必须同口径，口径在 busResourceHelpers 里统一。
 */
const props = defineProps<{
  nodeOnline: boolean
  nodeOffline: boolean
  capabilities: Capabilities
  channels: Channel[]
  dmaChannels: DmaChannelInfo[]
  /** dmaStore.toggling：按 dma_id 的绑定中标记（开关 loading 态）。 */
  dmaToggling: Record<number, boolean>
  activeBusType: BusType
  selectedResourceId: string
  busPage: number
  busPageSize: number
  busLoading: boolean
  busLoadError: boolean
  resourceQuerying: boolean
  i2cScanning: boolean
  scanResult: string[] | null
  baudToolSelection: boolean
  baudToolSelectionText: string
  baudToolAvailable: boolean
}>()

const emit = defineEmits<{
  'refresh-resources': []; 'retry-load': []; scan: []; 'navigate-channels': []; 'open-baud-tool': []
  'select-bus-type': [type: BusType]; 'select-resource': [id: string]; 'bus-page-change': [page: number]
  'change-dma': [resource: BusResource, value: string]
  'toggle-dma': [resource: BusResource, enabled: string | number | boolean]
  'open-channel-manager': [resource: BusResource]
}>()

// label 一律取共享源，避免此处再维护一份总线展示名；description 是说明性文案，不参与实体命名。
const busTabs: Array<{ type: BusType; label: string; icon: any; description: string }> = [
  { type: 'i2c', label: getHardwareLabel('i2c'), icon: Cpu, description: 'I2C 总线用于连接低速外设，支持多主多从通信' },
  { type: 'uart', label: getHardwareLabel('uart'), icon: Connection, description: 'UART 串口用于异步串行通信' },
  { type: 'spi', label: getHardwareLabel('spi'), icon: MagicStick, description: 'SPI 总线用于高速同步串行通信' },
  { type: 'adc', label: getHardwareLabel('adc'), icon: DataLine, description: 'ADC 通道用于采集模拟量输入' },
  { type: 'gpio', label: getHardwareLabel('gpio'), icon: Share, description: 'GPIO 是引脚直控资源，不属于通道协议总线' },
  { type: 'pwm', label: getHardwareLabel('pwm'), icon: Tools, description: 'PWM 资源用于占空比控制输出' },
]
const activeBusTab = computed(() => busTabs.find(tab => tab.type === props.activeBusType) || busTabs[0])
const activeBusDescription = computed(() => activeBusTab.value.description)
/** 当前是否在 UART TAB。"改波特率"等 UART 专属工具只在此时渲染。 */
const isUartTab = computed(() => props.activeBusType === 'uart')

const activeBusResources = computed<BusResource[]>(() => resourcesOf(props.capabilities, props.activeBusType))
const selectedResource = computed(() => activeBusResources.value.find(resource => resource.id === props.selectedResourceId) || null)
const busTotalPages = computed(() => Math.max(1, Math.ceil(activeBusResources.value.length / props.busPageSize)))
const pagedBusResources = computed(() => activeBusResources.value.slice((props.busPage - 1) * props.busPageSize, props.busPage * props.busPageSize))
const busSupportsDma = computed(() => ['uart', 'i2c', 'spi'].includes(props.activeBusType))
// GPIO/PWM 是直接控制资源，不能错误地纳入协议通道系统。
const busSupportsChannels = computed(() => ['uart', 'i2c', 'spi', 'adc'].includes(props.activeBusType))

function resourcePins(resource: BusResource): string { return pinsOf(props.activeBusType, resource) }
function resourceParameters(resource: BusResource): string[] { return parametersOf(props.activeBusType, resource, props.channels) }
function resourceMode(resource: BusResource): string { return modeOf(resource) }
function resourceMountedChannels(resource: BusResource): Channel[] { return mountedChannelsOf(props.activeBusType, resource, props.channels) }
function resourceDmaCandidates(_resource: BusResource): DmaChannelInfo[] { return dmaCandidatesOf(props.activeBusType, props.dmaChannels) }
function resourceDmaBinding(resource: BusResource): DmaChannelInfo | undefined { return resourceDmaBindingOf(props.activeBusType, resource, props.dmaChannels) }
function dmaSelectionFor(resource: BusResource): string { return dmaSelectionOf(props.activeBusType, resource, props.dmaChannels) }
function isDmaBoundToResource(dma: DmaChannelInfo, resource: BusResource): boolean { return isDmaBoundToResourceOf(props.activeBusType, dma, resource) }
function canToggleResourceDma(resource: BusResource): boolean { return canToggleResourceDmaOf(props.activeBusType, resource, props.dmaChannels) }
function resourceDmaUnavailableText(): string { return dmaUnavailableTextOf(props.activeBusType, props.dmaChannels) }

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

// 选中/分页/写操作全部回抛给页面：本组件不持有任何跨页签可见的状态。
function selectBusType(type: BusType) { emit('select-bus-type', type) }
function selectResource(id: string) { emit('select-resource', id) }
function setBusPage(page: number) { emit('bus-page-change', page) }
function changeResourceDma(resource: BusResource, value: string) { emit('change-dma', resource, value) }
function toggleResourceDma(resource: BusResource, enabled: string | number | boolean) { emit('toggle-dma', resource, enabled) }
function scanSelectedI2C() { emit('scan') }
function requestRefresh() { emit('refresh-resources') }
function retryLoad() { emit('retry-load') }
function navigateToNodeChannels() { emit('navigate-channels') }
function openChannelManager(resource: BusResource) { emit('open-channel-manager', resource) }
function openBaudToolInline() { emit('open-baud-tool') }
</script>

<style scoped>
/* 总线配置页签的桌面规则。窄屏规则统一在 nodeOverview.shared.css（门禁要求同条件 @media 块只出现一处），
   因此本文件只保留桌面密度，移动端表现由共享窄屏层按本组件作用域生效。 */
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
.bus-table tbody tr { cursor: pointer; transition: background .15s; }
.bus-table tbody tr:hover { background: var(--no-bg-hover); }
.bus-table tbody tr.selected { background: var(--no-bg-active); }
.bus-table tbody tr.disabled { opacity: .58; }
.bus-select-col { width: 28px; }
.bus-radio { display: block; width: 12px; height: 12px; border: 2px solid var(--no-border); border-radius: 50%; box-sizing: border-box; }
.bus-radio.checked { border-color: var(--no-primary); box-shadow: inset 0 0 0 2px var(--card-bg, #fff); background: var(--no-primary); }
.dma-na { color: var(--no-text-muted); }
.bus-row-actions { display: flex; align-items: center; gap: 12px; }
.bus-pagination { min-height: 48px; display: flex; align-items: center; gap: 10px; color: var(--no-text-secondary); font-size: 12px; }
.bus-page-controls { margin-left: auto; display: flex; align-items: center; gap: 6px; }
.bus-page-btn, .bus-page-current { height: 26px; padding: 0 8px; border: 1px solid var(--no-border); border-radius: 5px; background: var(--card-bg, #fff); color: var(--no-text-secondary); font: inherit; }
.bus-page-btn:not(:disabled) { cursor: pointer; }
.bus-page-btn:disabled { opacity: .5; cursor: not-allowed; }
.bus-page-current { display: inline-flex; align-items: center; color: var(--text-on-fill); border-color: var(--no-primary); background: var(--no-primary); }
/* B3：无可兼容 DMA 时的显式说明（不留空白控件） */
.dma-none { color: var(--no-text-muted); font-size: 12px; line-height: 18px; white-space: nowrap; }
/* 选择器宽度必须跟着列走：原先写死 200px，而「DMA 绑定」列在 812px 表宽下只有 ~81px，
   控件直接溢出并盖住右侧「操作」列（2026-10-02 实测该单元格 scrollWidth=206 > clientWidth=81）。 */
/* ── DMA 绑定：可写配置的「值 + 轻量编辑入口」（2026-10-15 UI-2）────────────
   问题：DMA 绑定用**裸 el-select** 直接塞进两处 —— 资源表单元格与「资源详情」描述
   列表行。裸选择器自带常驻 1px 描边（EP 用 box-shadow inset 实现，实测亮色
   rgb(220,223,230)、暗色 rgb(74,74,76)），于是：
     · 描述列表其余行都是右对齐纯文本，唯独这一行横着一条 1090px 的带框下拉
       （实测 width=1090 / height=24），把整列节奏打断；
     · 表格单元格里它又显得比同行的 tag/开关"重一档"，不像同一套语言。
   目标：两种位置都读成「值 + 轻量编辑入口」——默认无框、hover/focus 才显形，
   且**不占满行**（行宽≠控件宽）。保留三态文字表达（规范 §4.2.2）：
     · 已绑定 → 通道名；未绑定 → "未绑定"；不可用/无候选 → 既有 .dma-na / .dma-none 文案。

   为什么必须用 :deep()：el-select 的内部结构在子组件里，scoped 样式选择不到
   .el-select__wrapper；这些类名来自本项目安装的 Element Plus 版本（已在亮/暗两套
   主题下用 getComputedStyle 实测确认），非跨版本稳定契约。 */
.dma-select { width: 100%; min-width: 0; }

/* 轻量入口：无常驻边框、无背景、无最小高度，与同行的 tag/开关同一视觉重量。
   —— .dma-select 与 .dma-select-detail 共用这一套，保证两处是同一语言。 */
.dma-select :deep(.el-select__wrapper),
.dma-select-detail :deep(.el-select__wrapper) {
  min-height: 0;
  padding: 0 2px;
  background: transparent;
  box-shadow: none;
}
/* 指向提示：让"这是可编辑值"在静止态也成立，但不靠描边 —— 用极轻的 hover 底色。 */
.dma-select :deep(.el-select__wrapper:hover:not(.is-disabled)),
.dma-select-detail :deep(.el-select__wrapper:hover:not(.is-disabled)) {
  background: var(--no-bg-hover);
  box-shadow: none;
}
/* 键盘焦点必须有可见指示（a11y）：仅聚焦时给出与其它输入一致的一圈。
   focus 态描边用全站输入控件的既有 token，不新造色值。 */
.dma-select :deep(.el-select__wrapper.is-focused),
.dma-select-detail :deep(.el-select__wrapper.is-focused) {
  box-shadow: 0 0 0 1px var(--el-color-primary) inset;
}
/* ⚠ 刻意**不**设 font-size：App.vue 的全局移动端防缩放规则
   （@media max-width:768px 下 .el-select__wrapper{font-size:16px}，防 iOS Safari
   聚焦缩放）特异性只有 (0,1,0)，而本文件的 :deep() 选择器是 (0,2,0) —— 一旦在这里
   写 font-size，媒体查询不会提供额外特异性，全局规则会被静默压掉，移动端聚焦就会
   缩放。字号交给 EP 默认（桌面 14px / 移动 16px）继承即可，与"轻量"目标无关。 */
.dma-select :deep(.el-select__placeholder),
.dma-select-detail :deep(.el-select__placeholder) {
  color: var(--no-text);
}
/* 未绑定/不可用：与同行只读值同色，不再像一条"待填写的空输入框"。 */
.dma-select :deep(.el-select__placeholder.is-transparent),
.dma-select-detail :deep(.el-select__placeholder.is-transparent) {
  color: var(--no-text-muted);
}
/* 箭头降到辅助信息层级：它只是"可展开"的提示，不是本行的主角。 */
.dma-select :deep(.el-select__caret),
.dma-select-detail :deep(.el-select__caret) {
  color: var(--no-text-muted);
  font-size: 13px;
}
/* 禁用态（节点离线 / 不可重绑）：与其它禁用控件同款降权，不额外加框。 */
.dma-select :deep(.el-select__wrapper.is-disabled),
.dma-select-detail :deep(.el-select__wrapper.is-disabled) {
  background: transparent;
  box-shadow: none;
}
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
/* 2026-10-15 UI-2：**不再让选择器吃满整行**。
   改前这里写的是 flex:1 + width:100%，于是 DMA 行横着一条 1090px 的带框下拉
   （实测 detailWrapperBox.w=1090），与上下"右对齐纯文本"的行完全不是一个节奏 ——
   这就是用户说的"破坏整列节奏"。
   现在：值本身保持右对齐，编辑入口是**贴着值**的窄控件（上限 150px，够显示
   "GDMA_CH0（#1）"这类最长紧凑文案），像这一行的轻量编辑按钮，
   而不是一条横贯整行的输入框。 */
/* ⚠ 必须给**确定宽度**，不能写 width:auto：EP 的 .el-select__placeholder 是
   position:absolute（浮动标签式），它不参与 wrapper 的固有宽度计算 ⇒ 在 flex 行内
   写 auto 时控件会塌缩到只剩箭头（实测 root 21px，placeholder 被 overflow:hidden
   裁掉，"未绑定"完全不可见）。150px 足够显示最长的紧凑值（如 "GDMA_CH0（#1）"），
   更长的值按 EP 默认省略号处理。 */
.bus-detail-list .bus-detail-edit { flex: 0 0 auto; max-width: 150px; display: inline-flex; justify-content: flex-end; }
.bus-detail-list .bus-detail-edit .dma-select { width: 150px; max-width: 150px; min-width: 0; }
/* 窄屏下编辑入口占满可用宽度：规则并入既有 768 块（见文件尾部 @media），
   本仓门禁要求"不得新增重复的 768px 媒体查询块"（NodeOverview.spec.ts）。 */
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
/* 右栏固定为 468px，六项资源统计最少需要约 640px 内容宽；
   在有桌面侧栏的中等视口继续双栏会把左栏压缩并被卡片裁切。 */
@media (max-width: 1440px) {
  .bus-main-cols { flex-direction: column; }
  .bus-col-left { width: 100%; flex: 0 0 auto; }
  .bus-col-right { width: 100%; flex: 0 0 auto; }
  .bus-create-card { min-height: 0; }
}
</style>

<style scoped src="./nodeOverview.shared.css"></style>
