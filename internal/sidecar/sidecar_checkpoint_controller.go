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

package sidecar

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"github.com/go-logr/logr"
	"io"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/retry"
	"net/http"
	"os"
	"time"

	checkpointv1 "github.com/example/external-checkpointer/api/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	kubeletClientCert = "/etc/kubernetes/pki/apiserver-kubelet-client.crt"
	kubeletClientKey  = "/etc/kubernetes/pki/apiserver-kubelet-client.key"
	caCertFile        = "/var/lib/kubelet/pki/kubelet.crt"
	maxRetries        = 5
	initialBackoff    = 2 * time.Second
	requestTimeout    = 10 * time.Second
)

// ContainerCheckpointContentSidecarReconciler watches ContainerCheckpointContent
// and performs the actual container checkpointing via kubelet's /checkpoint API.
type ContainerCheckpointContentSidecarReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// Reconcile gets called whenever a ContainerCheckpointContent event occurs.

func (r *ContainerCheckpointContentSidecarReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrl.Log.WithValues("ContainerCheckpointContent", req.NamespacedName)

	// 1. Fetch the ContainerCheckpointContent (cluster-scoped)
	var content checkpointv1.ContainerCheckpointContent
	if err := r.Get(ctx, req.NamespacedName, &content); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("ContainerCheckpointContent not found; ignoring.")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to fetch ContainerCheckpointContent")
		return ctrl.Result{}, err
	}

	if content.Status.ReadyToRestore || content.Status.ErrorMessage != "" {
		return ctrl.Result{}, nil
	}

	logger.Info("Sidecar reconciling ContainerCheckpointContent", "name", content.Name)

	// 2. Get the associated ContainerCheckpoint to obtain Pod/Container info
	ref := content.Spec.ContainerCheckpointRef
	var checkpoint checkpointv1.ContainerCheckpoint
	if err := r.Get(ctx, client.ObjectKey{Name: ref.Name, Namespace: ref.Namespace}, &checkpoint); err != nil {
		msg := fmt.Sprintf("Cannot fetch associated ContainerCheckpoint: %v", err)
		logger.Error(err, msg)
		content.Status.ErrorMessage = msg
		_ = r.Status().Update(ctx, &content)
		return ctrl.Result{}, err
	}

	// 2a. Fetch the Pod using its name and namespace.
	var pod corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{Name: checkpoint.Spec.Source.PodName, Namespace: checkpoint.Namespace}, &pod); err != nil {
		msg := fmt.Sprintf("Failed to fetch Pod %q: %v", checkpoint.Spec.Source.PodName, err)
		logger.Error(err, msg)
		content.Status.ErrorMessage = msg
		_ = r.Status().Update(ctx, &content)
		return ctrl.Result{}, err
	}
	// Compare the Pod's node with the sidecar's node.
	currentNode := os.Getenv("NODE_NAME")
	if pod.Spec.NodeName != currentNode {
		logger.Info("Skipping checkpoint; pod is on a different node", "podNode", pod.Spec.NodeName, "currentNode", currentNode)
		return ctrl.Result{}, nil
	}

	// 3. Build the URL for the kubelet checkpoint API.
	url := fmt.Sprintf("https://%s:10250/checkpoint/%s/%s/%s",
		currentNode,
		ref.Namespace,
		checkpoint.Spec.Source.PodName,
		checkpoint.Spec.Source.ContainerName,
	)
	logger.Info("Calling kubelet checkpoint endpoint", "url", url)

	httpClient, err := r.newHTTPClient(logger)
	if err != nil {
		msg := fmt.Sprintf("Failed to create kubelet HTTP client: %v", err)
		logger.Error(err, msg)
		content.Status.ErrorMessage = msg
		_ = r.Status().Update(ctx, &content)
		return ctrl.Result{}, err
	}

	// 4. Attempt the checkpoint request with exponential backoff.
	success, respBody, err := r.doHTTPRequestWithBackoff(ctx, httpClient, http.MethodPost, url, logger)
	if err != nil {
		msg := fmt.Sprintf("Failed to checkpoint after %d attempts: %v", maxRetries, err)
		logger.Error(err, msg)
		content.Status.ErrorMessage = msg
		_ = r.Status().Update(ctx, &content)
		return ctrl.Result{}, err
	}

	if success {
		logger.Info("Kubelet checkpoint successful", "name", content.Name, "response", string(respBody))
		// Retry update on conflict.
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var latestContent checkpointv1.ContainerCheckpointContent
			if err := r.Get(ctx, req.NamespacedName, &latestContent); err != nil {
				return err
			}
			latestContent.Status.ReadyToRestore = true
			latestContent.Status.ErrorMessage = ""
			return r.Status().Update(ctx, &latestContent)
		})
		if err != nil {
			logger.Error(err, "Failed to update ContainerCheckpointContent status after retry")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager tells Kubebuilder how to set up this sidecar controller
// to watch ContainerCheckpointContent (cluster-scoped).
func (r *ContainerCheckpointContentSidecarReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&checkpointv1.ContainerCheckpointContent{}).
		Complete(r)
}

// newHTTPClient loads TLS credentials to talk to the kubelet and returns an *http.Client
func (r *ContainerCheckpointContentSidecarReconciler) newHTTPClient(logger logr.Logger) (*http.Client, error) {
	cert, err := tls.LoadX509KeyPair(kubeletClientCert, kubeletClientKey)
	if err != nil {
		return nil, fmt.Errorf("failed loading key pair: %w", err)
	}
	caData, err := os.ReadFile(caCertFile)
	if err != nil {
		logger.Info("Could not read CA cert file, will skip verifying the kubelet CA", "file", caCertFile, "err", err)
	}
	caCertPool := x509.NewCertPool()
	insecureSkip := true
	if len(caData) > 0 && caCertPool.AppendCertsFromPEM(caData) {
		insecureSkip = false
	}

	tlsConfig := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: insecureSkip,
	}
	if !insecureSkip {
		tlsConfig.RootCAs = caCertPool
	}

	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
		Timeout: requestTimeout,
	}, nil
}

// doHTTPRequestWithBackoff attempts an HTTP request up to maxRetries, doubling the backoff on each fail.
func (r *ContainerCheckpointContentSidecarReconciler) doHTTPRequestWithBackoff(
	ctx context.Context,
	client *http.Client,
	method, url string,
	logger logr.Logger,
) (bool, []byte, error) {

	backoff := initialBackoff
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		req, err := http.NewRequestWithContext(ctx, method, url, nil)
		if err != nil {
			logger.Error(err, "Failed to create HTTP request", "attempt", i+1)
			return false, nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			logger.Error(err, "HTTP request failed", "attempt", i+1)
			lastErr = err
		} else {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()

			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				// Success
				return true, body, nil
			}
			// Non-2XX
			lastErr = fmt.Errorf("kubelet status code %d: %s", resp.StatusCode, string(body))
			logger.Error(lastErr, "Unsuccessful response", "attempt", i+1)
		}

		// Retry after backoff
		time.Sleep(backoff)
		backoff *= 2
	}
	return false, nil, lastErr
}
