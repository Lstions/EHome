package metrics

// build_info_test.go — ehome_build_info 的形状契约（方案 §9.5 / T2.1）。
//
// 单独成文件而不是往 metrics_test.go 里塞：那个文件是"所有指标都能 Inc/Set"的
// 大清单，本用例守的是**info 型指标**特有的两条（值恒为 1、Reset 后只有一个身份），
// 混进去会让两类断言互相影响（顺序、共享 default registry）。

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func buildInfoFamily(t *testing.T) *dto.MetricFamily {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() == "ehome_build_info" {
			return family
		}
	}
	return nil
}

// TestSetBuildInfoExposesSingleSeries 钉住：值=1，version/commit 在 label 上，
// 且连续注入两次后**只**留下最后一个身份（Reset 语义）。
//
// 为什么"只留一个"重要：看板按 ehome_build_info 做版本对账，同一实例出现两条
// 序列会被读成"两个版本同时在跑"，比没有指标更误导。
func TestSetBuildInfoExposesSingleSeries(t *testing.T) {
	SetBuildInfo("2.8.0-old", "0000000stale")
	SetBuildInfo("2.9.0", "a1b2c3d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3")

	family := buildInfoFamily(t)
	if family == nil {
		t.Fatal("default registry 里没有 ehome_build_info（指标名改了或未注册）")
	}
	series := family.GetMetric()
	if len(series) != 1 {
		t.Fatalf("ehome_build_info 序列数=%d want 1（Reset 后只应剩当前构建身份）", len(series))
	}
	m := series[0]
	if got := m.GetGauge().GetValue(); got != 1 {
		t.Errorf("ehome_build_info 值=%v want 1（info 型指标惯例）", got)
	}
	labels := map[string]string{}
	for _, pair := range m.GetLabel() {
		labels[pair.GetName()] = pair.GetValue()
	}
	if labels["version"] != "2.9.0" {
		t.Errorf("version label=%q want 2.9.0", labels["version"])
	}
	if labels["commit"] != "a1b2c3d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3" {
		t.Errorf("commit label=%q want 全量 SHA", labels["commit"])
	}
}
