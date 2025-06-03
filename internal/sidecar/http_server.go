package sidecar

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-logr/logr"
)

type Server struct {
	checkpointDir string
	port          string
	log           logr.Logger
	server        *http.Server
}

func NewServer(checkpointDir, port string, log logr.Logger) *Server {
	if port == "" {
		port = "8081" // Default port
	}
	return &Server{
		checkpointDir: checkpointDir,
		port:          port,
		log:           log.WithName("http-server"),
	}
}

func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/upload", s.uploadHandler)
	mux.HandleFunc("/health", s.healthHandler)

	s.server = &http.Server{
		Addr:         ":" + s.port,
		Handler:      mux,
		ReadTimeout:  5 * time.Minute, // Allow time for large file uploads
		WriteTimeout: 5 * time.Minute,
	}

	s.log.Info("Starting HTTP server", "port", s.port)
	return s.server.ListenAndServe()
}

func (s *Server) Stop() error {
	if s.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.server.Shutdown(ctx)
	}
	return nil
}

func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "OK")
}

func (s *Server) uploadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse the multipart form with 1GB max memory
	if err := r.ParseMultipartForm(1 << 30); err != nil {
		s.log.Error(err, "Error parsing multipart form")
		http.Error(w, "Error parsing form", http.StatusBadRequest)
		return
	}

	// Get the file from the form data
	file, header, err := r.FormFile("file")
	if err != nil {
		s.log.Error(err, "Error retrieving file from form data")
		http.Error(w, "Error retrieving file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Ensure the checkpoint directory exists
	if err := os.MkdirAll(s.checkpointDir, 0755); err != nil {
		s.log.Error(err, "Error creating checkpoint directory", "path", s.checkpointDir)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Create the destination file
	destPath := filepath.Join(s.checkpointDir, header.Filename)
	destFile, err := os.Create(destPath)
	if err != nil {
		s.log.Error(err, "Error creating destination file", "path", destPath)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer destFile.Close()

	// Copy the file content
	written, err := io.Copy(destFile, file)
	if err != nil {
		s.log.Error(err, "Error saving file", "path", destPath)
		http.Error(w, "Error saving file", http.StatusInternalServerError)
		return
	}

	// Verify file size matches
	if written != header.Size {
		s.log.Error(fmt.Errorf("file size mismatch"), 
			"Expected size", header.Size, 
			"Actual size", written, 
			"path", destPath)
		http.Error(w, "File size mismatch", http.StatusInternalServerError)
		return
	}

	s.log.Info("Successfully uploaded file", "filename", header.Filename, "path", destPath, "size", written)
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, "File %s uploaded successfully", header.Filename)
}
