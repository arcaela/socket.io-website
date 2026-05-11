# mini Roadmap

Orden por valor / dependencia. Cada item lo cierro con tests + commit antes de
pasar al siguiente.

---

## Fase 1 — Endurecer fundamentos (en curso)

- [ ] **`web_fetch`** real: HTTP GET con timeout, redirects, HTML → texto.
  ~100 LOC. Empezamos por aquí (más valor por menos esfuerzo).
- [ ] **`web_search`** real: DuckDuckGo HTML scrape como v1 (sin API key).
  Plan B: dejar pluggable para añadir Brave/Serper después.
- [ ] **`/jobs`** slash command + tool `bash_kill(job_id)`: listar y matar
  background jobs desde el REPL.
- [ ] **Confirmation policy** para tools destructivas (`bash` con `rm`/`mv`/`>`,
  `write` que reemplace mucho). Modo `--yolo` para saltarla.

## Fase 2 — Multi-provider

- [ ] **OpenAI provider** (gpt-5, etc.) — valida la abstracción. Auth por
  API key (OPENAI_API_KEY env), no OAuth.
- [ ] **Anthropic provider** (Claude) — segunda validación.

## Fase 3 — MCP

- [ ] **MCP client** stdio + HTTP/SSE — abre el ecosistema externo
  (filesystem, github, slack, etc.) sin codear cada tool.
- [ ] Config `~/.mini/mcp.json` para servers + lifecycle (spawn/restart).

## Fase 4 — UX de sesiones largas

- [ ] **Sesiones persistentes** opcionales (`~/.mini/sessions/<id>.jsonl`)
  con `/save` y `/load`.
- [ ] **Compactación de historial** automática cuando prompt > 50% del
  context window: el modelo se auto-resume.
- [ ] **Token budget** visible en REPL (usado vs cuota estimada).

## Fase 5 — Pulido

- [ ] Implementar tests para `bash_input` / `bash_output` (eliminados al
  refactorizar; rehacerlos con el nuevo API).
- [ ] Soporte real para `--provider` flag (hoy sólo env var).
- [ ] Distribución: GitHub Releases con binarios pre-compilados para
  linux/amd64, linux/arm64, darwin/arm64.
