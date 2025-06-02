package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PodCheckpointSpec describes how a Pod checkpoint is created or
// references a pre-existing PodCheckpointContent.
type PodCheckpointSpec struct {
	// PodCheckpointSource identifies either the namespaced Pod to capture or
	// a pre-existing PodCheckpointContent object.
	Source PodCheckpointSource `json:"source"`

	// DeletionPolicy is "Retain" or "Delete". If "Delete", the PodCheckpointContent
	// should be removed when this PodCheckpoint is deleted.
	// +optional
	DeletionPolicy string `json:"deletionPolicy,omitempty"`
}

// PodCheckpointSource needs exactly one of PodName or PodCheckpointContentName.
type PodCheckpointSource struct {
	// PodName references the name of a Pod in the same namespace to checkpoint.
	// +optional
	PodName *string `json:"podName,omitempty"`

	// PodCheckpointContentName references an existing cluster-scoped content object.
	// +optional
	PodCheckpointContentName *string `json:"podCheckpointContentName,omitempty"`
}

type PodCheckpointStatus struct {
	// BoundPodCheckpointContentName is the name of the PodCheckpointContent object
	// to which this PodCheckpoint is bound.
	// +optional
	BoundPodCheckpointContentName *string `json:"boundPodCheckpointContentName,omitempty"`

	// Time the pod-level checkpoint was completed
	// +optional
	CheckpointTime *metav1.Time `json:"checkpointTime,omitempty"`

	// Whether the Pod checkpoint is fully ready to restore
	// +optional
	ReadyToRestore bool `json:"readyToRestore,omitempty"`

	// Last error encountered, if any
	// +optional
	ErrorMessage string `json:"errorMessage,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=pc
// +kubebuilder:subresource:status

type PodCheckpoint struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PodCheckpointSpec   `json:"spec"`
	Status PodCheckpointStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type PodCheckpointList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PodCheckpoint `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PodCheckpoint{}, &PodCheckpointList{})
}
