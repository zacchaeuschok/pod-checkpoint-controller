// Copyright 2025.
// SPDX‑License‑Identifier: Apache‑2.0

package sidecar

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-logr/logr"

	checkpointv1 "github.com/zacchaeuschok/pod-checkpoint-controller/api/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// -----------------------------------------------------------------------------
// constants & helpers
// -----------------------------------------------------------------------------

const (
	sidecarFinalizer = "checkpointfiles.deletion.sidecar"

	certFile = "/etc/kubernetes/pki/apiserver-kubelet-client.crt"
	keyFile  = "/etc/kubernetes/pki/apiserver-kubelet-client.key"
	caFile   = "/var/lib/kubelet/pki/kubelet.crt"

	kubeletPort    = 10250
	maxRetries     = 5
	initialBackoff = 2 * time.Second
	requestTimeout = 10 * time.Second

	restoreAnn     = "checkpointing.zacchaeuschok.github.io/restore-from"
	restoreDoneAnn = "checkpointing.zacchaeuschok.github.io/restore-done"
)

// -----------------------------------------------------------------------------
// ContainerCheckpointContent reconciler  (create checkpoint)
// -----------------------------------------------------------------------------

type ContainerCheckpointContentSidecarReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *ContainerCheckpointContentSidecarReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("ccc", req.Name)

	var ccc checkpointv1.ContainerCheckpointContent
	if err := r.Get(ctx, req.NamespacedName, &ccc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// --- finaliser --------------------------------------------------------- //
	if !ccc.DeletionTimestamp.IsZero() {
		if contains(ccc.Finalizers, sidecarFinalizer) {
			r.cleanupFiles(log, &ccc)
			ccc.Finalizers = remove(ccc.Finalizers, sidecarFinalizer)
			_ = r.Update(ctx, &ccc)
		}
		return ctrl.Result{}, nil
	}
	if !contains(ccc.Finalizers, sidecarFinalizer) {
		ccc.Finalizers = append(ccc.Finalizers, sidecarFinalizer)
		_ = r.Update(ctx, &ccc)
	}

	if ccc.Status.ReadyToRestore || ccc.Status.ErrorMessage != "" {
		return ctrl.Result{}, nil
	}

	// --- owning ContainerCheckpoint --------------------------------------- //
	var cc checkpointv1.ContainerCheckpoint
	if err := r.Get(ctx,
		client.ObjectKey{
			Namespace: ccc.Spec.ContainerCheckpointRef.Namespace,
			Name:      ccc.Spec.ContainerCheckpointRef.Name,
		}, &cc); err != nil {

		return r.fail(ctx, &ccc, "owning ContainerCheckpoint not found")
	}

	// --- ensure pod is on *this* node ------------------------------------- //
	var pod corev1.Pod
	if err := r.Get(ctx,
		client.ObjectKey{Namespace: cc.Namespace, Name: cc.Spec.Source.PodName},
		&pod); err != nil {

		return r.fail(ctx, &ccc, "pod lookup failed")
	}
	nodeName := os.Getenv("NODE_NAME")
	if pod.Spec.NodeName != nodeName {
		return ctrl.Result{}, nil // other node will handle
	}

	// --- kubelet /checkpoint --------------------------------------------- //
	url := fmt.Sprintf("https://%s:%d/checkpoint/%s/%s/%s",
		nodeName, kubeletPort, cc.Namespace,
		cc.Spec.Source.PodName, cc.Spec.Source.ContainerName)

	httpClient, err := newTLSClient()
	if err != nil {
		return r.fail(ctx, &ccc, err.Error())
	}
	ok, body, err := doWithBackoff(ctx, httpClient, url, log)
	if err != nil || !ok {
		return r.fail(ctx, &ccc, fmt.Sprintf("kubelet error: %v", err))
	}

	// --- optional copy to remote storage ---------------------------------- //
	if cc.Spec.StorageLocation != "" && cc.Spec.StorageLocation != "local" {
		if err := copyFile(body, cc.Spec.StorageLocation, log); err != nil {
			return r.fail(ctx, &ccc, err.Error())
		}
	}

	// --- mark Ready ------------------------------------------------------- //
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest checkpointv1.ContainerCheckpointContent
		if err := r.Get(ctx, req.NamespacedName, &latest); err != nil {
			return err
		}
		latest.Status.ReadyToRestore = true
		latest.Status.ErrorMessage = ""
		return r.Status().Update(ctx, &latest)
	})
	return ctrl.Result{}, err
}

var k8sClient client.Client

func (r *ContainerCheckpointContentSidecarReconciler) SetupWithManager(mgr ctrl.Manager) error {
	k8sClient = mgr.GetClient()
	return ctrl.NewControllerManagedBy(mgr).
		For(&checkpointv1.ContainerCheckpointContent{}).
		Complete(r)
}

// -----------------------------------------------------------------------------
// Pod restore reconciler  (annotation‑driven)
// -----------------------------------------------------------------------------

type PodRestoreSidecarReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *PodRestoreSidecarReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("pod", req.NamespacedName)

	var pod corev1.Pod
	if err := r.Get(ctx, req.NamespacedName, &pod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// annotation present?
	contentName := pod.Annotations[restoreAnn]
	if contentName == "" || pod.Annotations[restoreDoneAnn] == "true" {
		return ctrl.Result{}, nil
	}

	// only act on pods scheduled to *this* node and still Pending / not running
	if pod.Spec.NodeName != os.Getenv("NODE_NAME") ||
		pod.Status.Phase != corev1.PodPending {

		return ctrl.Result{}, nil
	}

	// kubelet /restore
	url := fmt.Sprintf("https://%s:%d/restore/%s/%s?checkpoint=%s",
		pod.Spec.NodeName, kubeletPort, pod.Namespace, pod.Name, contentName)

	httpClient, err := newTLSClient()
	if err != nil {
		return ctrl.Result{}, err
	}
	ok, _, err := doWithBackoff(ctx, httpClient, url, log)
	if err != nil || !ok {
		log.Error(err, "restore failed")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// patch annotation restore-done
	patch := client.MergeFrom(pod.DeepCopy())
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[restoreDoneAnn] = "true"
	if err := r.Patch(ctx, &pod, patch); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("pod restored")
	return ctrl.Result{}, nil
}

func (r *PodRestoreSidecarReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		Complete(r)
}

// -----------------------------------------------------------------------------
// shared utils
// -----------------------------------------------------------------------------

func newTLSClient() (*http.Client, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	caBytes, _ := os.ReadFile(caFile)
	cp := x509.NewCertPool()
	insecure := true
	if cp.AppendCertsFromPEM(caBytes) {
		insecure = false
	}
	return &http.Client{
		Timeout: requestTimeout,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			Certificates:       []tls.Certificate{cert},
			RootCAs:            cp,
			InsecureSkipVerify: insecure,
		}},
	}, nil
}

func doWithBackoff(ctx context.Context, c *http.Client, url string, l logr.Logger) (bool, []byte, error) {
	back := initialBackoff
	var last error
	for i := 0; i < maxRetries; i++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
		resp, err := c.Do(req)
		if err == nil && resp.StatusCode/100 == 2 {
			data, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return true, data, nil
		}
		if err == nil {
			data, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			last = fmt.Errorf("kubelet %d: %s", resp.StatusCode, string(data))
		} else {
			last = err
		}
		l.Info("retry", "err", last, "attempt", i+1)
		time.Sleep(back)
		back *= 2
	}
	return false, nil, last
}

func copyFile(resp []byte, dest string, log logr.Logger) error {
	// dest == "/some/dir"   OR   "node://node2"
	var parsed struct {
		Items []string `json:"items"`
	}
	if json.Unmarshal(resp, &parsed) != nil || len(parsed.Items) == 0 {
		return fmt.Errorf("unable to parse kubelet response")
	}
	src := parsed.Items[0]

	if strings.HasPrefix(dest, "node://") {
		return pushToPeer(src, strings.TrimPrefix(dest, "node://"), log)
	}
	return localCopy(src, filepath.Join(dest, filepath.Base(src)), log)
}

func localCopy(src, dst string, log logr.Logger) error {
	from, err := os.Open(src)
	if err != nil {
		return err
	}
	defer from.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	to, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer to.Close()
	_, err = io.Copy(to, from)
	log.Info("checkpoint copied", "dst", dst)
	return err
}

func pushToPeer(src, node string, log logr.Logger) error {
	// look up InternalIP of the node
	var n corev1.Node
	if err := k8sClient.Get(context.TODO(), client.ObjectKey{Name: node}, &n); err != nil {
		return err
	}
	ip := ""
	for _, a := range n.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			ip = a.Address
			break
		}
	}
	if ip == "" {
		return fmt.Errorf("no InternalIP on node %s", node)
	}

	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	url := fmt.Sprintf("http://%s:8081/checkpoints/%s", ip, filepath.Base(src))
	req, _ := http.NewRequest(http.MethodPut, url, f)
	resp, err := http.DefaultClient.Do(req)
	if err == nil && resp.StatusCode/100 == 2 {
		_ = resp.Body.Close()
		log.Info("pushed checkpoint", "node", node)
		return nil
	}
	if err == nil {
		err = fmt.Errorf("peer %d", resp.StatusCode)
	}
	return err
}

func (r *ContainerCheckpointContentSidecarReconciler) cleanupFiles(l logr.Logger, c *checkpointv1.ContainerCheckpointContent) {
	glob := fmt.Sprintf("/var/lib/kubelet/checkpoints/%s-*", c.Name)
	files, _ := filepath.Glob(glob)
	for _, f := range files {
		if err := os.Remove(f); err == nil {
			l.Info("deleted", "file", f)
		}
	}
}

func (r *ContainerCheckpointContentSidecarReconciler) fail(ctx context.Context, c *checkpointv1.ContainerCheckpointContent, msg string) (ctrl.Result, error) {
	c.Status.ErrorMessage = msg
	_ = r.Status().Update(ctx, c)
	return ctrl.Result{}, nil
}

func contains(sl []string, v string) bool {
	for _, s := range sl {
		if s == v {
			return true
		}
	}
	return false
}
func remove(sl []string, v string) []string {
	out := sl[:0]
	for _, s := range sl {
		if s != v {
			out = append(out, s)
		}
	}
	return out
}
