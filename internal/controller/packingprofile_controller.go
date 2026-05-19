package controller

import (
	"context"
	"strings"
	"time"

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
	"github.com/reyshazni/kompakt/internal/rules"
)

const (
	labelProfile       = "packer.kompakt.io/packing-profile"
	annotationPriority = "kompakt.io/priority"
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

	// 4. Get PackingProfile
	profile := &v1alpha1.PackingProfile{}
	if err := r.Get(ctx, client.ObjectKey{Name: profileName}, profile); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("profile not found, releasing gates", "profile", profileName, "pod", pod.Name)
			return ctrl.Result{}, r.releaseGates(ctx, pod)
		}
		return ctrl.Result{}, err
	}

	// 5. Sync ledger from cluster state
	if err := r.syncLedger(ctx); err != nil {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	// 6. Check priority override
	if pod.Annotations[annotationPriority] == "high" {
		logger.Info("priority=high, releasing gates immediately", "pod", pod.Name)
		return ctrl.Result{}, r.releaseGates(ctx, pod)
	}

	// 7. Check reservation timeout
	if isTimedOut(pod, profile) {
		logger.Info("reservation timed out, releasing gates", "pod", pod.Name)
		return ctrl.Result{}, r.releaseGates(ctx, pod)
	}

	// 8. Run rules
	allRelease := true
	var targetNode string
	for _, ruleRef := range profile.Spec.Rules {
		rule, exists := rules.Registry[ruleRef.Name]
		if !exists {
			continue
		}
		release, nodeName, err := rule.Evaluate(ctx, pod, r.Ledger, profile)
		if err != nil {
			logger.Error(err, "rule evaluation failed", "rule", ruleRef.Name)
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		if !release {
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

	// 9. Release gates with optional node affinity
	logger.Info("capacity available, releasing gates", "pod", pod.Name, "node", targetNode)
	if err := r.releaseGatesWithAffinity(ctx, pod, targetNode); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
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
	if nodeName != "" && !strings.HasPrefix(nodeName, "inflight-") {
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

func (r *PackingProfileReconciler) syncLedger(ctx context.Context) error {
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

	// Sync inflight nodes from detectors using a direct (uncached) reader
	// to avoid triggering cluster-scoped list/watch on restricted resources.
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	for _, d := range r.Detectors {
		nodes, err := d.Detect(ctx, reader)
		if err != nil {
			continue
		}
		for _, n := range nodes {
			r.Ledger.AddInflightNode(n.Name, n.Allocatable)
		}
	}

	return nil
}

func isTimedOut(pod *corev1.Pod, profile *v1alpha1.PackingProfile) bool {
	if pod.CreationTimestamp.IsZero() {
		return false
	}
	timeout, err := time.ParseDuration(profile.Spec.ReservationTimeout)
	if err != nil {
		timeout = 3 * time.Minute
	}
	return time.Since(pod.CreationTimestamp.Time) > timeout
}
