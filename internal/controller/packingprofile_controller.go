package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	annotationReason   = "kompakt.io/gate-reason"
	annotationNode     = "kompakt.io/target-node"
	annotationHeldBy   = "kompakt.io/held-by"
	gatePrefix         = "kompakt.io/"
)

// PackingProfileReconciler reconciles gated pods by evaluating rules
// against the node ledger and releasing gates when capacity is available.
type PackingProfileReconciler struct {
	client.Client
	APIReader       client.Reader
	Ledger          *ledger.NodeLedger
	Detectors       []inflight.Detector
	Recorder        record.EventRecorder
	activeDetectors []string // populated per reconcile by syncLedger
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
			return ctrl.Result{}, r.releaseGates(ctx, pod, "profile_not_found")
		}
		return ctrl.Result{}, err
	}

	var ledgerSyncErr error
	defer func() {
		setLedgerReadyCondition(profile, ledgerSyncErr)
		if err := r.updateProfileStatus(ctx, profile); err != nil {
			logger.Error(err, "failed to update profile status")
		}
	}()

	// 5. Warn on misconfigured profile
	warnProfileMisconfig(logger, profile)

	// 6. Sync ledger from cluster state
	ledgerSyncErr = r.syncLedger(ctx, logger, profile)
	if ledgerSyncErr != nil {
		logger.Error(ledgerSyncErr, "ledger sync failed, retrying")
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	// 7. Check priority override
	if pod.Annotations[annotationPriority] == "high" {
		r.recordRelease(pod, profileName, "priority", "")
		logger.Info("Gate released", "reason", "priority")
		return ctrl.Result{}, r.releaseGates(ctx, pod, "priority")
	}

	// 8. Check reservation timeout
	if isTimedOut(pod, profile, logger) {
		r.recordRelease(pod, profileName, "timeout", "")
		logger.Info("Gate released", "reason", "timeout")
		return ctrl.Result{}, r.releaseGates(ctx, pod, "timeout")
	}

	// 9. Run rules
	allRelease := true
	var targetNode string
	var holdingRule string
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
			r.recordHold(pod, profileName, ruleRef.Name)
			holdingRule = ruleRef.Name
			allRelease = false
			break
		}
		if nodeName != "" {
			targetNode = nodeName
		}
	}

	if !allRelease {
		if err := r.annotatePod(ctx, pod, annotationHeldBy, holdingRule); err != nil {
			logger.V(1).Info("failed to annotate held-by", "error", err)
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	// 10. Release gates with optional node affinity
	r.recordRelease(pod, profileName, "capacity", targetNode)
	logger.Info("Gate released", "reason", "capacity", "node", targetNode)
	if err := r.releaseGatesWithAffinity(ctx, pod, targetNode, "capacity"); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// recordRelease records metrics and emits a Kubernetes Event for a gate release.
func (r *PackingProfileReconciler) recordRelease(pod *corev1.Pod, profileName, reason, targetNode string) {
	kompaktmetrics.GateReleasesTotal.WithLabelValues(profileName, reason).Inc()
	kompaktmetrics.GatedPods.WithLabelValues(pod.Namespace, profileName).Dec()
	if !pod.CreationTimestamp.IsZero() {
		kompaktmetrics.GateHoldDuration.WithLabelValues(profileName, reason).Observe(time.Since(pod.CreationTimestamp.Time).Seconds())
	}

	msg := fmt.Sprintf("gate released, reason=%s, profile=%s", reason, profileName)
	if targetNode != "" {
		msg += fmt.Sprintf(", targetNode=%s", targetNode)
	}
	r.Recorder.Event(pod, corev1.EventTypeNormal, "GateReleased", msg)
}

// recordHold emits a Kubernetes Event when a rule holds the gate.
func (r *PackingProfileReconciler) recordHold(pod *corev1.Pod, profileName, ruleName string) {
	r.Recorder.Eventf(pod, corev1.EventTypeNormal, "GateHeld",
		"gate held by rule %s, profile=%s", ruleName, profileName)
}

// SetupWithManager registers the reconciler with the manager.
// WARNING: Do not set MaxConcurrentReconciles > 1. The ledger is rebuilt
// each reconcile cycle and concurrent reconciles would interleave
// destructively (syncLedger + FindFit + Reserve is not atomic).
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
	profile.Status.InflightNodes = int32(r.Ledger.Snapshot().InflightCount)
	profile.Status.ActiveDetectors = r.activeDetectors

	// Set conditions
	setProfileValidCondition(profile)
	setInflightDetectionCondition(profile, r.Ledger.Snapshot().InflightCount, r.activeDetectors, r.Detectors)
	setReadyCondition(profile)

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

func (r *PackingProfileReconciler) releaseGates(ctx context.Context, pod *corev1.Pod, reason string) error {
	return r.releaseGatesWithAffinity(ctx, pod, "", reason)
}

func (r *PackingProfileReconciler) releaseGatesWithAffinity(ctx context.Context, pod *corev1.Pod, nodeName, reason string) error {
	patch := client.MergeFrom(pod.DeepCopy())

	// Annotate pod with release reason and target node
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	pod.Annotations[annotationReason] = reason
	if nodeName != "" {
		pod.Annotations[annotationNode] = nodeName
	}
	delete(pod.Annotations, annotationHeldBy)

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
		hostnameReq := corev1.NodeSelectorRequirement{
			Key:      "kubernetes.io/hostname",
			Operator: corev1.NodeSelectorOpIn,
			Values:   []string{nodeName},
		}

		if pod.Spec.Affinity == nil {
			pod.Spec.Affinity = &corev1.Affinity{}
		}
		if pod.Spec.Affinity.NodeAffinity == nil {
			pod.Spec.Affinity.NodeAffinity = &corev1.NodeAffinity{}
		}

		existing := pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		if existing != nil && len(existing.NodeSelectorTerms) > 0 {
			// Merge: add hostname match to each existing term (AND logic)
			for i := range existing.NodeSelectorTerms {
				existing.NodeSelectorTerms[i].MatchExpressions = append(
					existing.NodeSelectorTerms[i].MatchExpressions, hostnameReq)
			}
		} else {
			// No existing affinity: create new
			pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{MatchExpressions: []corev1.NodeSelectorRequirement{hostnameReq}},
				},
			}
		}
	}

	return r.Patch(ctx, pod, patch)
}

