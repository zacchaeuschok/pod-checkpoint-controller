package sidecar

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
	checkpointv1 "github.com/zacchaeuschok/pod-checkpoint-controller/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	// Finalizer to ensure we clean up checkpoint files when CCC is deleted
	sidecarFinalizer = "checkpointfiles.deletion.sidecar"

	// Default kubelet certificates
	checkpointCertFile = "/etc/kubernetes/pki/apiserver-kubelet-client.crt"
	checkpointKeyFile  = "/etc/kubernetes/pki/apiserver-kubelet-client.key"
	checkpointCAFile   = "/var/lib/kubelet/pki/kubelet.crt"
)

// CheckpointReconciler watches ContainerCheckpointContent
// (cluster-scoped) and performs local container checkpoint creation by calling
// the kubelet's /checkpoint/<namespace>/<pod>/<container> API if the Pod is on
// this node. It also optionally copies the resulting file to remote storage.
type CheckpointReconciler struct {
	client.Client
	checkpointMgr *CheckpointManager
	nodeName      string
	log           logr.Logger
}

func CreateCheckpointReconciler(client client.Client, log logr.Logger) (*CheckpointReconciler, error) {
	log.Info("Creating CheckpointReconciler")

	// Get current node name from environment
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		log.Error(fmt.Errorf("NODE_NAME not set"), "NODE_NAME environment variable is required")
		return nil, fmt.Errorf("NODE_NAME environment variable is required")
	}
	log.Info("Checkpoint reconciler using node name", "nodeName", nodeName)

	// Create checkpoint manager
	log.Info("Creating checkpoint manager")
	checkpointMgr, err := NewCheckpointManager(client, log.WithName("checkpoint-manager"))
	if err != nil {
		log.Error(err, "Failed to create checkpoint manager")
		return nil, fmt.Errorf("failed to create checkpoint manager: %w", err)
	}
	log.Info("Checkpoint manager created successfully")

	return &CheckpointReconciler{
		Client:        client,
		checkpointMgr: checkpointMgr,
		nodeName:      nodeName,
		log:           log,
	}, nil
}

// SetupWithManager wires up a watch on ContainerCheckpointContent objects.
func (r *CheckpointReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.log.Info("Setting up CheckpointReconciler with manager")

	// Add field indexer for Pod.spec.nodeName to enable finding pods by node
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Pod{}, "spec.nodeName", func(obj client.Object) []string {
		pod := obj.(*corev1.Pod)
		return []string{pod.Spec.NodeName}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&checkpointv1.ContainerCheckpointContent{}).
		Complete(r)
}

