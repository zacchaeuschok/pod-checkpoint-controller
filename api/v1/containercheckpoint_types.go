// Copyright 2025.
// SPDX‑License‑Identifier: Apache‑2.0

package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// -----------------------------------------------------------------------------
// Spec / Status
// -----------------------------------------------------------------------------

type ContainerCheckpointSpec struct {
	Source             Source `json:"source"`                    // pod + container to checkpoint
	StorageLocation    string `json:"storageLocation,omitempty"` // local path or remote URI
	RetainAfterRestore bool   `json:"retainAfterRestore,omitempty"`
}

type Source struct {
	PodName       string `json:"podName"`
	ContainerName string `json:"containerName"`
}

type ContainerCheckpointStatus struct {
	CheckpointTime *metav1.Time `json:"checkpointTime,omitempty"`
	ReadyToRestore bool         `json:"readyToRestore,omitempty"`
	ErrorMessage   string       `json:"errorMessage,omitempty"`
}

// -----------------------------------------------------------------------------
// CRD markers
// -----------------------------------------------------------------------------

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

type ContainerCheckpoint struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ContainerCheckpointSpec   `json:"spec,omitempty"`
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
