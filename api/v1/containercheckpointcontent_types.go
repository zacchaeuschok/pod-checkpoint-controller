// Copyright 2025.
// SPDX‑License‑Identifier: Apache‑2.0

package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type ContainerCheckpointContentSpec struct {
	ContainerCheckpointRef ContainerCheckpointRef `json:"containerCheckpointRef"`
	StorageLocation        string                 `json:"storageLocation,omitempty"`
	DeletionPolicy         string                 `json:"deletionPolicy"` // Retain | Delete
	RetainAfterRestore     bool                   `json:"retainAfterRestore"`
	BaseImage              string                 `json:"baseImage,omitempty"`
}

type ContainerCheckpointRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type ContainerCheckpointContentStatus struct {
	ReadyToRestore bool   `json:"readyToRestore"`
	ErrorMessage   string `json:"errorMessage,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

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
