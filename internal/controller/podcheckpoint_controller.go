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
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	pcFinalizer   = "checkpointing.zacchaeuschok.github.io/pc-finalizer"
	requeuePeriod = 8 * time.Second

	deletePolicy = "Delete"
	retainPolicy = "Retain"
)

type PodCheckpointReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *PodCheckpointReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	startTime := time.Now()
	logger := log.FromContext(ctx).WithValues("controller", "PodCheckpoint")
	logger.Info("Starting reconciliation of PodCheckpoint", 
		"namespace", req.Namespace, 
		"name", req.Name)
	
	defer func() {
		logger.Info("Completed reconciliation of PodCheckpoint", 
			"namespace", req.Namespace, 
			"name", req.Name, 
			"durationMs", time.Since(startTime).Milliseconds())
	}()

	// 1. Fetch PodCheckpoint
	var pc checkpointv1.PodCheckpoint
	logger.Info("Fetching PodCheckpoint resource", 
		"namespace", req.Namespace, 
		"name", req.Name)
	
	if err := r.Get(ctx, req.NamespacedName, &pc); err != nil {
		if apierr.IsNotFound(err) {
			logger.Info("PodCheckpoint not found, it may have been deleted", 
				"namespace", req.Namespace, 
				"name", req.Name)
		} else {
			logger.Error(err, "Failed to fetch PodCheckpoint", 
				"namespace", req.Namespace, 
				"name", req.Name)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	logger.Info("Successfully fetched PodCheckpoint", 
		"namespace", pc.Namespace, 
		"name", pc.Name, 
		"readyToRestore", pc.Status.ReadyToRestore,
		"hasError", pc.Status.ErrorMessage != "",
		"boundContentName", pc.Status.BoundPodCheckpointContentName)

	// 2. Deletion & finalizer
	if !pc.DeletionTimestamp.IsZero() && controllerutil.ContainsFinalizer(&pc, pcFinalizer) {
		logger.Info("PodCheckpoint is being deleted, processing finalizer", 
			"namespace", pc.Namespace,
			"name", pc.Name,
			"deletionTimestamp", pc.DeletionTimestamp)
		
		if pc.Spec.DeletionPolicy == deletePolicy &&
			pc.Status.BoundPodCheckpointContentName != nil && 
			*pc.Status.BoundPodCheckpointContentName != "" {
			
			contentName := *pc.Status.BoundPodCheckpointContentName
			logger.Info("Deleting associated PodCheckpointContent due to deletion policy", 
				"contentName", contentName,
				"deletionPolicy", pc.Spec.DeletionPolicy)
			
			pcc := &checkpointv1.PodCheckpointContent{
				ObjectMeta: metav1.ObjectMeta{Name: contentName},
			}
			if err := r.Delete(ctx, pcc); err != nil && !apierr.IsNotFound(err) {
				logger.Error(err, "Failed to delete PodCheckpointContent", 
					"contentName", contentName)
				return ctrl.Result{}, err
			}
			logger.Info("Successfully deleted PodCheckpointContent", 
				"contentName", contentName)
		} else {
			logger.Info("Not deleting content due to policy or missing content reference",
				"deletionPolicy", pc.Spec.DeletionPolicy,
				"hasBoundContent", pc.Status.BoundPodCheckpointContentName != nil)
		}
		
		// Remove finalizer with retry-on-conflict
		logger.Info("Removing finalizer from PodCheckpoint")
		removeFinalizerStart := time.Now()
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var latest checkpointv1.PodCheckpoint
			if err := r.Get(ctx, req.NamespacedName, &latest); err != nil {
				logger.Error(err, "Failed to get latest PodCheckpoint for finalizer removal")
				return err
			}
			controllerutil.RemoveFinalizer(&latest, pcFinalizer)
			return r.Patch(ctx, &latest, client.MergeFrom(&pc))
		}); err != nil {
			logger.Error(err, "Failed to remove finalizer from PodCheckpoint")
			return ctrl.Result{}, err
		}
		logger.Info("Successfully removed finalizer from PodCheckpoint", 
			"durationMs", time.Since(removeFinalizerStart).Milliseconds())
		return ctrl.Result{}, nil
	}

	// 3. Ensure finalizer
	if !controllerutil.ContainsFinalizer(&pc, pcFinalizer) {
		logger.Info("Adding finalizer to PodCheckpoint")
		addFinalizerStart := time.Now()
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var latest checkpointv1.PodCheckpoint
			if err := r.Get(ctx, req.NamespacedName, &latest); err != nil {
				logger.Error(err, "Failed to get latest PodCheckpoint for adding finalizer")
				return err
			}
			controllerutil.AddFinalizer(&latest, pcFinalizer)
			return r.Patch(ctx, &latest, client.MergeFrom(&pc))
		}); err != nil {
			logger.Error(err, "Failed to add finalizer to PodCheckpoint")
			return ctrl.Result{}, err
		}
		logger.Info("Successfully added finalizer to PodCheckpoint", 
			"durationMs", time.Since(addFinalizerStart).Milliseconds())
		return ctrl.Result{Requeue: true}, nil
	}

	// 4. Pre-existing content
	if ref := pc.Spec.Source.PodCheckpointContentName; ref != nil && *ref != "" {
		contentName := *ref
		logger.Info("Found pre-existing PodCheckpointContent reference", 
			"contentName", contentName)
		
		var existing checkpointv1.PodCheckpointContent
		if err := r.Get(ctx, client.ObjectKey{Name: contentName}, &existing); err != nil {
			logger.Error(err, "Failed to get referenced PodCheckpointContent", 
				"contentName", contentName)
			_ = r.updateStatus(ctx, &pc, func(c *checkpointv1.PodCheckpoint) {
				c.Status.ErrorMessage = fmt.Sprintf("content not found: %v", err)
				c.Status.ReadyToRestore = false
			})
			return ctrl.Result{}, err
		}
		
		logger.Info("Successfully found referenced PodCheckpointContent", 
			"contentName", contentName,
			"contentReadyToRestore", existing.Status.ReadyToRestore,
			"contentHasError", existing.Status.ErrorMessage != "")
		
		updateStart := time.Now()
		_ = r.updateStatus(ctx, &pc, func(c *checkpointv1.PodCheckpoint) {
			c.Status.BoundPodCheckpointContentName = &contentName
			c.Status.ErrorMessage = existing.Status.ErrorMessage
			c.Status.ReadyToRestore = existing.Status.ReadyToRestore
			c.Status.CheckpointTime = existing.Status.CheckpointTime
		})
		logger.Info("Successfully updated PodCheckpoint with pre-existing content info", 
			"contentName", contentName,
			"durationMs", time.Since(updateStart).Milliseconds())
		return ctrl.Result{}, nil
	}

	// 5. Validate PodName
	logger.Info("Validating pod reference")
	if pc.Spec.Source.PodName == nil || *pc.Spec.Source.PodName == "" {
		logger.Error(nil, "Invalid PodName in source",
			"podNameNil", pc.Spec.Source.PodName == nil,
			"podNameEmpty", pc.Spec.Source.PodName != nil && *pc.Spec.Source.PodName == "")
		_ = r.updateStatus(ctx, &pc, func(c *checkpointv1.PodCheckpoint) {
			c.Status.ErrorMessage = "no valid PodName in source"
			c.Status.ReadyToRestore = false
		})
		return ctrl.Result{}, nil
	}
	
	podName := *pc.Spec.Source.PodName
	logger.Info("Looking up pod", "namespace", pc.Namespace, "podName", podName)

	// fetch Pod
	var pod corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{Namespace: pc.Namespace, Name: podName}, &pod); err != nil {
		if apierr.IsNotFound(err) {
			logger.Error(err, "Target pod not found", 
				"namespace", pc.Namespace, 
				"podName", podName)
			_ = r.updateStatus(ctx, &pc, func(c *checkpointv1.PodCheckpoint) {
				c.Status.ErrorMessage = "target pod not found"
				c.Status.ReadyToRestore = false
			})
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Error fetching pod", 
			"namespace", pc.Namespace, 
			"podName", podName)
		return ctrl.Result{}, err
	}
	logger.Info("Successfully found pod", 
		"podName", podName, 
		"podNodeName", pod.Spec.NodeName,
		"containerCount", len(pod.Spec.Containers))

	// 6. Skip if already bound
	if pc.Status.BoundPodCheckpointContentName != nil {
		contentName := *pc.Status.BoundPodCheckpointContentName
		logger.Info("PodCheckpoint already bound to PodCheckpointContent; skipping creation", 
			"contentName", contentName)
		return ctrl.Result{}, nil
	}

	// 7. Get or create PodCheckpointContent
	// Using just the PodCheckpoint name for better readability and to avoid redundancy
	pccName := fmt.Sprintf("%s-pcc", pc.Name)
	logger.Info("Checking if PodCheckpointContent already exists", "contentName", pccName)
	
	var pcc checkpointv1.PodCheckpointContent
	if err := r.Get(ctx, client.ObjectKey{Name: pccName}, &pcc); apierr.IsNotFound(err) {
		logger.Info("PodCheckpointContent not found, creating new", "contentName", pccName)
		
		newPCC := &checkpointv1.PodCheckpointContent{
			ObjectMeta: metav1.ObjectMeta{Name: pccName},
			Spec: checkpointv1.PodCheckpointContentSpec{
				PodCheckpointRef: &checkpointv1.PodCheckpointRef{Name: pc.Name, Namespace: pc.Namespace},
				DeletionPolicy:   pc.Spec.DeletionPolicy,
				PodUID:           string(pod.UID),
				PodNamespace:     pc.Namespace,
				PodName:          *pc.Spec.Source.PodName,
			},
		}
		
		if t := pc.Annotations["checkpointing.zacchaeuschok.github.io/targetNode"]; t != "" {
			newPCC.Spec.StorageLocation = "node://" + t
			logger.Info("Using target node from annotation", 
				"targetNode", t,
				"storageLocation", newPCC.Spec.StorageLocation)
		}

		// We can't set the PodCheckpoint as the owner because PodCheckpointContent is cluster-scoped
		// and PodCheckpoint is namespace-scoped. Instead, we use a back-reference in the spec.
		
		createStart := time.Now()
		if err := r.Create(ctx, newPCC); err != nil && !apierr.IsAlreadyExists(err) {
			logger.Error(err, "Failed to create PodCheckpointContent", "contentName", pccName)
			return ctrl.Result{}, err
		}
		logger.Info("Successfully created PodCheckpointContent", 
			"contentName", pccName,
			"durationMs", time.Since(createStart).Milliseconds())
		
		logger.Info("Requeuing to wait for PodCheckpointContent to be processed", 
			"requeueAfter", requeuePeriod)
		return ctrl.Result{RequeueAfter: requeuePeriod}, nil
	} else if err != nil {
		logger.Error(err, "Error checking for existing PodCheckpointContent", 
			"contentName", pccName)
		return ctrl.Result{}, err
	} else {
		logger.Info("Found existing PodCheckpointContent", 
			"contentName", pccName,
			"readyToRestore", pcc.Status.ReadyToRestore,
			"hasError", pcc.Status.ErrorMessage != "")
	}

	// 8. Create/update ContainerCheckpoints
	target := pc.Annotations["checkpointing.zacchaeuschok.github.io/targetNode"]
	logger.Info("Creating/updating ContainerCheckpoints for each container", 
		"podName", podName,
		"containerCount", len(pod.Spec.Containers),
		"targetNode", target)
	
	for i, ctr := range pod.Spec.Containers {
		name := fmt.Sprintf("%s-%s", pc.Name, ctr.Name)
		logger.Info("Processing container checkpoint", 
			"containerIndex", i,
			"containerName", ctr.Name,
			"ccName", name)
		
		cc := &checkpointv1.ContainerCheckpoint{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: pc.Namespace},
		}
		
		createUpdateStart := time.Now()
		op, err := controllerutil.CreateOrUpdate(ctx, r.Client, cc, func() error {
			if cc.Spec.Source.PodName == nil {
				cc.Spec.Source.PodName = &pod.Name
			}
			if cc.Spec.Source.ContainerName == nil {
				cc.Spec.Source.ContainerName = &ctr.Name
			}
			if cc.Spec.DeletionPolicy == "" {
				cc.Spec.DeletionPolicy = pc.Spec.DeletionPolicy
			}
			if cc.Annotations == nil {
				cc.Annotations = make(map[string]string)
			}
			if target != "" {
				cc.Annotations["checkpointing.zacchaeuschok.github.io/targetNode"] = target
			}
			return nil
		})
		
		if err != nil {
			logger.Error(err, "CreateOrUpdate ContainerCheckpoint failed", 
				"ccName", name,
				"containerName", ctr.Name)
			_ = r.updateStatus(ctx, &pc, func(c *checkpointv1.PodCheckpoint) {
				c.Status.ErrorMessage = err.Error()
				c.Status.ReadyToRestore = false
			})
			return ctrl.Result{}, err
		}
		
		logger.Info("Successfully processed ContainerCheckpoint", 
			"ccName", name,
			"containerName", ctr.Name,
			"operation", op,
			"durationMs", time.Since(createUpdateStart).Milliseconds())
	}

	// 9. Aggregate readiness...
	logger.Info("Aggregating ContainerCheckpoint readiness status")
	allReady := true
	var aggErr string
	for i, ctr := range pod.Spec.Containers {
		name := fmt.Sprintf("%s-%s", pc.Name, ctr.Name)
		var cc checkpointv1.ContainerCheckpoint
		
		logger.Info("Checking ContainerCheckpoint status", 
			"containerIndex", i,
			"containerName", ctr.Name,
			"ccName", name)
		
		if err := r.Get(ctx, client.ObjectKey{Namespace: pc.Namespace, Name: name}, &cc); err != nil {
			if !apierr.IsNotFound(err) {
				logger.Error(err, "Failed to get ContainerCheckpoint", 
					"ccName", name)
				allReady = false
				aggErr = err.Error()
				break
			}
			logger.Info("ContainerCheckpoint not found", "ccName", name)
			allReady = false
			continue
		}
		
		logger.Info("ContainerCheckpoint status", 
			"ccName", name,
			"readyToRestore", cc.Status.ReadyToRestore,
			"errorMessage", cc.Status.ErrorMessage)
		
		if !cc.Status.ReadyToRestore || cc.Status.ErrorMessage != "" {
			logger.Info("ContainerCheckpoint not ready or has error", 
				"ccName", name,
				"readyToRestore", cc.Status.ReadyToRestore,
				"errorMessage", cc.Status.ErrorMessage)
			allReady = false
			aggErr = cc.Status.ErrorMessage
			break
		}
	}
	
	logger.Info("Aggregate readiness result", 
		"allReady", allReady, 
		"aggregateError", aggErr)

	// 10. Update statuses
	now := metav1.Now()
	logger.Info("Updating statuses of PodCheckpoint and PodCheckpointContent")
	if allReady {
		logger.Info("All ContainerCheckpoints are ready, marking PodCheckpoint as ready")
		pc.Status.BoundPodCheckpointContentName = &pccName
		pc.Status.ReadyToRestore = true
		pc.Status.CheckpointTime = &now
		pc.Status.ErrorMessage = ""
		pcc.Status.ReadyToRestore = true
		pcc.Status.CheckpointTime = &now
		pcc.Status.ErrorMessage = ""
	} else {
		logger.Info("Not all ContainerCheckpoints are ready, marking PodCheckpoint as not ready",
			"aggregateError", aggErr)
		pc.Status.ReadyToRestore = false
		pc.Status.ErrorMessage = aggErr
		pcc.Status.ReadyToRestore = false
		pcc.Status.ErrorMessage = aggErr
	}

	// retry-on-conflict when updating PodCheckpoint
	updatePcStart := time.Now()
	logger.Info("Updating PodCheckpoint status")
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		return r.Status().Update(ctx, &pc)
	}); err != nil {
		logger.Error(err, "Failed to update PodCheckpoint status")
		return ctrl.Result{}, err
	}
	logger.Info("Successfully updated PodCheckpoint status",
		"durationMs", time.Since(updatePcStart).Milliseconds())

	// retry-on-conflict + re-Get when updating PodCheckpointContent
	updatePccStart := time.Now()
	logger.Info("Updating PodCheckpointContent status")
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latestPCC checkpointv1.PodCheckpointContent
		if err := r.Get(ctx, client.ObjectKey{Name: pccName}, &latestPCC); err != nil {
			logger.Error(err, "Failed to get latest PodCheckpointContent for status update")
			return err
		}
		latestPCC.Status = pcc.Status
		return r.Status().Update(ctx, &latestPCC)
	}); err != nil {
		logger.Error(err, "Failed to update PodCheckpointContent status")
		return ctrl.Result{}, err
	}
	logger.Info("Successfully updated PodCheckpointContent status",
		"durationMs", time.Since(updatePccStart).Milliseconds())

	if allReady {
		logger.Info("PodCheckpoint reconcile complete successfully, all ready", 
			"name", pc.Name,
			"contentName", pccName)
		return ctrl.Result{}, nil
	}
	
	logger.Info("PodCheckpoint reconcile complete, but not all ready, requeuing", 
		"name", pc.Name, 
		"requeueAfter", requeuePeriod)
	return ctrl.Result{RequeueAfter: requeuePeriod}, nil
}

