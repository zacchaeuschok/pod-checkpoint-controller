package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ContainerCheckpointSpec defines the desired state of ContainerCheckpoint.
type ContainerCheckpointSpec struct {
	PodName   string `json:"podName"`
	Namespace string `json:"namespace"`
	Container string `json:"container"`
	NodeName  string `json:"nodeName,omitempty"`
}

// ContainerCheckpointStatus defines the observed state of ContainerCheckpoint.
type ContainerCheckpointStatus struct {
	State   string `json:"state,omitempty"`
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=cc,singular=containercheckpoint
// +kubebuilder:subresource:status

// ContainerCheckpoint is the Schema for the containercheckpoints API.
type ContainerCheckpoint struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ContainerCheckpointSpec   `json:"spec,omitempty"`
	Status ContainerCheckpointStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ContainerCheckpointList contains a list of ContainerCheckpoint.
type ContainerCheckpointList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ContainerCheckpoint `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ContainerCheckpoint{}, &ContainerCheckpointList{})
}
