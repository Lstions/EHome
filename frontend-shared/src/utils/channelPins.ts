export interface ChannelPinConfig {
  id?: number
  enabled?: boolean
  bus_type?: string
  hardware_type?: string
  bus_config?: unknown
}

function normalizeBusType(channel: ChannelPinConfig): string {
  const raw = String(channel.bus_type || channel.hardware_type || '').trim().toUpperCase()
  return ({ '1': 'UART', '2': 'I2C', '3': 'SPI', '5': 'ADC' } as Record<string, string>)[raw] || raw
}

function decodeHex(value: unknown): number[] | null {
  if (typeof value !== 'string') return null
  const normalized = value.trim().replace(/^\\x/i, '')
  if (!normalized || normalized.length % 2 !== 0 || !/^[0-9a-f]+$/i.test(normalized)) return null
  const bytes: number[] = []
  for (let offset = 0; offset < normalized.length; offset += 2) bytes.push(Number.parseInt(normalized.slice(offset, offset + 2), 16))
  return bytes
}

/** Decode the actual GPIO routes persisted for enabled transport channels. */
export function enabledChannelPins(channels: ChannelPinConfig[]): Map<number, string> {
  const occupied = new Map<number, string>()
  const add = (pin: number, label: string) => {
    if (Number.isInteger(pin) && pin >= 0 && !occupied.has(pin)) occupied.set(pin, label)
  }
  for (const channel of channels) {
    if (channel.enabled !== true) continue
    const busType = normalizeBusType(channel)
    const bytes = decodeHex(channel.bus_config)
    if (!bytes) continue
    const label = `${busType}${channel.id ? ` #${channel.id}` : ''}`
    if ((busType === 'UART' || busType === 'I2C') && bytes.length >= 2) {
      add(bytes[0], label)
      add(bytes[1], label)
    } else if (busType === 'SPI' && bytes.length >= 6) {
      add(bytes[0], label)
      if (bytes.length >= 9) {
        add(bytes[6], label)
        add(bytes[7], label)
        add(bytes[8], label)
      }
    }
  }
  return occupied
}
