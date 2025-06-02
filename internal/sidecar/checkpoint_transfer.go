package sidecar

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CheckpointManager handles the creation and transfer of container checkpoints
type CheckpointManager struct {
	// K8s clients
	client    client.Client
	clientset *kubernetes.Clientset
	// Local paths
	checkpointDir string
	// Node information
	nodeName string
	// Logger
	log logr.Logger
}

// NewCheckpointManager creates a new checkpoint manager
// config is a package-level variable for use in exec functions
var config *rest.Config

func NewCheckpointManager(client client.Client, log logr.Logger) (*CheckpointManager, error) {
	// Get the current node name from environment
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		return nil, fmt.Errorf("NODE_NAME environment variable is required")
	}

	// Create Kubernetes clientset for exec operations
	var err error
	config, err = rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get in-cluster config: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create clientset: %w", err)
	}

	return &CheckpointManager{
		client:        client,
		clientset:     clientset,
		checkpointDir: "/var/lib/kubelet/checkpoints",
		nodeName:      nodeName,
		log:           log,
	}, nil
}

// Constants for checkpoint operations
const (
	checkpointTimeout     = 10 * time.Second
	checkpointBackoffSteps   = 5
	checkpointBackoffInitial = 2 * time.Second
	checkpointBackoffFactor  = 2.0
)

// CreateCheckpoint calls the kubelet API to create a checkpoint
func (cm *CheckpointManager) CreateCheckpoint(ctx context.Context, namespace, podName, containerName string) ([]string, error) {
	// Construct the kubelet API URL using the node name instead of localhost
	url := fmt.Sprintf("https://%s:10250/checkpoint/%s/%s/%s", 
		cm.nodeName, namespace, podName, containerName)

	// Create an HTTP client with TLS credentials
	httpClient, err := makeTLSClient(
		checkpointCertFile,
		checkpointKeyFile,
		checkpointCAFile,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create TLS client: %w", err)
	}

	// Call the kubelet API with backoff
	return doCheckpointWithBackoff(ctx, httpClient, url, cm.log)
}

// TransferCheckpoint moves a checkpoint file to another node using kubectl cp
func (cm *CheckpointManager) TransferCheckpoint(ctx context.Context, srcPath, targetNode string) error {
	// If it's a local transfer, just copy the file
	if targetNode == cm.nodeName {
		cm.log.Info("Local transfer - copying file locally", "path", srcPath)
		targetPath := srcPath
		return cm.copyLocal(srcPath, targetPath)
	}

	cm.log.Info("Transferring checkpoint to remote node", "targetNode", targetNode)

	// Find a pod running on the target node to use as transfer destination
	// We'll use a hostPath volume in that pod as an intermediate location
	var pod corev1.Pod
	err := cm.findPodOnNode(ctx, targetNode, &pod)
	if err != nil {
		return fmt.Errorf("failed to find pod on target node: %w", err)
	}

	// Get the basename of the checkpoint file
	fileName := filepath.Base(srcPath)
	
	// Create temporary file name on target node
	targetPath := fmt.Sprintf("/var/lib/kubelet/checkpoints/%s", fileName)
	
	// Use kubectl cp to copy the file to the pod on the target node
	err = cm.copyFileToPod(ctx, srcPath, pod.Namespace, pod.Name, targetPath)
	if err != nil {
		return fmt.Errorf("failed to copy file to pod: %w", err)
	}

	cm.log.Info("Successfully transferred checkpoint file", 
		"source", srcPath, 
		"target", targetNode,
		"targetPath", targetPath)
	
	return nil
}

// findPodOnNode finds a pod running on the specified node
func (cm *CheckpointManager) findPodOnNode(ctx context.Context, nodeName string, pod *corev1.Pod) error {
	var podList corev1.PodList
	err := cm.client.List(ctx, &podList, client.MatchingFields{
		"spec.nodeName": nodeName,
	})
	if err != nil {
		return err
	}

	// Find a running pod
	for _, p := range podList.Items {
		if p.Status.Phase == corev1.PodRunning {
			*pod = p
			return nil
		}
	}

	return fmt.Errorf("no running pods found on node %s", nodeName)
}

