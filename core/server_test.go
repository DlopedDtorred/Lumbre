package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHealthDoesNotReportModelWhenNoneIsLoaded(t *testing.T) {
	engine := filepath.Join(t.TempDir(), "llama-server")
	if err := os.WriteFile(engine, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LUMBRE_LLAMA_SERVER", engine)
	recorder := httptest.NewRecorder()
	newServer().health(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	var response struct {
		LoadedModel string `json:"loadedModel"`
		Available   bool   `json:"available"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Available || response.LoadedModel != "" {
		t.Fatalf("unexpected health response: %#v", response)
	}
}

func TestModelListOnlyIncludesGGUFFiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LUMBRE_MODELS", root)
	if err := os.WriteFile(filepath.Join(root, "model.gguf"), []byte("gguf"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("not a model"), 0o600); err != nil {
		t.Fatal(err)
	}

	models, err := listModels()
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Name != "model.gguf" || models[0].Size != 4 {
		t.Fatalf("unexpected model list: %#v", models)
	}
}

func TestDeleteModelDeletesOnlyNamedUnloadedModel(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LUMBRE_MODELS", root)
	modelPath := filepath.Join(root, "model.gguf")
	if err := os.WriteFile(modelPath, []byte("gguf"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := newServer()
	recorder := httptest.NewRecorder()
	server.deleteModel(recorder, httptest.NewRequest(http.MethodDelete, "/api/v1/models?name=model.gguf", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected model deletion to succeed, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(modelPath); !os.IsNotExist(err) {
		t.Fatalf("model still exists after deletion: %v", err)
	}
}

func TestDeleteModelRejectsTraversalAndLoadedModel(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LUMBRE_MODELS", root)
	modelPath := filepath.Join(root, "model.gguf")
	if err := os.WriteFile(modelPath, []byte("gguf"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := newServer()
	for _, name := range []string{"../model.gguf", "/tmp/model.gguf", "not-a-model.txt"} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodDelete, "/api/v1/models?name="+url.QueryEscape(name), nil)
		server.deleteModel(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("model name %q returned status %d", name, recorder.Code)
		}
	}
	server.engineFile = modelPath
	server.engine = &exec.Cmd{Process: &os.Process{Pid: 12345}}
	recorder := httptest.NewRecorder()
	server.deleteModel(recorder, httptest.NewRequest(http.MethodDelete, "/api/v1/models?name=model.gguf", nil))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected loaded model deletion to be rejected, got %d", recorder.Code)
	}
	if _, err := os.Stat(modelPath); err != nil {
		t.Fatalf("loaded model was deleted: %v", err)
	}
}

func TestModelRouteAllowsCORSPreflightForDelete(t *testing.T) {
	handler := newServer().security(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodOptions, "/api/v1/models", nil)
	request.Header.Set("Origin", "http://localhost:5173")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected successful preflight, got %d", recorder.Code)
	}
	if methods := recorder.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(methods, "DELETE") {
		t.Fatalf("CORS methods do not permit DELETE: %q", methods)
	}
}

func TestDownloadRejectsNonHTTPSAndNonGGUF(t *testing.T) {
	server := newServer()
	for _, modelURL := range []string{
		"http://huggingface.co/example/model.gguf",
		"https://huggingface.co/example/model.bin",
		"file:///tmp/model.gguf",
	} {
		body := `{"url":"` + modelURL + `"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/models/download", strings.NewReader(body))
		recorder := httptest.NewRecorder()
		server.download(recorder, req)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("URL %q returned status %d", modelURL, recorder.Code)
		}
	}
}

func TestLoadModelRejectsPathsOutsideModelDirectory(t *testing.T) {
	server := newServer()
	body := `{"model":"../outside.gguf"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/models/load", strings.NewReader(body))
	recorder := httptest.NewRecorder()
	server.loadModel(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid model path to fail, got %d", recorder.Code)
	}
	var response map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response["error"], "local GGUF") {
		t.Fatalf("unexpected error: %q", response["error"])
	}
}

func TestAPIKeyProtectsAPIAndOpenAIEndpoints(t *testing.T) {
	t.Setenv("LUMBRE_API_KEY", "test-secret")
	handler := newServer().security(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, path := range []string{"/api/v1/models", "/v1/chat/completions"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s returned status %d without a key", path, recorder.Code)
		}
		req = httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer test-secret")
		recorder = httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusNoContent {
			t.Errorf("%s returned status %d with a valid key", path, recorder.Code)
		}
	}
}
