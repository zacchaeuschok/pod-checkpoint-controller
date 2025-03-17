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

package controller

import (
	"context"
	"fmt"
	checkpointingv1alpha1 "github.com/example/external-checkpointer/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ContainerCheckpointReconciler reconciles a ContainerCheckpoint object
type ContainerCheckpointReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=checkpointing.zacchaeuschok.github.io,resources=containercheckpoints,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=checkpointing.zacchaeuschok.github.io,resources=containercheckpoints/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=checkpointing.zacchaeuschok.github.io,resources=containercheckpoints/finalizers,verbs=update
// +kubebuilder:rbac:groups=checkpointing.zacchaeuschok.github.io,resources=containercheckpointcontents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=checkpointing.zacchaeuschok.github.io,resources=containercheckpointcontents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;patch;update

func (r *ContainerCheckpointReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.Log.WithValues("containercheckpoint", req.NamespacedName)

	// Fetch the ContainerCheckpoint instance
	var checkpoint checkpointingv1alpha1.ContainerCheckpoint
	if err := r.Get(ctx, req.NamespacedName, &checkpoint); err != nil {
		if errors.IsNotFound(err) {
			log.Info("ContainerCheckpoint resource not found. Ignoring since object must be deleted.")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get ContainerCheckpoint")
		return ctrl.Result{}, err
	}

	// Fetch the target Pod
	var pod v1.Pod
	err := r.Get(ctx, client.ObjectKey{Name: checkpoint.Spec.PodName, Namespace: checkpoint.Spec.Namespace}, &pod)
	if err != nil {
		log.Error(err, "Failed to get Pod")
		return ctrl.Result{}, err
	}

	// Assign the NodeName if not already set
	if checkpoint.Spec.NodeName == "" {
		checkpoint.Spec.NodeName = pod.Spec.NodeName
		err := r.Update(ctx, &checkpoint)
		if err != nil {
			log.Error(err, "Failed to update ContainerCheckpoint")
			return ctrl.Result{}, err
		}
	}

	// Check if CheckpointContent already exists
	var existingContent checkpointingv1alpha1.ContainerCheckpointContent
	err = r.Get(ctx, client.ObjectKey{Name: checkpoint.Name, Namespace: checkpoint.Namespace}, &existingContent)
	if err == nil {
		log.Info("ContainerCheckpointContent already exists, skipping creation")
		return ctrl.Result{}, nil
	} else if !errors.IsNotFound(err) {
		log.Error(err, "Failed to get ContainerCheckpointContent")
		return ctrl.Result{}, err
	}

	// Construct the checkpoint file path
	checkpointPath := fmt.Sprintf("/var/lib/kubelet/checkpoints/%s_%s-%s.tar", checkpoint.Spec.PodName, checkpoint.Spec.Namespace, checkpoint.Spec.Container)

	// Create the CheckpointContent resource
	checkpointContent := &checkpointingv1alpha1.ContainerCheckpointContent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      checkpoint.Name,
			Namespace: checkpoint.Namespace,
		},
		Spec: checkpointingv1alpha1.ContainerCheckpointContentSpec{
			ContainerCheckpointRef: checkpoint.Name,
			Namespace:              checkpoint.Spec.Namespace,
			PodName:                checkpoint.Spec.PodName,
			ContainerName:          checkpoint.Spec.Container,
			NodeName:               checkpoint.Spec.NodeName,
			CheckpointPath:         checkpointPath,
			CreationTimestamp:      metav1.Now(),
		},
	}

	if err := r.Create(ctx, checkpointContent); err != nil {
		log.Error(err, "Failed to create ContainerCheckpointContent")
		return ctrl.Result{}, err
	}

	// Update the status of the ContainerCheckpoint
	checkpoint.Status.State = "Completed"
	checkpoint.Status.Message = "Checkpointing completed successfully"
	if err := r.Status().Update(ctx, &checkpoint); err != nil {
		log.Error(err, "Failed to update ContainerCheckpoint status")
		return ctrl.Result{}, err
	}

	log.Info("Successfully created CheckpointContent", "CheckpointContent", checkpointContent.Name)
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ContainerCheckpointReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&checkpointingv1alpha1.ContainerCheckpoint{}).
		Named("containercheckpoint").
		Complete(r)
}
