package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// CollectorsOnline tracks number of online collectors
	CollectorsOnline = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ehome_collectors_online",
		Help: "Number of online collectors",
	})

	// MessagesReceived counts total MQTT messages received by type
	MessagesReceived = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ehome_messages_received_total",
		Help: "Total MQTT messages received by type",
	}, []string{"type"})

	// MessagesSent counts total MQTT messages sent by type
	MessagesSent = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ehome_messages_sent_total",
		Help: "Total MQTT messages sent by type",
	}, []string{"type"})

	// DataReportsProcessed counts data reports processed
	DataReportsProcessed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ehome_data_reports_processed_total",
		Help: "Total data reports processed and stored",
	})

	// DataReportErrors counts data report processing failures
	DataReportErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ehome_data_report_errors_total",
		Help: "Total data report processing errors",
	})

	// PingRTT records ping round-trip time in milliseconds
	PingRTT = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "ehome_ping_rtt_milliseconds",
		Help:    "Ping round-trip time in milliseconds",
		Buckets: []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000},
	})

	// WorkerPoolQueueSize tracks current worker pool queue depth
	WorkerPoolQueueSize = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ehome_worker_pool_queue_size",
		Help: "Current data report worker pool queue depth",
	})

	// PendingWrites tracks pending write commands
	PendingWrites = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ehome_pending_writes",
		Help: "Number of pending write commands awaiting response",
	})

	// ConfigManifestsSent counts config manifests sent
	ConfigManifestsSent = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ehome_config_manifests_sent_total",
		Help: "Total config manifest messages sent",
	})

	// OTAUpdates counts OTA update attempts
	OTAUpdates = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ehome_ota_updates_total",
		Help: "Total OTA update attempts by status",
	}, []string{"status"})

	// HTTPRequests counts API requests by path and method
	HTTPRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ehome_http_requests_total",
		Help: "Total HTTP API requests by path and method",
	}, []string{"method", "path"})

	// HTTPDuration records HTTP request duration
	HTTPDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "ehome_http_duration_seconds",
		Help:    "HTTP request duration in seconds",
		Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
	}, []string{"method", "path"})

	// --- G10: Additional design metrics ---

	// NodeOnlineCount tracks online node count
	NodeOnlineCount = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ehome_node_online_count",
		Help: "Online node count",
	})

	// EdgeDeviceTotal tracks edge device count by status
	EdgeDeviceTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ehome_edge_device_total",
		Help: "Edge device count by status",
	}, []string{"status"})

	// DataReceivedTotal counts data reports received by node and status
	DataReceivedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ehome_data_received_total",
		Help: "Data reports received",
	}, []string{"node_id", "status"})

	// MqttPublishFailures counts MQTT publish failures by topic
	MqttPublishFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ehome_mqtt_publish_failures_total",
		Help: "MQTT publish failures",
	}, []string{"topic"})

	// HttpRequestDuration records HTTP request duration with status code label
	HttpRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "ehome_http_request_duration_seconds",
		Help:    "HTTP request duration",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route", "status"})

	// SyncDecisionsTotal counts sync gate decisions by reason and action
	SyncDecisionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ehome_sync_decisions_total",
		Help: "Sync decisions total",
	}, []string{"reason", "action"})

	// EventBusDroppedTotal counts ConfigEventBus drops
	EventBusDroppedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ehome_event_bus_dropped_total",
		Help: "ConfigEventBus drops",
	})
)
