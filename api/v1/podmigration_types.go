package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type PodMigrationPhase string

const (
	MigrationPhasePending       PodMigrationPhase = "Pending"
	MigrationPhaseCheckpointing PodMigrationPhase = "Checkpointing"
	MigrationPhaseRestoring     PodMigrationPhase = "Restoring"
	MigrationPhaseSucceeded     PodMigrationPhase = "Succeeded"
	MigrationPhaseFailed        PodMigrationPhase = "Failed"
)

type PodMigrationSpec struct {
	// The name of the existing source Pod in the same namespace
	SourcePodName string `json:"sourcePodName"`

	// The target node name where the new Pod should be restored
	TargetNode string `json:"targetNode"`
}

type PodMigrationStatus struct {
	Phase               PodMigrationPhase `json:"phase,omitempty"`
	Message             string            `json:"message,omitempty"`
	BoundCheckpointName string            `json:"boundCheckpointName,omitempty"`
	RestoredPodName     string            `json:"restoredPodName,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=pm
// +kubebuilder:subresource:status

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
