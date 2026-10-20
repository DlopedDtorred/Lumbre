# Lumbre

**Private, local AI — no Ollama required.** Lumbre is written in Go and
distributed as prebuilt packages for Linux, Windows, and macOS. Install the
program without Go, CMake, or a compiler; the package includes a CPU build of
`llama-server`. Download GGUF models separately.

## Install

Linux or macOS:

```sh
curl -fsSL https://raw.githubusercontent.com/DlopedDtorred/Lumbre/main/scripts/install.sh | sh
```

Windows PowerShell:

```powershell
irm https://raw.githubusercontent.com/DlopedDtorred/Lumbre/main/scripts/install.ps1 | iex
```

The installer downloads the package for your OS and architecture, verifies its
SHA-256 checksum, installs it for your user, and adds the CLI to your `PATH`.
It does not require administrator access. Open a new terminal and run:

```sh
lumbre run
```

The CLI starts the local server automatically and lets you choose an installed
model. Or run `lumbre serve` and open <http://127.0.0.1:11435> to use the web
interface. In **Models → Add a model**, paste a direct HTTPS link to a public
GGUF file. Model files are not bundled with the app because they can be several
gigabytes.

## CLI

```text
lumbre run [MODEL]              Load a model and start an interactive chat
lumbre pull MODEL_URL           Download a GGUF model from a public HTTPS URL
lumbre list                     List installed models (alias: ls)
lumbre ps                       Show the currently loaded model
lumbre show MODEL               Show model size and format
lumbre rm MODEL                 Delete an installed model
lumbre stop                     Unload the current model
lumbre serve [--host HOST] [--port PORT]
lumbre version
lumbre help
```

Use `/bye` or `/exit` to end a chat. Models are stored under
`~/.lumbre/models`; set `LUMBRE_MODELS` to choose another directory. For
example:

```sh
lumbre pull 'https://huggingface.co/OWNER/REPO/resolve/main/model.Q4_K_M.gguf'
lumbre list
lumbre run model.Q4_K_M.gguf
lumbre show model.Q4_K_M.gguf
lumbre ps
lumbre stop
lumbre rm model.Q4_K_M.gguf
```

Choose a model whose quantization fits your available RAM or VRAM, and review
its license and model card before downloading it.

## CPU, GPU, and servers

The official packages include a CPU engine. GPU acceleration requires a
`llama-server` build configured for the target GPU, such as CUDA, Metal, or
Vulkan. Set `LUMBRE_GPU_LAYERS=0` to force CPU inference, or choose an
offload-layer count. `LUMBRE_CONTEXT_SIZE` controls the context size (default
`8192`); `LUMBRE_THREADS` optionally sets CPU threads. Performance depends on
the model, quantization, hardware, and engine build; Lumbre does not claim to
outperform other runtimes.

By default, the server listens only on `127.0.0.1:11435`. For remote access,
set `LUMBRE_HOST` and `LUMBRE_API_KEY`, and protect the service with a firewall
and TLS reverse proxy. The server exposes an OpenAI-compatible
`POST /v1/chat/completions` endpoint.

## Project layout

- `core/` — Go CLI, API server, and llama.cpp process manager.
- `web/` — local chat interface and installable progressive web app.
- `app/` — Tauri desktop shell.
- `scripts/` — per-user installers.
- `.github/workflows/` — CI and prebuilt release package workflows.

Run the Go tests and build the CLI from source:

```sh
cd core
go test ./...
go build -o lumbre .
```
