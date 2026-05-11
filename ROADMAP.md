# mini Roadmap

Estado actual: 7 commits acumulados en `mini-v4` (Fase 1–4 cerradas).
Próximo bloque ordenado por relación valor/coste:

---

## Fase 5 — Visibilidad de coste y sesiones (en curso)

- [ ] **Token budget visible**: contador acumulado por sesión en REPL +
  slash `/tokens`. Muestra qué tan cerca está el prompt del umbral de
  compactación.
- [ ] **Sesiones persistentes**: guardar la conversación a
  `~/.mini/sessions/<id>.jsonl` y poder cargarla. Slash `/save [name]` y
  `/load <id>`. Lista con `/sessions`.

## Fase 6 — CLI polish

- [ ] **Flags `--provider`**, **`--model`**, **`--yolo`** en `mini chat`
  (hoy sólo por env var).
- [ ] **Tests reales de `bash_input` / `bash_output`** con el API nuevo
  (los viejos se borraron al refactor).

## Fase 7 — Más proveedores y transportes

- [ ] **Anthropic provider** (tercera validación de la abstracción).
- [ ] **MCP HTTP/SSE transport** (hoy sólo stdio).

## Fase 8 — Distribución

- [ ] **GitHub Actions** para producir binarios pre-compilados en releases
  (`linux/amd64`, `linux/arm64`, `darwin/arm64`).
- [ ] **README publicable** con ejemplos reales del agente.
