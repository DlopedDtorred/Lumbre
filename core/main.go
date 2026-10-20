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
	"os"
	"path/filepath"
	"strings"
)

const version = "0.1.1"

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
		if err := runModels(); err != nil {
			fatal(err)
		}
	case "pull":
		if len(os.Args) < 3 {
			fatal(errors.New("usage: lumbre pull MODEL_URL"))
		}
		if err := runPull(os.Args[2]); err != nil {
			fatal(err)
		}
	case "chat":
		model := ""
		if len(os.Args) >= 3 {
			model = os.Args[2]
		}
		if err := runChat(model); err != nil {
			fatal(err)
		}
	default:
		printUsage()
		os.Exit(2)
	}
}

func printUsage() {
	fmt.Print(`Lumbre — local AI, your way

Usage:
  lumbre serve [--host HOST] [--port PORT]  Start the local API and web app
  lumbre models                             List local GGUF models
  lumbre pull MODEL_URL                     Download a GGUF model from HTTPS
  lumbre chat [MODEL]                       Start an interactive chat
  lumbre version                            Print the version
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
		fmt.Println("No GGUF models installed. Download one with: lumbre pull MODEL_URL")
		return nil
	}
	fmt.Printf("%-40s %s\n", "MODEL", "SIZE")
	for _, model := range result.Models {
		fmt.Printf("%-40s %s\n", model.Name, humanSize(model.Size))
	}
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
	if model == "" {
		fmt.Print("Model: ")
		value, err := reader.ReadString('\n')
		if err != nil && value == "" {
			return errors.New("provide a model name, for example: lumbre chat model.gguf")
		}
		model = strings.TrimSpace(value)
	}
	messages := make([]map[string]string, 0)
	fmt.Printf("Chatting with %s. Type /exit to leave.\n\n", model)
	for {
		fmt.Print("you> ")
		text, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		text = strings.TrimSpace(text)
		if text == "/exit" || text == "/quit" {
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
	return envOr("LUMBRE_URL", fmt.Sprintf("http://127.0.0.1:%d", envInt("LUMBRE_PORT", 11435)))
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
