// Copyright 2025.
// SPDX‑License‑Identifier: Apache‑2.0

package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// -----------------------------------------------------------------------------
// Spec / Status
// -----------------------------------------------------------------------------

// PodMigrationPhase is a simple string enum.
type PodMigrationPhase string

const (
	MigrationPending       PodMigrationPhase = "Pending" // waiting for checkpoint
	MigrationCheckpointing PodMigrationPhase = "Checkpointing"
	MigrationRestoring     PodMigrationPhase = "Restoring"
	MigrationSucceeded     PodMigrationPhase = "Succeeded"
	MigrationFailed        PodMigrationPhase = "Failed"
)

type PodMigrationSpec struct {
	SourcePodName string `json:"sourcePodName"` // pod in the *same* namespace
	TargetNode    string `json:"targetNode"`    // nodeName (kubernetes.io/hostname)
}

type PodMigrationStatus struct {
	Phase       PodMigrationPhase `json:"phase,omitempty"`
	Message     string            `json:"message,omitempty"`
	Checkpoint  string            `json:"checkpoint,omitempty"`  // PodCheckpoint name
	RestoredPod string            `json:"restoredPod,omitempty"` // new Pod name
}

// -----------------------------------------------------------------------------
// CRD markers
// -----------------------------------------------------------------------------

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=pm

type PodMigration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PodMigrationSpec   `json:"spec,omitempty"`
	Status PodMigrationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type PodMigrationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PodMigration `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PodMigration{}, &PodMigrationList{})
}
