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
	"time"

	"github.com/go-logr/logr"
	checkpointv1 "github.com/zacchaeuschok/pod-checkpoint-controller/api/v1"
	corev1 "k8s.io/api/core/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	sidecarFinalizer = "checkpointfiles.deletion.sidecar"

	certFile       = "/etc/kubernetes/pki/apiserver-kubelet-client.crt"
	keyFile        = "/etc/kubernetes/pki/apiserver-kubelet-client.key"
	caFile         = "/var/lib/kubelet/pki/kubelet.crt"
	kubeletPort    = 10250
	maxRetries     = 5
	initialBackoff = 2 * time.Second
	requestTimeout = 10 * time.Second
)

// ContainerCheckpointContentSidecarReconciler runs as a DaemonSet on every node.
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

	// --------------------------------------------------------------------- //
	// Deletion / finaliser
	// --------------------------------------------------------------------- //
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

	// Already processed?
	if ccc.Status.ReadyToRestore || ccc.Status.ErrorMessage != "" {
		return ctrl.Result{}, nil
	}

	// --------------------------------------------------------------------- //
	// Resolve owning ContainerCheckpoint
	// --------------------------------------------------------------------- //
	var cc checkpointv1.ContainerCheckpoint
	if err := r.Get(ctx,
		client.ObjectKey{
			Namespace: ccc.Spec.ContainerCheckpointRef.Namespace,
			Name:      ccc.Spec.ContainerCheckpointRef.Name,
		}, &cc); err != nil {
		if apierrs.IsNotFound(err) {
			return r.fail(ctx, &ccc, "owning ContainerCheckpoint not found")
		}
		return ctrl.Result{}, err
	}

	// --------------------------------------------------------------------- //
	// Ensure pod is on this node
	// --------------------------------------------------------------------- //
	var pod corev1.Pod
	if err := r.Get(ctx,
		client.ObjectKey{Namespace: cc.Namespace, Name: cc.Spec.Source.PodName},
		&pod); err != nil {
		return r.fail(ctx, &ccc, "pod not found")
	}
	nodeName := os.Getenv("NODE_NAME")
	if pod.Spec.NodeName != nodeName {
		// not our node – skip
		return ctrl.Result{}, nil
	}

	// --------------------------------------------------------------------- //
	// Call kubelet /checkpoint
	// --------------------------------------------------------------------- //
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

	// --------------------------------------------------------------------- //
	// Optional remote copy
	// --------------------------------------------------------------------- //
	if cc.Spec.StorageLocation != "" && cc.Spec.StorageLocation != "local" {
		if err := copyFile(body, cc.Spec.StorageLocation, log); err != nil {
			return r.fail(ctx, &ccc, err.Error())
		}
	}

	// --------------------------------------------------------------------- //
	// Mark Ready
	// --------------------------------------------------------------------- //
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

// SetupWithManager registers the sidecar reconciler.
func (r *ContainerCheckpointContentSidecarReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&checkpointv1.ContainerCheckpointContent{}).
		Complete(r)
}

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
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				Certificates:       []tls.Certificate{cert},
				RootCAs:            cp,
				InsecureSkipVerify: insecure,
			},
		},
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

func copyFile(resp []byte, destDir string, l logr.Logger) error {
	var parsed struct {
		Items []string `json:"items"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil || len(parsed.Items) == 0 {
		return fmt.Errorf("unable to parse kubelet response")
	}
	src := parsed.Items[0]
	dst := filepath.Join(destDir, filepath.Base(src))
	from, err := os.Open(src)
	if err != nil {
		return err
	}
	defer from.Close()
	to, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer to.Close()
	_, err = io.Copy(to, from)
	l.Info("checkpoint copied", "to", dst)
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

// helpers
func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
func remove(slice []string, s string) []string {
	out := slice[:0]
	for _, v := range slice {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}