func (r *PackingProfileReconciler) annotatePod(ctx context.Context, pod *corev1.Pod, key, value string) error {
	if pod.Annotations != nil && pod.Annotations[key] == value {
		return nil // already set, skip patch
	}
	patch := client.MergeFrom(pod.DeepCopy())
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	pod.Annotations[key] = value
	return r.Patch(ctx, pod, patch)
}

func (r *PackingProfileReconciler) syncLedger(ctx context.Context, logger logr.Logger, profile *v1alpha1.PackingProfile) error {
	// Snapshot reservations before rebuild so they survive across cycles
	reservations := r.Ledger.SnapshotReservations()
	r.Ledger.ClearNodes()
	r.Ledger.ClearInflightByPrefix(profile.Name + "/")

	// Sync existing nodes
	nodeList := &corev1.NodeList{}
	if err := r.List(ctx, nodeList); err != nil {
		return err
	}
	for i := range nodeList.Items {
		node := &nodeList.Items[i]
		// Skip nodes being deleted (scale-down race guard)
		if node.DeletionTimestamp != nil {
			continue
		}
		alloc := make(map[string]int64)
		for res, qty := range node.Status.Allocatable {
			alloc[string(res)] = qty.MilliValue()
		}
		// Resource readiness: skip nodes missing demanded resources
		// (device plugin not yet registered). Only for ResourceRequest type.
		if profile.Spec.DemandSource.Type == "ResourceRequest" {
			missingResource := false
			for _, res := range profile.Spec.DemandSource.Resources {
				// Standard resources (cpu, memory) are always present
				if res == "cpu" || res == "memory" {
					continue
				}
				if _, exists := alloc[res]; !exists {
					missingResource = true
					break
				}
			}
			if missingResource {
				continue
			}
		}

		r.Ledger.AddNode(node.Name, alloc, node.Labels, node.Spec.Taints)

		// Transition tracking: if this node was previously inflight
		// (GOATScaler provision-task-id links event name to real node)
		if taskID := node.Labels["goatscaler.io/provision-task-id"]; taskID != "" {
			r.Ledger.RemoveInflightNode(profile.Name + "/" + taskID)
		}
	}

	// Sync pod usage per node
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList); err != nil {
		return err
	}
	usage := make(map[string]map[string]int64)
	for i := range podList.Items {
		p := &podList.Items[i]
		if p.Spec.NodeName == "" {
			continue
		}
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
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

	// Priority chain: try Layer 1 detectors (autoscaler-aware) first.
	// If any Layer 1 detector finds nodes, use it and skip the rest.
	// If all Layer 1 detectors return empty, fall back to Layer 2 (not-ready-nodes).
	r.activeDetectors = nil
	var inflightNodes []inflight.InflightNode
	var activeDetector string

	for _, d := range r.Detectors {
		nodes, err := d.Detect(ctx, reader)
		if err != nil {
			logger.V(1).Info("Inflight detection failed", "detector", d.Name(), "error", err)
			continue
		}
		kompaktmetrics.LedgerInflightNodes.WithLabelValues(d.Name()).Set(float64(len(nodes)))
		if len(nodes) > 0 {
			inflightNodes = nodes
			activeDetector = d.Name()
			break
		}
	}

	if activeDetector != "" {
		r.activeDetectors = []string{activeDetector}
	}

	// Check for NoStock condition from GOATScaler
	for _, d := range r.Detectors {
		if gs, ok := d.(*inflight.GOATScalerDetector); ok && gs.NoStockDetected {
			logger.Info("ECS stock shortage detected, some nodepools have no available capacity")
			break
		}
	}

	for _, n := range inflightNodes {
		alloc := n.Allocatable
		nodeLabels := n.Labels
		var nodeTaints []corev1.Taint

		tmpl := findMatchingTemplate(n, profile.Spec.CapacitySource.NodeGroupTemplates)
		if tmpl != nil {
			if len(alloc) == 0 {
				alloc = copyTemplateAllocatable(tmpl.Allocatable)
			}
			// Enrich inflight node with template labels and taints
			if len(nodeLabels) == 0 {
				nodeLabels = tmpl.Labels
			}
			nodeTaints = templateTaintsToCoreTaints(tmpl.Taints)
			logger.V(1).Info("inflight node enriched from template", "node", n.Name,
				"detector", activeDetector, "instanceType", n.InstanceType)
		} else if len(alloc) == 0 {
			logger.Info("inflight node has no matching template, capacity unknown",
				"node", n.Name, "detector", activeDetector,
				"configuredTemplates", templateIdentifiers(profile.Spec.CapacitySource.NodeGroupTemplates))
		}

		inflightKey := profile.Name + "/" + n.Name
		r.Ledger.AddInflightNode(inflightKey, alloc, nodeLabels, nodeTaints)
	}

	// Warn about slow provisions
	for _, n := range inflightNodes {
		if !n.DetectedAt.IsZero() && time.Since(n.DetectedAt) > 5*time.Minute {
			logger.Info("inflight node provisioning is taking longer than expected",
				"node", n.Name, "age", time.Since(n.DetectedAt).Round(time.Second).String(),
				"detector", activeDetector)
		}
	}

	// Restore reservations from previous cycle onto rebuilt entries
	r.Ledger.RestoreReservations(reservations)

	return nil
}

