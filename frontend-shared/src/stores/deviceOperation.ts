import { defineStore } from 'pinia'
import { ref } from 'vue'
import { deviceOperationApi, type ConfirmationGrant, type DeviceOperation, type EffectiveAction } from '@/api/deviceOperation'
import { assertSessionGeneration, getSessionGeneration, registerSessionCacheClearer } from '@/utils/sessionCache'

const terminalStatuses = new Set(['SUCCEEDED', 'FAILED', 'UNKNOWN', 'CANCELLED'])
const statusRank: Record<DeviceOperation['status'], number> = {
  QUEUED: 1,
  DISPATCHED: 2,
  DEVICE_ACCEPTED: 3,
  VERIFYING: 4,
  SUCCEEDED: 5,
  FAILED: 5,
  UNKNOWN: 5,
  CANCELLED: 5,
}

function mayReplace(current: DeviceOperation, next: DeviceOperation): boolean {
  // A REST response can arrive after an early WebSocket acceptance/final. Do
  // not let it regress the timeline, and never let any later pending event
  // overwrite an observed terminal result.
  if (terminalStatuses.has(current.status)) return false
  return statusRank[next.status] >= statusRank[current.status]
}

export const useDeviceOperationStore = defineStore('deviceOperation', () => {
  const catalogs = ref(new Map<number, EffectiveAction[]>())
  const histories = ref(new Map<number, DeviceOperation[]>())
  async function refresh(id: number) {
    const session = getSessionGeneration()
    const [catalog, history] = await Promise.all([deviceOperationApi.actions(id), deviceOperationApi.list(id)])
    if (session !== getSessionGeneration()) return
    catalogs.value.set(id, catalog)
    // REST is authoritative, but must merge rather than replace to retain an
    // ACK/final event that raced ahead of this refresh response.
    for (const operation of history) apply(operation)
  }
  async function create(id: number, actionId: string, params: Record<string, unknown> = {}, confirmationToken = '', reason = '') {
    const session = getSessionGeneration()
    const execution = await deviceOperationApi.create(id, actionId, params, confirmationToken, reason)
    assertSessionGeneration(session)
    apply(execution)
    return execution
  }
  async function confirm(id: number, actionId: string, params: Record<string, unknown>, reason: string): Promise<ConfirmationGrant> {
    const session = getSessionGeneration()
    const grant = await deviceOperationApi.confirm(id, actionId, params, reason)
    assertSessionGeneration(session)
    return grant
  }
  function apply(operation: DeviceOperation) {
    const history = histories.value.get(operation.edge_device_id) ?? []
    const index = history.findIndex(item => item.command_id === operation.command_id)
    if (index >= 0) {
      if (!mayReplace(history[index], operation)) return
      history.splice(index, 1, operation)
    } else {
      history.unshift(operation)
    }
    histories.value.set(operation.edge_device_id, [...history].slice(0, 100))
  }
  function clear() { catalogs.value.clear(); histories.value.clear() }
  return { catalogs, histories, refresh, create, confirm, apply, clear }
})
registerSessionCacheClearer(() => useDeviceOperationStore().clear())
