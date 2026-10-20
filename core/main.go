package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const version = "0.1.2"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		return
	}
	switch os.Args[1] {
	case "version", "--version", "-v":
		fmt.Printf("lumbre %s\n", version)
	case "serve":
		flags := flag.NewFlagSet("serve", flag.ExitOnError)
		host := flags.String("host", envOr("LUMBRE_HOST", "127.0.0.1"), "Address to listen on")
		port := flags.Int("port", envInt("LUMBRE_PORT", 11435), "Port to listen on")
		_ = flags.Parse(os.Args[2:])
		if err := runServer(*host, *port); err != nil {
			fatal(err)
		}
	case "models":
		if err := ensureServer(); err != nil {
			fatal(err)
		}
		if err := runModels(); err != nil {
			fatal(err)
		}
	case "list", "ls":
		if err := ensureServer(); err != nil {
			fatal(err)
		}
		if err := runModels(); err != nil {
			fatal(err)
		}
	case "pull":
		if len(os.Args) < 3 {
			fatal(errors.New("usage: lumbre pull MODEL_URL"))
		}
		if err := ensureServer(); err != nil {
			fatal(err)
		}
		if err := runPull(os.Args[2]); err != nil {
			fatal(err)
		}
	case "run", "chat":
		model := ""
		if len(os.Args) >= 3 {
			model = os.Args[2]
		}
		if err := ensureServer(); err != nil {
			fatal(err)
		}
		if model == "" {
			if err := selectModel(&model); err != nil {
				fatal(err)
			}
		}
		if err := runChat(model); err != nil {
			fatal(err)
		}
	case "ps":
		if err := ensureServer(); err != nil {
			fatal(err)
		}
		if err := runRunningModels(); err != nil {
			fatal(err)
		}
	case "show":
		if len(os.Args) < 3 {
			fatal(errors.New("usage: lumbre show MODEL"))
		}
		if err := ensureServer(); err != nil {
			fatal(err)
		}
		if err := runShow(os.Args[2]); err != nil {
			fatal(err)
		}
	case "rm", "remove":
		if len(os.Args) < 3 {
			fatal(errors.New("usage: lumbre rm MODEL"))
		}
		if err := ensureServer(); err != nil {
			fatal(err)
		}
		if err := runRemove(os.Args[2]); err != nil {
			fatal(err)
		}
	case "stop":
		if err := ensureServer(); err != nil {
			fatal(err)
		}
		if err := runStop(); err != nil {
			fatal(err)
		}
	case "help", "--help", "-h":
		printUsage()
	default:
		printUsage()
		os.Exit(2)
	}
}

func printUsage() {
	fmt.Print(`Lumbre — local AI, your way

Usage:
  lumbre run MODEL                  Run a model and start chatting
  lumbre run                        Choose a model and start chatting
  lumbre pull MODEL_URL             Download a GGUF model
  lumbre list                       List installed models (alias: ls)
  lumbre ps                         Show the loaded model
  lumbre show MODEL                 Show model details
  lumbre rm MODEL                   Remove an installed model
  lumbre stop                       Stop the loaded model
  lumbre serve [--host HOST] [--port PORT]
                                    Start the local API and web app
  lumbre version                    Print the version
  lumbre help                       Show this help
`)
}

func runServer(host string, port int) error {
	if port < 1 || port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	if !isLoopbackHost(host) && os.Getenv("LUMBRE_API_KEY") == "" {
		return errors.New("refusing to expose the server without LUMBRE_API_KEY")
	}
	addr := net.JoinHostPort(host, fmt.Sprint(port))
	fmt.Printf("Lumbre %s listening on http://%s\n", version, addr)
	fmt.Printf("Inference engine: llama.cpp (%s)\n", llamaServerPath())
	return newServer().listen(addr)
}

