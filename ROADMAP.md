# mini Roadmap

Reordenado por valor real (no por dependencia técnica). Cada item lo cierro
con tests + commit antes de pasar al siguiente.

---

## Fase 1 — Seguridad y usabilidad inmediata

- [x] **`web_fetch`** real (GET + HTML→text)
- [x] **`web_search`** real (DDG scraper)
- [ ] **Confirmation policy** ← AHORA. Tools "destructivas" (bash con
  rm/dd/mv/>, write masivo, bash_input) requieren aprobación. REPL pregunta
  por stdin; one-shot deniega por defecto a menos que `--yolo`. La decisión
  "allow always" se recuerda por sesión.
- [ ] **`/jobs`** slash command + tool `bash_kill(job_id)`.

## Fase 2 — Sesiones largas

- [ ] **Compactación automática del historial**: cuando el prompt > 50% del
  context window, el agente se auto-resume turn-by-turn y reemplaza el bloque.
- [ ] **Sesiones persistentes** opcionales (`~/.mini/sessions/<id>.jsonl`)
  con `/save` y `/load`.
- [ ] **Token budget visible** en REPL (tokens usados vs cap del provider).

## Fase 3 — Multi-provider (valida la abstracción)

- [ ] **OpenAI provider**: API key, sin OAuth, default `gpt-5`.
- [ ] **Anthropic provider**: API key, default Claude más reciente.

## Fase 4 — MCP

- [ ] **MCP client** (stdio + HTTP/SSE).
- [ ] Config `~/.mini/mcp.json` con lifecycle (spawn/restart).

## Fase 5 — Pulido

- [ ] Tests para `bash_input`/`bash_output` con el API nuevo.
- [ ] Flag `--provider` y `--model` en `mini chat`.
- [ ] GitHub Releases con binarios pre-compilados.
