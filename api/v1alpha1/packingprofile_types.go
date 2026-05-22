package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PackingProfileSpec defines the coordination behavior for a class of workloads.
// Pods opt in to a profile by setting the label:
//
//	packer.kompakt.io/packing-profile: <profile-name>
//
// The webhook validates that the referenced profile exists at pod creation time.
// If the profile does not exist, the pod is rejected.
type PackingProfileSpec struct {
	// DemandSource defines how to extract resource demand from matched pods.
	DemandSource DemandSource `json:"demandSource"`

	// CapacitySource defines how to determine available and in-flight node capacity.
	CapacitySource CapacitySource `json:"capacitySource"`

	// ReadinessSignal defines when a node is considered ready to receive gated pods.
	ReadinessSignal ReadinessSignal `json:"readinessSignal"`

	// Rules is the ordered list of rule plugins to execute for matched pods.
	// +kubebuilder:validation:MinItems=1
	Rules []RuleRef `json:"rules"`

	// ReservationTimeout is the maximum duration a pod's capacity reservation
	// is held before the gate is released unconditionally.
	// +kubebuilder:default="3m"
	ReservationTimeout string `json:"reservationTimeout,omitempty"`
}

// DemandSource defines how Kompakt extracts resource demand from a pod.
type DemandSource struct {
	// Type is the demand extraction method.
	// +kubebuilder:validation:Enum=ResourceRequest;Annotation
	Type string `json:"type"`

	// Resources lists the resource names to extract from container requests.
	// Used when Type is ResourceRequest.
	// Deprecated: use AdditionalResources instead. When Resources is set,
	// it is used as-is for backward compatibility (cpu/memory NOT auto-added).
	Resources []string `json:"resources,omitempty"`

	// AdditionalResources lists extended resource names to track beyond cpu and memory.
	// CPU and memory are always tracked implicitly when this field is used.
	// Used when Type is ResourceRequest.
	AdditionalResources []string `json:"additionalResources,omitempty"`

	// Annotation is the annotation key holding the demand value.
	// Used when Type is Annotation.
	Annotation string `json:"annotation,omitempty"`

	// Unit is the unit of the annotation value (e.g., "MiB", "cores").
	// Used when Type is Annotation.
	Unit string `json:"unit,omitempty"`
}

// CapacitySource defines how Kompakt determines node capacity.
type CapacitySource struct {
	// Type is the capacity detection method.
	// +kubebuilder:validation:Enum=NodeAllocatable;NodeLabel
	Type string `json:"type"`

	// Resources lists the resource names to read from node allocatable.
	// Used when Type is NodeAllocatable.
	Resources []string `json:"resources,omitempty"`

	// Label is the node label key holding the total capacity value.
	// Used when Type is NodeLabel.
	Label string `json:"label,omitempty"`

	// PerDeviceCount specifies a node label whose value indicates the number
	// of devices on the node. Used for fractional GPU calculations.
	PerDeviceCount *LabelRef `json:"perDeviceCount,omitempty"`

	// NodeGroupTemplates maps node group name prefixes to expected allocatable
	// resources for in-flight nodes detected from that group. The controller
	// matches detected inflight node names against these prefixes and populates
	// their allocatable accordingly.
	NodeGroupTemplates []NodeGroupTemplate `json:"nodeGroupTemplates,omitempty"`
}

// NodeGroupTemplate declares expected allocatable resources for nodes in a
// specific node group. Used to populate capacity on in-flight nodes whose
// actual allocatable is unknown until they arrive.
type NodeGroupTemplate struct {
	// NamePrefix is the node group name prefix to match against inflight node names.
	// Used by ClusterAutoscaler detector (CA status ConfigMap names).
	NamePrefix string `json:"namePrefix,omitempty"`

	// InstanceType is the cloud instance type to match against inflight nodes.
	// Used by GOATScaler detector (event message) and NotReady detector (node label).
	InstanceType string `json:"instanceType,omitempty"`

	// Allocatable is the expected allocatable resources in millivalue.
	// Optional when using NotReady node detection (kubelet reports allocatable
	// before the node is Ready). Required when only Layer 1 detection is available
	// and capacity must be known before the Node object exists.
	Allocatable map[string]int64 `json:"allocatable,omitempty"`

	// Labels are the expected node labels for inflight nodes from this group.
	// Used for nodeSelector matching on inflight nodes whose labels are not
	// yet available from the K8s API (GOATScaler, CA detectors).
	Labels map[string]string `json:"labels,omitempty"`

	// Taints are the expected node taints for inflight nodes from this group.
	// Used for toleration matching. Format: "key=value:effect".
	Taints []NodeGroupTaint `json:"taints,omitempty"`
}

// NodeGroupTaint declares an expected taint on nodes in a node group.
type NodeGroupTaint struct {
	// Key is the taint key.
	Key string `json:"key"`
	// Value is the taint value.
	Value string `json:"value,omitempty"`
	// Effect is the taint effect (NoSchedule, NoExecute, PreferNoSchedule).
	Effect string `json:"effect"`
}

// LabelRef is a reference to a node label.
type LabelRef struct {
	// Label is the node label key.
	Label string `json:"label"`
}

// ReadinessSignal defines when a node is considered ready for gated pods.
type ReadinessSignal struct {
	// NodeConditions lists the conditions that must be true on the node.
	NodeConditions []NodeConditionRequirement `json:"nodeConditions,omitempty"`

	// RequiredLabels lists node labels that must be present.
	RequiredLabels []string `json:"requiredLabels,omitempty"`
}

// NodeConditionRequirement specifies a required node condition.
type NodeConditionRequirement struct {
	// Type is the node condition type (e.g., "Ready").
	Type string `json:"type"`

	// Status is the required condition status (e.g., "True").
	Status string `json:"status"`
}

// RuleRef references a rule plugin by name.
type RuleRef struct {
	// Name is the rule plugin name.
	// +kubebuilder:validation:Enum=BinPackOnInflightCapacity;WaitForScaleUp;WaitForImagePrePull;WaitForMIGProfile;WaitForCoLocation
	Name string `json:"name"`
}

// PackingProfileStatus defines the observed state of PackingProfile.
type PackingProfileStatus struct {
	// ActiveGates is the number of pods currently gated by this profile.
	ActiveGates int32 `json:"activeGates,omitempty"`

	// InflightNodes is the number of in-flight nodes detected for this profile.
	InflightNodes int32 `json:"inflightNodes,omitempty"`

	// ActiveDetectors lists the names of inflight detectors that found nodes
	// in the last reconcile cycle. Empty if no detectors found anything.
	ActiveDetectors []string `json:"activeDetectors,omitempty"`

	// Conditions represent the latest available observations of the profile state.
	// Known condition types: Ready, ProfileValid, LedgerReady, InflightDetectionActive.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=pp
// +kubebuilder:printcolumn:name="Demand",type=string,JSONPath=`.spec.demandSource.type`
// +kubebuilder:printcolumn:name="Rules",type=string,JSONPath=`.spec.rules[*].name`
// +kubebuilder:printcolumn:name="Gates",type=integer,JSONPath=`.status.activeGates`
// +kubebuilder:printcolumn:name="Inflight",type=integer,JSONPath=`.status.inflightNodes`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PackingProfile is the Schema for the packingprofiles API.
type PackingProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PackingProfileSpec   `json:"spec,omitempty"`
	Status PackingProfileStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PackingProfileList contains a list of PackingProfile.
type PackingProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PackingProfile `json:"items"`
}
