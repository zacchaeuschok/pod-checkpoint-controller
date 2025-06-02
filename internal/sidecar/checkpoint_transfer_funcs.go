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

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/wait"
)

// makeTLSClient constructs an HTTPS client for kubelet calls
func makeTLSClient(certPath, keyPath, caPath string) (*http.Client, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	caBytes, _ := os.ReadFile(caPath)
	pool := x509.NewCertPool()
	insecure := true
	if pool.AppendCertsFromPEM(caBytes) {
		insecure = false
	}
	return &http.Client{
		Timeout: checkpointTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				Certificates:       []tls.Certificate{cert},
				RootCAs:            pool,
				InsecureSkipVerify: insecure, // if CA doesn't match
			},
		},
	}, nil
}

// doCheckpointWithBackoff calls the kubelet's /checkpoint via POST with exponential backoff.
// Returns a list of file paths that the kubelet created.
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