func runModels() error {
	resp, err := apiRequest(http.MethodGet, "/api/v1/models", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responseError(resp)
	}
	var result struct {
		Models []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if len(result.Models) == 0 {
		fmt.Println("No models installed. Download a GGUF model with: lumbre pull MODEL_URL")
		return nil
	}
	fmt.Printf("%-40s %s\n", "NAME", "SIZE")
	for _, model := range result.Models {
		fmt.Printf("%-40s %s\n", model.Name, humanSize(model.Size))
	}
	return nil
}

func selectModel(selected *string) error {
	resp, err := apiRequest(http.MethodGet, "/api/v1/models", nil)
	if err != nil {
		return fmt.Errorf("could not list models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responseError(resp)
	}
	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if len(result.Models) == 0 {
		return errors.New("no models installed; add a GGUF model with `lumbre pull MODEL_URL`")
	}
	fmt.Println("Choose a model:")
	for index, model := range result.Models {
		fmt.Printf("  %d. %s\n", index+1, model.Name)
	}
	fmt.Print("Model number: ")
	var choice int
	if _, err := fmt.Scan(&choice); err != nil {
		return fmt.Errorf("read model choice: %w", err)
	}
	if choice < 1 || choice > len(result.Models) {
		return errors.New("model number is out of range")
	}
	*selected = result.Models[choice-1].Name
	return nil
}

func runRunningModels() error {
	resp, err := apiRequest(http.MethodGet, "/api/v1/health", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responseError(resp)
	}
	var health struct {
		LoadedModel string `json:"loadedModel"`
		Running     bool   `json:"running"`
		Engine      string `json:"engine"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return err
	}
	fmt.Printf("%-40s %-12s %s\n", "NAME", "PROCESSOR", "STATUS")
	if !health.Running || health.LoadedModel == "" {
		fmt.Println("No models are currently loaded.")
		return nil
	}
	fmt.Printf("%-40s %-12s %s\n", health.LoadedModel, "CPU/GPU", "running")
	return nil
}

func runShow(name string) error {
	resp, err := apiRequest(http.MethodGet, "/api/v1/models", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responseError(resp)
	}
	var result struct {
		Models []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	for _, model := range result.Models {
		if model.Name != name {
			continue
		}
		fmt.Printf("  Name:        %s\n", model.Name)
		fmt.Printf("  Size:        %s\n", humanSize(model.Size))
		fmt.Printf("  Format:      GGUF\n")
		fmt.Printf("  Engine:      llama.cpp\n")
		fmt.Printf("  Parameters:  use `llama-server --help` for engine options\n")
		return nil
	}
	return fmt.Errorf("model %q is not installed; run `lumbre list` to see installed models", name)
}

func runRemove(name string) error {
	path := "/api/v1/models?name=" + url.QueryEscape(name)
	resp, err := apiRequest(http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responseError(resp)
	}
	fmt.Printf("Deleted model %s.\n", name)
	return nil
}

func runStop() error {
	resp, err := apiRequest(http.MethodPost, "/api/v1/models/stop", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responseError(resp)
	}
	fmt.Println("Model stopped.")
	return nil
}

func runPull(url string) error {
	body, err := json.Marshal(map[string]string{"url": url})
	if err != nil {
		return err
	}
	resp, err := apiRequest(http.MethodPost, "/api/v1/models/download", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responseError(resp)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var progress struct {
			Progress float64 `json:"progress"`
			Status   string  `json:"status"`
			Error    string  `json:"error"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &progress); err != nil {
			continue
		}
		if progress.Error != "" {
			return errors.New(progress.Error)
		}
		fmt.Printf("\r%-30s %5.1f%%", progress.Status, progress.Progress)
	}
	fmt.Println()
	return scanner.Err()
}

func runChat(model string) error {
	reader := bufio.NewReader(os.Stdin)
	messages := make([]map[string]string, 0)
	fmt.Printf(">>> %s\nType /exit or /bye to leave.\n\n", model)
	for {
		fmt.Print(">>> ")
		text, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		text = strings.TrimSpace(text)
		if text == "/exit" || text == "/quit" {
			return nil
		}
		if text == "/bye" {
			return nil
		}
		if text != "" {
			messages = append(messages, map[string]string{"role": "user", "content": text})
			body, _ := json.Marshal(map[string]any{"model": model, "messages": messages, "stream": true})
			resp, requestErr := apiRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			if requestErr != nil {
				return requestErr
			}
			if resp.StatusCode != http.StatusOK {
				requestErr = responseError(resp)
				resp.Body.Close()
				return requestErr
			}
			answer, requestErr := printOpenAIStream(resp.Body)
			resp.Body.Close()
			if requestErr != nil {
				return requestErr
			}
			fmt.Print("\n\n")
			if answer == "" {
				fmt.Println("(The model returned an empty response.)")
			}
			messages = append(messages, map[string]string{"role": "assistant", "content": answer})
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

func printOpenAIStream(body io.Reader) (string, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var answer strings.Builder
	for scanner.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "data:"))
		if line == "" || line == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			continue
		}
		if chunk.Error.Message != "" {
			return answer.String(), errors.New(chunk.Error.Message)
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			part := chunk.Choices[0].Delta.Content
			fmt.Print(part)
			answer.WriteString(part)
		}
	}
	return answer.String(), scanner.Err()
}

