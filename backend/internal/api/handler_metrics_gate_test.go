package api

// handler_metrics_gate_test.go — 「发生拒绝 → 计数器 +1」的 HTTP 路径验证，
// 并顺带打印 /metrics 里两条新指标的 HELP/TYPE/样本行（验收证据）。
//
// 为什么用 Before/After 差值：prometheus 计数器是进程全局的，同包其它用例
// 也会自增同名序列；绝对值断言会随执行顺序 flaky。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ehome/backend/internal/firmwareid"
	"ehome/backend/internal/models"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// metricCounterValue 读默认 registry 里 name 且标签完全等于 labels 的计数器值。
func metricCounterValue(t *testing.T, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			got := make(map[string]string, len(metric.GetLabel()))
			for _, pair := range metric.GetLabel() {
				got[pair.GetName()] = pair.GetValue()
			}
			if len(got) != len(labels) {
				continue
			}
			match := true
			for key, value := range labels {
				if got[key] != value {
					match = false
					break
				}
			}
			if match {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}

// logGateMetricExposition 打印 /metrics（promhttp）里两条新指标的行。
// 只在对应 family 已有样本时才会出现（CounterVec 无样本时 prometheus 不导出）。
func logGateMetricExposition(t *testing.T) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(rec, req)
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if strings.Contains(line, "ehome_ota_gate_rejected_total") ||
			strings.Contains(line, "ehome_firmware_upload_rejected_total") {
			t.Logf("/metrics: %s", line)
		}
	}
}

// TestGateRejectionMetric_HTTPPath：HTTP 建任务被闸门拒绝 → 指标 reason 维度 +1。
func TestGateRejectionMetric_HTTPPath(t *testing.T) {
	r, db, _ := setupOTATestWithDownlink(t, &envelopeDownlinkStub{})
	node := seedGateHTTPNode(t, db, "NODE-METRIC-GATE", "1.0.0")
	fw := seedGateHTTPFirmware(t, db, gateFirmwareSeed{Version: "2.0.0"})
	if err := db.Model(&models.Node{}).Where("id = ?", node.ID).Update("hw_flash_mb", 0).Error; err != nil {
		t.Fatalf("clear node flash profile: %v", err)
	}

	labels := map[string]string{"reason": firmwareid.GateHardwareUnknown}
	before := metricCounterValue(t, "ehome_ota_gate_rejected_total", labels)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, gateHTTPRequest(t, http.MethodPost, "/api/v1/ota/tasks", createTaskBody(node.NodeID, fw.ID, nil)))
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	after := metricCounterValue(t, "ehome_ota_gate_rejected_total", labels)
	if got := after - before; got != 1 {
		t.Errorf("ehome_ota_gate_rejected_total{reason=%q} increment = %v, want 1", firmwareid.GateHardwareUnknown, got)
	}
	logGateMetricExposition(t)
}

// TestFirmwareUploadRejectionMetric_HTTPPath：上传拒绝的统一出口覆盖多个 FW_* 码。
func TestFirmwareUploadRejectionMetric_HTTPPath(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("EHOME_EXTERNAL_HOST", "firmware-metrics-test.local")
	r, _, _ := setupOTATest(t)

	image := firmwareTestImage(0x0009, 4, "2.8.0", "metrics-upload")

	// 1) 合法镜像但缺 sidecar → FW_META_REQUIRED
	missingSidecar := map[string]string{"reason": "FW_META_REQUIRED"}
	before := metricCounterValue(t, "ehome_firmware_upload_rejected_total", missingSidecar)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, firmwareUploadWithSidecar(t, "metric-fw.bin", image, nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if got := metricCounterValue(t, "ehome_firmware_upload_rejected_total", missingSidecar) - before; got != 1 {
		t.Errorf("ehome_firmware_upload_rejected_total{reason=\"FW_META_REQUIRED\"} increment = %v, want 1", got)
	}

	// 2) 非镜像 → FW_NOT_IMAGE（证明统一拒绝出口不止覆盖 sidecar 分支）
	notImage := map[string]string{"reason": "FW_NOT_IMAGE"}
	before = metricCounterValue(t, "ehome_firmware_upload_rejected_total", notImage)
	w = httptest.NewRecorder()
	// 0x100 字节且 magic 不是 0xE9 → FW_NOT_IMAGE（短于 0xD0 会先报 FW_TRUNCATED）。
	notAnImage := make([]byte, 0x100)
	r.ServeHTTP(w, firmwareUploadWithSidecar(t, "not-an-image.bin", notAnImage, nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if got := metricCounterValue(t, "ehome_firmware_upload_rejected_total", notImage) - before; got != 1 {
		t.Errorf("ehome_firmware_upload_rejected_total{reason=\"FW_NOT_IMAGE\"} increment = %v, want 1", got)
	}

	logGateMetricExposition(t)
}
