package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// NodesOnline tracks number of online nodes
	NodesOnline = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ehome_nodes_online",
		Help: "Number of online nodes",
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

	// NodeOnlineCount tracks online node count (deprecated, use NodesOnline)
	_ = promauto.NewGauge(prometheus.GaugeOpts{
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

	// --- 8.1: PendingWrite observability metrics ---

	// PendingWriteActiveEntries tracks current pending write entries
	PendingWriteActiveEntries = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ehome_pendingwrite_active_entries",
		Help: "Current pending write entries",
	})

	// PendingWriteTimeoutTotal counts timeout events in cleanup
	PendingWriteTimeoutTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ehome_pendingwrite_timeout_total",
		Help: "Total timeout count",
	})

	// PendingWriteLateResponseTotal counts late responses (entry already removed)
	PendingWriteLateResponseTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ehome_pendingwrite_late_response_total",
		Help: "Total late response count",
	})

	// PendingWriteDuration records response latency distribution
	PendingWriteDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "ehome_pendingwrite_duration_seconds",
		Help:    "Response latency distribution",
		Buckets: []float64{0.1, 0.5, 1, 2, 5, 10},
	})

	// --- 8.1: /execute observability metrics ---

	// ExecuteRequestTotal counts execute requests by type and status
	ExecuteRequestTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ehome_execute_request_total",
		Help: "Request count",
	}, []string{"type", "status"})

	// ExecuteReadDuration records read operation latency
	ExecuteReadDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "ehome_execute_read_duration_seconds",
		Help:    "Read operation latency",
		Buckets: []float64{0.1, 0.5, 1, 2, 5, 10},
	})

	// ExecuteConcurrentActive tracks current concurrent executions
	ExecuteConcurrentActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ehome_execute_concurrent_active",
		Help: "Current concurrent executions",
	})

	// ExecuteRateLimitRejected counts rate limit rejections
	ExecuteRateLimitRejected = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ehome_execute_rate_limit_rejected_total",
		Help: "Rate limit rejection count",
	})

	// --- 8.1: Worker pool observability metrics ---

	// WorkerPoolOverflowTotal counts queue overflow events
	WorkerPoolOverflowTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ehome_worker_pool_overflow_total",
		Help: "Overflow count",
	})

	// WorkerPoolBackpressureBlockTotal counts backpressure block events
	WorkerPoolBackpressureBlockTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ehome_worker_pool_backpressure_block_total",
		Help: "Backpressure block count",
	})

	// WorkerPoolProcessDuration records processing latency
	WorkerPoolProcessDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "ehome_worker_pool_process_duration_seconds",
		Help:    "Processing latency",
		Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1},
	})

	// EventBusDroppedTotal counts ConfigEventBus drops
	EventBusDroppedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ehome_event_bus_dropped_total",
		Help: "ConfigEventBus drops",
	})

	// DataConsumerDBWriteFailures counts persistence failures by consumer and table.
	DataConsumerDBWriteFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ehome_data_consumer_db_write_failures_total",
		Help: "DataEventBus consumer database write failures",
	}, []string{"consumer", "table"})

	// DataEventBusDroppedTotal counts data reports dropped by the bounded
	// DataEventBus input queue under backpressure.
	DataEventBusDroppedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ehome_data_event_bus_dropped_total",
		Help: "DataEventBus data reports dropped under backpressure",
	})

	// LogEventBusDroppedTotal counts log batches dropped by stage and consumer.
	LogEventBusDroppedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ehome_log_event_bus_dropped_total",
		Help: "LogEventBus batches dropped under backpressure",
	}, []string{"stage", "consumer"})

	// DeviceActionCreatedTotal deliberately uses a bounded result label rather
	// than action, node, command, or parameter identifiers.
	DeviceActionCreatedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ehome_device_action_created_total",
		Help: "Durable device action creation results",
	}, []string{"result"})

	DeviceActionAdmissionTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ehome_device_action_admission_total",
		Help: "Device action admission requests by bounded result",
	}, []string{"result"})

	DeviceActionTransitionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ehome_device_action_transitions_total",
		Help: "Durable device action state transitions",
	}, []string{"status"})

	DeviceActionDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "ehome_device_action_duration_seconds",
		Help:    "Duration from durable creation to a terminal action state",
		Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 120, 300},
	})

	DeviceActionQueueDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "ehome_device_action_queue_duration_seconds",
		Help:    "Duration from durable creation until MQTT publication",
		Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60},
	})

	DeviceActionAcceptDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "ehome_device_action_accept_duration_seconds",
		Help:    "Duration from MQTT publication until collector acceptance",
		Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30},
	})

	DeviceActionDispatchTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ehome_device_action_dispatch_total",
		Help: "Outbox dispatch results",
	}, []string{"result"})

	DeviceActionManualResolutionTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ehome_device_action_manual_resolution_total",
		Help: "Manual resolution results for UNKNOWN device actions",
	}, []string{"result", "outcome"})

	DeviceActionCapabilityStaleTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ehome_device_action_capability_stale_total",
		Help: "Action catalog responses rejected by a stale ResourceReport",
	})

	SecurityAuditWriteFailuresTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ehome_security_audit_write_failures_total",
		Help: "Security audit events that failed validation, encoding, or persistence",
	})
)