const (
	condReady                   = "Ready"
	condProfileValid            = "ProfileValid"
	condLedgerReady             = "LedgerReady"
	condInflightDetectionActive = "InflightDetectionActive"
)

func setCondition(profile *v1alpha1.PackingProfile, condType string, status metav1.ConditionStatus, reason, message string) {
	now := metav1.Now()
	for i, c := range profile.Status.Conditions {
		if c.Type == condType {
			if c.Status != status || c.Reason != reason {
				profile.Status.Conditions[i].Status = status
				profile.Status.Conditions[i].Reason = reason
				profile.Status.Conditions[i].Message = message
				profile.Status.Conditions[i].LastTransitionTime = now
			}
			return
		}
	}
	profile.Status.Conditions = append(profile.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
	})
}

func setProfileValidCondition(profile *v1alpha1.PackingProfile) {
	var issues []string

	ds := profile.Spec.DemandSource
	if ds.Type == "ResourceRequest" && len(ds.Resources) == 0 {
		issues = append(issues, "demandSource.type=ResourceRequest but resources is empty")
	}
	if ds.Type == "Annotation" && ds.Annotation == "" {
		issues = append(issues, "demandSource.type=Annotation but annotation is empty")
	}

	cs := profile.Spec.CapacitySource
	if cs.Type == "NodeLabel" && cs.Label == "" {
		issues = append(issues, "capacitySource.type=NodeLabel but label is empty")
	}

	hasScaleUp := false
	for _, r := range profile.Spec.Rules {
		if r.Name == "WaitForScaleUp" {
			hasScaleUp = true
			break
		}
	}
	if hasScaleUp && len(cs.NodeGroupTemplates) == 0 {
		issues = append(issues, "WaitForScaleUp rule requires nodeGroupTemplates")
	}

	if len(issues) > 0 {
		setCondition(profile, condProfileValid, metav1.ConditionFalse, "ConfigurationError", strings.Join(issues, "; "))
	} else {
		setCondition(profile, condProfileValid, metav1.ConditionTrue, "Valid", "profile configuration is valid")
	}
}