func (r *PodCheckpointReconciler) updateStatus(
	ctx context.Context,
	pc *checkpointv1.PodCheckpoint,
	mutate func(*checkpointv1.PodCheckpoint),
) error {
	logger := log.FromContext(ctx).WithValues(
		"operation", "updateStatus",
		"namespace", pc.Namespace,
		"name", pc.Name)
	
	startTime := time.Now()
	logger.Info("Updating PodCheckpoint status")
	
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest checkpointv1.PodCheckpoint
		if err := r.Get(ctx, client.ObjectKeyFromObject(pc), &latest); err != nil {
			logger.Error(err, "Failed to get latest PodCheckpoint for status update")
			return err
		}
		mutate(&latest)
		if err := r.Status().Update(ctx, &latest); err != nil {
			logger.Error(err, "Failed to update PodCheckpoint status")
			return err
		}
		return nil
	})
	
	if err != nil {
		logger.Error(err, "Failed to update PodCheckpoint status after retries")
	} else {
		logger.Info("Successfully updated PodCheckpoint status", 
			"durationMs", time.Since(startTime).Milliseconds())
	}
	
	return err
}

func (r *PodCheckpointReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// Watch PodCheckpoint resources in all namespaces
		For(&checkpointv1.PodCheckpoint{}).
		// We can only own namespace-scoped resources
		Owns(&checkpointv1.ContainerCheckpoint{}).
		Complete(r)
}
