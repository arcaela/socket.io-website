# mini Roadmap

## Closed (done in `mini-v4`)

### Fase 1 — Seguridad y usabilidad
- [x] `web_fetch` (real HTTP + HTML→text)
- [x] `web_search` (DuckDuckGo HTML scraper)
- [x] Confirmation policy (risk types, Strict/Yolo/Terminal approvers)
- [x] `/jobs` slash + `bash_kill` tool

### Fase 2 — Sesiones largas
- [x] Compactación automática del historial (con `/compact` manual)
- [x] Token budget visible (`/tokens` + headroom hint)
- [x] Sesiones persistentes (`/save` / `/load` / `/sessions`)

### Fase 3 — Multi-provider
- [x] OpenAI provider (API key, streaming, OpenAI-compatible base URL)
- [x] Anthropic provider (`/v1/messages`, system out-of-band, tool_use blocks)

### Fase 4 — MCP
- [x] MCP client stdio transport (JSON-RPC 2.0, ~250 LOC, zero deps)
- [x] MCP HTTP transport (stateless POST + JSON response, header forwarding)

### Fase 5 — CLI polish
- [x] Flags `--provider`, `--model`, `--yolo` en `mini chat`
- [x] Tests reales de `bash_input` / `bash_output` con el API nuevo

### Fase 6 — Distribución
- [x] GitHub Actions: CI (vet+test+build smoke) + Release (linux amd64/arm64, darwin arm64)
- [x] README publicable con ejemplos reales

---

## Diferido

- **MCP HTTP/SSE streaming** sobre el mismo canal (hoy sólo request/response síncrono).
- **Compactación durante el turno** (hoy sólo al final del turno; un turno con MUY muchos pasos podría agotar contexto antes).
- **Windows support** (Setpgid y SIGTERM al process group son Unix-only).
- **Tool `edit_file` con preview de diff** antes de aplicar.
- **Multi-modal**: imágenes / archivos adjuntos en mensajes (sólo texto hoy).
- **Telemetría opcional** local (latencia por tool, costo agregado por sesión).
