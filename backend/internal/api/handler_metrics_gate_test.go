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

// TestFirmwareUpload_RejectionMetrics 覆盖 §5.7 最后一条（评审 A-3 的回归锁）：
//
// 新增的 FW_PKG_* 码必须真的出现在 ehome_firmware_upload_rejected_total{reason} 里，
// **而不是**落到 FW_UNCLASSIFIED。后者是「拿不到机器码」的兜底标签：若 firmwarepkg
// 另造一个 Error 类型，firmwareid.Code(err) 会返回空串，所有包格式拒绝都会挤进
// 这一个无意义的维度，排障断链。
func TestFirmwareUpload_RejectionMetrics(t *testing.T) {
	unclassified := map[string]string{"reason": "FW_UNCLASSIFIED"}
	unclassifiedBefore := metricCounterValue(t, "ehome_firmware_upload_rejected_total", unclassified)

	image := firmwareTestImage(0x000D, 3, "2.8.0", "metrics-upload")
	wrongDigest := firmwareTestManifest(t, "ehome_collector.bin", image, false, "c6-n8")
	wrongDigest["artifact"].(map[string]interface{})["sha256"] = strings.Repeat("ab", 32)

	cases := []struct {
		name    string
		file    string
		reason  string
		payload []byte
	}{
		{
			name:   "bare image is not a package",
			file:   "metric-fw.bin",
			reason: "FW_PKG_REQUIRED",
			// 合法 ESP 镜像但没打包：最常犯的错，必须有专属机器码。
			payload: image,
		},
		{
			name:    "manifest digest differs",
			file:    "metric-fw.ehfw",
			reason:  "FW_PKG_DIGEST_MISMATCH",
			payload: firmwareTestPackageFromManifest(t, wrongDigest, "ehome_collector.bin", image),
		},
		{
			name:   "neither package nor image",
			file:   "metric-junk.bin",
			reason: "FW_NOT_IMAGE",
			// 0x100 字节且首字节既不是 PK 也不是 0xE9 → 内容嗅探给出 FW_NOT_IMAGE。
			payload: make([]byte, 0x100),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("EHOME_EXTERNAL_HOST", "firmware-metrics-test.local")
			r, _, _ := setupOTATest(t)

			labels := map[string]string{"reason": tc.reason}
			before := metricCounterValue(t, "ehome_firmware_upload_rejected_total", labels)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, firmwareUploadWithPackage(t, tc.file, tc.payload))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
			if _, code := decodeEnvelopeErrorCode(t, w.Body.String()); code != tc.reason {
				t.Fatalf("error_code = %q, want %q (%s)", code, tc.reason, truncateBody(w.Body.String()))
			}
			if got := metricCounterValue(t, "ehome_firmware_upload_rejected_total", labels) - before; got != 1 {
				t.Errorf("ehome_firmware_upload_rejected_total{reason=%q} increment = %v, want 1", tc.reason, got)
			}
		})
	}

	// 以上三次拒绝都必须带得出机器码：兜底维度一次都不许涨。
	if got := metricCounterValue(t, "ehome_firmware_upload_rejected_total", unclassified) - unclassifiedBefore; got != 0 {
		t.Errorf("ehome_firmware_upload_rejected_total{reason=\"FW_UNCLASSIFIED\"} increment = %v, want 0 (a typed *firmwareid.Error must carry a code)", got)
	}
	logGateMetricExposition(t)
}
