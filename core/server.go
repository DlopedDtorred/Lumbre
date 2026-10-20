package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const enginePort = 11436

type localServer struct {
	client      *http.Client
	engineMu    sync.Mutex
	inferenceMu sync.Mutex
	engine      *exec.Cmd
	engineDone  chan error
	engineFile  string
}

type modelInfo struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

func newServer() *localServer {
	return &localServer{client: &http.Client{Timeout: 0}}
}

func (s *localServer) listen(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", s.health)
	mux.HandleFunc("/api/v1/models", s.models)
	mux.HandleFunc("/api/v1/models/download", s.download)
	mux.HandleFunc("/api/v1/models/load", s.loadModel)
	mux.HandleFunc("/v1/models", s.openAIModels)
	mux.HandleFunc("/v1/chat/completions", s.openAIChat)
	mux.HandleFunc("/", s.static)
	server := &http.Server{
		Addr:              addr,
		Handler:           s.security(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	err := server.ListenAndServe()
	s.stopEngine()
	return err
}

func (s *localServer) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && !allowedOrigin(origin) {
			writeError(w, http.StatusForbidden, "origin is not allowed")
			return
		}
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if key := os.Getenv("LUMBRE_API_KEY"); key != "" &&
			(strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/")) {
			if r.Header.Get("Authorization") != "Bearer "+key {
				writeError(w, http.StatusUnauthorized, "missing or invalid API key")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func allowedOrigin(origin string) bool {
	return origin == "tauri://localhost" ||
		origin == "http://tauri.localhost" ||
		origin == "https://tauri.localhost" ||
		origin == "http://localhost:5173" ||
		origin == "http://127.0.0.1:5173"
}

func (s *localServer) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	enginePath, engineErr := exec.LookPath(llamaServerPath())
	s.engineMu.Lock()
	loaded := ""
	if s.engineFile != "" {
		loaded = filepath.Base(s.engineFile)
	}
	running := s.engine != nil && s.engine.Process != nil
	s.engineMu.Unlock()
	if engineErr != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"engine":    "llama.cpp",
			"message":   "llama-server no está instalado o no está en PATH.",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available":   true,
		"engine":      "llama.cpp",
		"enginePath":  enginePath,
		"loadedModel": loaded,
		"running":     running,
		"gpuLayers":   envOr("LUMBRE_GPU_LAYERS", "auto"),
	})
}

func (s *localServer) models(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	models, err := listModels()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

func listModels() ([]modelInfo, error) {
	root, err := modelDirectory()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []modelInfo{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read model directory: %w", err)
	}
	models := make([]modelInfo, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".gguf") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("read model information: %w", err)
		}
		models = append(models, modelInfo{Name: entry.Name(), Size: info.Size()})
	}
	return models, nil
}

