package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	log "github.com/obalunenko/logger"
	"golang.org/x/sync/errgroup"
	"io"
	"net/http"
	"strings"
	"sync"
)

type response struct {
	Message string `json:"message"`
	Payload any    `json:"payload,omitempty"`
}

type uploadResponse struct {
	Resource string `json:"resource"`
	Checksum string `json:"checksum"`
}

func uploadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST is supported", http.StatusMethodNotAllowed)

		return
	}

	file, fh, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Failed to get the uploaded file", http.StatusBadRequest)

		return
	}

	defer func() {
		if err = file.Close(); err != nil {
			fmt.Printf("Failed to close file descriptor: %v \n", err)
		}
	}()

	log.WithFields(r.Context(), log.Fields{
		"file_name": fh.Filename,
		"file_size": fh.Size,
		"file_type": fh.Header.Get("Content-Type"),
	}).Info("Received file")

	h := sha256.New()

	if _, err = io.Copy(h, file); err != nil {
		http.Error(w, "Failed to calculate checksum", http.StatusInternalServerError)

		return
	}

	checksum := fmt.Sprintf("%x", h.Sum(nil))

	fileBytes, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "Failed to read the file", http.StatusInternalServerError)

		return
	}

	// Делим файл на части
	parts := splitFile(fileBytes)

	filename := fh.Filename

	resourceID := generateResourceID()

	uploadFn := func(mx sync.Locker, filename string, id int, data []byte) error {
		partName := buildPartName(filename, id)

		srv := servers[id]

		mx.Lock()

		chunks := fileChunksDB[resourceID]

		chunks = append(chunks, fileChunkInfo{
			ResourceID: resourceID,
			FileName:   filename,
			Order:      uint(id),
			URL:        "TBD",
		})

		fileChunksDB[resourceID] = chunks

		mx.Unlock()

		fmt.Printf("Sending part %s to server %d \n", partName, id)

		return srv.SaveChunk(resourceID, uint(id), data)
	}

	dbMutex.Lock()
	fileChunksDB[resourceID] = make([]fileChunkInfo, 0, len(parts))
	dbMutex.Unlock()

	g, _ := errgroup.WithContext(r.Context())

	// Отправляем каждую часть на сервер B
	for i := range parts {
		part := parts[i]

		g.Go(func() error {
			if err = uploadFn(dbMutex, filename, i, part); err != nil {
				return fmt.Errorf("failed to upload part %d to server: %w", i, err)
			}

			return nil
		})
	}

	if err = g.Wait(); err != nil {
		http.Error(w, "Failed to upload file to storage", http.StatusInternalServerError)

		return
	}

	log.WithFields(r.Context(), log.Fields{
		"file_name":   fh.Filename,
		"checksum":    checksum,
		"resource_id": resourceID,
	}).Info("File uploaded successfully")

	w.Header().Set("Content-Type", "application/json")

	w.WriteHeader(http.StatusCreated)

	var resp uploadResponse

	resp.Resource = resourceID
	resp.Checksum = checksum

	if err = json.NewEncoder(w).Encode(resp); err != nil {
		fmt.Printf("Failed to encode response: %v \n", err)

		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)

		return
	}
}

func downloadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Only GET is supported", http.StatusMethodNotAllowed)

		return
	}

	resourceID := r.URL.Query().Get("resource_id")
	if resourceID == "" {
		http.Error(w, "Resource ID is required", http.StatusBadRequest)

		return
	}

	log.WithFields(r.Context(), log.Fields{
		"resource_id": resourceID,
	}).Info("Received download request")

	dbMutex.Lock()
	chunks, ok := fileChunksDB[resourceID]
	dbMutex.Unlock()

	if !ok || len(chunks) == 0 {
		http.Error(w, "File not found", http.StatusNotFound)

		return
	}

	var (
		buf      strings.Builder
		fileName string
	)

	for i := range chunks {
		if i == 0 {
			fileName = chunks[i].FileName
		}

		info := chunks[i]

		srv := servers[info.Order]

		log.FromContext(r.Context()).WithFields(log.Fields{
			"resource_id": resourceID,
			"order":       info.Order,
			"url":         info.URL,
			"file_name":   info.FileName,
		}).Info("Downloading file chunk")

		ch, err := srv.GetChunk(resourceID)
		if err != nil {
			http.Error(w, "Failed to get file chunk", http.StatusInternalServerError)

			return
		}

		log.WithFields(r.Context(), log.Fields{
			"resource_id": resourceID,
			"order":       info.Order,
			"chunk_size":  len(ch.data),
			"chunk":       ch.data[:10],
		}).Info("File chunk downloaded successfully")

		n, err := buf.Write(ch.data)
		if err != nil {
			http.Error(w, "Failed to write file chunk", http.StatusInternalServerError)

			return
		}

		if n != len(ch.data) {
			http.Error(w, "Failed to write file chunk", http.StatusInternalServerError)

			return
		}
	}

	log.WithFields(r.Context(), log.Fields{
		"resource_id": resourceID,
	}).Info("File downloaded successfully")

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", fileName))
	w.Header().Set("Content-Type", "application/octet-stream")

	content := buf.String()

	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))

	log.WithFields(r.Context(), log.Fields{
		"resource_id":    resourceID,
		"content_length": len(content),
		"content":        content[:10],
	}).Info("Content of  downloaded file")

	if _, err := w.Write([]byte(content)); err != nil {
		fmt.Printf("Failed to write response: %v \n", err)

		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)

		return
	}
}

func generateResourceID() string {
	uid := uuid.New()

	return uid.String()
}
