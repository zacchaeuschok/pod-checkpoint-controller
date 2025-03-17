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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ContainerCheckpointContentSpec defines the desired state of ContainerCheckpointContent.
type ContainerCheckpointContentSpec struct {
	ContainerCheckpointRef string      `json:"containerCheckpointRef"`
	Namespace              string      `json:"namespace"`
	PodName                string      `json:"podName"`
	ContainerName          string      `json:"containerName"`
	NodeName               string      `json:"nodeName"`
	CheckpointPath         string      `json:"checkpointPath"`
	CreationTimestamp      metav1.Time `json:"creationTimestamp"`
}

// ContainerCheckpointContentStatus defines the observed state of ContainerCheckpointContent.
type ContainerCheckpointContentStatus struct {
	Ready   *bool  `json:"ready,omitempty"`
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=ccc,singular=containercheckpointcontent
// +kubebuilder:subresource:status

// ContainerCheckpointContent is the Schema for the containercheckpointcontents API.
type ContainerCheckpointContent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ContainerCheckpointContentSpec   `json:"spec,omitempty"`
	Status ContainerCheckpointContentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ContainerCheckpointContentList contains a list of ContainerCheckpointContent.
type ContainerCheckpointContentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ContainerCheckpointContent `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ContainerCheckpointContent{}, &ContainerCheckpointContentList{})
}
