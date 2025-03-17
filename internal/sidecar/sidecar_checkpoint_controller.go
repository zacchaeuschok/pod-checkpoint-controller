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
	log := log.FromContext(ctx)

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
	url := fmt.Sprintf("https://127.0.0.1:10250/checkpoint/%s/%s/%s",
		checkpoint.Spec.Namespace, checkpoint.Spec.PodName, checkpoint.Spec.Container)

	// Load client certificate from mounted volume - Using kubelet's own client certificates
	cert, err := tls.LoadX509KeyPair(
		"/var/lib/kubelet/pki/kubelet.crt",
		"/var/lib/kubelet/pki/kubelet.key",
	)
	if err != nil {
		log.Error(err, "failed to load client certificates")
		checkpoint.Status.State = "Failed"
		checkpoint.Status.Message = fmt.Sprintf("Failed to load client certificates: %v", err)
		if updateErr := r.Status().Update(ctx, &checkpoint); updateErr != nil {
			log.Error(updateErr, "failed to update checkpoint status")
		}
		return ctrl.Result{}, err
	}

	// Load CA certificate
	caCert, err := os.ReadFile("/etc/kubernetes/pki/ca.crt")
	if err != nil {
		log.Error(err, "failed to load CA certificate")
		checkpoint.Status.State = "Failed"
		checkpoint.Status.Message = fmt.Sprintf("Failed to load CA certificate: %v", err)
		if updateErr := r.Status().Update(ctx, &checkpoint); updateErr != nil {
			log.Error(updateErr, "failed to update checkpoint status")
		}
		return ctrl.Result{}, err
	}

	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		err := fmt.Errorf("failed to parse CA certificate")
		log.Error(err, "failed to parse CA certificate")
		checkpoint.Status.State = "Failed"
		checkpoint.Status.Message = "Failed to parse CA certificate"
		if updateErr := r.Status().Update(ctx, &checkpoint); updateErr != nil {
			log.Error(updateErr, "failed to update checkpoint status")
		}
		return ctrl.Result{}, err
	}

	tlsConfig := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		RootCAs:            caCertPool,
		InsecureSkipVerify: true,
	}
	httpClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: tlsConfig},
		Timeout:   10 * time.Second,
	}

	// Exponential backoff settings
	maxRetries := 5
	backoff := 2 * time.Second
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		resp, err := httpClient.Post(url, "application/json", nil)
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			// Successfully triggered checkpoint
			checkpoint.Status.State = "Completed"
			checkpoint.Status.Message = "Checkpoint completed successfully"
			if updateErr := r.Status().Update(ctx, &checkpoint); updateErr != nil {
				log.Error(updateErr, "failed to update checkpoint status")
				return ctrl.Result{}, updateErr
			}
			return ctrl.Result{}, nil
		}

		if err != nil {
			lastErr = err
			log.Error(err, "checkpoint request failed", "attempt", i+1)
		} else {
			lastErr = fmt.Errorf("unexpected status code: %d", resp.StatusCode)
			log.Error(lastErr, "checkpoint request returned non-success", "attempt", i+1)
			resp.Body.Close()
		}

		// Don't sleep on the last attempt
		if i < maxRetries-1 {
			time.Sleep(backoff)
			backoff *= 2
		}
	}

	// Update CR status with failure message
	checkpoint.Status.State = "Failed"
	checkpoint.Status.Message = fmt.Sprintf("Checkpoint failed after %d attempts: %v", maxRetries, lastErr)
	if updateErr := r.Status().Update(ctx, &checkpoint); updateErr != nil {
		log.Error(updateErr, "failed to update checkpoint status")
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
