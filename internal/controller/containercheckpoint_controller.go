package controller

import (
	"context"
	"fmt"
	"time"

	checkpointv1 "github.com/zacchaeuschok/pod-checkpoint-controller/api/v1"

	corev1 "k8s.io/api/core/v1"
	apierr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const ccFinalizer = "checkpointing.zacchaeuschok.github.io/cc-finalizer"

type ContainerCheckpointReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *ContainerCheckpointReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	startTime := time.Now()
	log := ctrl.LoggerFrom(ctx)
	log.Info("Starting reconciliation of ContainerCheckpoint", 
		"namespace", req.Namespace, "name", req.Name)
	defer func() {
		log.Info("Completed reconciliation of ContainerCheckpoint", 
			"namespace", req.Namespace, "name", req.Name,
			"durationMs", time.Since(startTime).Milliseconds())
	}()

	// 1. Fetch the ContainerCheckpoint
	var cc checkpointv1.ContainerCheckpoint
	if err := r.Get(ctx, req.NamespacedName, &cc); err != nil {
		if apierr.IsNotFound(err) {
			log.Info("ContainerCheckpoint not found, it may have been deleted", 
				"namespace", req.Namespace, "name", req.Name)
		} else {
			log.Error(err, "Failed to fetch ContainerCheckpoint", 
				"namespace", req.Namespace, "name", req.Name)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("Found ContainerCheckpoint", 
		"namespace", cc.Namespace, 
		"name", cc.Name, 
		"readyToRestore", cc.Status.ReadyToRestore,
		"hasError", cc.Status.ErrorMessage != "",
		"boundContentName", cc.Status.BoundContainerCheckpointContentName)

	// 2. If deleting: delete content then remove finalizer
	if !cc.DeletionTimestamp.IsZero() && controllerutil.ContainsFinalizer(&cc, ccFinalizer) {
		log.Info("ContainerCheckpoint is being deleted, processing finalizer", 
			"deletionTimestamp", cc.DeletionTimestamp)
		
		if cc.Spec.DeletionPolicy == "Delete" &&
			cc.Status.BoundContainerCheckpointContentName != nil &&
			*cc.Status.BoundContainerCheckpointContentName != "" {

			contentName := *cc.Status.BoundContainerCheckpointContentName
			log.Info("Deleting associated ContainerCheckpointContent", "contentName", contentName)
			
			toDelete := &checkpointv1.ContainerCheckpointContent{
				ObjectMeta: metav1.ObjectMeta{Name: contentName},
			}
			if err := r.Delete(ctx, toDelete); err != nil && !apierr.IsNotFound(err) {
				log.Error(err, "Failed to delete ContainerCheckpointContent", "contentName", contentName)
				return ctrl.Result{}, err
			}
			log.Info("Successfully deleted ContainerCheckpointContent", "contentName", contentName)
		} else {
			log.Info("Not deleting content due to policy or missing content reference",
				"deletionPolicy", cc.Spec.DeletionPolicy,
				"hasBoundContent", cc.Status.BoundContainerCheckpointContentName != nil)
		}
		
		// remove finalizer with retry-on-conflict
		log.Info("Removing finalizer from ContainerCheckpoint")
		removeStartTime := time.Now()
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var latest checkpointv1.ContainerCheckpoint
			if err := r.Get(ctx, req.NamespacedName, &latest); err != nil {
				log.Error(err, "Failed to get latest ContainerCheckpoint for finalizer removal")
				return err
			}
			controllerutil.RemoveFinalizer(&latest, ccFinalizer)
			return r.Update(ctx, &latest)
		}); err != nil {
			log.Error(err, "Failed to remove finalizer from ContainerCheckpoint")
			return ctrl.Result{}, err
		}
		log.Info("Successfully removed finalizer from ContainerCheckpoint", 
			"durationMs", time.Since(removeStartTime).Milliseconds())
		return ctrl.Result{}, nil
	}

	// 3. Ensure finalizer is present
	if !controllerutil.ContainsFinalizer(&cc, ccFinalizer) {
		log.Info("Adding finalizer to ContainerCheckpoint")
		addStartTime := time.Now()
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var latest checkpointv1.ContainerCheckpoint
			if err := r.Get(ctx, req.NamespacedName, &latest); err != nil {
				log.Error(err, "Failed to get latest ContainerCheckpoint for adding finalizer")
				return err
			}
			controllerutil.AddFinalizer(&latest, ccFinalizer)
			return r.Update(ctx, &latest)
		}); err != nil {
			log.Error(err, "Failed to add finalizer to ContainerCheckpoint")
			return ctrl.Result{}, err
		}
		log.Info("Successfully added finalizer to ContainerCheckpoint", 
			"durationMs", time.Since(addStartTime).Milliseconds())
		return ctrl.Result{Requeue: true}, nil
	}

	// 4. Bind pre-existing content
	if ref := cc.Spec.Source.ContainerCheckpointContentName; ref != nil && *ref != "" {
		contentName := *ref
		log.Info("Found pre-existing ContainerCheckpointContent reference", "contentName", contentName)
		
		var existing checkpointv1.ContainerCheckpointContent
		if err := r.Get(ctx, client.ObjectKey{Name: contentName}, &existing); err != nil {
			log.Error(err, "Failed to get referenced ContainerCheckpointContent", "contentName", contentName)
			_ = r.updateStatus(ctx, &cc, func(c *checkpointv1.ContainerCheckpoint) {
				c.Status.ErrorMessage = fmt.Sprintf("content reference not found: %v", err)
				c.Status.ReadyToRestore = false
			})
			return ctrl.Result{}, err
		}
		
		log.Info("Successfully found referenced ContainerCheckpointContent", 
			"contentName", contentName,
			"contentReadyToRestore", existing.Status.ReadyToRestore,
			"contentHasError", existing.Status.ErrorMessage != "")
		
		updateStartTime := time.Now()
		_ = r.updateStatus(ctx, &cc, func(c *checkpointv1.ContainerCheckpoint) {
			c.Status.BoundContainerCheckpointContentName = &contentName
			c.Status.ErrorMessage = existing.Status.ErrorMessage
			c.Status.ReadyToRestore = existing.Status.ReadyToRestore
			c.Status.CheckpointTime = existing.Status.CheckpointTime
		})
		log.Info("Successfully bound to pre-existing ContainerCheckpointContent", 
			"contentName", contentName,
			"durationMs", time.Since(updateStartTime).Milliseconds())
		return ctrl.Result{}, nil
	}

	// 5. Validate pod and container
	log.Info("Validating pod and container references")
	if cc.Spec.Source.PodName == nil || cc.Spec.Source.ContainerName == nil {
		log.Error(nil, "Invalid container source specified",
			"podNameNil", cc.Spec.Source.PodName == nil,
			"containerNameNil", cc.Spec.Source.ContainerName == nil)
		_ = r.updateStatus(ctx, &cc, func(c *checkpointv1.ContainerCheckpoint) {
			c.Status.ErrorMessage = "no valid container source specified"
			c.Status.ReadyToRestore = false
		})
		return ctrl.Result{}, nil
	}
	
	podName := *cc.Spec.Source.PodName
	containerName := *cc.Spec.Source.ContainerName
	log.Info("Looking up pod", "namespace", cc.Namespace, "podName", podName)
	
	var pod corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{Namespace: cc.Namespace, Name: podName}, &pod); err != nil {
		log.Error(err, "Failed to find pod", "namespace", cc.Namespace, "podName", podName)
		_ = r.updateStatus(ctx, &cc, func(c *checkpointv1.ContainerCheckpoint) {
			c.Status.ErrorMessage = fmt.Sprintf("pod lookup failed: %v", err)
			c.Status.ReadyToRestore = false
		})
		return ctrl.Result{}, err
	}
	log.Info("Successfully found pod", "podName", podName, "podNodeName", pod.Spec.NodeName)
	
	found := false
	for _, ctr := range pod.Spec.Containers {
		if ctr.Name == containerName {
			found = true
			break
		}
	}
	if !found {
		log.Error(nil, "Container not found in pod", 
			"podName", podName, 
			"containerName", containerName)
		_ = r.updateStatus(ctx, &cc, func(c *checkpointv1.ContainerCheckpoint) {
			c.Status.ErrorMessage = "container not found in pod"
			c.Status.ReadyToRestore = false
		})
		return ctrl.Result{}, nil
	}
	log.Info("Successfully validated container exists in pod", 
		"podName", podName, 
		"containerName", containerName)

	// 6. Skip if already bound
	if cc.Status.BoundContainerCheckpointContentName != nil {
		log.Info("Content already created; skipping", 
			"contentName", *cc.Status.BoundContainerCheckpointContentName)
		return ctrl.Result{}, nil
	}

	// 7. Get or create cluster-scoped ContainerCheckpointContent
	// Using just the ContainerCheckpoint name for the content to avoid redundancy
	// since the ContainerCheckpoint name likely already includes the container name
	contentName := cc.Name
	log.Info("Checking if ContainerCheckpointContent already exists", "contentName", contentName)
	
	var content checkpointv1.ContainerCheckpointContent
	if err := r.Get(ctx, client.ObjectKey{Name: contentName}, &content); apierr.IsNotFound(err) {
		log.Info("ContainerCheckpointContent not found, creating new", "contentName", contentName)
		
		storageLocation := "local"
		if t, ok := cc.Annotations["checkpointing.zacchaeuschok.github.io/targetNode"]; ok && t != "" {
			storageLocation = "node://" + t
			log.Info("Using target node from annotation", 
				"targetNode", t,
				"storageLocation", storageLocation)
		}
		
		newContent := &checkpointv1.ContainerCheckpointContent{
			ObjectMeta: metav1.ObjectMeta{
				Name:   contentName,
				Labels: map[string]string{"checkpoint": cc.Name},
			},
			Spec: checkpointv1.ContainerCheckpointContentSpec{
				ContainerCheckpointRef: &checkpointv1.ContainerCheckpointRef{Name: cc.Name, Namespace: cc.Namespace},
				DeletionPolicy:         cc.Spec.DeletionPolicy,
				StorageLocation:        storageLocation,
			},
		}

		// We can't set the ContainerCheckpoint as the owner because ContainerCheckpointContent is cluster-scoped
		// and ContainerCheckpoint is namespace-scoped. Instead, we use a back-reference in the spec.

		createStartTime := time.Now()
		if err := r.Create(ctx, newContent); err != nil && !apierr.IsAlreadyExists(err) {
			log.Error(err, "Failed to create ContainerCheckpointContent", "contentName", contentName)
			return ctrl.Result{}, err
		}
		log.Info("Successfully created ContainerCheckpointContent", 
			"contentName", contentName,
			"durationMs", time.Since(createStartTime).Milliseconds())
		
		// requeue to allow content controller to set .Status.ReadyToRestore
		log.Info("Requeuing to wait for ContainerCheckpointContent to be processed", 
			"contentName", contentName,
			"requeueAfter", "5s")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	} else if err != nil {
		log.Error(err, "Error checking for existing ContainerCheckpointContent", "contentName", contentName)
		return ctrl.Result{}, err
	} else {
		log.Info("Found existing ContainerCheckpointContent", 
			"contentName", contentName,
			"readyToRestore", content.Status.ReadyToRestore,
			"hasError", content.Status.ErrorMessage != "")
	}

	// 8. Mirror status
	if !content.Status.ReadyToRestore {
		log.Info("ContainerCheckpointContent not ready to restore, requeuing",
			"contentName", contentName,
			"contentErrorMessage", content.Status.ErrorMessage)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	
	log.Info("ContainerCheckpointContent is ready, updating ContainerCheckpoint status",
		"contentName", contentName)
	
	updateStartTime := time.Now()
	if err := r.updateStatus(ctx, &cc, func(c *checkpointv1.ContainerCheckpoint) {
		c.Status.BoundContainerCheckpointContentName = &contentName
		c.Status.ErrorMessage = content.Status.ErrorMessage
		c.Status.ReadyToRestore = content.Status.ReadyToRestore
		c.Status.CheckpointTime = content.Status.CheckpointTime
	}); err != nil {
		log.Error(err, "Failed to update ContainerCheckpoint status",
			"contentName", contentName)
		return ctrl.Result{}, err
	}
	log.Info("Successfully updated ContainerCheckpoint status",
		"contentName", contentName,
		"durationMs", time.Since(updateStartTime).Milliseconds())

	log.Info("Reconcile complete", "checkpoint", cc.Name)
	return ctrl.Result{}, nil
}

