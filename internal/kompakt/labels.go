package kompakt

// Shared label and annotation keys used by webhook and controller.
const (
	LabelProfile       = "packer.kompakt.io/packing-profile"
	LabelExclude       = "kompakt.io/exclude"
	AnnotationPriority = "kompakt.io/priority"
	AnnotationTraceID  = "kompakt.io/trace-id"
	AnnotationReason   = "kompakt.io/gate-reason"
	AnnotationNode     = "kompakt.io/target-node"
	AnnotationHeldBy   = "kompakt.io/held-by"
	GatePrefix         = "kompakt.io/"
)