// Reconcile ensures that if ContainerCheckpointContent isn't yet "ReadyToRestore"
// (and no error), we call the kubelet to produce the checkpoint data. If
// StorageLocation is remote, we copy the file(s). Finally, we mark the content ready.
func (r *CheckpointReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	startTime := time.Now()
	log := r.log.WithValues("containerCheckpointContent", req.Name)
	log.Info("Starting reconciliation of ContainerCheckpointContent")
	defer func() {
		log.Info("Completed reconciliation of ContainerCheckpointContent",
			"durationMs", time.Since(startTime).Milliseconds())
	}()

	// 1. Load ContainerCheckpointContent
	var ccc checkpointv1.ContainerCheckpointContent
	if err := r.Get(ctx, client.ObjectKey{Name: req.Name}, &ccc); err != nil {
		log.Error(err, "Failed to get ContainerCheckpointContent")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.Info("Successfully loaded ContainerCheckpointContent",
		"readyToRestore", ccc.Status.ReadyToRestore,
		"hasError", ccc.Status.ErrorMessage != "")

	// 2. Handle finalizer for cleanup
	if !ccc.DeletionTimestamp.IsZero() {
		log.Info("ContainerCheckpointContent is being deleted",
			"deletionTimestamp", ccc.DeletionTimestamp)
		if controllerutil.ContainsFinalizer(&ccc, sidecarFinalizer) {
			log.Info("Processing finalizer for ContainerCheckpointContent")
			// Clean up checkpoint files before removing finalizer
			if err := r.checkpointMgr.CleanupCheckpointFiles(ccc.Name); err != nil {
				log.Error(err, "Failed to clean up checkpoint files")
			} else {
				log.Info("Successfully cleaned up checkpoint files")
			}

			// Remove the finalizer via patch
			patch := client.MergeFrom(ccc.DeepCopy())
			controllerutil.RemoveFinalizer(&ccc, sidecarFinalizer)
			if err := r.Patch(ctx, &ccc, patch); err != nil {
				log.Error(err, "Failed to remove finalizer from ContainerCheckpointContent")
				return ctrl.Result{}, err
			}
			log.Info("Successfully removed finalizer from ContainerCheckpointContent")
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer if it doesn't exist
	if !controllerutil.ContainsFinalizer(&ccc, sidecarFinalizer) {
		log.Info("Adding finalizer to ContainerCheckpointContent")
		// Add the finalizer via patch
		patch := client.MergeFrom(ccc.DeepCopy())
		controllerutil.AddFinalizer(&ccc, sidecarFinalizer)
		if err := r.Patch(ctx, &ccc, patch); err != nil {
			log.Error(err, "Failed to add finalizer to ContainerCheckpointContent")
			return ctrl.Result{}, err
		}
		log.Info("Successfully added finalizer to ContainerCheckpointContent")
	}

	// 3. Validate the parent ContainerCheckpoint
	log.Info("Validating parent ContainerCheckpoint")
	if ccc.Spec.ContainerCheckpointRef == nil {
		log.Error(nil, "Missing ContainerCheckpointRef in spec")
		r.setErrorStatus(ctx, &ccc, "missing ContainerCheckpointRef")
		return ctrl.Result{}, nil
	}

	var cc checkpointv1.ContainerCheckpoint
	key := client.ObjectKey{
		Namespace: ccc.Spec.ContainerCheckpointRef.Namespace,
		Name:      ccc.Spec.ContainerCheckpointRef.Name,
	}
	log.Info("Looking up parent ContainerCheckpoint",
		"namespace", key.Namespace, "name", key.Name)
	if err := r.Get(ctx, key, &cc); err != nil {
		log.Error(err, "Failed to find parent ContainerCheckpoint",
			"namespace", key.Namespace, "name", key.Name)
		r.setErrorStatus(ctx, &ccc, fmt.Sprintf("failed to find parent ContainerCheckpoint: %v", err))
		return ctrl.Result{}, nil
	}
	log.Info("Successfully found parent ContainerCheckpoint")

	if cc.Spec.Source.PodName == nil || cc.Spec.Source.ContainerName == nil {
		log.Error(nil, "Invalid container checkpoint source",
			"podNameNil", cc.Spec.Source.PodName == nil,
			"containerNameNil", cc.Spec.Source.ContainerName == nil)
		r.setErrorStatus(ctx, &ccc, "invalid container checkpoint source")
		return ctrl.Result{}, nil
	}
	log.Info("ContainerCheckpoint source is valid",
		"podName", *cc.Spec.Source.PodName,
		"containerName", *cc.Spec.Source.ContainerName)

	// 4. Check if the CCC is already ReadyToRestore
	if ccc.Status.ReadyToRestore {
		log.Info("ContainerCheckpointContent is already ready to restore, skipping reconciliation")
		return ctrl.Result{}, nil
	}

	// Check if we're already processing this CCC (has checkpoint time but not ready)
	if ccc.Status.CheckpointTime != nil && !ccc.Status.ReadyToRestore {
		timeSinceCheckpoint := time.Since(ccc.Status.CheckpointTime.Time)
		log.Info("ContainerCheckpointContent is currently being processed",
			"checkpointTime", ccc.Status.CheckpointTime,
			"timeSince", timeSinceCheckpoint)

		// If it's been less than 5 minutes since the last checkpoint attempt,
		// don't create another checkpoint to avoid duplication
		if timeSinceCheckpoint < 5*time.Minute {
			log.Info("Skipping reconciliation to avoid duplicate checkpoint creation",
				"nextAttemptIn", (5*time.Minute - timeSinceCheckpoint).String())
			return ctrl.Result{RequeueAfter: 5*time.Minute - timeSinceCheckpoint}, nil
		}

		// Otherwise, continue and retry the checkpoint
		log.Info("Previous checkpoint attempt timed out, retrying")
	}

	// 5. Check if the Pod is on this node
	var pod corev1.Pod
	podKey := client.ObjectKey{
		Namespace: cc.Namespace,
		Name:      *cc.Spec.Source.PodName,
	}
	log.Info("Looking up pod", "namespace", podKey.Namespace, "name", podKey.Name)
	if err := r.Get(ctx, podKey, &pod); err != nil {
		log.Error(err, "Failed to find pod",
			"namespace", podKey.Namespace, "name", podKey.Name)
		r.setErrorStatus(ctx, &ccc, fmt.Sprintf("failed to find pod %s: %v", *cc.Spec.Source.PodName, err))
		return ctrl.Result{}, nil
	}
	log.Info("Successfully found pod", "podNodeName", pod.Spec.NodeName)

	// Only process if the pod is on this node
	if pod.Spec.NodeName != r.nodeName {
		log.Info("Pod is not on this node, skipping",
			"podNode", pod.Spec.NodeName,
			"thisNode", r.nodeName)
		return ctrl.Result{}, nil
	}
	log.Info("Pod is on this node, proceeding with checkpoint")

	// 6. Create the checkpoint using the kubelet API
	log.Info("Creating checkpoint",
		"namespace", cc.Namespace,
		"pod", *cc.Spec.Source.PodName,
		"container", *cc.Spec.Source.ContainerName)

	// Mark that we're starting the checkpoint process by setting the checkpoint time
	// This helps prevent duplicate checkpoint attempts
	patch := client.MergeFrom(ccc.DeepCopy())
	ccc.Status.CheckpointTime = &metav1.Time{Time: time.Now()}
	if err := r.Status().Patch(ctx, &ccc, patch); err != nil {
		log.Error(err, "Failed to update checkpoint time")
		return ctrl.Result{RequeueAfter: time.Second * 5}, nil
	}

	checkpointStartTime := time.Now()
	checkpointFiles, err := r.checkpointMgr.CreateCheckpoint(
		ctx,
		cc.Namespace,
		*cc.Spec.Source.PodName,
		*cc.Spec.Source.ContainerName,
	)
	checkpointDuration := time.Since(checkpointStartTime)
	log.Info("CreateCheckpoint operation completed",
		"durationMs", checkpointDuration.Milliseconds(),
		"success", err == nil)

	if err != nil {
		log.Error(err, "Failed to create checkpoint")
		r.setErrorStatus(ctx, &ccc, fmt.Sprintf("failed to create checkpoint: %v", err))
		return ctrl.Result{RequeueAfter: time.Second * 30}, nil
	}

	if len(checkpointFiles) == 0 {
		log.Error(nil, "No checkpoint files were created")
		r.setErrorStatus(ctx, &ccc, "no checkpoint files were created")
		return ctrl.Result{RequeueAfter: time.Second * 30}, nil
	}

	log.Info("Created checkpoint files successfully",
		"fileCount", len(checkpointFiles),
		"files", checkpointFiles)

	// 7. If StorageLocation specifies a remote location, copy files there
	if ccc.Spec.StorageLocation != "" {
		storageLocation := ccc.Spec.StorageLocation
		log.Info("Processing storage location", "storageLocation", storageLocation)

		// Parse the storage location - handle both node://nodeName and direct node name formats
		targetNode := storageLocation
		if strings.HasPrefix(storageLocation, "node://") {
			targetNode = strings.TrimPrefix(storageLocation, "node://")
		}

		log.Info("Target node identified for checkpoint transfer", "targetNode", targetNode)

		// Transfer each checkpoint file to the target node
		for i, checkpointFile := range checkpointFiles {
			log.Info("Starting transfer of checkpoint file",
				"fileIndex", i,
				"source", checkpointFile,
				"destination", targetNode)

			transferStartTime := time.Now()
			err := r.checkpointMgr.TransferCheckpoint(ctx, checkpointFile, targetNode)
			transferDuration := time.Since(transferStartTime)

			if err != nil {
				log.Error(err, "Failed to transfer checkpoint",
					"fileIndex", i,
					"source", checkpointFile,
					"destination", targetNode,
					"durationMs", transferDuration.Milliseconds())
				r.setErrorStatus(ctx, &ccc, fmt.Sprintf("failed to transfer checkpoint: %v", err))
				return ctrl.Result{RequeueAfter: time.Second * 30}, nil
			}

			log.Info("Successfully transferred checkpoint file",
				"fileIndex", i,
				"source", checkpointFile,
				"destination", targetNode,
				"durationMs", transferDuration.Milliseconds())
		}
	} else {
		log.Info("No remote storage location specified, keeping checkpoint local")
	}

	// 8. Mark the ContainerCheckpointContent as ReadyToRestore
	log.Info("Marking ContainerCheckpointContent as ReadyToRestore")
	return r.updateStatusReady(ctx, &ccc)
}

// updateStatusReady marks the ContainerCheckpointContent as ready to restore
func (r *CheckpointReconciler) updateStatusReady(
	ctx context.Context,
	ccc *checkpointv1.ContainerCheckpointContent,
) (ctrl.Result, error) {
	log := r.log.WithValues("containerCheckpointContent", ccc.Name)
	log.Info("Updating ContainerCheckpointContent status to ready")

	startTime := time.Now()
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get fresh copy
		var fresh checkpointv1.ContainerCheckpointContent
		if err := r.Get(ctx, client.ObjectKey{Name: ccc.Name}, &fresh); err != nil {
			log.Error(err, "Failed to get fresh copy of ContainerCheckpointContent")
			return err
		}

		fresh.Status.ReadyToRestore = true
		fresh.Status.ErrorMessage = ""
		fresh.Status.CheckpointTime = &metav1.Time{Time: time.Now()}

		return r.Status().Update(ctx, &fresh)
	}); err != nil {
		log.Error(err, "Failed to update ContainerCheckpointContent status")
		return ctrl.Result{}, err
	}

	log.Info("Successfully updated ContainerCheckpointContent status to ready",
		"durationMs", time.Since(startTime).Milliseconds())
	return ctrl.Result{}, nil
}

// setErrorStatus sets the status error field
func (r *CheckpointReconciler) setErrorStatus(
	ctx context.Context,
	ccc *checkpointv1.ContainerCheckpointContent,
	msg string,
) {
	log := r.log.WithValues("containerCheckpointContent", ccc.Name)
	log.Error(nil, "Setting error status", "errorMessage", msg)

	startTime := time.Now()
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get fresh copy
		var fresh checkpointv1.ContainerCheckpointContent
		if err := r.Get(ctx, client.ObjectKey{Name: ccc.Name}, &fresh); err != nil {
			log.Error(err, "Failed to get fresh copy of ContainerCheckpointContent for error status")
			return err
		}

		fresh.Status.ReadyToRestore = false
		fresh.Status.ErrorMessage = msg

		return r.Status().Update(ctx, &fresh)
	}); err != nil {
		log.Error(err, "Failed to update ContainerCheckpointContent error status", "errorMessage", msg)
	} else {
		log.Info("Successfully updated ContainerCheckpointContent error status",
			"durationMs", time.Since(startTime).Milliseconds())
	}
}
