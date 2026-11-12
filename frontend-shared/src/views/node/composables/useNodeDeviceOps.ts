import { computed, ref, type Ref } from 'vue'
import { ElMessage } from 'element-plus'
import type { Node } from '@/api/node'
import { nodeDeviceOpApi, type NodeDeviceOp } from '@/api/nodeDeviceOp'
import { getSessionGeneration } from '@/utils/sessionCache'
import { logger } from '@/utils/logger'
import feedback from '@/utils/feedback'

/**
 * 节点级设备操作（重启 / 恢复出厂）。
 *
 * 为什么单独成 composable：这两个动作有一整段「产品语义」的注释必须跟着代码走
 * （acked=false 是"结果未知"而不是失败、恢复出厂不擦除 WiFi、成功提示要先说
 * "页面可能短暂失联"）。把它们留在页头模板附近会让页头越滚越长，
 * 而它们与页签内容完全无关。
 */
export function useNodeDeviceOps(options: {
  node: Ref<Node | null>
  nodeSerial: Ref<string>
  nodeOffline: Ref<boolean>
}) {
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
    if (!options.node.value || options.nodeOffline.value || deviceOpBusy.value) return
    const serial = options.nodeSerial.value
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

  return {
    deviceOpSupported,
    deviceOpBusy,
    deviceOpInFlight,
    deviceOpReady,
    loadDeviceOpSupport,
    handleDeviceOp,
  }
}
