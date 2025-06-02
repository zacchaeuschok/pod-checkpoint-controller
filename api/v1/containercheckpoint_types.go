package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ContainerCheckpointSpec describes how a container checkpoint is created or
// bound to a pre-existing ContainerCheckpointContent.
type ContainerCheckpointSpec struct {
	// ContainerCheckpointSource holds info about how to create or bind the checkpoint.
	// Exactly one of ContainerName or ContainerCheckpointContentName must be set.
	Source ContainerCheckpointSource `json:"source"`

	// DeletionPolicy determines whether ContainerCheckpointContent should be
	// deleted when this ContainerCheckpoint is deleted. Allowed values: "Delete" or "Retain".
	// Defaults to "Delete" if not set.
	// +optional
	DeletionPolicy string `json:"deletionPolicy,omitempty"`
}

// ContainerCheckpointSource specifies whether to dynamically create a container
// checkpoint (from a Pod/Container) or reference an existing checkpoint content.
type ContainerCheckpointSource struct {
	// PodName is the name of the Pod that contains the container to be checkpointed.
	// This Pod must be in the same namespace as the ContainerCheckpoint.
	// +optional
	PodName *string `json:"podName,omitempty"`

	// ContainerName is the name of the container in the Pod that will be checkpointed.
	// +optional
	ContainerName *string `json:"containerName,omitempty"`

	// ContainerCheckpointContentName references a pre-existing cluster-scoped
	// ContainerCheckpointContent object. If set, no new container checkpoint is created.
	// +optional
	ContainerCheckpointContentName *string `json:"containerCheckpointContentName,omitempty"`
}

type ContainerCheckpointStatus struct {
	// BoundContainerCheckpointContentName is the name of the ContainerCheckpointContent
	// to which this ContainerCheckpoint is bound. If empty, binding isn’t complete yet.
	// +optional
	BoundContainerCheckpointContentName *string `json:"boundContainerCheckpointContentName,omitempty"`

	// Indicates that the checkpoint data is valid and can be restored.
	// +optional
	ReadyToRestore bool `json:"readyToRestore,omitempty"`

	// ErrorMessage holds the last observed error when creating/updating the checkpoint.
	// +optional
	ErrorMessage string `json:"errorMessage,omitempty"`

	// The time when the container’s state was captured, if known.
	// +optional
	CheckpointTime *metav1.Time `json:"checkpointTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=cc
// +kubebuilder:subresource:status

type ContainerCheckpoint struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ContainerCheckpointSpec   `json:"spec"`
	Status ContainerCheckpointStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type ContainerCheckpointList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ContainerCheckpoint `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ContainerCheckpoint{}, &ContainerCheckpointList{})
}
