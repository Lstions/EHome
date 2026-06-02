// HAL error code mapping for channel data
export const HAL_ERROR_CODES: Record<number, { label: string; type: string }> = {
  0: { label: '成功', type: 'success' },
  1: { label: '超时', type: 'warning' },
  2: { label: '忙碌', type: 'info' },
  3: { label: '无应答', type: 'warning' },
  4: { label: '参数错误', type: 'danger' },
  5: { label: '不支持', type: 'danger' },
  6: { label: '硬件故障', type: 'danger' },
  7: { label: '部分成功', type: 'warning' },
  8: { label: '配置已变更', type: 'info' },
}

export function getErrorInfo(code: number | undefined | null): { label: string; type: string } {
  if (code === undefined || code === null || code === 0) {
    return { label: '成功', type: 'success' }
  }
  return HAL_ERROR_CODES[code] || { label: `未知错误(${code})`, type: 'danger' }
}