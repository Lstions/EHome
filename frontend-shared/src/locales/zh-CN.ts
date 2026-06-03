/**
 * 国际化文案 - 简体中文（默认）
 *
 * 设计原则:
 * - 嵌套 key 用 . 访问，例如 t('device.form.name')
 * - 共享文案（按钮、提示、状态）放 common 下
 * - 模块化：每个业务域独立 namespace
 */

export default {
  common: {
    appName: 'EHomeSystem',
    confirm: '确定',
    cancel: '取消',
    save: '保存',
    delete: '删除',
    edit: '编辑',
    create: '创建',
    search: '搜索',
    reset: '重置',
    refresh: '刷新',
    export: '导出',
    import: '导入',
    loading: '加载中...',
    noData: '暂无数据',
    success: '操作成功',
    failed: '操作失败',
    deleteConfirm: '确认删除？该操作不可撤销。',
    yes: '是',
    no: '否',
    on: '开',
    off: '关',
    enabled: '启用',
    disabled: '禁用',
  },
  menu: {
    dashboard: '仪表盘',
    collectors: '采集器',
    devices: '设备',
    data: '数据面板',
    firmware: '固件管理',
    deviceConfigs: '配置模板',
    monitor: '系统监控',
  },
  role: {
    admin: '管理员',
    operator: '操作员',
    viewer: '观察者',
  },
  device: {
    status: {
      online: '在线',
      offline: '离线',
      unknown: '未知',
    },
    type: {
      wind_speed: '风速传感器',
      wind_direction: '风向传感器',
      rain: '雨量传感器',
      light: '光照传感器',
      temp_humidity: '温湿度传感器',
      battery: '电池保护板',
      inverter: '光伏逆变器',
      bmp280: 'BMP280温压传感器',
      sht40: 'SHT40温湿度传感器',
    },
    protocol: {
      modbus: 'MODBUS',
      stream: '字节流',
      custom: '自定义',
    },
    hardware: {
      uart: 'UART',
      i2c: 'I2C',
      spi: 'SPI',
      gpio: 'GPIO',
      adc: 'ADC',
    },
  },
  error: {
    notFound: '页面不存在',
    notFoundDesc: '您访问的页面已被移除、重命名或暂时不可用。',
    forbidden: '无权访问',
    forbiddenDesc: '当前账号没有访问该页面的权限。',
    forbiddenHint: '如需访问该功能，请联系管理员调整角色权限。',
    backHome: '返回首页',
    backPrev: '返回上页',
    relogin: '切换账号',
  },
  login: {
    title: '登录',
    rememberMe: '记住我',
    forgotPassword: '忘记密码？',
    submit: '登 录',
    submitting: '登录中...',
    lockout: '登录已锁定，请 {seconds} 秒后重试',
    failRemaining: '用户名或密码错误，还剩 {n} 次机会',
    locked: '连续 {n} 次登录失败，已锁定 {minutes} 分钟',
  },
  network: {
    offline: '网络已断开',
    reconnecting: '与服务器的连接已断开，正在尝试重新连接...',
    retry: '重试',
  },
  feedback: {
    createSuccess: '创建成功',
    updateSuccess: '更新成功',
    deleteSuccess: '删除成功',
    saveSuccess: '保存成功',
    exportSuccess: '导出成功',
    importSuccess: '导入成功',
    copySuccess: '已复制',
  },
}