func (r *ContainerCheckpointReconciler) updateStatus(
	ctx context.Context,
	cc *checkpointv1.ContainerCheckpoint,
	mutate func(*checkpointv1.ContainerCheckpoint),
) error {
	log := ctrl.LoggerFrom(ctx).WithValues(
		"namespace", cc.Namespace, 
		"name", cc.Name,
		"operation", "updateStatus")
	
	startTime := time.Now()
	log.Info("Updating ContainerCheckpoint status")
	
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest checkpointv1.ContainerCheckpoint
		if err := r.Get(ctx, client.ObjectKeyFromObject(cc), &latest); err != nil {
			log.Error(err, "Failed to get latest ContainerCheckpoint for status update")
			return err
		}
		
		// Apply the status changes
		mutate(&latest)
		
		// Update the status
		if err := r.Status().Update(ctx, &latest); err != nil {
			log.Error(err, "Failed to update ContainerCheckpoint status")
			return err
		}
		return nil
	})
	
	if err != nil {
		log.Error(err, "Failed to update ContainerCheckpoint status after retries")
	} else {
		log.Info("Successfully updated ContainerCheckpoint status", 
			"durationMs", time.Since(startTime).Milliseconds())
	}
	
	return err
}

func (r *ContainerCheckpointReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&checkpointv1.ContainerCheckpoint{}).
		Complete(r)
}
