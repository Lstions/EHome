import { describe, expect, it } from 'vitest'
import {
  FIRMWARE_ERROR_TEXT,
  buildFirmwareUploadFormData,
  describeGateReasons,
  firmwareActionErrorMessage,
  firmwareChannelText,
  firmwareProfileText,
  isFirmwareSelectable,
  uploadErrorMessage,
  type Firmware,
} from '@/api/firmware'
import {
  buildFirmwareListParams,
  createFirmwareFilters,
  hasActiveFirmwareFilters,
  isFirmwareBinFile,
  isFirmwareMetaFile,
  paginateFirmwares,
} from '../firmwarePage'

/**
 * 固件页纯函数口径（M1b / T1b.1、T1b.2、T1b.4）。
 *
 * 这些断言点在组件里都看不见（表格行由 el-table 作用域槽渲染、下拉项在 happy-dom 里
 * 只有 option 文本），但它们是「筛选发错参数」「置灰判定错」「409 说成删除失败」的
 * 唯一可机械验证处，所以单独钉住。
 */

function makeFirmware(patch: Partial<Firmware> = {}): Firmware {
  return {
    id: 1,
    version: '2.9.0',
    filename: 'ehome_collector.bin',
    checksum: 'a'.repeat(64),
    size_bytes: 1024,
    url: '/api/v1/firmwares/1/download',
    created_at: '2026-11-18T00:00:00Z',
    chip: 'esp32c6',
    flash_mb: 8,
    psram: 'no',
    model_key: 'esp32c6-n8',
    build_profile: 'c6-n8',
    channel: 'stable',
    identity_state: 'verified',
    ...patch,
  }
}

describe('firmwarePage · 筛选参数', () => {
  it('空串一律不发；include_withdrawn 恒定发送', () => {
    expect(buildFirmwareListParams(createFirmwareFilters())).toEqual({ include_withdrawn: false })
  })

  it('逐字段映射到契约参数（node_id/chip/flash_mb/channel/q）', () => {
    const filters = { ...createFirmwareFilters(), node_id: ' F0F5BD02F35C ', chip: 'esp32s3', flash_mb: '16', channel: 'beta', q: ' s3p ', include_withdrawn: true }
    expect(buildFirmwareListParams(filters)).toEqual({
      include_withdrawn: true,
      node_id: 'F0F5BD02F35C',
      chip: 'esp32s3',
      flash_mb: 16,
      channel: 'beta',
      q: 's3p',
    })
  })

  it('hasActiveFirmwareFilters 只认「收窄」条件（不含 include_withdrawn）', () => {
    expect(hasActiveFirmwareFilters(createFirmwareFilters())).toBe(false)
    expect(hasActiveFirmwareFilters({ ...createFirmwareFilters(), include_withdrawn: true })).toBe(false)
    expect(hasActiveFirmwareFilters({ ...createFirmwareFilters(), q: 'x' })).toBe(true)
  })
})

describe('firmwarePage · 前端分页', () => {
  const rows = Array.from({ length: 25 }, (_, i) => makeFirmware({ id: i + 1 }))

  it('按页切片，且忽略空列表', () => {
    expect(paginateFirmwares(rows, 1, 10)).toHaveLength(10)
    expect(paginateFirmwares(rows, 3, 10)).toHaveLength(5)
    expect(paginateFirmwares([], 1, 10)).toEqual([])
  })

  it('页码越界时钳制到有效范围（筛选后页码可能悬空）', () => {
    expect(paginateFirmwares(rows, 99, 10)[0].id).toBe(21)
    expect(paginateFirmwares(rows, 0, 10)[0].id).toBe(1)
  })
})

describe('firmwarePage · 上传文件校验', () => {
  it('只认 .bin 与 .json', () => {
    expect(isFirmwareBinFile(new File(['x'], 'ehome.bin'))).toBe(true)
    expect(isFirmwareBinFile(new File(['x'], 'ehome.BIN'))).toBe(true)
    expect(isFirmwareBinFile(new File(['x'], 'ehome.meta.json'))).toBe(false)
    expect(isFirmwareMetaFile(new File(['{}'], 'ehome.meta.json'))).toBe(true)
    expect(isFirmwareMetaFile(new File(['{}'], 'ehome.bin'))).toBe(false)
    expect(isFirmwareBinFile(null)).toBe(false)
  })

  it('上传体携带 bin + sidecar 两个字段（字段名是前后端唯一契约点）', () => {
    const bin = new File(['bin'], 'ehome.bin')
    const meta = new File(['{}'], 'ehome.meta.json')
    const formData = buildFirmwareUploadFormData(bin, meta)
    expect(formData.get('file')).toBe(bin)
    expect(formData.get('meta')).toBe(meta)
  })
})

