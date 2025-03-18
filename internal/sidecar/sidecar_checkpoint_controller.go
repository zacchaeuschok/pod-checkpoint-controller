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
	"io"
	"net/http"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	checkpointingv1alpha1 "github.com/example/external-checkpointer/api/v1alpha1"
)

// SidecarCheckpointReconciler reconciles ContainerCheckpoint objects for the local node
type SidecarCheckpointReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	NodeName string
}

// +kubebuilder:rbac:groups=checkpointing.zacchaeuschok.github.io,resources=containercheckpoints,verbs=get;list;watch
// +kubebuilder:rbac:groups=checkpointing.zacchaeuschok.github.io,resources=containercheckpoints/status,verbs=get;update;patch

func (r *SidecarCheckpointReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the ContainerCheckpoint instance
	var checkpoint checkpointingv1alpha1.ContainerCheckpoint
	if err := r.Get(ctx, req.NamespacedName, &checkpoint); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Skip if this CR is not for the current node
	if checkpoint.Spec.NodeName != "" && checkpoint.Spec.NodeName != r.NodeName {
		return ctrl.Result{}, nil
	}

	// Construct the Kubelet checkpoint URL
	url := fmt.Sprintf("https://%s:10250/checkpoint/%s/%s/%s",
		r.NodeName,
		checkpoint.Spec.Namespace,
		checkpoint.Spec.PodName,
		checkpoint.Spec.Container,
	)
	logger.Info("Constructed checkpoint URL", "url", url)

	// Load API server's kubelet client certificate from mounted volume.
	// The kubelet typically trusts this certificate for checkpoint operations.
	cert, err := tls.LoadX509KeyPair(
		"/etc/kubernetes/pki/apiserver-kubelet-client.crt",
		"/etc/kubernetes/pki/apiserver-kubelet-client.key",
	)
	if err != nil {
		logger.Error(err, "failed to load API server kubelet client certificates")
		checkpoint.Status.State = "Failed"
		checkpoint.Status.Message = fmt.Sprintf("Failed to load API server kubelet client certificates: %v", err)
		_ = r.Status().Update(ctx, &checkpoint)
		return ctrl.Result{}, err
	}
	logger.Info("Loaded API server kubelet client certificate successfully")

	// Load CA certificate
	caCert, err := os.ReadFile("/etc/kubernetes/pki/ca.crt")
	if err != nil {
		logger.Error(err, "failed to load CA certificate")
		checkpoint.Status.State = "Failed"
		checkpoint.Status.Message = fmt.Sprintf("Failed to load CA certificate: %v", err)
		_ = r.Status().Update(ctx, &checkpoint)
		return ctrl.Result{}, err
	}
	logger.Info("Loaded CA certificate successfully")

	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		err := fmt.Errorf("failed to parse CA certificate")
		logger.Error(err, "failed to parse CA certificate")
		checkpoint.Status.State = "Failed"
		checkpoint.Status.Message = "Failed to parse CA certificate"
		_ = r.Status().Update(ctx, &checkpoint)
		return ctrl.Result{}, err
	}

	tlsConfig := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		RootCAs:            caCertPool,
		InsecureSkipVerify: true,
	}
	logger.Info("TLS configuration established", "InsecureSkipVerify", tlsConfig.InsecureSkipVerify)

	httpClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: tlsConfig},
		Timeout:   10 * time.Second,
	}

	// Note: We are not loading or sending a service account token here.
	// The checkpoint endpoint will be authenticated via the client certificate alone.

	// Exponential backoff settings
	maxRetries := 5
	backoff := 2 * time.Second
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		logger.Info("Attempting checkpoint request", "attempt", i+1, "url", url)
		reqHTTP, err := http.NewRequest("POST", url, nil)
		if err != nil {
			logger.Error(err, "failed to create new request", "attempt", i+1)
			checkpoint.Status.State = "Failed"
			checkpoint.Status.Message = fmt.Sprintf("Failed to create new request: %v", err)
			_ = r.Status().Update(ctx, &checkpoint)
			return ctrl.Result{}, err
		}

		// Do not set the Authorization header, since the client certificate is used for auth.
		reqHTTP.Header.Set("Content-Type", "application/json")
		logger.Info("HTTP request headers set", "Content-Type", reqHTTP.Header.Get("Content-Type"))

		resp, err := httpClient.Do(reqHTTP)
		if err != nil {
			lastErr = err
			logger.Error(err, "checkpoint request failed", "attempt", i+1)
		} else {
			bodyBytes, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			logger.Info("Received response", "attempt", i+1, "statusCode", resp.StatusCode, "body", string(bodyBytes))
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				// Successfully triggered checkpoint
				checkpoint.Status.State = "Completed"
				checkpoint.Status.Message = "Checkpoint completed successfully"
				if updateErr := r.Status().Update(ctx, &checkpoint); updateErr != nil {
					logger.Error(updateErr, "failed to update checkpoint status after success")
					return ctrl.Result{}, updateErr
				}
				return ctrl.Result{}, nil
			}
			lastErr = fmt.Errorf("unexpected status code: %d, response body: %s", resp.StatusCode, string(bodyBytes))
			logger.Error(lastErr, "checkpoint request returned non-success", "attempt", i+1)
		}

		if i < maxRetries-1 {
			logger.Info("Sleeping before next attempt", "sleepDuration", backoff)
			time.Sleep(backoff)
			backoff *= 2
		}
	}

	checkpoint.Status.State = "Failed"
	checkpoint.Status.Message = fmt.Sprintf("Checkpoint failed after %d attempts: %v", maxRetries, lastErr)
	if updateErr := r.Status().Update(ctx, &checkpoint); updateErr != nil {
		logger.Error(updateErr, "failed to update checkpoint status after exhausting retries")
		return ctrl.Result{}, updateErr
	}

	return ctrl.Result{}, lastErr
}

// SetupWithManager sets up the controller with the Manager.
func (r *SidecarCheckpointReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&checkpointingv1alpha1.ContainerCheckpoint{}).
		Complete(r)
}
