/**
 * 验证用户名
 */
export function validateUsername(username: unknown): username is string {
  return typeof username === 'string' && username.trim().length > 0
}

/**
 * 验证密码
 */
export function validatePassword(password: unknown): password is string {
  return typeof password === 'string' && password.length >= 8
}

/**
 * 验证设备名称
 */
export function validateDeviceName(name: unknown): name is string {
  return typeof name === 'string' && name.trim().length > 0
}

/**
 * 验证IP地址
 */
export function validateIP(ip: unknown): ip is string {
  if (typeof ip !== 'string') return false
  const ipRegex = /^((25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.){3}(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)$/
  return ipRegex.test(ip)
}

/**
 * 验证端口号
 */
export function validatePort(port: unknown): port is number {
  return typeof port === 'number' && port > 0 && port <= 65535
}

/**
 * 验证邮箱地址
 */
export function validateEmail(email: unknown): email is string {
  if (typeof email !== 'string') return false
  const emailRegex = /^[^\s@]+@[^\s@]+\.[^\s@]+$/
  return emailRegex.test(email)
}

/**
 * 验证MODBUS地址
 */
export function validateModbusAddress(address: unknown): address is number {
  return typeof address === 'number' && Number.isInteger(address) && address >= 1 && address <= 247
}

/**
 * 验证波特率
 */
export function validateBaudrate(baudrate: unknown): baudrate is number {
  if (typeof baudrate !== 'number') return false
  const validRates = [300, 600, 1200, 2400, 4800, 9600, 19200, 38400, 57600, 115200]
  return validRates.includes(baudrate)
}
