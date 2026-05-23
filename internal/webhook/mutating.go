package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/reyshazni/kompakt/internal/kompakt"
	"github.com/reyshazni/kompakt/internal/matcher"
	kompaktmetrics "github.com/reyshazni/kompakt/internal/metrics"
)

var (
	labelProfile      = kompakt.LabelProfile
	labelExclude      = kompakt.LabelExclude
	annotationTraceID = kompakt.AnnotationTraceID
)

// gateNames maps rule plugin names to scheduling gate names.
var gateNames = map[string]string{
	"WaitForWorkloadPacking": "kompakt.io/wait-for-workload-packing",
	"WaitForNodeReady":            "kompakt.io/wait-for-node-ready",
	"WaitForImagePrePull":       "kompakt.io/awaiting-image-prepull",
	"WaitForMIGProfile":         "kompakt.io/awaiting-mig-reconfig",
	"WaitForCoLocation":         "kompakt.io/awaiting-colocation",
}

// PodGateInjector is a mutating admission webhook that intercepts pod creation
// and injects scheduling gates based on the packer.kompakt.io/packing-profile label.
type PodGateInjector struct {
	resolver *matcher.ProfileResolver
}

// NewPodGateInjector creates a new webhook handler.
func NewPodGateInjector(resolver *matcher.ProfileResolver) *PodGateInjector {
	return &PodGateInjector{resolver: resolver}
}

// Handle processes an admission request for a pod.
func (p *PodGateInjector) Handle(ctx context.Context, req admission.Request) admission.Response {
	start := time.Now()
	logger := log.FromContext(ctx).WithName("webhook")

	pod := &corev1.Pod{}
	if err := json.Unmarshal(req.Object.Raw, pod); err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("decode pod: %w", err))
	}

	// Check exclude label first
	if pod.Labels[labelExclude] == "true" {
		kompaktmetrics.WebhookRequestsTotal.WithLabelValues("passthrough").Inc()
		kompaktmetrics.WebhookRequestDuration.WithLabelValues("passthrough").Observe(time.Since(start).Seconds())
		return admission.Allowed("excluded from gating")
	}

	// Check for profile label
	profileName, ok := pod.Labels[labelProfile]
	if !ok {
		kompaktmetrics.WebhookRequestsTotal.WithLabelValues("passthrough").Inc()
		kompaktmetrics.WebhookRequestDuration.WithLabelValues("passthrough").Observe(time.Since(start).Seconds())
		return admission.Allowed("no packing profile label")
	}

	// Resolve profile
	profile, err := p.resolver.Resolve(ctx, profileName)
	if err != nil {
		kompaktmetrics.WebhookRequestsTotal.WithLabelValues("reject").Inc()
		kompaktmetrics.WebhookRequestDuration.WithLabelValues("reject").Observe(time.Since(start).Seconds())
		logger.Info("Pod rejected", "pod", pod.Name, "namespace", pod.Namespace, "profile", profileName)
		return admission.Denied(fmt.Sprintf("PackingProfile %q not found", profileName))
	}

	// Build scheduling gates from profile rules
	var gates []corev1.PodSchedulingGate
	for _, rule := range profile.Spec.Rules {
		gateName, exists := gateNames[rule.Name]
		if !exists {
			continue
		}
		gates = append(gates, corev1.PodSchedulingGate{Name: gateName})
	}

	if len(gates) == 0 {
		kompaktmetrics.WebhookRequestsTotal.WithLabelValues("passthrough").Inc()
		kompaktmetrics.WebhookRequestDuration.WithLabelValues("passthrough").Observe(time.Since(start).Seconds())
		return admission.Allowed("no gates to inject")
	}

	// Generate trace ID for end-to-end correlation
	traceID := uuid.New().String()[:8]

	// Inject trace ID annotation
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	pod.Annotations[annotationTraceID] = traceID

	// Inject scheduling gates
	pod.Spec.SchedulingGates = append(pod.Spec.SchedulingGates, gates...)
	patched, err := json.Marshal(pod)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, fmt.Errorf("marshal patched pod: %w", err))
	}

	kompaktmetrics.WebhookRequestsTotal.WithLabelValues("gate").Inc()
	kompaktmetrics.WebhookRequestDuration.WithLabelValues("gate").Observe(time.Since(start).Seconds())
	logger.Info("Pod gated", "pod", pod.Name, "namespace", pod.Namespace, "profile", profileName, "traceID", traceID, "gates", len(gates))

	return admission.PatchResponseFromRaw(req.Object.Raw, patched)
}
