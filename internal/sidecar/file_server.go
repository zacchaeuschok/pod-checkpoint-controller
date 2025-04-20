package sidecar

import (
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-logr/logr"
)

// startFileServer launches a tiny PUT /checkpoints/<name> handler on :8081
func StartFileServer(log logr.Logger) {
	mux := http.NewServeMux()
	mux.HandleFunc("/checkpoints/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "PUT only", http.StatusMethodNotAllowed)
			return
		}
		dst := filepath.Join("/var/lib/kubelet/checkpoints", filepath.Base(r.URL.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		out, err := os.Create(dst)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer out.Close()
		if _, err = io.Copy(out, r.Body); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		log.Info("received checkpoint", "file", dst)
	})
	go func() {
		log.Info("file‑server listening", "addr", ":8081")
		_ = http.ListenAndServe(":8081", mux)
	}()
}