func apiRequest(method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, lumbreBaseURL()+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key := os.Getenv("LUMBRE_API_KEY"); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	return (&http.Client{Timeout: 0}).Do(req)
}

func ensureServer() error {
	resp, err := apiRequest(http.MethodGet, "/api/v1/health", nil)
	if err == nil {
		resp.Body.Close()
		return nil
	}
	base, parseErr := url.Parse(lumbreBaseURL())
	if parseErr != nil || base.Hostname() != "127.0.0.1" && base.Hostname() != "localhost" && base.Hostname() != "::1" {
		return fmt.Errorf("could not connect to Lumbre at %s: %w", lumbreBaseURL(), err)
	}
	executable, execErr := os.Executable()
	if execErr != nil {
		return fmt.Errorf("locate Lumbre executable: %w", execErr)
	}
	command := exec.Command(executable, "serve")
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		return fmt.Errorf("start Lumbre server: %w", err)
	}
	if err := command.Process.Release(); err != nil {
		return fmt.Errorf("detach Lumbre server: %w", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := apiRequest(http.MethodGet, "/api/v1/health", nil)
		if err == nil {
			resp.Body.Close()
			fmt.Println("Started Lumbre server.")
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("Lumbre server did not start; run `lumbre serve` to see diagnostic output")
}

func responseError(resp *http.Response) error {
	content, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("request failed (%s): %w", resp.Status, err)
	}
	var body struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(content, &body) == nil && body.Error != "" {
		return errors.New(body.Error)
	}
	return fmt.Errorf("request failed: %s: %s", resp.Status, strings.TrimSpace(string(content)))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "lumbre:", err)
	os.Exit(1)
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if value := os.Getenv(key); value != "" {
		var parsed int
		if _, err := fmt.Sscanf(value, "%d", &parsed); err == nil {
			return parsed
		}
	}
	return fallback
}

func lumbreBaseURL() string {
	base := envOr("LUMBRE_URL", fmt.Sprintf("http://127.0.0.1:%d", envInt("LUMBRE_PORT", 11435)))
	return strings.TrimRight(base, "/")
}

func llamaServerPath() string {
	if path := os.Getenv("LUMBRE_LLAMA_SERVER"); path != "" {
		return path
	}
	executable, err := os.Executable()
	if err == nil {
		name := "llama-server"
		if os.PathSeparator == '\\' {
			name += ".exe"
		}
		sibling := filepath.Join(filepath.Dir(executable), name)
		if info, err := os.Stat(sibling); err == nil && !info.IsDir() {
			return sibling
		}
	}
	return "llama-server"
}

func humanSize(size int64) string {
	if size >= 1_000_000_000 {
		return fmt.Sprintf("%.1f GB", float64(size)/1_000_000_000)
	}
	if size >= 1_000_000 {
		return fmt.Sprintf("%.0f MB", float64(size)/1_000_000)
	}
	return fmt.Sprintf("%d B", size)
}
