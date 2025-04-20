// Copyright 2025.
// SPDX‑License‑Identifier: Apache‑2.0

package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type PodCheckpointContentSpec struct {
	PodUID               string                         `json:"podUID"`
	PodNamespace         string                         `json:"podNamespace"`
	PodName              string                         `json:"podName"`
	ContainerCheckpoints []ContainerCheckpointReference `json:"containerCheckpoints,omitempty"`
	StorageLocation      string                         `json:"storageLocation,omitempty"`
	DeletionPolicy       string                         `json:"deletionPolicy,omitempty"`
}

type ContainerCheckpointReference struct {
	ContainerName                  string `json:"containerName"`
	ContainerCheckpointName        string `json:"containerCheckpointName,omitempty"`
	ContainerCheckpointContentName string `json:"containerCheckpointContentName,omitempty"`
}

type PodCheckpointContentStatus struct {
	CheckpointTime *metav1.Time `json:"checkpointTime,omitempty"`
	ReadyToRestore bool         `json:"readyToRestore,omitempty"`
	ErrorMessage   string       `json:"errorMessage,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

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
