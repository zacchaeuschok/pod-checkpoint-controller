// Copyright 2025.
// SPDX‑License‑Identifier: Apache‑2.0

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

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	pcFinalizer   = "checkpointing.zacchaeuschok.github.io/pc-finalizer"
	requeuePeriod = 8 * time.Second
)

// -----------------------------------------------------------------------------
// RBAC
// -----------------------------------------------------------------------------
// +kubebuilder:rbac:groups=checkpointing.zacchaeuschok.github.io,resources=podcheckpoints,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=checkpointing.zacchaeuschok.github.io,resources=podcheckpoints/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=checkpointing.zacchaeuschok.github.io,resources=containercheckpoints;podcheckpointcontents;containercheckpointcontents,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// -----------------------------------------------------------------------------

type PodCheckpointReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *PodCheckpointReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	//-------------------------------------------------------------------//
	// 1· Fetch CR
	//-------------------------------------------------------------------//
	var pc checkpointv1.PodCheckpoint
	if err := r.Get(ctx, req.NamespacedName, &pc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	//-------------------------------------------------------------------//
	// 2· Finaliser
	//-------------------------------------------------------------------//
	if pc.DeletionTimestamp != nil {
		if controllerutil.ContainsFinalizer(&pc, pcFinalizer) {
			controllerutil.RemoveFinalizer(&pc, pcFinalizer)
			_ = r.Update(ctx, &pc)
		}
		return ctrl.Result{}, nil
	}
	if !controllerutil.ContainsFinalizer(&pc, pcFinalizer) {
		controllerutil.AddFinalizer(&pc, pcFinalizer)
		_ = r.Update(ctx, &pc)
	}

	//-------------------------------------------------------------------//
	// 3· Target pod
	//-------------------------------------------------------------------//
	var pod corev1.Pod
	if err := r.Get(ctx,
		client.ObjectKey{Namespace: pc.Namespace, Name: pc.Spec.PodName}, &pod); err != nil {

		if apierr.IsNotFound(err) {
			pc.Status.ErrorMessage = "target pod not found"
			_ = r.Status().Update(ctx, &pc)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	//-------------------------------------------------------------------//
	// 4· One ContainerCheckpoint per container
	//-------------------------------------------------------------------//
	for _, c := range pod.Spec.Containers {
		name := pc.Name + "-" + c.Name
		var cc checkpointv1.ContainerCheckpoint
		err := r.Get(ctx, client.ObjectKey{Namespace: pc.Namespace, Name: name}, &cc)

		switch {
		case apierr.IsNotFound(err):
			cc = checkpointv1.ContainerCheckpoint{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: pc.Namespace,
					Labels:    map[string]string{"podcheckpoint": pc.Name},
				},
				Spec: checkpointv1.ContainerCheckpointSpec{
					Source: checkpointv1.Source{
						PodName: pod.Name, ContainerName: c.Name,
					},
					StorageLocation:    pc.Spec.StorageLocation,
					RetainAfterRestore: pc.Spec.RetainAfterRestore,
				},
			}
			_ = controllerutil.SetControllerReference(&pc, &cc, r.Scheme)
			if err := r.Create(ctx, &cc); err != nil {
				return ctrl.Result{}, err
			}
		case err != nil:
			return ctrl.Result{}, err
		}
	}

	//-------------------------------------------------------------------//
	// 5· Ensure PodCheckpointContent
	//-------------------------------------------------------------------//
	contentName := pc.Namespace + "-" + pc.Name + "-content"
	var pcc checkpointv1.PodCheckpointContent
	err := r.Get(ctx, client.ObjectKey{Name: contentName}, &pcc)

	switch {
	case apierr.IsNotFound(err):
		pcc = checkpointv1.PodCheckpointContent{
			ObjectMeta: metav1.ObjectMeta{Name: contentName},
			Spec: checkpointv1.PodCheckpointContentSpec{
				PodUID:          string(pod.UID),
				PodNamespace:    pc.Namespace,
				PodName:         pc.Spec.PodName,
				StorageLocation: pc.Spec.StorageLocation,
				DeletionPolicy:  boolToPolicy(pc.Spec.RetainAfterRestore),
			},
		}
		if err := r.Create(ctx, &pcc); err != nil {
			return ctrl.Result{}, err
		}
	case err != nil:
		return ctrl.Result{}, err
	}

	//-------------------------------------------------------------------//
	// 6· Aggregate readiness
	//-------------------------------------------------------------------//
	ready := true
	for _, c := range pod.Spec.Containers {
		cccName := fmt.Sprintf("%s-%s-%s", pc.Name, c.Name, c.Name)
		var ccc checkpointv1.ContainerCheckpointContent
		if err := r.Get(ctx, client.ObjectKey{Name: cccName}, &ccc); err != nil ||
			!ccc.Status.ReadyToRestore || ccc.Status.ErrorMessage != "" {
			ready = false
			break
		}
	}

	if ready && !pc.Status.ReadyToRestore {
		now := metav1.Now()
		pc.Status.ReadyToRestore = true
		pc.Status.CheckpointTime = &now
		_ = r.Status().Update(ctx, &pc)

		pcc.Status.ReadyToRestore = true
		pcc.Status.CheckpointTime = &now
		_ = r.Status().Update(ctx, &pcc)

		log.Info("pod checkpoint ready", "name", pc.Name)
		return ctrl.Result{}, nil
	}

	return ctrl.Result{RequeueAfter: requeuePeriod}, nil
}

func (r *PodCheckpointReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&checkpointv1.PodCheckpoint{}).
		Complete(r)
}

func boolToPolicy(b bool) string {
	if b {
		return "Retain"
	}
	return "Delete"
}
