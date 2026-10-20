# Lumbre

**IA local, privada y sin Ollama.** Lumbre esta escrita en Go y empaqueta
binarios ya compilados para Linux, Windows y macOS: se instala sin Go, CMake
ni compiladores. El servidor ejecuta `llama-server` compilado para cada
sistema y trabaja con modelos GGUF descargados por separado.

## Instalar

Linux o macOS:

```sh
curl -fsSL https://raw.githubusercontent.com/DlopedDtorred/Lumbre/main/scripts/install.sh | sh
```

Windows, en PowerShell:

```powershell
irm https://raw.githubusercontent.com/DlopedDtorred/Lumbre/main/scripts/install.ps1 | iex
```

El instalador descarga el paquete adecuado para la arquitectura, comprueba su
SHA-256, lo instala en la carpeta del usuario y anade la CLI al `PATH`. No
requiere permisos de administrador. Abre una terminal nueva y ejecuta:

```sh
lumbre serve
```

Visita <http://127.0.0.1:11435>. La web tambien puede instalarse como
aplicacion PWA desde el menu del navegador. Descarga un archivo `.gguf` en
**Modelos -> Anadir modelo**; los modelos no se incluyen en el instalador
porque pueden ocupar varios GB.

> El instalador se activa al publicar una version de GitHub que adjunte los
> paquetes de lanzamiento. GitHub Actions los compila para cada sistema; el
> usuario no compila el motor en su equipo.

## Estructura y desarrollo

- `core/` — CLI, API y gestion del motor, en Go.
- `web/` — chat web instalable (PWA).
- `app/` — shell de escritorio Tauri.
- `scripts/` — instaladores por usuario.
- `.github/workflows/` — pruebas y compilacion de versiones multiplataforma.

Para desarrollar la CLI:

```sh
cd core
go test ./...
go build -o lumbre .
```

## Modelos y hardware

Los GGUF se guardan en `~/.lumbre/models` (en Windows, en la carpeta home del
usuario); `LUMBRE_MODELS` permite elegir otra ubicacion. Puedes descargar un
modelo publico con la CLI:

```sh
lumbre pull 'https://huggingface.co/OWNER/REPO/resolve/main/model.Q4_K_M.gguf'
lumbre models
lumbre chat model.Q4_K_M.gguf
```

Los paquetes oficiales incluyen el motor CPU. Para activar CUDA, Metal o
Vulkan se necesita una compilacion del motor compatible con tu GPU. El
rendimiento y el tamano de modelo adecuado dependen del hardware y la
cuantizacion; no afirmamos superar a otros motores.

## Servidor y API

```sh
lumbre serve [--host HOST] [--port PORT]
```

Por defecto solo escucha en `127.0.0.1:11435`. Para otras interfaces configura
`LUMBRE_HOST` y define `LUMBRE_API_KEY`; protegelo tambien con cortafuegos y
TLS antes de exponerlo a redes no confiables. Ofrece `POST
/v1/chat/completions` compatible con la API de OpenAI.
