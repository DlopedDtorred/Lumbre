package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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
