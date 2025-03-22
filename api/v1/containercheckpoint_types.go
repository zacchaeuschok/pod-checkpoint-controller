/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ContainerCheckpointSpec defines the desired state of ContainerCheckpoint.
type ContainerCheckpointSpec struct {
	ContainerCheckpointClassName string `json:"containerCheckpointClassName,omitempty"`
	Source                       Source `json:"source"`
	StorageLocation              string `json:"storageLocation,omitempty"`
	RetainAfterRestore           bool   `json:"retainAfterRestore,omitempty"`
}

type Source struct {
	PodName       string `json:"podName"`
	ContainerName string `json:"containerName"`
}

// ContainerCheckpointStatus defines the observed state of ContainerCheckpoint.
type ContainerCheckpointStatus struct {
	CheckpointTime *metav1.Time `json:"checkpointTime,omitempty"`
	ReadyToRestore bool         `json:"readyToRestore,omitempty"`
	ErrorMessage   string       `json:"errorMessage,omitempty"`
}

// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:storageversion
// +kubebuilder:object:root=true
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
