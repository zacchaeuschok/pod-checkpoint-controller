// Copyright 2025.
// SPDX‑License‑Identifier: Apache‑2.0

package controller

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	checkpointv1 "github.com/zacchaeuschok/pod-checkpoint-controller/api/v1"

	corev1 "k8s.io/api/core/v1"
	apierr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const restoreAnn = "checkpointing.zacchaeuschok.github.io/restore-from"

// -----------------------------------------------------------------------------
// RBAC
// -----------------------------------------------------------------------------
// +kubebuilder:rbac:groups=checkpointing.zacchaeuschok.github.io,resources=podmigrations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=checkpointing.zacchaeuschok.github.io,resources=podmigrations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=checkpointing.zacchaeuschok.github.io,resources=podcheckpoints,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=checkpointing.zacchaeuschok.github.io,resources=podcheckpoints/status,verbs=get
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete
// -----------------------------------------------------------------------------

type PodMigrationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *PodMigrationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	var mig checkpointv1.PodMigration
	if err := r.Get(ctx, req.NamespacedName, &mig); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	switch mig.Status.Phase {
	case "", checkpointv1.MigrationPending:
		return r.ensureCheckpoint(ctx, &mig, log)
	case checkpointv1.MigrationCheckpointing:
		return r.waitCheckpointReady(ctx, &mig, log)
	case checkpointv1.MigrationRestoring:
		return r.waitRestore(ctx, &mig, log)
	default:
		return ctrl.Result{}, nil
	}
}

// ------------------------------------------------------------ //
// Phase 1 – request PodCheckpoint on the **source** node
// ------------------------------------------------------------ //
func (r *PodMigrationReconciler) ensureCheckpoint(ctx context.Context, m *checkpointv1.PodMigration, log logr.Logger) (ctrl.Result, error) {
	pcName := m.Name + "-checkpoint"

	if err := r.updateStatus(ctx, m, func(mm *checkpointv1.PodMigration) {
		mm.Status.Phase = checkpointv1.MigrationCheckpointing
		mm.Status.Checkpoint = pcName
	}); err != nil {
		return ctrl.Result{}, err
	}

	var pc checkpointv1.PodCheckpoint
	err := r.Get(ctx, client.ObjectKey{Namespace: m.Namespace, Name: pcName}, &pc)
	switch {
	case apierr.IsNotFound(err):
		pc = checkpointv1.PodCheckpoint{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pcName,
				Namespace: m.Namespace,
				Labels:    map[string]string{"migration": m.Name},
			},
			Spec: checkpointv1.PodCheckpointSpec{
				PodName:            m.Spec.SourcePodName,
				StorageLocation:    "node://" + m.Spec.TargetNode,
				RetainAfterRestore: true,
			},
		}
		if err := r.Create(ctx, &pc); err != nil {
			return ctrl.Result{}, err
		}
	case err != nil:
		return ctrl.Result{}, err
	}

	log.Info("pod‑checkpoint requested", "checkpoint", pcName)
	return ctrl.Result{RequeueAfter: 4 * time.Second}, nil
}

// ------------------------------------------------------------ //
// Phase 2 – wait Ready, then create *restore* pod on target
// ------------------------------------------------------------ //
func (r *PodMigrationReconciler) waitCheckpointReady(ctx context.Context, m *checkpointv1.PodMigration, log logr.Logger) (ctrl.Result, error) {
	var pc checkpointv1.PodCheckpoint
	if err := r.Get(ctx, client.ObjectKey{Namespace: m.Namespace, Name: m.Status.Checkpoint}, &pc); err != nil {
		return ctrl.Result{}, err
	}
	if !pc.Status.ReadyToRestore {
		return ctrl.Result{RequeueAfter: 4 * time.Second}, nil
	}

	// clone spec of the source pod
	var src corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{Namespace: m.Namespace, Name: m.Spec.SourcePodName}, &src); err != nil {
		return ctrl.Result{}, err
	}

	newName := m.Name + "-restored"
	restorePod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      newName,
			Namespace: m.Namespace,
			Labels:    src.Labels,
			Annotations: map[string]string{
				restoreAnn: pc.Status.PodCheckpointContentName,
			},
		},
		Spec: src.Spec,
	}

	// strip scheduling hints that belong to the old node
	restorePod.Spec.NodeSelector = nil
	restorePod.Spec.Affinity = nil
	restorePod.Spec.TopologySpreadConstraints = nil

	restorePod.Spec.NodeName = m.Spec.TargetNode

	if err := r.Create(ctx, &restorePod); err != nil && !apierr.IsAlreadyExists(err) {
		return ctrl.Result{}, err
	}

	if err := r.updateStatus(ctx, m, func(mm *checkpointv1.PodMigration) {
		mm.Status.Phase = checkpointv1.MigrationRestoring
		mm.Status.RestoredPod = newName
	}); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("restore‑pod created", "pod", newName)
	return ctrl.Result{RequeueAfter: 6 * time.Second}, nil
}

// ------------------------------------------------------------ //
// Phase 3 – wait Running, then delete the source pod
// ------------------------------------------------------------ //
func (r *PodMigrationReconciler) waitRestore(ctx context.Context, m *checkpointv1.PodMigration, log logr.Logger) (ctrl.Result, error) {
	var pod corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{Namespace: m.Namespace, Name: m.Status.RestoredPod}, &pod); err != nil {
		return ctrl.Result{}, err
	}
	if pod.Status.Phase != corev1.PodRunning {
		return ctrl.Result{RequeueAfter: 6 * time.Second}, nil
	}

	// delete the original
	var src corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{Namespace: m.Namespace, Name: m.Spec.SourcePodName}, &src); err == nil {
		_ = r.Delete(ctx, &src)
	}

	if err := r.updateStatus(ctx, m, func(mm *checkpointv1.PodMigration) {
		mm.Status.Phase = checkpointv1.MigrationSucceeded
	}); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("migration succeeded")
	return ctrl.Result{}, nil
}

//---------------------------------------------------------------------------//
// helpers
//---------------------------------------------------------------------------//

func (r *PodMigrationReconciler) updateStatus(
	ctx context.Context,
	m *checkpointv1.PodMigration,
	mutate func(*checkpointv1.PodMigration),
) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest checkpointv1.PodMigration
		if err := r.Get(ctx, client.ObjectKeyFromObject(m), &latest); err != nil {
			return err
		}
		mutate(&latest)
		return r.Status().Update(ctx, &latest)
	})
}

func (r *PodMigrationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&checkpointv1.PodMigration{}).
		Complete(r)
}
