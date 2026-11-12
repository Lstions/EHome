import { ref } from 'vue'
import PeripheralControl from '@/components/periph/PeripheralControl.vue'
import { useWebSocketStore, type WebSocketMessage } from '@/stores/websocket'
import { WS_EVENT } from '@/events/events'

/**
 * 外设直控（GPIO/PWM）的「写后回填」：把 WS periph_result 按 request_id 回填到行控件。
 *
 * 为什么整块搬：pending 表 + 代际 + 三重比对（代际/类型动作/资源身份）必须一起看才有意义，
 * 拆开任何一项都会让「旧节点的 ACK 写进新节点 UI」重新变得可能。它只依赖本页持有的
 * PeripheralControl 引用，与节点数据、总线资源无关。
 */
export function usePeripheralAck() {
  const wsStore = useWebSocketStore()

  // 语义与 components/node/ChannelPanel.vue 完全一致（同一套 pending + 代际校验），
  // 差异只在于回填入口改为本页持有的 PeripheralControl 引用。
  // periph_generation 在节点切换/卸载时自增，使在途请求的 ACK 失效 —— 否则旧节点的
  // 响应会写进新节点的行控件（跨节点状态串台）。
  const peripheralControlRef = ref<InstanceType<typeof PeripheralControl> | null>(null)
  let periphGeneration = 0
  const pendingPeriphRequests = new Map<number, { generation: number; type: number; resource: string; action: number }>()
  let unsubscribePeriph: (() => void) | null = null

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
   * 该 TAB 对 gpio/pwm 有 `busSupportsChannels=false`（只含 uart/i2c/spi/adc），
   * 主按钮显示「此资源不支持通道」且 disabled，全文件对 gpioApi/pwmApi 零调用。
   */
  function onPeripheralConfigure(_resourceName: string) {
    // 对话框由 PeripheralControl 自行打开（见其 @configure/@edit 处理）。
    // 此处刻意不弹消息：配置动作本身已有明确视觉反馈。
  }

  /** 外设写后回填：GPIO/PWM 直控的 ACK 经 periph_result 到达（PeriphRsp 0x1C）。 */
  function startSubscription() {
    const unsubPeriph = wsStore.subscribe(WS_EVENT.PERIPH_RESULT, onPeriphResult)
    unsubscribePeriph = () => { unsubPeriph() }
  }

  /** 路由切换：旧节点的在途 ACK 一律作废。 */
  function resetForRouteChange() {
    periphGeneration++
    pendingPeriphRequests.clear()
  }

  /** 卸载：同上，并停订阅（否则回调会打到已卸载组件的 ref 上）。 */
  function stopAll() {
    periphGeneration++
    pendingPeriphRequests.clear()
    if (unsubscribePeriph) { unsubscribePeriph(); unsubscribePeriph = null }
  }

  /** 模板引用用函数式绑定（ref 对象在模板里会被自动解包，不能直接绑）。 */
  function setPeripheralControlRef(instance: unknown) {
    peripheralControlRef.value = (instance as InstanceType<typeof PeripheralControl> | null) ?? null
  }

  return {
    setPeripheralControlRef,
    registerPendingPeripheral,
    registerPendingPWM,
    onPeripheralConfigure,
    startSubscription,
    resetForRouteChange,
    stopAll,
  }
}
