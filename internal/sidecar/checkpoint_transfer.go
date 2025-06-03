package sidecar

import (
	"context"
	"fmt"
	"io"
	"math/rand"
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
	"strconv"
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

	cm.log.Info("Transferring checkpoint to remote node", "targetNode", targetNode, "sourcePath", srcPath)

	// Simple file existence check with basic retry logic
	maxRetries := 3
	var fileExists bool
	
	for attempt := 0; attempt < maxRetries; attempt++ {
		if _, err := os.Stat(srcPath); err == nil {
			fileExists = true
			break
		} else if !os.IsNotExist(err) {
			// If error is something other than "not exists", report it
			return fmt.Errorf("error checking source file: %w", err)
		}
		
		cm.log.Info("Source file not found, retrying", 
			"path", srcPath, 
			"attempt", attempt+1, 
			"maxRetries", maxRetries)
		
		// Short delay before retry
		time.Sleep(time.Duration(500*(attempt+1)) * time.Millisecond)
	}
	
	if !fileExists {
		return fmt.Errorf("source checkpoint file not found after %d attempts: %s", maxRetries, srcPath)
	}

	// Find a pod running on the target node to use as transfer destination
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
	// First, verify the source file exists and is readable
	fileInfo, err := os.Stat(srcPath)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	
	sourceSize := fileInfo.Size()
	cm.log.Info("Source file verified before transfer", 
		"path", srcPath, 
		"size", sourceSize, 
		"modTime", fileInfo.ModTime())
	
	// Create the directory on the target pod if it doesn't exist
	targetDir := filepath.Dir(targetPath)
	_, err = cm.execCommand(namespace, podName, "mkdir", "-p", targetDir)
	if err != nil {
		return fmt.Errorf("failed to create directory on target pod: %w", err)
	}

	// Custom retry parameters
	maxRetries := 5
	initialDelay := 1 * time.Second
	maxDelay := 30 * time.Second
	
	var lastErr error
	var verifyOut string
	
	// Retry loop with exponential backoff
	for attempt := 0; attempt < maxRetries; attempt++ {
		// Use the Kubernetes API to copy the file to the pod
		reader, err := os.Open(srcPath)
		if err != nil {
			return fmt.Errorf("failed to open source file: %w", err)
		}
		
		// Execute cp command in the pod to place the file in the target location
		cmd := []string{"cp", "/dev/stdin", targetPath}
		stdout, stderr, err := cm.execPodCp(namespace, podName, cmd, reader)
		reader.Close()
		
		if err != nil {
			lastErr = fmt.Errorf("failed to copy file: %v, stdout: %s, stderr: %s", err, stdout, stderr)
			cm.log.Info("File transfer failed, retrying", "attempt", attempt+1, "error", err)
			time.Sleep(getBackoffDelay(attempt, initialDelay, maxDelay))
			continue
		}
		
		// Verify the file was transferred correctly in the pod
		verifyCmd := []string{"ls", "-la", targetPath}
		verifyOut, err = cm.execCommand(namespace, podName, verifyCmd...)
		if err != nil {
			cm.log.Info("File transfer verification failed, retrying", 
				"attempt", attempt+1, 
				"targetPath", targetPath, 
				"error", err)
			time.Sleep(getBackoffDelay(attempt, initialDelay, maxDelay))
			continue
		}
		
		// Verify file size to ensure complete transfer
		sizeCheckCmd := []string{"stat", "-c", "%s", targetPath}
		sizeOutput, err := cm.execCommand(namespace, podName, sizeCheckCmd...)
		if err != nil {
			cm.log.Info("File size verification failed, retrying", 
				"attempt", attempt+1, 
				"error", err)
			time.Sleep(getBackoffDelay(attempt, initialDelay, maxDelay))
			continue
		}

		// Parse the size output and compare with source file size
		size, err := strconv.ParseInt(strings.TrimSpace(sizeOutput), 10, 64)
		if err != nil {
			cm.log.Info("Failed to parse file size, retrying", 
				"attempt", attempt+1,
				"output", sizeOutput)
			time.Sleep(getBackoffDelay(attempt, initialDelay, maxDelay))
			continue
		}

		if size != sourceSize {
			cm.log.Info("File size mismatch, retrying", 
				"attempt", attempt+1,
				"sourceSize", sourceSize, 
				"targetSize", size)
			time.Sleep(getBackoffDelay(attempt, initialDelay, maxDelay))
			continue
		}

		cm.log.Info("File transfer verification", "output", verifyOut)
		
		// Allow some time for the mount propagation to complete
		// This is crucial for volume propagation to work correctly
		// Instead of using nsenter (which may not be available in all containers),
		// we'll use a delay and verification approach
		
		// Add a short sleep to allow propagation to complete
		time.Sleep(500 * time.Millisecond)
		
		// Check file again to verify it's still accessible after propagation time
		recheckCmd := []string{"ls", "-la", targetPath}
		recheckOut, err := cm.execCommand(namespace, podName, recheckCmd...)
		if err != nil {
			cm.log.Info("Post-propagation file verification failed, retrying", 
				"attempt", attempt+1,
				"targetPath", targetPath, 
				"error", err)
			time.Sleep(getBackoffDelay(attempt, initialDelay, maxDelay))
			continue
		}
		
		cm.log.Info("Post-propagation file verification", "output", recheckOut)
		
		// Verify file permissions and ownership are correct after propagation
		permCmd := []string{"stat", "-c", "%a %U %G", targetPath}
		permOut, err := cm.execCommand(namespace, podName, permCmd...)
		if err != nil {
			cm.log.Info("File permission verification failed, retrying", 
				"attempt", attempt+1,
				"error", err)
			time.Sleep(getBackoffDelay(attempt, initialDelay, maxDelay))
			continue
		}
		
		cm.log.Info("File permission verification", "permissions", strings.TrimSpace(permOut))
		
		// Double-check file size after propagation delay
		recheckSizeCmd := []string{"stat", "-c", "%s", targetPath}
		recheckSizeOut, err := cm.execCommand(namespace, podName, recheckSizeCmd...)
		if err != nil {
			cm.log.Info("Post-propagation size verification failed, retrying", 
				"attempt", attempt+1,
				"error", err)
			time.Sleep(getBackoffDelay(attempt, initialDelay, maxDelay))
			continue
		}
		
		recheckSize, err := strconv.ParseInt(strings.TrimSpace(recheckSizeOut), 10, 64)
		if err != nil {
			cm.log.Info("Failed to parse post-propagation file size, retrying", 
				"attempt", attempt+1,
				"output", recheckSizeOut)
			time.Sleep(getBackoffDelay(attempt, initialDelay, maxDelay))
			continue
		}
		
		if recheckSize != sourceSize {
			cm.log.Info("Post-propagation file size mismatch, retrying", 
				"attempt", attempt+1,
				"sourceSize", sourceSize, 
				"targetSize", recheckSize)
			time.Sleep(getBackoffDelay(attempt, initialDelay, maxDelay))
			continue
		}
		
		cm.log.Info("Post-propagation file size verification passed", 
			"path", targetPath,
			"size", recheckSize)
		
		// If we get here, all verifications passed
		return nil
	}
	
	if lastErr != nil {
		return fmt.Errorf("file transfer failed after %d attempts: %w", maxRetries, lastErr)
	}
	
	return fmt.Errorf("file transfer failed after %d attempts", maxRetries)
}

// getBackoffDelay calculates exponential backoff delay
func getBackoffDelay(attempt int, initialDelay, maxDelay time.Duration) time.Duration {
	// Calculate delay with exponential backoff: initialDelay * 2^attempt
	delay := initialDelay * time.Duration(1<<uint(attempt))
	// Add some jitter (±20%)
	jitter := float64(delay) * (0.8 + rand.Float64()*0.4)
	delay = time.Duration(jitter)
	// Cap at maxDelay
	if delay > maxDelay {
		delay = maxDelay
	}
	return delay
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