func (s *localServer) download(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var input struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	source, err := url.Parse(input.URL)
	if err != nil || source.Scheme != "https" || source.Host == "" || source.User != nil {
		writeError(w, http.StatusBadRequest, "provide a public HTTPS URL to a .gguf file")
		return
	}
	filename := filepath.Base(source.Path)
	if filename == "" || filename == "." || !strings.EqualFold(filepath.Ext(filename), ".gguf") {
		writeError(w, http.StatusBadRequest, "download URL must end with a .gguf file")
		return
	}
	root, err := modelDirectory()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("create model directory: %v", err))
		return
	}
	target := filepath.Join(root, filename)
	if _, err := os.Stat(target); err == nil {
		writeError(w, http.StatusConflict, "this model is already installed")
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("check existing model: %v", err))
		return
	}
	temp := target + ".part"
	defer os.Remove(temp)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, source.String(), nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	client := &http.Client{
		Timeout: 0,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" || req.URL.User != nil || len(via) >= 10 {
				return errors.New("unsafe or excessive redirect while downloading model")
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("download failed: %v", err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("model host returned %s", resp.Status))
		return
	}
	file, err := os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("create model file: %v", err))
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	buffer := make([]byte, 1024*1024)
	var downloaded int64
	lastProgress := time.Time{}
	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			written, writeErr := file.Write(buffer[:n])
			downloaded += int64(written)
			if writeErr != nil || written != n {
				file.Close()
				log.Printf("model download failed writing %s: %v", filename, writeErr)
				writeDownloadError(w, flusher, "could not write downloaded model")
				return
			}
			if time.Since(lastProgress) > 300*time.Millisecond {
				progress := float64(0)
				if resp.ContentLength > 0 {
					progress = float64(downloaded) * 100 / float64(resp.ContentLength)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"status": filename, "progress": progress})
				if flusher != nil {
					flusher.Flush()
				}
				lastProgress = time.Now()
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			file.Close()
			log.Printf("model download interrupted for %s: %v", filename, readErr)
			writeDownloadError(w, flusher, "model download interrupted")
			return
		}
	}
	if err := file.Sync(); err != nil {
		file.Close()
		log.Printf("could not sync downloaded model %s: %v", filename, err)
		writeDownloadError(w, flusher, "could not save downloaded model")
		return
	}
	if err := file.Close(); err != nil {
		log.Printf("could not close downloaded model %s: %v", filename, err)
		writeDownloadError(w, flusher, "could not save downloaded model")
		return
	}
	if err := os.Rename(temp, target); err != nil {
		log.Printf("could not finish downloaded model %s: %v", filename, err)
		writeDownloadError(w, flusher, "could not finish saving downloaded model")
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"status": filename, "progress": 100})
	if flusher != nil {
		flusher.Flush()
	}
}

func (s *localServer) loadModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var input struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !validModelName(input.Model) {
		writeError(w, http.StatusBadRequest, "select a local GGUF model by filename")
		return
	}
	s.inferenceMu.Lock()
	defer s.inferenceMu.Unlock()
	if err := s.startEngine(r.Context(), input.Model); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"loaded": filepath.Base(input.Model)})
}

func (s *localServer) openAIModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	models, err := listModels()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	data := make([]map[string]any, 0, len(models))
	for _, model := range models {
		data = append(data, map[string]any{"id": model.Name, "object": "model", "owned_by": "local"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (s *localServer) openAIChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "request body too large or unreadable")
		return
	}
	var input struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &input); err != nil || strings.TrimSpace(input.Model) == "" {
		writeError(w, http.StatusBadRequest, "model is required")
		return
	}
	if !validModelName(input.Model) {
		writeError(w, http.StatusBadRequest, "select a local GGUF model by filename")
		return
	}
	s.inferenceMu.Lock()
	defer s.inferenceMu.Unlock()
	if err := s.startEngine(r.Context(), input.Model); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", enginePort), strings.NewReader(string(body)))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("llama.cpp request failed: %v", err))
		return
	}
	defer resp.Body.Close()
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	copyStream(w, resp, contentType)
}

func (s *localServer) startEngine(ctx context.Context, name string) error {
	if !validModelName(name) {
		return errors.New("select a local GGUF model by filename")
	}
	root, err := modelDirectory()
	if err != nil {
		return err
	}
	modelPath := filepath.Join(root, name)
	info, err := os.Stat(modelPath)
	if err != nil || info.IsDir() {
		return fmt.Errorf("model %q is not installed; download its GGUF file first", name)
	}
	s.engineMu.Lock()
	defer s.engineMu.Unlock()
	if s.engine != nil && s.engine.Process != nil && s.engineFile == modelPath {
		resp, err := s.client.Get(fmt.Sprintf("http://127.0.0.1:%d/health", enginePort))
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
	}
	if s.engine != nil && s.engine.Process != nil {
		if err := s.engine.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("stop previous model: %w", err)
		}
		if s.engineDone != nil {
			<-s.engineDone
		}
		s.engine = nil
		s.engineDone = nil
		s.engineFile = ""
	}
	args := []string{"--model", modelPath, "--host", "127.0.0.1", "--port", fmt.Sprint(enginePort),
		"--ctx-size", envOr("LUMBRE_CONTEXT_SIZE", "8192"),
		"--n-gpu-layers", envOr("LUMBRE_GPU_LAYERS", "auto")}
	if threads := os.Getenv("LUMBRE_THREADS"); threads != "" {
		args = append(args, "--threads", threads)
	}
	cmd := exec.CommandContext(context.Background(), llamaServerPath(), args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start llama.cpp (%s): %w; install llama.cpp and make llama-server available in PATH or set LUMBRE_LLAMA_SERVER", llamaServerPath(), err)
	}
	s.engine = cmd
	s.engineFile = modelPath
	ready := make(chan error, 1)
	s.engineDone = ready
	go func() { ready <- cmd.Wait() }()
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-ready:
			s.engine = nil
			s.engineDone = nil
			s.engineFile = ""
			if err == nil {
				return errors.New("llama.cpp exited before the model was ready")
			}
			return fmt.Errorf("llama.cpp failed to start: %w", err)
		case <-deadline.C:
			_ = cmd.Process.Kill()
			<-ready
			s.engine = nil
			s.engineDone = nil
			s.engineFile = ""
			return errors.New("timed out waiting for llama.cpp to load the model")
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			<-ready
			s.engine = nil
			s.engineDone = nil
			s.engineFile = ""
			return ctx.Err()
		case <-ticker.C:
			resp, err := s.client.Get(fmt.Sprintf("http://127.0.0.1:%d/health", enginePort))
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
	}
}

