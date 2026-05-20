package controller

import (
	"context"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	v1alpha1 "github.com/reyshazni/kompakt/api/v1alpha1"
	"github.com/reyshazni/kompakt/internal/inflight"
	"github.com/reyshazni/kompakt/internal/ledger"
	kompaktmetrics "github.com/reyshazni/kompakt/internal/metrics"
	"github.com/reyshazni/kompakt/internal/rules"
)

const (
	labelProfile       = "packer.kompakt.io/packing-profile"
	annotationPriority = "kompakt.io/priority"
	annotationTraceID  = "kompakt.io/trace-id"
	gatePrefix         = "kompakt.io/"
)

// PackingProfileReconciler reconciles gated pods by evaluating rules
// against the node ledger and releasing gates when capacity is available.
type PackingProfileReconciler struct {
	client.Client
	APIReader client.Reader
	Ledger    *ledger.NodeLedger
	Detectors []inflight.Detector
	Recorder  record.EventRecorder
}

// Reconcile handles a single gated pod.
func (r *PackingProfileReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// 1. Get the pod
	pod := &corev1.Pod{}
	if err := r.Get(ctx, req.NamespacedName, pod); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. Check if pod has kompakt gates
	if !hasKompaktGates(pod) {
		return ctrl.Result{}, nil
	}

	// 3. Get profile name from label
	profileName, ok := pod.Labels[labelProfile]
	if !ok {
		return ctrl.Result{}, nil
	}

	// Enrich logger with trace context for all downstream log lines
	traceID := pod.Annotations[annotationTraceID]
	logger = logger.WithValues("traceID", traceID, "profile", profileName, "podUID", pod.UID)

	// 4. Get PackingProfile
	profile := &v1alpha1.PackingProfile{}
	if err := r.Get(ctx, client.ObjectKey{Name: profileName}, profile); err != nil {
		if errors.IsNotFound(err) {
			r.recordRelease(pod, profileName, "profile_not_found", "")
			logger.Info("Gate released", "reason", "profile_not_found")
			return ctrl.Result{}, r.releaseGates(ctx, pod)
		}
		return ctrl.Result{}, err
	}

	defer func() {
		if err := r.updateProfileStatus(ctx, profile); err != nil {
			logger.Error(err, "failed to update profile status")
		}
	}()

	// 5. Warn on misconfigured profile
	warnProfileMisconfig(logger, profile)

	// 6. Sync ledger from cluster state
	if err := r.syncLedger(ctx, logger, profile); err != nil {
		logger.Error(err, "ledger sync failed, retrying")
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	// 7. Check priority override
	if pod.Annotations[annotationPriority] == "high" {
		r.recordRelease(pod, profileName, "priority", "")
		logger.Info("Gate released", "reason", "priority")
		return ctrl.Result{}, r.releaseGates(ctx, pod)
	}

	// 8. Check reservation timeout
	if isTimedOut(pod, profile, logger) {
		r.recordRelease(pod, profileName, "timeout", "")
		logger.Info("Gate released", "reason", "timeout")
		return ctrl.Result{}, r.releaseGates(ctx, pod)
	}

	// 9. Run rules
	allRelease := true
	var targetNode string
	for _, ruleRef := range profile.Spec.Rules {
		rule, exists := rules.Registry[ruleRef.Name]
		if !exists {
			logger.Info("unknown rule skipped, check profile spec", "rule", ruleRef.Name)
			continue
		}
		evalStart := time.Now()
		release, nodeName, err := rule.Evaluate(ctx, pod, r.Ledger, profile)
		kompaktmetrics.RuleEvaluationDuration.WithLabelValues(ruleRef.Name).Observe(time.Since(evalStart).Seconds())
		if err != nil {
			kompaktmetrics.RuleEvaluationsTotal.WithLabelValues(ruleRef.Name, "error").Inc()
			logger.Error(err, "rule evaluation failed", "rule", ruleRef.Name)
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		if release {
			kompaktmetrics.RuleEvaluationsTotal.WithLabelValues(ruleRef.Name, "release").Inc()
			logger.V(1).Info("rule released gate", "rule", ruleRef.Name, "node", nodeName)
		} else {
			kompaktmetrics.RuleEvaluationsTotal.WithLabelValues(ruleRef.Name, "hold").Inc()
			logger.Info("rule holding gate", "rule", ruleRef.Name)
			allRelease = false
			break
		}
		if nodeName != "" {
			targetNode = nodeName
		}
	}

	if !allRelease {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	// 10. Release gates with optional node affinity
	r.recordRelease(pod, profileName, "capacity", targetNode)
	logger.Info("Gate released", "reason", "capacity", "node", targetNode)
	if err := r.releaseGatesWithAffinity(ctx, pod, targetNode); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// recordRelease records metrics for a gate release event.
func (r *PackingProfileReconciler) recordRelease(pod *corev1.Pod, profileName, reason, _ string) {
	kompaktmetrics.GateReleasesTotal.WithLabelValues(profileName, reason).Inc()
	kompaktmetrics.GatedPods.WithLabelValues(pod.Namespace, profileName).Dec()
	if !pod.CreationTimestamp.IsZero() {
		kompaktmetrics.GateHoldDuration.WithLabelValues(profileName, reason).Observe(time.Since(pod.CreationTimestamp.Time).Seconds())
	}
}

// SetupWithManager registers the reconciler with the manager.
func (r *PackingProfileReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		WithEventFilter(predicate.NewPredicateFuncs(func(obj client.Object) bool {
			pod, ok := obj.(*corev1.Pod)
			if !ok {
				return false
			}
			return hasKompaktGates(pod)
		})).
		Complete(r)
}

func (r *PackingProfileReconciler) updateProfileStatus(ctx context.Context, profile *v1alpha1.PackingProfile) error {
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList, client.MatchingLabels{labelProfile: profile.Name}); err != nil {
		return err
	}

	var activeGates int32
	for i := range podList.Items {
		if hasKompaktGates(&podList.Items[i]) {
			activeGates++
		}
	}

	profile.Status.ActiveGates = activeGates
	profile.Status.ActiveReservations = activeGates
	return r.Status().Update(ctx, profile)
}

func hasKompaktGates(pod *corev1.Pod) bool {
	for _, gate := range pod.Spec.SchedulingGates {
		if strings.HasPrefix(gate.Name, gatePrefix) {
			return true
		}
	}
	return false
}

func (r *PackingProfileReconciler) releaseGates(ctx context.Context, pod *corev1.Pod) error {
	return r.releaseGatesWithAffinity(ctx, pod, "")
}

func (r *PackingProfileReconciler) releaseGatesWithAffinity(ctx context.Context, pod *corev1.Pod, nodeName string) error {
	patch := client.MergeFrom(pod.DeepCopy())

	// Remove all kompakt gates
	var remaining []corev1.PodSchedulingGate
	for _, gate := range pod.Spec.SchedulingGates {
		if !strings.HasPrefix(gate.Name, gatePrefix) {
			remaining = append(remaining, gate)
		}
	}
	pod.Spec.SchedulingGates = remaining

	// Add node affinity if we have a target
	if nodeName != "" {
		if pod.Spec.Affinity == nil {
			pod.Spec.Affinity = &corev1.Affinity{}
		}
		if pod.Spec.Affinity.NodeAffinity == nil {
			pod.Spec.Affinity.NodeAffinity = &corev1.NodeAffinity{}
		}
		pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{
					MatchExpressions: []corev1.NodeSelectorRequirement{
						{
							Key:      "kubernetes.io/hostname",
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{nodeName},
						},
					},
				},
			},
		}
	}

	return r.Patch(ctx, pod, patch)
}

func (r *PackingProfileReconciler) syncLedger(ctx context.Context, logger logr.Logger, profile *v1alpha1.PackingProfile) error {
	// Sync existing nodes
	nodeList := &corev1.NodeList{}
	if err := r.List(ctx, nodeList); err != nil {
		return err
	}
	for i := range nodeList.Items {
		node := &nodeList.Items[i]
		alloc := make(map[string]int64)
		for res, qty := range node.Status.Allocatable {
			alloc[string(res)] = qty.MilliValue()
		}
		r.Ledger.AddNode(node.Name, alloc)
	}

	// Sync pod usage per node
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList); err != nil {
		return err
	}
	usage := make(map[string]map[string]int64)
	for i := range podList.Items {
		p := &podList.Items[i]
		if p.Spec.NodeName == "" || p.Status.Phase != corev1.PodRunning {
			continue
		}
		if usage[p.Spec.NodeName] == nil {
			usage[p.Spec.NodeName] = make(map[string]int64)
		}
		for _, c := range p.Spec.Containers {
			for res, qty := range c.Resources.Requests {
				usage[p.Spec.NodeName][string(res)] += qty.MilliValue()
			}
		}
	}
	for nodeName, used := range usage {
		r.Ledger.UpdateUsage(nodeName, used)
	}

	// Update ledger metrics
	snapshot := r.Ledger.Snapshot()
	kompaktmetrics.LedgerNodes.Set(float64(snapshot.NodeCount))
	kompaktmetrics.LedgerAllocatableMillicores.Set(float64(snapshot.TotalAllocatable["cpu"]))
	kompaktmetrics.LedgerAllocatableMemoryBytes.Set(float64(snapshot.TotalAllocatable["memory"]))

	// Sync inflight nodes from detectors using a direct (uncached) reader
	// to avoid triggering cluster-scoped list/watch on restricted resources.
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	for _, d := range r.Detectors {
		nodes, err := d.Detect(ctx, reader)
		if err != nil {
			logger.V(1).Info("Inflight detection failed", "detector", d.Name(), "error", err)
			continue
		}
		for _, n := range nodes {
			alloc := n.Allocatable
			if len(alloc) == 0 {
				alloc = matchNodeGroupTemplate(n.Name, profile.Spec.CapacitySource.NodeGroupTemplates)
				if alloc != nil {
					logger.V(1).Info("inflight node enriched from template", "node", n.Name, "allocatable", alloc)
				} else {
					logger.Info("inflight node has no matching template, capacity unknown",
						"node", n.Name, "configuredPrefixes", templatePrefixes(profile.Spec.CapacitySource.NodeGroupTemplates))
				}
			}
			r.Ledger.AddInflightNode(n.Name, alloc)
		}
		kompaktmetrics.LedgerInflightNodes.WithLabelValues(d.Name()).Set(float64(len(nodes)))
	}

	return nil
}

