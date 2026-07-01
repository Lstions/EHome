<template>
  <div ref="chartRef" class="gauge-chart" :style="{ width: width, height: height }"></div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch } from 'vue'
import * as echarts from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { GaugeChart as GaugeChartSeries } from 'echarts/charts'
import { TitleComponent, TooltipComponent } from 'echarts/components'
import type { EChartsOption } from 'echarts/core'

// Register required ECharts components for tree-shaking mode
echarts.use([CanvasRenderer, GaugeChartSeries, TitleComponent, TooltipComponent])

const props = withDefaults(defineProps<{
  value: number
  min?: number
  max?: number
  unit?: string
  title?: string
  width?: string
  height?: string
}>(), {
  min: 0,
  max: 100,
  unit: '',
  title: '',
  width: '200px',
  height: '200px'
})

const chartRef = ref<HTMLElement>()
let chartInstance: echarts.ECharts | null = null
let resizeObserver: ResizeObserver | null = null

const initChart = () => {
  if (!chartRef.value) return

  chartInstance = echarts.init(chartRef.value)

  // 计算百分比位置
  const percent = (props.value - props.min) / (props.max - props.min) * 100

  const option: EChartsOption = {
    series: [
      {
        type: 'gauge',
        min: props.min,
        max: props.max,
        startAngle: 225,
        endAngle: -45,
        radius: '75%',
        center: ['50%', '60%'],
        detail: {
          value: {
            formatter: (value: number) => {
              return `${value.toFixed(1)}${props.unit}`
            }
          }
        },
        data: [
          {
            value: props.value,
            name: props.title || '数值',
            title: {
              offsetCenter: [0, '50%'],
              color: '#fff',
              fontSize: 14,
              formatter: (value: number) => {
                return props.title || '数值'
              }
            },
            detail: {
              value: {
                formatter: (value: number) => {
                  return `${value.toFixed(2)}${props.unit}`
                },
                color: 'auto',
                fontSize: 20,
                offsetCenter: [0, '65%'],
                textStyle: {
                  color: '#fff'
                }
              },
              progress: {
                show: true,
                roundCap: 'round',
                width: 10,
                itemStyle: {
                  color: percent > 80 ? '#f56c6c' : percent > 60 ? '#e6a23c' : '#409eff',
                  shadowColor: 'rgba(0, 0, 0, 0.1)',
                  shadowBlur: 10,
                  shadowOffsetX: 2,
                  shadowOffsetY: 2
                }
              },
              pointer: {
                itemStyle: {
                  color: percent > 80 ? '#f56c6c' : percent > 60 ? '#e6a23c' : '#409eff'
                }
              }
            }
          }
        ]
      }
    ]
  }

  chartInstance.setOption(option)
}

watch(() => props.value, () => {
  if (chartInstance) {
    chartInstance.setOption({
      animation: false,
      series: [
        {
          data: [
            {
              value: props.value
            }
          ]
        }
      ]
    })
  }
})

onUnmounted(() => {
  resizeObserver?.disconnect()
  resizeObserver = null
  if (chartInstance) {
    chartInstance.dispose()
    chartInstance = null
  }
})

onMounted(() => {
  initChart()
  if (chartRef.value) {
    resizeObserver = new ResizeObserver(() => chartInstance?.resize())
    resizeObserver.observe(chartRef.value)
  }
})
</script>

<style scoped>
.gauge-chart {
}
</style>
