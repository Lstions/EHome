import client from './client'

export interface Notification {
  id: number
  type: 'info' | 'warning' | 'success' | 'error'
  title: string
  description: string
  source: string
  // 后端 models.Notification.SourceID 是 size:64 字符串列 (如 merge_job id /
  // logical_device id 的十进制文本); 前端按字符串处理。
  source_id?: number | string
  read: boolean
  created_at: string
}

export async function getNotifications(limit = 20): Promise<Notification[]> {
  const response = await client.get(`/api/v1/notifications?limit=${limit}`)
  // Interceptor returns {code, data, message} → response.data = the array
  return (response as any).data ?? []
}

export async function getUnreadCount(): Promise<number> {
  const response = await client.get('/api/v1/notifications/unread-count')
  // Interceptor returns {code, data: {count}, message} → response.data.count
  return (response as any).data?.count ?? 0
}

export async function markAsRead(id: number): Promise<void> {
  await client.put(`/api/v1/notifications/${id}/read`)
}

export async function markAllAsRead(): Promise<void> {
  await client.post('/api/v1/notifications/read-all')
}