describe('固件可选性（后端闸门 1/2 条的前端镜像）', () => {
  it('verified 且在架 → 可选', () => {
    expect(isFirmwareSelectable(makeFirmware())).toBe(true)
  })

  it('已撤回 → 不可选', () => {
    expect(isFirmwareSelectable(makeFirmware({ withdrawn_at: '2026-11-01T00:00:00Z' }))).toBe(false)
  })

  it('legacy / duplicate → 不可选（AC-17：可见但不可选）', () => {
    expect(isFirmwareSelectable(makeFirmware({ identity_state: 'legacy' }))).toBe(false)
    expect(isFirmwareSelectable(makeFirmware({ identity_state: 'duplicate' }))).toBe(false)
  })

  it('后端逐行 compatible=false 优先 → 不可选', () => {
    expect(isFirmwareSelectable(makeFirmware({ compatible: false }))).toBe(false)
    expect(isFirmwareSelectable(makeFirmware({ compatible: true }))).toBe(true)
  })
})

describe('闸门原因与文案', () => {
  it('10 条闸门机器码都有中文文案（附录 A 全覆盖）', () => {
    const codes = [
      'OTA_GATE_WITHDRAWN',
      'OTA_GATE_FIRMWARE_UNVERIFIED',
      'OTA_GATE_HARDWARE_UNKNOWN',
      'OTA_GATE_MODEL_MISMATCH',
      'OTA_GATE_FLASH_MISMATCH',
      'OTA_GATE_PSRAM_REQUIRED',
      'OTA_GATE_PSRAM_MISMATCH',
      'OTA_GATE_IMAGE_TOO_LARGE',
      'OTA_GATE_NOT_AN_UPGRADE',
      'OTA_GATE_BELOW_MIN_VERSION',
    ]
    for (const code of codes) {
      expect(FIRMWARE_ERROR_TEXT[code] ?? code).toBeTruthy()
      const text = describeGateReasons([code])[0]
      expect(text, code + ' 缺少可读文案').toBeTruthy()
      expect(text).not.toBe(code)
    }
  })

  it('去重、忽略非字符串与空串', () => {
    expect(describeGateReasons(['OTA_GATE_WITHDRAWN', 'OTA_GATE_WITHDRAWN', '', null as never])).toHaveLength(1)
    expect(describeGateReasons(undefined)).toEqual([])
  })

  it('未知机器码原样返回（后端新增分支时不至于空白）', () => {
    expect(describeGateReasons(['OTA_GATE_FUTURE'])).toEqual(['OTA_GATE_FUTURE'])
  })
})

describe('上传与撤回/删除的错误文案', () => {
  it('FW_META_REQUIRED / FW_META_CONFLICT 给出可执行动作', () => {
    expect(uploadErrorMessage({ errorCode: 'FW_META_REQUIRED' })).toContain('.meta.json')
    expect(uploadErrorMessage({ errorCode: 'FW_META_CONFLICT' })).toContain('不一致')
  })

  it('未知错误码退回服务端 message，再退回兜底', () => {
    expect(uploadErrorMessage({ errorCode: 'SOMETHING_ELSE', message: '服务端说明' })).toBe('服务端说明')
    expect(uploadErrorMessage({})).toBe('上传失败')
  })

  it('409 FIRMWARE_IN_USE 必须说「有在途任务」，不能只说失败', () => {
    const error = { errorCode: 'FIRMWARE_IN_USE', message: 'firmware in use' }
    expect(firmwareActionErrorMessage(error, 'delete')).toContain('在途 OTA 任务')
    expect(firmwareActionErrorMessage(error, 'withdraw')).toContain('撤回被拒绝')
  })

  it('从 response.data.error_code 也能识别（HTTP 4xx 与 2xx 内嵌两种入口）', () => {
    const error = { response: { data: { error_code: 'FIRMWARE_IN_USE' } } }
    expect(firmwareActionErrorMessage(error, 'delete')).toContain('在途 OTA 任务')
  })
})

describe('展示字段', () => {
  it('档位优先 model_key，profile 不同时两者都显示', () => {
    expect(firmwareProfileText(makeFirmware())).toBe('esp32c6-n8 / c6-n8')
    expect(firmwareProfileText(makeFirmware({ build_profile: '' }))).toBe('esp32c6-n8')
    expect(firmwareProfileText(makeFirmware({ model_key: '', build_profile: 's3p-n16' }))).toBe('s3p-n16')
  })

  it('channel 未知时不显示英文枚举名', () => {
    expect(firmwareChannelText('stable')).toBe('稳定')
    expect(firmwareChannelText(undefined)).toBe('未知')
  })
})
