// Copyright 2025.
// SPDX‑License‑Identifier: Apache‑2.0

package controller

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
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

// -----------------------------------------------------------------------------
// RBAC
// -----------------------------------------------------------------------------
// +kubebuilder:rbac:groups=checkpointing.zacchaeuschok.github.io,resources=containercheckpoints,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=checkpointing.zacchaeuschok.github.io,resources=containercheckpoints/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=checkpointing.zacchaeuschok.github.io,resources=containercheckpointcontents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=checkpointing.zacchaeuschok.github.io,resources=containercheckpointcontents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// -----------------------------------------------------------------------------

const ccFinalizer = "checkpointing.zacchaeuschok.github.io/cc-finalizer"

type ContainerCheckpointReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *ContainerCheckpointReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	// ------------------------------------------------------------------ //
	// 1. fetch CR
	// ------------------------------------------------------------------ //
	var cc checkpointv1.ContainerCheckpoint
	if err := r.Get(ctx, req.NamespacedName, &cc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// ------------------------------------------------------------------ //
	// 2. deletion / finaliser
	// ------------------------------------------------------------------ //
	if cc.DeletionTimestamp != nil {
		if controllerutil.ContainsFinalizer(&cc, ccFinalizer) {
			_ = r.deleteContent(ctx, &cc, log)
			controllerutil.RemoveFinalizer(&cc, ccFinalizer)
			_ = r.Update(ctx, &cc)
		}
		return ctrl.Result{}, nil
	}
	if !controllerutil.ContainsFinalizer(&cc, ccFinalizer) {
		controllerutil.AddFinalizer(&cc, ccFinalizer)
		_ = r.Update(ctx, &cc)
	}

	// ------------------------------------------------------------------ //
	// 3. fast validation — pod + container exist
	// ------------------------------------------------------------------ //
	var pod corev1.Pod
	if err := r.Get(ctx,
		client.ObjectKey{Namespace: cc.Namespace, Name: cc.Spec.Source.PodName}, &pod); err != nil {

		_ = r.updateStatus(ctx, &cc, func(c *checkpointv1.ContainerCheckpoint) {
			c.Status.ErrorMessage = fmt.Sprintf("pod lookup failed: %v", err)
		})
		return ctrl.Result{}, err
	}
	found := false
	for _, c := range pod.Spec.Containers {
		if c.Name == cc.Spec.Source.ContainerName {
			found = true
			break
		}
	}
	if !found {
		_ = r.updateStatus(ctx, &cc, func(c *checkpointv1.ContainerCheckpoint) {
			c.Status.ErrorMessage = "container not found in pod"
		})
		return ctrl.Result{}, nil
	}

	// ------------------------------------------------------------------ //
	// 4. ensure / update ContainerCheckpointContent (cluster‑scoped)
	// ------------------------------------------------------------------ //
	contentName := r.contentName(&cc)
	var ccc checkpointv1.ContainerCheckpointContent
	err := r.Get(ctx, client.ObjectKey{Name: contentName}, &ccc)

	switch {
	case apierr.IsNotFound(err):
		ccc = checkpointv1.ContainerCheckpointContent{
			ObjectMeta: metav1.ObjectMeta{Name: contentName},
			Spec: checkpointv1.ContainerCheckpointContentSpec{
				ContainerCheckpointRef: checkpointv1.ContainerCheckpointRef{
					Name: cc.Name, Namespace: cc.Namespace,
				},
				StorageLocation:    cc.Spec.StorageLocation,
				DeletionPolicy:     boolToPolicy(cc.Spec.RetainAfterRestore),
				RetainAfterRestore: cc.Spec.RetainAfterRestore,
			},
		}
		if err := r.Create(ctx, &ccc); err != nil {
			return ctrl.Result{}, err
		}
	case err == nil:
		updated := false
		if ccc.Spec.StorageLocation != cc.Spec.StorageLocation {
			ccc.Spec.StorageLocation = cc.Spec.StorageLocation
			updated = true
		}
		if updated {
			if err := r.Update(ctx, &ccc); err != nil {
				return ctrl.Result{}, err
			}
		}
	default:
		return ctrl.Result{}, err
	}

	// ------------------------------------------------------------------ //
	// 5. propagate ready flag (with retry)
	// ------------------------------------------------------------------ //
	if err := r.updateStatus(ctx, &cc, func(c *checkpointv1.ContainerCheckpoint) {
		if ccc.Status.ReadyToRestore {
			c.Status.ReadyToRestore = true
			c.Status.ErrorMessage = ""
		} else {
			c.Status.ReadyToRestore = false
		}
	}); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("reconcile complete", "name", cc.Name)
	return ctrl.Result{}, nil
}

//---------------------------------------------------------------------------//
// helpers
//---------------------------------------------------------------------------//

func (r *ContainerCheckpointReconciler) updateStatus(
	ctx context.Context,
	cc *checkpointv1.ContainerCheckpoint,
	mutate func(*checkpointv1.ContainerCheckpoint),
) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest checkpointv1.ContainerCheckpoint
		if err := r.Get(ctx, client.ObjectKeyFromObject(cc), &latest); err != nil {
			return err
		}
		mutate(&latest)
		return r.Status().Update(ctx, &latest)
	})
}

func (r *ContainerCheckpointReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&checkpointv1.ContainerCheckpoint{}).
		Owns(&checkpointv1.ContainerCheckpointContent{}).
		Complete(r)
}

func (r *ContainerCheckpointReconciler) contentName(cc *checkpointv1.ContainerCheckpoint) string {
	return fmt.Sprintf("%s-%s", cc.Name, cc.Spec.Source.ContainerName)
}

func (r *ContainerCheckpointReconciler) deleteContent(ctx context.Context, cc *checkpointv1.ContainerCheckpoint, log logr.Logger) error {
	var ccc checkpointv1.ContainerCheckpointContent
	if err := r.Get(ctx, client.ObjectKey{Name: r.contentName(cc)}, &ccc); err == nil {
		_ = r.Delete(ctx, &ccc)
		log.Info("deleted content", "name", ccc.Name)
	}
	return nil
}

func boolToPolicy(b bool) string {
	if b {
		return "Retain"
	}
	return "Delete"
}
