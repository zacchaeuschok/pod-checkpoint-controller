package controller

import (
	"context"
	"fmt"
	"time"

	checkpointv1 "github.com/zacchaeuschok/pod-checkpoint-controller/api/v1"
	migrationv1 "github.com/zacchaeuschok/pod-checkpoint-controller/api/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// PodMigrationReconciler reconciles a PodMigration object
type PodMigrationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// Reconcile drives PodMigration through phases: Pending -> Checkpointing -> Restoring -> Succeeded/Failed
func (r *PodMigrationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Log every reconciliation request
	logger.Info("Reconciling PodMigration", "namespace", req.Namespace, "name", req.Name)

	var pm migrationv1.PodMigration
	if err := r.Get(ctx, req.NamespacedName, &pm); err != nil {
		logger.Error(err, "Unable to fetch PodMigration", "namespace", req.Namespace, "name", req.Name)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Log that we found the PodMigration
	logger.Info("Found PodMigration", "namespace", pm.Namespace, "name", pm.Name, "phase", pm.Status.Phase)

	switch pm.Status.Phase {
	case "", migrationv1.MigrationPhasePending:
		return r.handlePending(ctx, &pm)
	case migrationv1.MigrationPhaseCheckpointing:
		return r.handleCheckpointing(ctx, &pm)
	case migrationv1.MigrationPhaseRestoring:
		return r.handleRestoring(ctx, &pm)
	case migrationv1.MigrationPhaseSucceeded, migrationv1.MigrationPhaseFailed:
		logger.Info("PodMigration is completed or failed", "phase", pm.Status.Phase)
		return ctrl.Result{}, nil
	default:
		logger.Info("Unknown phase, nothing to do", "phase", pm.Status.Phase)
		return ctrl.Result{}, nil
	}
}

// handlePending creates (if needed) a PodCheckpoint for the source Pod, then transitions to Checkpointing.
func (r *PodMigrationReconciler) handlePending(ctx context.Context, pm *migrationv1.PodMigration) (ctrl.Result, error) {
	cpName := fmt.Sprintf("%s-checkpoint", pm.Name)
	var cp checkpointv1.PodCheckpoint
	err := r.Get(ctx, client.ObjectKey{Name: cpName, Namespace: pm.Namespace}, &cp)
	if apierrors.IsNotFound(err) {
		cp = checkpointv1.PodCheckpoint{
			ObjectMeta: ctrl.ObjectMeta{
				Name:      cpName,
				Namespace: pm.Namespace,
				Annotations: map[string]string{
					"checkpointing.zacchaeuschok.github.io/targetNode": pm.Spec.TargetNode,
				},
			},
			Spec: checkpointv1.PodCheckpointSpec{
				Source: checkpointv1.PodCheckpointSource{
					PodName: &pm.Spec.SourcePodName,
				},
				DeletionPolicy: "Retain",
			},
		}

		// Set the PodMigration as the owner of the PodCheckpoint
		if err := controllerutil.SetControllerReference(pm, &cp, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}

		if createErr := r.Create(ctx, &cp); createErr != nil {
			return ctrl.Result{}, createErr
		}
	} else if err != nil {
		return ctrl.Result{}, err
	}

	// Mark PodMigration as “Checkpointing”
	if err := r.updateStatus(ctx, pm, func(m *migrationv1.PodMigration) {
		m.Status.Phase = migrationv1.MigrationPhaseCheckpointing
		m.Status.BoundCheckpointName = cp.Name
		m.Status.Message = "Requested PodCheckpoint"
	}); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue soon to check if checkpoint becomes Ready
	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

// handleCheckpointing polls the PodCheckpoint status. Once ReadyToRestore, transitions to Restoring.
func (r *PodMigrationReconciler) handleCheckpointing(ctx context.Context, pm *migrationv1.PodMigration) (ctrl.Result, error) {
	var cp checkpointv1.PodCheckpoint
	if err := r.Get(ctx, client.ObjectKey{Namespace: pm.Namespace, Name: pm.Status.BoundCheckpointName}, &cp); err != nil {
		return ctrl.Result{}, err
	}

	if !cp.Status.ReadyToRestore {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	if err := r.updateStatus(ctx, pm, func(m *migrationv1.PodMigration) {
		m.Status.Phase = migrationv1.MigrationPhaseRestoring
		m.Status.Message = "Checkpoint is ready; restoring"
	}); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// handleRestoring creates a new Pod on the target node with special annotations that trigger kubelet’s restore logic.
func (r *PodMigrationReconciler) handleRestoring(ctx context.Context, pm *migrationv1.PodMigration) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	restoredName := pm.Name + "-restored"

	// Retrieve the source Pod to copy certain details (like container specs)
	var srcPod corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{Namespace: pm.Namespace, Name: pm.Spec.SourcePodName}, &srcPod); err != nil {
		return ctrl.Result{}, err
	}

	// In real usage, you might handle multiple containers. For demonstration, we'll pick the first container.
	if len(srcPod.Spec.Containers) == 0 {
		errMsg := "source Pod has no containers"
		_ = r.updateStatus(ctx, pm, func(m *migrationv1.PodMigration) {
			m.Status.Phase = migrationv1.MigrationPhaseFailed
			m.Status.Message = errMsg
		})
		return ctrl.Result{}, nil
	}
	sourceContainerName := srcPod.Spec.Containers[0].Name

	// The node where the source Pod currently runs
	sourceNode := srcPod.Spec.NodeName
	if sourceNode == "" {
		sourceNode = "unknown"
	}

	// Build a new Pod spec for the restored Pod
	restorePod := corev1.Pod{
		ObjectMeta: ctrl.ObjectMeta{
			Name:      restoredName,
			Namespace: pm.Namespace,
			Annotations: map[string]string{
				// The custom kubelet restore logic references these:
				"kubernetes.io/source-pod":       pm.Spec.SourcePodName,
				"kubernetes.io/source-namespace": pm.Namespace,
				"kubernetes.io/source-container": sourceContainerName,
				"kubernetes.io/source-node":      sourceNode,
			},
		},
		Spec: srcPod.Spec,
	}

	// Force schedule onto target node
	restorePod.Spec.NodeName = pm.Spec.TargetNode

	// Remove any old node selector or affinity constraints
	restorePod.Spec.NodeSelector = nil
	restorePod.Spec.Affinity = nil

	// Create the restored Pod
	if err := r.Create(ctx, &restorePod); err != nil && !apierrors.IsAlreadyExists(err) {
		return ctrl.Result{}, err
	}

	// For demonstration, mark Succeeded immediately
	if err := r.updateStatus(ctx, pm, func(m *migrationv1.PodMigration) {
		m.Status.Phase = migrationv1.MigrationPhaseSucceeded
		m.Status.RestoredPodName = restoredName
		m.Status.Message = fmt.Sprintf("Created restored Pod %q on node %q", restoredName, pm.Spec.TargetNode)
	}); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("Migration restoration complete", "restoredPod", restoredName, "targetNode", pm.Spec.TargetNode)
	return ctrl.Result{}, nil
}

// updateStatus helps safely mutate and persist the PodMigration’s status with retry-on-conflict.
func (r *PodMigrationReconciler) updateStatus(ctx context.Context, pm *migrationv1.PodMigration, mutate func(*migrationv1.PodMigration)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest migrationv1.PodMigration
		if err := r.Get(ctx, client.ObjectKeyFromObject(pm), &latest); err != nil {
			return err
		}
		mutate(&latest)
		return r.Status().Update(ctx, &latest)
	})
}

// SetupWithManager registers this reconciler with the controller-manager.
func (r *PodMigrationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&migrationv1.PodMigration{}).
		Owns(&checkpointv1.PodCheckpoint{}).
		Complete(r)
}
