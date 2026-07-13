import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { downloadText, exportCSV } from '../exportData'

describe('exportCSV', () => {
  const createObjectURL = vi.fn((_blob: Blob) => 'blob:csv')
  const revokeObjectURL = vi.fn()

  beforeEach(() => {
    vi.useFakeTimers()
    createObjectURL.mockClear()
    revokeObjectURL.mockClear()
    vi.stubGlobal('URL', {
      ...URL,
      createObjectURL,
      revokeObjectURL,
    })
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined)
  })

  afterEach(() => {
    vi.runOnlyPendingTimers()
    vi.useRealTimers()
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('neutralizes formulas after leading whitespace and control characters while preserving CSV quoting', async () => {
    exportCSV('logs', ['值'], [
      { 值: '=plain' },
      { 值: ' =space' },
      { 值: '\t+tab' },
      { 值: '\r-carriage' },
      { 值: '\n@line-feed' },
      { 值: '\u0000\u001f  =controls' },
      { 值: '\u007f+delete' },
      { 值: '\u00a0=unicode-space' },
      { 值: '  normal text' },
      { 值: 'prefix=not-formula' },
      { 值: '\t@SUM(1,2)\n"next"' },
    ])

    expect(createObjectURL).toHaveBeenCalledOnce()
    const blob = createObjectURL.mock.calls[0][0] as Blob
    const csv = await blob.text()
    expect(csv).toBe(
      '\ufeff值\r\n'
      + '\'=plain\r\n'
      + '\' =space\r\n'
      + '\'\t+tab\r\n'
      + '"\'\r-carriage"\r\n'
      + '"\'\n@line-feed"\r\n'
      + '\'\u0000\u001f  =controls\r\n'
      + '\'\u007f+delete\r\n'
      + '\'\u00a0=unicode-space\r\n'
      + '  normal text\r\n'
      + 'prefix=not-formula\r\n'
      + '"\'\t@SUM(1,2)\n""next"""',
    )
  })

  it('keeps text object URLs alive through the click and revokes them later', () => {
    downloadText('line one\nline two', 'logs.txt')

    expect(createObjectURL).toHaveBeenCalledOnce()
    expect(revokeObjectURL).not.toHaveBeenCalled()
    vi.advanceTimersByTime(999)
    expect(revokeObjectURL).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1)
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:csv')
  })
})
