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

package main

import (
	"flag"
	"os"

	checkpointv1 "github.com/zacchaeuschok/pod-checkpoint-controller/api/v1"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/zacchaeuschok/pod-checkpoint-controller/internal/sidecar"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	// Register core K8s types + your ContainerCheckpointContent in the scheme
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(checkpointv1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var probeAddr string

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080",
		"The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081",
		"The address the probe endpoint binds to.")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// We no longer need to start a file server with our new implementation

	// If you need to filter by node, or specify a node name for logging, read it here
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		setupLog.Info("NODE_NAME environment variable not set; proceeding without node filtering.")
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Set up the checkpoint reconciler to watch ContainerCheckpointContent
	reconciler, err := sidecar.CreateCheckpointReconciler(
		mgr.GetClient(),
		ctrl.Log.WithName("checkpoint-controller"),
	)
	if err != nil {
		setupLog.Error(err, "unable to create checkpoint controller")
		os.Exit(1)
	}
	
	if err = reconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to set up checkpoint controller", "controller", "CheckpointReconciler")
		os.Exit(1)
	}

	setupLog.Info("Starting sidecar manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
