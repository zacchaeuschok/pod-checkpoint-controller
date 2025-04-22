// sidecar/file_server.go
// Copyright 2025.
// SPDX‑License‑Identifier: Apache‑2.0

package sidecar

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-logr/logr"
)

// StartFileServer launches a PUT /checkpoints/<name> handler on :8081.
// It stores the tarball, wraps it into an OCI image, and pushes it
// into the local CRI‑O store via Buildah.
func StartFileServer(log logr.Logger) {
	mux := http.NewServeMux()
	mux.HandleFunc("/checkpoints/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "PUT only", http.StatusMethodNotAllowed)
			return
		}

		dst := filepath.Join("/var/lib/kubelet/checkpoints", filepath.Base(r.URL.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out, err := os.Create(dst)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer out.Close()
		if _, err = io.Copy(out, r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Info("received checkpoint", "file", dst)

		// ── Build OCI image ──────────────────────────────────────────────
		tmp := fmt.Sprintf("tmp-%s", filepath.Base(dst)) // buildah working ctr
		run := func(cmd ...string) error {
			o, err := exec.Command(cmd[0], cmd[1:]...).CombinedOutput()
			if err != nil {
				log.Error(err, "buildah", "args", cmd[1:], "out", string(o))
			} else {
				log.Info("buildah", "args", cmd[1:], "out", string(o))
			}
			return err
		}

		if err := run("buildah", "from", "--name", tmp, "docker.io/library/busybox:latest"); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := run("buildah", "add", tmp, dst, "/"); err != nil {
			_ = exec.Command("buildah", "rm", tmp).Run()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// ── derive repository + immutable tag ───────────────────────────
		orig := strings.TrimSuffix(filepath.Base(dst), filepath.Ext(dst))
		noTs := regexp.MustCompile(`-\d{4}-\d{2}-\d{2}[Tt]\d{2}[:\-]\d{2}[:\-]\d{2}[-+]\d{2}[:\-]?\d{2}$`).
			ReplaceAllString(orig, "")
		// keep original pattern (no “‑restored”)
		safe := strings.ToLower(strings.NewReplacer(
			":", "-", "+", "-", "@", "-", "%", "-", "_", "-").Replace(noTs))
		ts := time.Now().UTC().Format("20060102T150405Z") // RFC3339 no colons
		imageRef := fmt.Sprintf("localhost/%s:%s", safe, ts)

		anno := fmt.Sprintf("io.kubernetes.cri-o.annotations.checkpoint.name=%s",
			noTs[strings.LastIndex(noTs, "-")+1:])
		if err := run("buildah", "config", "--annotation", anno, tmp); err != nil {
			_ = exec.Command("buildah", "rm", tmp).Run()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if err := run("buildah", "commit", tmp, imageRef); err != nil {
			_ = exec.Command("buildah", "rm", tmp).Run()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// push immutable tag only
		if err := run("buildah", "push", "--quiet",
			imageRef, "oci:/var/lib/containers/storage:"+imageRef); err != nil {
			_ = exec.Command("buildah", "rm", tmp).Run()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = exec.Command("buildah", "rm", tmp).Run()

		log.Info("checkpoint image ready", "image", imageRef)
		w.WriteHeader(http.StatusCreated)
	})

	go func() {
		log.Info("file‑server listening", "addr", ":8081")
		_ = http.ListenAndServe(":8081", mux)
	}()
}
