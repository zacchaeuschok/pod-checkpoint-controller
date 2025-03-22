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
	checkpointv1 "github.com/example/external-checkpointer/api/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const finalizerName = "containercheckpoints.checkpointing.zacchaeuschok.io/finalizer"

// ContainerCheckpointReconciler reconciles a ContainerCheckpoint object.
type ContainerCheckpointReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=checkpointing.zacchaeuschok.github.io,resources=containercheckpoints,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=checkpointing.zacchaeuschok.github.io,resources=containercheckpoints/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=checkpointing.zacchaeuschok.github.io,resources=containercheckpoints/finalizers,verbs=update
//
// +kubebuilder:rbac:groups=checkpointing.zacchaeuschok.github.io,resources=containercheckpointcontents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=checkpointing.zacchaeuschok.github.io,resources=containercheckpointcontents/status,verbs=get;update;patch
//
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch

func (r *ContainerCheckpointReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.Log.WithValues("ContainerCheckpoint", req.NamespacedName)

	// 1. Fetch the ContainerCheckpoint instance
	var checkpoint checkpointv1.ContainerCheckpoint
	if err := r.Get(ctx, req.NamespacedName, &checkpoint); err != nil {
		if errors.IsNotFound(err) {
			log.Info("ContainerCheckpoint not found; likely deleted.")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get ContainerCheckpoint")
		return ctrl.Result{}, err
	}

	// 2. Handle Deletion / Finalizer
	if !checkpoint.ObjectMeta.DeletionTimestamp.IsZero() {
		// Check if finalizer is present
		if controllerutil.ContainsFinalizer(&checkpoint, finalizerName) {
			// Attempt to clean up ContainerCheckpointContent if needed
			// e.g., only delete if the user sets DeletionPolicy="Delete".
			// In this example, we always try deleting the matching content.
			contentName := r.getContentName(&checkpoint)
			var content checkpointv1.ContainerCheckpointContent
			if err := r.Get(ctx, client.ObjectKey{Name: contentName}, &content); err == nil {
				// If found, we can check DeletionPolicy or just remove it
				// content.Spec.DeletionPolicy could be used. For now, we do unconditional removal:
				if errDel := r.Delete(ctx, &content); errDel != nil {
					log.Error(errDel, "Failed to delete ContainerCheckpointContent", "name", contentName)
					return ctrl.Result{}, errDel
				}
				log.Info("Deleted ContainerCheckpointContent", "name", contentName)
			} else if !errors.IsNotFound(err) {
				log.Error(err, "Failed to get ContainerCheckpointContent for finalizer cleanup")
				return ctrl.Result{}, err
			}

			// Remove finalizer to allow checkpoint deletion
			controllerutil.RemoveFinalizer(&checkpoint, finalizerName)
			if err := r.Update(ctx, &checkpoint); err != nil {
				return ctrl.Result{}, err
			}
		}
		// If no finalizer, nothing else to do
		return ctrl.Result{}, nil
	}

	// Ensure finalizer is set if we want to handle cleanup
	if !controllerutil.ContainsFinalizer(&checkpoint, finalizerName) {
		controllerutil.AddFinalizer(&checkpoint, finalizerName)
		if err := r.Update(ctx, &checkpoint); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 3. Check if the Pod/Container exist.
	// This is optional if you just want to create the Content and let a sidecar do further checks.
	var pod v1.Pod
	err := r.Get(ctx, client.ObjectKey{Name: checkpoint.Spec.Source.PodName, Namespace: checkpoint.Namespace}, &pod)
	if err != nil {
		checkpoint.Status.ErrorMessage = fmt.Sprintf("Failed to fetch pod: %v", err)
		_ = r.Status().Update(ctx, &checkpoint)
		return ctrl.Result{}, err
	}

	// Check if container is in the Pod's spec (basic validation).
	foundContainer := false
	for _, c := range pod.Spec.Containers {
		if c.Name == checkpoint.Spec.Source.ContainerName {
			foundContainer = true
			break
		}
	}
	if !foundContainer {
		msg := fmt.Sprintf("Container %q not found in Pod %q", checkpoint.Spec.Source.ContainerName, checkpoint.Spec.Source.PodName)
		log.Error(nil, msg)
		checkpoint.Status.ErrorMessage = msg
		_ = r.Status().Update(ctx, &checkpoint)
		return ctrl.Result{}, nil
	}

	// 4. Attempt to find or create ContainerCheckpointContent (cluster-scoped)
	contentName := r.getContentName(&checkpoint)
	var existingContent checkpointv1.ContainerCheckpointContent
	if err := r.Get(ctx, client.ObjectKey{Name: contentName}, &existingContent); err != nil {
		if errors.IsNotFound(err) {
			log.Info("No ContainerCheckpointContent found, creating one", "contentName", contentName)
			// Create the new Content
			newContent := checkpointv1.ContainerCheckpointContent{
				ObjectMeta: metav1.ObjectMeta{
					Name: contentName,
				},
				Spec: checkpointv1.ContainerCheckpointContentSpec{
					ContainerCheckpointRef: checkpointv1.ContainerCheckpointRef{
						Name:      checkpoint.Name,
						Namespace: checkpoint.Namespace, // this is just a reference, not the object's namespace
					},
					StorageLocation:    checkpoint.Spec.StorageLocation,
					DeletionPolicy:     "Retain",
					RetainAfterRestore: checkpoint.Spec.RetainAfterRestore,
				},
				Status: checkpointv1.ContainerCheckpointContentStatus{
					ReadyToRestore: false,
				},
			}

			// Create the new Content
			if createErr := r.Create(ctx, &newContent); createErr != nil {
				log.Error(createErr, "Failed to create ContainerCheckpointContent")
				checkpoint.Status.ErrorMessage = fmt.Sprintf("Failed to create content: %v", createErr)
				_ = r.Status().Update(ctx, &checkpoint)
				return ctrl.Result{}, createErr
			}
			log.Info("Created ContainerCheckpointContent", "name", contentName)
		} else {
			// Some other error
			log.Error(err, "Failed to get ContainerCheckpointContent")
			checkpoint.Status.ErrorMessage = fmt.Sprintf("Error fetching content: %v", err)
			_ = r.Status().Update(ctx, &checkpoint)
			return ctrl.Result{}, err
		}
	} else {
		// If it exists, optionally update certain fields
		changed := false
		if existingContent.Spec.StorageLocation != checkpoint.Spec.StorageLocation {
			existingContent.Spec.StorageLocation = checkpoint.Spec.StorageLocation
			changed = true
		}
		if existingContent.Spec.RetainAfterRestore != checkpoint.Spec.RetainAfterRestore {
			existingContent.Spec.RetainAfterRestore = checkpoint.Spec.RetainAfterRestore
			changed = true
		}
		// Additional logic as needed, e.g. if referencing a ContainerCheckpointClass

		if changed {
			if updateErr := r.Update(ctx, &existingContent); updateErr != nil {
				log.Error(updateErr, "Failed to update ContainerCheckpointContent", "name", contentName)
				checkpoint.Status.ErrorMessage = fmt.Sprintf("Failed to update content: %v", updateErr)
				_ = r.Status().Update(ctx, &checkpoint)
				return ctrl.Result{}, updateErr
			}
			log.Info("Updated ContainerCheckpointContent", "name", contentName)
		}
	}

	// 5. If ContainerCheckpointContent is ready, mark ContainerCheckpoint as Ready
	// The sidecar will set existingContent.Status.ReadyToRestore=true once the checkpoint is actually done.
	if existingContent.Status.ReadyToRestore {
		checkpoint.Status.ReadyToRestore = true
		checkpoint.Status.ErrorMessage = ""
		// We can also set a time if the sidecar reports it,
		// but the content status currently lacks a checkpointTime.
		// If you add "CheckpointTime *metav1.Time" in content status, you can copy it here.
	} else {
		// Not ready yet; maybe sidecar is still working
		checkpoint.Status.ReadyToRestore = false
	}

	if err := r.Status().Update(ctx, &checkpoint); err != nil {
		log.Error(err, "Failed to update ContainerCheckpoint status")
		return ctrl.Result{}, err
	}

	log.Info("Reconcile complete for ContainerCheckpoint", "name", checkpoint.Name)
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ContainerCheckpointReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&checkpointv1.ContainerCheckpoint{}).
		// Optionally watch ContainerCheckpointContent changes to trigger a reconcile
		Owns(&checkpointv1.ContainerCheckpointContent{}).
		Complete(r)
}

// Helper function to generate a cluster-unique name for ContainerCheckpointContent
func (r *ContainerCheckpointReconciler) getContentName(checkpoint *checkpointv1.ContainerCheckpoint) string {
	// Example: <CheckpointName>-<ContainerName>
	return fmt.Sprintf("%s-%s", checkpoint.Name, checkpoint.Spec.Source.ContainerName)
}
