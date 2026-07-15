export const loadNodeList = () => import('@/views/node/NodeList.vue')
export const loadEdgeDeviceList = () => import('@/views/edge-device/EdgeDeviceList.vue')

export const preloadPrimaryRoutes = () => Promise.allSettled([
  loadNodeList(),
  loadEdgeDeviceList(),
])