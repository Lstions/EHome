/**
 * 通用数据导出工具
 *
 * 支持 CSV / JSON 两种格式
 * CSV 自动处理 BOM 头，Excel 打开不会中文乱码
 * 字段值含逗号/引号/换行时自动加引号转义
 */

/** 转义 CSV 单元格 */
function escapeCSV(value: unknown): string {
  if (value === null || value === undefined) return ''
  const s = typeof value === 'string' ? value : String(value)
  // 含逗号/双引号/换行 → 加双引号并转义内部双引号
  if (/[",\n\r]/.test(s)) {
    return '"' + s.replace(/"/g, '""') + '"'
  }
  return s
}

/**
 * 导出 CSV
 * @param filename 文件名（不含扩展名）
 * @param headers 表头数组
 * @param rows 数据行
 */
export function exportCSV(
  filename: string,
  headers: string[],
  rows: Array<Record<string, unknown>>,
): void {
  if (!rows.length) {
    return
  }
  const lines: string[] = []
  lines.push(headers.map(escapeCSV).join(','))
  for (const row of rows) {
    lines.push(headers.map((h) => escapeCSV(row[h])).join(','))
  }
  // 加 BOM 让 Excel 正确识别 UTF-8 中文
  const csv = '\ufeff' + lines.join('\r\n')
  triggerDownload(csv, `${filename}.csv`, 'text/csv;charset=utf-8;')
}

/**
 * 导出 JSON
 * @param filename 文件名（不含扩展名）
 * @param data 任意可序列化对象
 */
export function exportJSON(filename: string, data: unknown): void {
  const json = JSON.stringify(data, null, 2)
  triggerDownload(json, `${filename}.json`, 'application/json;charset=utf-8;')
}

/**
 * 通用下载触发
 */
function triggerDownload(content: string, filename: string, mimeType: string): void {
  const blob = new Blob([content], { type: mimeType })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.style.display = 'none'
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  // 异步释放 URL
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

/** 生成默认文件名：业务名_时间戳 */
export function defaultFilename(prefix: string): string {
  const d = new Date()
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${prefix}_${d.getFullYear()}${pad(d.getMonth() + 1)}${pad(d.getDate())}_${pad(d.getHours())}${pad(d.getMinutes())}${pad(d.getSeconds())}`
}
