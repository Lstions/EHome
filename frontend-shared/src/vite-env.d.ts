/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_API_BASE_URL: string
  /**
   * WebSocket 地址。语义二选一（唯一，见 stores/websocket.ts）：
   * - 完整端点：以 ws:// 或 wss:// 开头 → 原样使用，不追加路径，不受 VITE_BASE_PATH 影响。
   *   注意 wss://host（无路径）即完整端点本身，不会自动补 /api/v1/ws。
   * - 相对路径：以 / 开头 → 拼 VITE_BASE_PATH 前缀后基于当前页面 origin 组装。
   */
  readonly VITE_WS_URL: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
