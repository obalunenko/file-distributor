package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	log "github.com/obalunenko/logger"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

const (
	port = "8080"
	name = "uploader"
)

var errSignal = errors.New("received signal")

func main() {
	l := log.FromContext(context.Background())

	ctx := log.ContextWithLogger(context.Background(), l)

	ctx, cancel := context.WithCancelCause(ctx)
	defer func() {
		const msg = "Exit"

		var code int

		err := context.Cause(ctx)
		if err != nil && !errors.Is(err, errSignal) {
			code = 1
		}

		l := log.WithField(ctx, "cause", err)

		if code == 0 {
			l.Info(msg)

			return
		}

		l.Error(msg)

		os.Exit(code)
	}()

	defer cancel(nil)

	signals := make(chan os.Signal, 1)

	signal.Notify(signals, syscall.SIGINT, syscall.SIGKILL, syscall.SIGTERM)

	go func() {
		s := <-signals

		cancel(fmt.Errorf("%w: %s", errSignal, s.String()))
	}()

	servers = make([]StorageClient, 0, len(addresses))

	for _, addr := range addresses {
		servers = append(servers, newStorageClient(addr))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/upload", uploadHandler)
	mux.HandleFunc("/download", downloadHandler)

	addr := net.JoinHostPort("", port)

	var wg sync.WaitGroup

	wg.Add(1)

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	server.RegisterOnShutdown(func() {
		defer wg.Done()

		log.Info(ctx, "Server shutting down")

		server.SetKeepAlivesEnabled(false)

		log.Info(ctx, "Server shutdown complete")
	})

	go func() {
		log.WithField(ctx, "addr", server.Addr).Info("Server started")

		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			cancel(fmt.Errorf("failed to start server: %w", err))
		}
	}()

	<-ctx.Done()

	if err := server.Shutdown(ctx); err != nil {
		log.WithError(ctx, err).Error("Error shutting down server")
	}

	wg.Wait()
}

type fileChunkInfo struct {
	ResourceID string
	FileName   string
	Order      uint
	URL        string
}

var (
	fileChunksDB = make(map[string][]fileChunkInfo)
	dbMutex      = &sync.Mutex{}
)

func buildPartName(filename string, part int) string {
	return fmt.Sprintf("%s-part-%d", filename, part)
}

func newStorageClient(addr string) StorageClient {
	fmt.Printf("Connecting to server %s \n", addr)

	return &mockStorageServer{
		addr:    addr,
		storage: make(map[string]chunk),
	}
}

var addresses = []string{
	"http://localhost:8081",
	"http://localhost:8082",
	"http://localhost:8083",
	"http://localhost:8084",
	"http://localhost:8085",
	"http://localhost:8086",
}

var servers []StorageClient

type StorageClient interface {
	SaveChunk(name string, order uint, data []byte) error
	GetChunk(name string) (chunk, error)
}

type chunk struct {
	order uint
	data  []byte
}

type storageServer struct {
	addr string
}

func (s *storageServer) SaveChunk(name string, order uint, data []byte) error {
	url := fmt.Sprintf("%s/save-chunk?name=%s&order=%s&size=%d", s.addr, name, order, len(data))

	resp, err := http.Post(url, "application/octet-stream", bytes.NewReader(data))
	if err != nil {
		return err
	}

	defer func() {
		if err = resp.Body.Close(); err != nil {
			fmt.Printf("Failed to close response body: %v \n", err)

			return
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {

		}

		return fmt.Errorf("failed to store on server B: %s", body)
	}

	return nil
}

type mockStorageServer struct {
	addr    string
	storage map[string]chunk
}

func (s *mockStorageServer) GetChunk(name string) (chunk, error) {
	fmt.Printf("Getting resource %q from server %s \n", name, s.addr)

	ch, ok := s.storage[name]
	if !ok {
		return chunk{}, fmt.Errorf("resource %q not found", name)
	}

	return ch, nil
}

func (s *mockStorageServer) SaveChunk(name string, order uint, data []byte) error {
	fmt.Printf("Storing resource %q chunk order %d on server %s \n", name, order, s.addr)

	s.storage[name] = chunk{
		order: order,
		data:  data,
	}

	return nil
}

func splitFile(file []byte) [][]byte {
	parts := len(servers)

	partSize := len(file) / parts

	splitted := make([][]byte, parts)

	for i := 0; i < parts; i++ {
		if i == parts-1 {
			splitted[i] = file[i*partSize:]
		} else {
			splitted[i] = file[i*partSize : (i+1)*partSize]
		}
	}

	return splitted
}