// warnProfileMisconfig logs warnings for common profile misconfiguration.
// These are not errors (the profile is valid per the CRD schema) but indicate
// likely user mistakes that will cause unexpected behavior.
func warnProfileMisconfig(logger logr.Logger, profile *v1alpha1.PackingProfile) {
	ds := profile.Spec.DemandSource
	if ds.Type == "ResourceRequest" && len(ds.Resources) == 0 {
		logger.Info("demandSource.type=ResourceRequest but resources list is empty, all pods will have zero demand")
	}
	if ds.Type == "Annotation" && ds.Annotation == "" {
		logger.Info("demandSource.type=Annotation but annotation field is empty, all pods will have zero demand")
	}
	cs := profile.Spec.CapacitySource
	if cs.Type == "NodeLabel" && cs.Label == "" {
		logger.Info("capacitySource.type=NodeLabel but label field is empty")
	}

	hasScaleUpRule := false
	for _, r := range profile.Spec.Rules {
		if r.Name == "WaitForScaleUp" {
			hasScaleUpRule = true
			break
		}
	}
	if hasScaleUpRule && len(cs.NodeGroupTemplates) == 0 {
		logger.Info("WaitForScaleUp rule configured but no nodeGroupTemplates defined, inflight nodes will have unknown capacity")
	}
}

