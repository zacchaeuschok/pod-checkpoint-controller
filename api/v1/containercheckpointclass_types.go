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

// ContainerCheckpointClassSpec defines the desired state of ContainerCheckpointClass.
type ContainerCheckpointClassSpec struct {
	Runtime    string            `json:"runtime"`
	Parameters map[string]string `json:"parameters,omitempty"`
}

// ContainerCheckpointClassStatus defines the observed state of ContainerCheckpointClass.
type ContainerCheckpointClassStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ContainerCheckpointClass is the Schema for the containercheckpointclasses API.
type ContainerCheckpointClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ContainerCheckpointClassSpec `json:"spec,omitempty"`
}

// +kubebuilder:resource:scope=Cluster
// +kubebuilder:storageversion
// +kubebuilder:object:root=true

// ContainerCheckpointClassList contains a list of ContainerCheckpointClass.
type ContainerCheckpointClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ContainerCheckpointClass `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ContainerCheckpointClass{}, &ContainerCheckpointClassList{})
}
