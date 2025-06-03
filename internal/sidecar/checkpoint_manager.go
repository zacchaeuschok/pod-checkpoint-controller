package sidecar

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"github.com/go-logr/logr"
	"io"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"time"
)

type CheckpointManager struct {
	client        client.Client
	clientset     *kubernetes.Clientset
	checkpointDir string
	nodeName      string
	log           logr.Logger
	httpServer    *Server
}

const (
	checkpointTimeout        = 10 * time.Second
	checkpointBackoffSteps   = 5
	checkpointBackoffInitial = 2 * time.Second
	checkpointBackoffFactor  = 2.0
)

var config *rest.Config

func NewCheckpointManager(client client.Client, log logr.Logger) (*CheckpointManager, error) {
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		return nil, fmt.Errorf("NODE_NAME environment variable is required")
	}

	var err error
	config, err = rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get in-cluster config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create clientset: %w", err)
	}

	checkpointDir := "/var/lib/kubelet/checkpoints"
	httpPort := "8081"
	if port := os.Getenv("HTTP_PORT"); port != "" {
		httpPort = port
	}

	manager := &CheckpointManager{
		client:        client,
		clientset:     clientset,
		checkpointDir: checkpointDir,
		nodeName:      nodeName,
		log:           log,
	}

	// Create and start the HTTP server from http_server.go
	manager.httpServer = NewServer(checkpointDir, httpPort, log)
	go func() {
		if err := manager.httpServer.Start(); err != nil {
			log.Error(err, "Failed to start HTTP server")
		}
	}()

	return manager, nil
}

func (cm *CheckpointManager) CreateCheckpoint(ctx context.Context, namespace, podName, containerName string) ([]string, error) {
	url := fmt.Sprintf("https://%s:10250/checkpoint/%s/%s/%s",
		cm.nodeName, namespace, podName, containerName)

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

func (cm *CheckpointManager) TransferCheckpoint(ctx context.Context, srcPath, targetNode string) error {
	file, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("failed to open checkpoint file %s: %w", srcPath, err)
	}
	defer func(file *os.File) {
		err := file.Close()
		if err != nil {
			cm.log.Error(err, "Failed to close checkpoint file")
		}
	}(file)

	// Verify source file exists and get its info
	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to get file info: %w", err)
	}
	cm.log.Info("Transferring checkpoint file", "path", srcPath, "size", fileInfo.Size())

	// Find the target pod on the destination node
	var podList corev1.PodList
	if err := cm.client.List(ctx, &podList, client.MatchingLabels{
		"app": "container-checkpoint-sidecar",
	}); err != nil {
		return fmt.Errorf("failed to list sidecar pods: %w", err)
	}

	var targetIP string
	for _, pod := range podList.Items {
		if pod.Spec.NodeName == targetNode && pod.Status.Phase == corev1.PodRunning {
			targetIP = pod.Status.PodIP
			break
		}
	}
	if targetIP == "" {
		return fmt.Errorf("no running sidecar pod found on node %s", targetNode)
	}

	// Prepare multipart form data
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", filepath.Base(srcPath))
	if err != nil {
		return fmt.Errorf("failed to create form file: %w", err)
	}
	if _, err := io.Copy(part, file); err != nil {
		return fmt.Errorf("failed to copy file into form body: %w", err)
	}
	err = writer.Close()
	if err != nil {
		return err
	}

	// Get the HTTP port from environment or use default
	httpPort := "8081"
	if port := os.Getenv("HTTP_PORT"); port != "" {
		httpPort = port
	}

	// Determine URL to target node's HTTP server
	uploadURL := fmt.Sprintf("http://%s:%s/upload", targetIP, httpPort)
	cm.log.Info("Uploading checkpoint to target", "url", uploadURL, "file", filepath.Base(srcPath))

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", uploadURL, &body)
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Set up HTTP http_client with appropriate timeout for large files
	httpClient := &http.Client{
		Timeout: 5 * time.Minute,
	}

	// Implement retry logic with exponential backoff
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			cm.log.Info("Retrying upload", "attempt", attempt+1)
			time.Sleep(time.Duration(1<<uint(attempt)) * time.Second)

			// Reset file position for retry
			if _, err := file.Seek(0, 0); err != nil {
				return fmt.Errorf("failed to reset file position: %w", err)
			}

			// Recreate the request body
			body.Reset()
			writer = multipart.NewWriter(&body)
			part, err = writer.CreateFormFile("file", filepath.Base(srcPath))
			if err != nil {
				return fmt.Errorf("failed to create form file: %w", err)
			}
			if _, err := io.Copy(part, file); err != nil {
				return fmt.Errorf("failed to copy file into form body: %w", err)
			}
			writer.Close()

			req, err = http.NewRequestWithContext(ctx, "POST", uploadURL, &body)
			if err != nil {
				return fmt.Errorf("failed to create HTTP request: %w", err)
			}
			req.Header.Set("Content-Type", writer.FormDataContentType())
		}

		resp, err := httpClient.Do(req)
		if err != nil {
			lastErr = err
			cm.log.Error(err, "HTTP request failed", "attempt", attempt+1)
			continue
		}

		// Handle the response
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("upload failed with status: %s, response: %s", resp.Status, string(body))
			cm.log.Error(lastErr, "Upload failed", "attempt", attempt+1)
			continue
		}

		// Success!
		cm.log.Info("Successfully transferred checkpoint file", "target", targetNode, "path", srcPath)

		// Verify upload with a health check
		healthURL := fmt.Sprintf("http://%s:%s/health", targetIP, httpPort)
		healthResp, err := http.Get(healthURL)
		if err != nil {
			cm.log.Error(err, "Health check failed after upload")
		} else {
			healthResp.Body.Close()
			cm.log.Info("Health check after upload successful", "status", healthResp.StatusCode)
		}

		return nil
	}

	return fmt.Errorf("failed to transfer checkpoint after multiple attempts: %w", lastErr)
}

func makeTLSClient(certPath, keyPath, caPath string) (*http.Client, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	caBytes, _ := os.ReadFile(caPath)
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caBytes)

	return &http.Client{
		Timeout: checkpointTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				Certificates:       []tls.Certificate{cert},
				RootCAs:            pool,
				InsecureSkipVerify: true, // Always skip verification to handle missing IP SANs
			},
		},
	}, nil
}

func doCheckpointWithBackoff(
	ctx context.Context,
	httpClient *http.Client,
	url string,
	log logr.Logger,
) ([]string, error) {
	var outFiles []string
	var lastErr error

	bo := wait.Backoff{
		Steps:    checkpointBackoffSteps,
		Duration: checkpointBackoffInitial,
		Factor:   checkpointBackoffFactor,
	}
	err := wait.ExponentialBackoff(bo, func() (bool, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
		resp, doErr := httpClient.Do(req)
		if doErr != nil {
			lastErr = doErr
			log.Info("kubelet request failed", "error", lastErr)
			return false, nil
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			data, _ := io.ReadAll(resp.Body)
			lastErr = fmt.Errorf("kubelet responded %d: %s", resp.StatusCode, string(data))
			log.Info("non-2xx from kubelet, retrying", "status", resp.StatusCode)
			return false, nil
		}
		data, _ := io.ReadAll(resp.Body)
		var parsed struct {
			Items []string `json:"items"`
		}
		if jerr := json.Unmarshal(data, &parsed); jerr != nil || len(parsed.Items) == 0 {
			lastErr = fmt.Errorf("bad kubelet JSON: %v", jerr)
			return false, nil
		}
		outFiles = parsed.Items
		return true, nil
	})
	if err != nil {
		return nil, lastErr
	}
	return outFiles, nil
}
