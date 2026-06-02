import client from './client'

// Backend Notification model (source of truth)
export interface Notification {
  id: number
  type: string
  message: string
  read: boolean
  created_at: string
}

// Frontend display model (adapted)
export interface NotificationDisplay {
  id: number
  type: 'info' | 'warning' | 'success' | 'error'
  title: string
  description: string
  source: string
  source_id?: number
  read: boolean
  created_at: string
}

// Adapt backend Notification to display model
function toDisplayModel(n: Notification): NotificationDisplay {
  // Parse message to extract title/description
  // Backend message format: "Collector X went offline" or similar
  const parts = n.message.split(': ')
  const title = parts[0] || n.message
  const description = parts.slice(1).join(': ') || ''

  // Map backend type to frontend type
  let displayType: 'info' | 'warning' | 'success' | 'error' = 'info'
  if (n.type === 'warning' || n.message.toLowerCase().includes('offline')) {
    displayType = 'warning'
  } else if (n.type === 'error' || n.message.toLowerCase().includes('failed')) {
    displayType = 'error'
  } else if (n.type === 'success') {
    displayType = 'success'
  }

  return {
    id: n.id,
    type: displayType,
    title,
    description,
    source: 'system',
    read: n.read,
    created_at: n.created_at,
  }
}

export async function getNotifications(limit = 20): Promise<NotificationDisplay[]> {
  // Backend returns bare array: [Notification, ...]
  const response = await client.get<unknown, Notification[]>(`/api/v1/notifications?limit=${limit}`)
  const list = Array.isArray(response) ? response : (response as any).data || []
  return list.map(toDisplayModel)
}

export async function getUnreadCount(): Promise<number> {
  // Backend doesn't have /notifications/unread-count; calculate client-side
  const notifications = await getNotifications(100)
  return notifications.filter(n => !n.read).length
}

export async function markAsRead(_id: number): Promise<void> {
  // Backend doesn't have PUT /notifications/:id/read yet
}

export async function markAllAsRead(): Promise<void> {
  // Backend doesn't have POST /notifications/read-all yet
}