func validModelName(name string) bool {
	return name != "" && filepath.Base(name) == name && strings.EqualFold(filepath.Ext(name), ".gguf")
}

func (s *localServer) stopEngine() {
	s.engineMu.Lock()
	defer s.engineMu.Unlock()
	if s.engine != nil && s.engine.Process != nil {
		_ = s.engine.Process.Kill()
		if s.engineDone != nil {
			<-s.engineDone
		}
		s.engine = nil
		s.engineDone = nil
	}
}

func writeDownloadError(w http.ResponseWriter, flusher http.Flusher, message string) {
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
	if flusher != nil {
		flusher.Flush()
	}
}

func (s *localServer) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	root, err := webDirectory()
	if err != nil {
		http.Error(w, "Lumbre web files are missing; install them or set LUMBRE_WEB_DIR.", http.StatusNotFound)
		return
	}
	path := filepath.Clean("/" + r.URL.Path)
	target := filepath.Join(root, path)
	if _, err := os.Stat(target); err != nil {
		target = filepath.Join(root, "index.html")
	}
	if _, err := os.Stat(target); err != nil {
		http.Error(w, "Lumbre web files are missing; run the server from the project directory.", http.StatusNotFound)
		return
	}
	http.ServeFile(w, r, target)
}

func webDirectory() (string, error) {
	if dir := os.Getenv("LUMBRE_WEB_DIR"); dir != "" {
		if hasWebIndex(dir) {
			return filepath.Abs(dir)
		}
		return "", fmt.Errorf("LUMBRE_WEB_DIR %q does not contain index.html", dir)
	}
	candidates := []string{filepath.Join("..", "web"), "web"}
	if executable, err := os.Executable(); err == nil {
		executableDir := filepath.Dir(executable)
		candidates = append(candidates,
			filepath.Join(executableDir, "..", "share", "lumbre", "web"),
			filepath.Join(executableDir, "..", "web"),
			filepath.Join(executableDir, "web"),
		)
	}
	for _, candidate := range candidates {
		if hasWebIndex(candidate) {
			return filepath.Abs(candidate)
		}
	}
	return "", errors.New("could not locate Lumbre web files")
}

func hasWebIndex(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "index.html"))
	return err == nil && !info.IsDir()
}

func modelDirectory() (string, error) {
	if dir := os.Getenv("LUMBRE_MODELS"); dir != "" {
		return filepath.Abs(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".lumbre", "models"), nil
}

func copyStream(w http.ResponseWriter, resp *http.Response, contentType string) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			writeError(w, http.StatusBadGateway, "could not read llama.cpp response")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buffer := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buffer)
		if n > 0 {
			if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			log.Printf("llama.cpp stream interrupted: %v", err)
			return
		}
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("could not write JSON response: %v", err)
	}
}
