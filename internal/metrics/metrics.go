package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Webhook metrics.
var (
	WebhookRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kompakt_webhook_requests_total",
			Help: "Total number of webhook admission requests by operation.",
		},
		[]string{"operation"},
	)

	WebhookRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "kompakt_webhook_request_duration_seconds",
			Help:    "Latency of webhook admission handler.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25},
		},
		[]string{"operation"},
	)
)

// Controller / gate lifecycle metrics.
var (
	GatedPods = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "kompakt_gated_pods",
			Help: "Current number of pods held with scheduling gates.",
		},
		[]string{"namespace", "profile"},
	)

	GateHoldDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "kompakt_gate_hold_duration_seconds",
			Help:    "Time a pod spends gated before release.",
			Buckets: []float64{1, 5, 15, 30, 60, 120, 180, 300, 600},
		},
		[]string{"profile", "reason"},
	)

	GateReleasesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kompakt_gate_releases_total",
			Help: "Total gate releases by profile and reason.",
		},
		[]string{"profile", "reason"},
	)
)

// Ledger metrics.
var (
	LedgerNodes = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "kompakt_ledger_nodes",
			Help: "Number of existing nodes tracked by the ledger.",
		},
	)

	LedgerInflightNodes = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "kompakt_ledger_inflight_nodes",
			Help: "Number of in-flight nodes by detection source.",
		},
		[]string{"source"},
	)

	LedgerAllocatableMillicores = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "kompakt_ledger_allocatable_millicores",
			Help: "Total allocatable CPU across tracked nodes in millicores.",
		},
	)

	LedgerAllocatableMemoryBytes = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "kompakt_ledger_allocatable_memory_bytes",
			Help: "Total allocatable memory across tracked nodes in bytes.",
		},
	)
)

// Rule engine metrics.
var (
	RuleEvaluationsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kompakt_rule_evaluations_total",
			Help: "Total rule evaluations by rule name and result.",
		},
		[]string{"rule", "result"},
	)

	RuleEvaluationDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "kompakt_rule_evaluation_duration_seconds",
			Help:    "Time per rule evaluation.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"rule"},
	)
)

func init() {
	metrics.Registry.MustRegister(
		// Webhook
		WebhookRequestsTotal,
		WebhookRequestDuration,
		// Controller
		GatedPods,
		GateHoldDuration,
		GateReleasesTotal,
		// Ledger
		LedgerNodes,
		LedgerInflightNodes,
		LedgerAllocatableMillicores,
		LedgerAllocatableMemoryBytes,
		// Rules
		RuleEvaluationsTotal,
		RuleEvaluationDuration,
	)
}
