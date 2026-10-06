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

	// PendingWrites tracks pending write commands awaiting response.
	//
	// 与 PendingWriteActiveEntries（ehome_pendingwrite_active_entries）语义重叠：
	// 两者都是"当前在途的写请求数"。这里刻意保留两个名字而不是删掉其一 ——
	// ehome_pending_writes 先存在，删掉会让可能按它配置的看板/告警静默失配；
	// 而保留一个**没有写入点**的 gauge 更糟：它会一直暴露为 0，看起来像
	// "永远没有待写命令"。现由 pendingwrite 的提交/完成路径同时刷新两者。
	PendingWrites = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ehome_pending_writes",
		Help: "Number of pending write commands awaiting response (same value as ehome_pendingwrite_active_entries)",
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

	// NodeOnlineCountDeprecated 是历史指标名 ehome_node_online_count。
	//
	// 为什么保留：docs/设计/系统监控.md 与 docs/archive/ 下的设计文档仍按此名
	// 描述在线节点数；删掉它会让这些文档与既有看板静默失配。
	//
	// 为什么必须是可写变量：它原先写成 `_ = promauto.NewGauge(...)` ——
	// 匿名注册后**没有任何写入点**，因此恒为 0。恒为 0 比不暴露更坏：用旧指标名
	// 做的看板会一直显示"0 个在线"，而不是"没有数据"。现已由
	// offlinedetector.publishNodesOnline 与 NodesOnline 同步刷新。
	NodeOnlineCountDeprecated = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ehome_node_online_count",
		Help: "Online node count (deprecated name; kept in sync with ehome_nodes_online)",
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

	// ManifestCommandSkippedNoTemplate counts edge-device command sub-frames
	// skipped during ConfigManifest encoding because the channel has no
	// usable template_id (channel.template_ids empty/dangling/unparseable).
	// F3: findTemplateID no longer falls back to template 1.
	ManifestCommandSkippedNoTemplate = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ehome_manifest_command_skipped_no_template_total",
		Help: "ConfigManifest edge commands skipped due to missing template_id",
	}, []string{"node_id"})

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

	// --- Data lifecycle task observability metrics ---

	// LifecycleTaskFailures counts data lifecycle task failures by task type
	// (retention|purge|partition).
	LifecycleTaskFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ehome_lifecycle_task_failures_total",
		Help: "Data lifecycle task failures by task",
	}, []string{"task"})

	// LifecyclePurgedRows counts rows deleted by data lifecycle tasks
	// (retention|purge). Partition DROP uses LifecycleDroppedPartitions instead.
	LifecyclePurgedRows = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ehome_lifecycle_purged_rows_total",
		Help: "Rows purged by data lifecycle task",
	}, []string{"task"})

	// LifecycleDroppedPartitions counts monthly partitions dropped by the
	// partition manager during retention cleanup.
	LifecycleDroppedPartitions = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ehome_lifecycle_dropped_partitions_total",
		Help: "Partitions dropped by data lifecycle partition management",
	})

	// --- 外发通知通道出站投递可观测性 (设计/外发通知通道.md §6) ---

	// NotificationDeliveriesTotal deliberately uses bounded labels only:
	// channel_type | result (delivered|failed|retrying). Channel id and target
	// URL must never become labels — unbounded cardinality and credential leak.
	NotificationDeliveriesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ehome_notification_deliveries_total",
		Help: "Outbound notification delivery attempts by channel type and result",
	}, []string{"channel_type", "result"})

	NotificationDeliveryDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "ehome_notification_delivery_duration_seconds",
		Help:    "Outbound notification delivery duration in seconds",
		Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60},
	}, []string{"channel_type"})

	// NotificationChannelsEnabled reports how many channels currently participate
	// in outbound delivery (enabled=true).
	NotificationChannelsEnabled = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ehome_notification_channels_enabled",
		Help: "Number of enabled outbound notification channels",
	})

	// --- V3-2a DataBatch (0x20) observability ---
	//
	// Labels are deliberately absent: node_id / channel_id would be unbounded
	// cardinality, and the per-node view already exists via DataReceivedTotal.
	// The pair frames vs samples is what proves batching actually happens
	// (samples/frames = mean batch size), and rejected is the fail-closed
	// counter that must stay 0 in steady state.

	// DataBatchFramesTotal counts accepted DataBatch frames (0x20).
	DataBatchFramesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ehome_data_batch_frames_total",
		Help: "Total DataBatch (0x20) frames accepted and fanned out",
	})

	// DataBatchSamplesTotal counts samples fanned out as DataEvents.
	DataBatchSamplesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ehome_data_batch_samples_total",
		Help: "Total samples fanned out from DataBatch frames into DataEvents",
	})

	// DataBatchRejectedTotal counts DataBatch frames rejected by a strict
	// invariant (count mismatch, out-of-range count, non-zero first delta,
	// non-monotonic delta, empty/oversized raw_data, duplicate known field).
	DataBatchRejectedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ehome_data_batch_rejected_total",
		Help: "Total DataBatch (0x20) frames rejected by a strict invariant",
	})
)