func templatePrefixes(templates []v1alpha1.NodeGroupTemplate) []string {
	prefixes := make([]string, len(templates))
	for i, t := range templates {
		prefixes[i] = t.NamePrefix
	}
	return prefixes
}

// matchNodeGroupTemplate finds the first NodeGroupTemplate whose NamePrefix
// matches the inflight node name and returns a copy of its allocatable map.
// Returns nil if no template matches.
func matchNodeGroupTemplate(nodeName string, templates []v1alpha1.NodeGroupTemplate) map[string]int64 {
	for _, t := range templates {
		if strings.HasPrefix(nodeName, t.NamePrefix) {
			alloc := make(map[string]int64, len(t.Allocatable))
			for k, v := range t.Allocatable {
				alloc[k] = v
			}
			return alloc
		}
	}
	return nil
}

func isTimedOut(pod *corev1.Pod, profile *v1alpha1.PackingProfile, logger logr.Logger) bool {
	if pod.CreationTimestamp.IsZero() {
		return false
	}
	timeout, err := time.ParseDuration(profile.Spec.ReservationTimeout)
	if err != nil {
		logger.Info("invalid reservationTimeout, using default 3m",
			"value", profile.Spec.ReservationTimeout, "error", err)
		timeout = 3 * time.Minute
	}
	return time.Since(pod.CreationTimestamp.Time) > timeout
}
