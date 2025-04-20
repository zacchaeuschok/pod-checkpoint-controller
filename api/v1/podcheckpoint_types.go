// Copyright 2025.
// SPDX‑License‑Identifier: Apache‑2.0

package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type PodCheckpointSpec struct {
	PodName            string `json:"podName"`
	StorageLocation    string `json:"storageLocation,omitempty"`
	RetainAfterRestore bool   `json:"retainAfterRestore,omitempty"`
}

type PodCheckpointStatus struct {
	CheckpointTime           *metav1.Time `json:"checkpointTime,omitempty"`
	ReadyToRestore           bool         `json:"readyToRestore,omitempty"`
	ErrorMessage             string       `json:"errorMessage,omitempty"`
	PodCheckpointContentName string       `json:"podCheckpointContentName,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

type PodCheckpoint struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PodCheckpointSpec   `json:"spec,omitempty"`
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