func setLedgerReadyCondition(profile *v1alpha1.PackingProfile, syncErr error) {
	if syncErr != nil {
		setCondition(profile, condLedgerReady, metav1.ConditionFalse, "SyncFailed", syncErr.Error())
	} else {
		setCondition(profile, condLedgerReady, metav1.ConditionTrue, "Synced", "ledger synced from cluster state")
	}
}

func setInflightDetectionCondition(profile *v1alpha1.PackingProfile, inflightCount int, activeDetectors []string, allDetectors []inflight.Detector) {
	if len(allDetectors) == 0 {
		setCondition(profile, condInflightDetectionActive, metav1.ConditionFalse, "NoDetectors", "no inflight detectors configured")
		return
	}
	if inflightCount > 0 {
		setCondition(profile, condInflightDetectionActive, metav1.ConditionTrue, "Detected",
			fmt.Sprintf("%d inflight node(s) detected by %s", inflightCount, strings.Join(activeDetectors, ", ")))
	} else {
		setCondition(profile, condInflightDetectionActive, metav1.ConditionFalse, "NoneDetected", "no inflight nodes detected")
	}
}

func setReadyCondition(profile *v1alpha1.PackingProfile) {
	profileValid := conditionIsTrue(profile, condProfileValid)
	ledgerReady := conditionIsTrue(profile, condLedgerReady)

	if profileValid && ledgerReady {
		setCondition(profile, condReady, metav1.ConditionTrue, "Ready", "profile is valid and ledger is synced")
		return
	}

	var reasons []string
	if !profileValid {
		reasons = append(reasons, "ProfileValid=False")
	}
	if !ledgerReady {
		reasons = append(reasons, "LedgerReady=False")
	}
	setCondition(profile, condReady, metav1.ConditionFalse, "NotReady", strings.Join(reasons, ", "))
}

func conditionIsTrue(profile *v1alpha1.PackingProfile, condType string) bool {
	for _, c := range profile.Status.Conditions {
		if c.Type == condType {
			return c.Status == metav1.ConditionTrue
		}
	}
	return false
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

func templateIdentifiers(templates []v1alpha1.NodeGroupTemplate) []string {
	ids := make([]string, len(templates))
	for i, t := range templates {
		switch {
		case t.InstanceType != "":
			ids[i] = "instanceType=" + t.InstanceType
		case t.NamePrefix != "":
			ids[i] = "namePrefix=" + t.NamePrefix
		default:
			ids[i] = "<empty>"
		}
	}
	return ids
}

// findMatchingTemplate finds the first NodeGroupTemplate matching the inflight node.
// Match priority: instanceType first, then namePrefix fallback.
// Returns nil if no template matches.
func findMatchingTemplate(node inflight.InflightNode, templates []v1alpha1.NodeGroupTemplate) *v1alpha1.NodeGroupTemplate {
	if node.InstanceType != "" {
		for i := range templates {
			if templates[i].InstanceType != "" && templates[i].InstanceType == node.InstanceType {
				return &templates[i]
			}
		}
	}
	for i := range templates {
		if templates[i].NamePrefix != "" && strings.HasPrefix(node.Name, templates[i].NamePrefix) {
			return &templates[i]
		}
	}
	return nil
}

func templateTaintsToCoreTaints(taints []v1alpha1.NodeGroupTaint) []corev1.Taint {
	if len(taints) == 0 {
		return nil
	}
	out := make([]corev1.Taint, len(taints))
	for i, t := range taints {
		out[i] = corev1.Taint{
			Key:    t.Key,
			Value:  t.Value,
			Effect: corev1.TaintEffect(t.Effect),
		}
	}
	return out
}

func copyTemplateAllocatable(m map[string]int64) map[string]int64 {
	alloc := make(map[string]int64, len(m))
	for k, v := range m {
		alloc[k] = v
	}
	return alloc
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
