package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ContainerCheckpointContentSpec describes the actual underlying data for
// a single container checkpoint.
type ContainerCheckpointContentSpec struct {
	// ContainerCheckpointRef points back to the namespaced ContainerCheckpoint
	// this content is bound to, if any. For dynamic creation, the controller
	// sets this field. For a pre-existing checkpoint, the user sets it.
	// +optional
	ContainerCheckpointRef *ContainerCheckpointRef `json:"containerCheckpointRef,omitempty"`

	// DeletionPolicy is "Retain" or "Delete". If "Delete", the checkpoint data
	// should be garbage-collected when the parent ContainerCheckpoint is removed.
	// +optional
	DeletionPolicy string `json:"deletionPolicy,omitempty"`

	// StorageLocation is a path or URI describing where the container checkpoint data lives.
	// +optional
	StorageLocation string `json:"storageLocation,omitempty"`
}

// ContainerCheckpointRef references the ContainerCheckpoint that binds to this content.
type ContainerCheckpointRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type ContainerCheckpointContentStatus struct {
	// ReadyToRestore signals if the container checkpoint data is fully available.
	// +optional
	ReadyToRestore bool `json:"readyToRestore,omitempty"`

	// ErrorMessage holds any errors from the underlying checkpoint creation or fetch process.
	// +optional
	ErrorMessage string `json:"errorMessage,omitempty"`

	// CheckpointTime is when the container data was captured, if known.
	// +optional
	CheckpointTime *metav1.Time `json:"checkpointTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=ccc
// +kubebuilder:subresource:status

type ContainerCheckpointContent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ContainerCheckpointContentSpec   `json:"spec,omitempty"`
	Status ContainerCheckpointContentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type ContainerCheckpointContentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ContainerCheckpointContent `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ContainerCheckpointContent{}, &ContainerCheckpointContentList{})
}
