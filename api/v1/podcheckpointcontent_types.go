package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// PodCheckpointContentSpec holds the actual data for a Pod-level checkpoint.
type PodCheckpointContentSpec struct {
	// PodCheckpointRef references the namespaced PodCheckpoint this content binds to.
	// For dynamic creation, the controller sets it. For pre-existing data, user sets it.
	// +optional
	PodCheckpointRef *PodCheckpointRef `json:"podCheckpointRef,omitempty"`

	// DeletionPolicy is "Retain" or "Delete".
	// +optional
	DeletionPolicy string `json:"deletionPolicy,omitempty"`

	// If you want to track the actual Pod name/UID, set them here.
	// +optional
	PodUID       string `json:"podUID,omitempty"`
	PodNamespace string `json:"podNamespace,omitempty"`
	PodName      string `json:"podName,omitempty"`

	// A location (filesystem path, object storage URL, etc.) where the Pod checkpoint data is stored.
	// +optional
	StorageLocation string `json:"storageLocation,omitempty"`

	// ContainerCheckpoints references each container's checkpoint data, if desired.
	// +optional
	ContainerCheckpoints []ContainerCheckpointReference `json:"containerCheckpoints,omitempty"`
}

// PodCheckpointRef references the parent PodCheckpoint that binds to this content.
type PodCheckpointRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// ContainerCheckpointReference can store the name of each container’s
// ContainerCheckpointContent if you want aggregated tracking at the Pod level.
type ContainerCheckpointReference struct {
	// The container name inside the Pod
	ContainerName string `json:"containerName"`

	// The container checkpoint content name if you want to link them.
	// +optional
	ContainerCheckpointContentName string `json:"containerCheckpointContentName,omitempty"`
}

type PodCheckpointContentStatus struct {
	// When the Pod-level checkpoint was created
	// +optional
	CheckpointTime *metav1.Time `json:"checkpointTime,omitempty"`

	// True if the Pod checkpoint data is fully restorable
	// +optional
	ReadyToRestore bool `json:"readyToRestore,omitempty"`

	// Last error encountered
	// +optional
	ErrorMessage string `json:"errorMessage,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=pcc
// +kubebuilder:subresource:status

type PodCheckpointContent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PodCheckpointContentSpec   `json:"spec,omitempty"`
	Status PodCheckpointContentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type PodCheckpointContentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PodCheckpointContent `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PodCheckpointContent{}, &PodCheckpointContentList{})
}