// copyFileToPod copies a file to a pod using kubectl cp
func (cm *CheckpointManager) copyFileToPod(ctx context.Context, srcPath, namespace, podName, targetPath string) error {
	// Create the directory on the target pod if it doesn't exist
	targetDir := filepath.Dir(targetPath)
	_, err := cm.execCommand(namespace, podName, "mkdir", "-p", targetDir)
	if err != nil {
		return fmt.Errorf("failed to create directory on target pod: %w", err)
	}

	// Use the Kubernetes API to copy the file to the pod
	reader, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer reader.Close()

	// Execute cp command in the pod to place the file in the target location
	cmd := []string{"cp", "/dev/stdin", targetPath}
	stdout, stderr, err := cm.execPodCp(namespace, podName, cmd, reader)
	if err != nil {
		return fmt.Errorf("failed to copy file: %v, stdout: %s, stderr: %s", err, stdout, stderr)
	}

	return nil
}

// execCommand executes a command in a pod
func (cm *CheckpointManager) execCommand(namespace, podName string, command ...string) (string, error) {
	req := cm.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		Param("container", "").
		Param("stdout", "true").
		Param("stderr", "true")

	for _, c := range command {
		req.Param("command", c)
	}

	executor, err := remotecommand.NewSPDYExecutor(rest.CopyConfig(config), "POST", req.URL())
	if err != nil {
		return "", fmt.Errorf("failed to create SPDY executor: %w", err)
	}
	
	stdout, stderr := new(strings.Builder), new(strings.Builder)
	err = executor.Stream(remotecommand.StreamOptions{
		Stdin:  nil,
		Stdout: stdout,
		Stderr: stderr,
	})
	if err != nil {
		return "", fmt.Errorf("failed to execute command: %w (stderr: %s)", err, stderr.String())
	}

	return stdout.String(), nil
}

// execPodCp executes a command in a pod with stdin
func (cm *CheckpointManager) execPodCp(namespace, podName string, command []string, stdin io.Reader) (string, string, error) {
	req := cm.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		Param("container", "").
		Param("stdin", "true").
		Param("stdout", "true").
		Param("stderr", "true")

	for _, c := range command {
		req.Param("command", c)
	}

	executor, err := remotecommand.NewSPDYExecutor(rest.CopyConfig(config), "POST", req.URL())
	if err != nil {
		return "", "", fmt.Errorf("failed to create SPDY executor: %w", err)
	}
	
	stdout, stderr := new(strings.Builder), new(strings.Builder)
	err = executor.Stream(remotecommand.StreamOptions{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
	})
	
	if err != nil {
		return stdout.String(), stderr.String(), fmt.Errorf("failed to execute command: %w (stderr: %s)", err, stderr.String())
	}
	return stdout.String(), stderr.String(), nil
}

// copyLocal performs a local file copy
func (cm *CheckpointManager) copyLocal(src, dst string) error {
	// Ensure the target directory exists
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}

	// Open source file
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	// Create destination file
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	// Copy the contents
	if _, err := io.Copy(out, in); err != nil {
		return err
	}

	cm.log.Info("Copied container checkpoint file locally", "dst", dst)
	return nil
}

// CleanupCheckpointFiles removes checkpoint files for a given checkpoint content
func (cm *CheckpointManager) CleanupCheckpointFiles(contentName string) error {
	pattern := filepath.Join(cm.checkpointDir, contentName+"-*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}

	for _, match := range matches {
		if err := os.Remove(match); err != nil {
			cm.log.Error(err, "Failed to remove checkpoint file", "file", match)
		} else {
			cm.log.Info("Removed checkpoint file", "file", match)
		}
	}

	return nil
}
