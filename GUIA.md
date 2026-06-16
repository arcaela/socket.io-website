# Guía de uso de `mini`

Una guía en lenguaje sencillo, con ejemplos reales, para entender qué es
`mini`, qué puede hacer por ti y cómo usarlo en sus dos formas: **conversando**
(modo interactivo) o **dándole una orden suelta** (modo no interactivo).

---

## ¿Qué es `mini`, en una frase?

`mini` es un asistente que vive en tu terminal. Le hablas en lenguaje normal y
él **trabaja en tu computadora por ti**: lee archivos, busca cosas, ejecuta
comandos, edita código, busca en internet… y te cuenta lo que hizo.

Piensa en él como un becario muy rápido al que le pides tareas y las realiza,
pidiéndote permiso antes de hacer algo que podría romper o borrar algo.

### Tres palabras que conviene conocer

- **Herramienta** (*tool*): una capacidad concreta de `mini`. Por ejemplo "leer
  un archivo", "buscar en internet" o "ejecutar un comando". Él decide cuáles
  usar según lo que le pidas.
- **Proveedor** (*provider*): el "cerebro" de IA que `mini` usa por detrás.
  Puedes elegir entre Google (Gemini), OpenAI o Anthropic. Por defecto usa
  Gemini, que tiene una capa gratuita.
- **Sesión**: una conversación completa. Puedes guardarla y retomarla después.

---

## Las dos formas de usarlo

| | **Interactivo** (conversación) | **No interactivo** (orden suelta) |
|---|---|---|
| Cómo se abre | `mini` | `mini chat "tu orden"` |
| Cómo se siente | Como un chat: preguntas, responde, sigues preguntando | Como dar una instrucción y recibir el resultado |
| Recuerda lo anterior | Sí, durante toda la conversación | No, cada orden empieza de cero |
| Te pide permiso | Sí, te pregunta antes de algo riesgoso | No pregunta: por seguridad **rechaza** lo riesgoso (a menos que tú lo permitas) |
| Ideal para | Trabajar codo a codo en algo que evoluciona | Tareas puntuales o automatizar |

---

## Modo interactivo: conversar con `mini`

### Cómo entrar

```
mini
```

Verás algo así y un cursor esperando que escribas:

```
mini-cli  provider=gemini  model=gemini-2.5-flash  tools=11
Type a message and press Enter. Slash commands: /help, /tools, /whoami, ...

›
```

A partir de ahí, simplemente **escribe lo que quieres** y pulsa Enter.

### Qué puedes hacer (ejemplos reales)

```
› ¿qué archivos de configuración hay en esta carpeta y qué hace cada uno?
```
`mini` busca, los lee y te explica.

```
› crea un archivo llamado notas.txt con una lista de la compra de ejemplo
```
Te avisará: *"voy a crear un archivo"* y te pedirá confirmación.

```
› busca en internet cuál es la última versión de Go y resúmemelo
```
Hace la búsqueda y te da el resumen.

```
› arranca el servidor de desarrollo y dime cuándo esté listo
```
Lo deja corriendo en segundo plano y te avisa.

### Te pide permiso antes de lo arriesgado

Cuando una acción puede modificar o borrar algo, `mini` se detiene y pregunta:

```
⚠  [high risk] write {"path":"notas.txt", ...}
   approve? [y]es / [n]o / [a]lways / [N]ever (default no):
```

- `y` → hazlo esta vez
- `n` → no lo hagas (por defecto si solo pulsas Enter)
- `a` → hazlo y no me vuelvas a preguntar por esta herramienta
- `N` → nunca uses esta herramienta en esta sesión

> Truco: para **previsualizar** un cambio sin aplicarlo, pídele *"muéstrame el
> cambio antes de aplicarlo"*. Te enseña el "antes y después" (un diff) sin
> tocar el archivo, y eso no necesita permiso porque no modifica nada.

### Comandos especiales (empiezan con `/`)

Dentro de la conversación, además de hablar, tienes atajos:

| Escribe | Para… |
|---|---|
| `/help` | ver la lista de atajos |
| `/tools` | ver qué herramientas tiene disponibles |
| `/whoami` | ver con qué cuenta/proveedor estás conectado |
| `/model` | ver o cambiar el modelo de IA |
| `/yolo on` / `/yolo off` | dejar de pedir permiso / volver a pedirlo |
| `/jobs` | ver procesos que dejó corriendo en segundo plano |
| `/tokens` | ver cuánto "consumo" llevas (ver más abajo) |
| `/compact` | resumir la conversación para que no se vuelva pesada |
| `/save mi-trabajo` | guardar esta conversación con un nombre |
| `/load mi-trabajo` | retomar una conversación guardada |
| `/sessions` | listar conversaciones guardadas |
| `/clear` | empezar la conversación de cero |
| `/history` | ver todo lo dicho hasta ahora |
| `/quit` | salir |

### Sobre `/tokens` y `/compact` (sin tecnicismos)

Cada vez que hablas con `mini`, él le manda a la IA **todo lo dicho hasta ese
momento**. Cuanto más larga la conversación, más "pesa" cada mensaje. Los
**tokens** son la unidad con la que se mide ese peso.

- `/tokens` te dice cuánto llevas y cuánto margen queda.
- `/compact` toma la parte vieja de la charla y la **resume**, para aligerar y
  poder seguir sin problemas. `mini` también lo hace **solo** cuando ve que la
  conversación se está volviendo demasiado larga, así no se "ahoga" a mitad de
  una tarea.

### Guardar y retomar trabajo

```
› /save refactor-login
saved 12 message(s) to ~/.mini/sessions/20260616T...-refactor-login.jsonl
```
Otro día:
```
mini
› /load refactor-login
loaded 12 message(s)
```
Y sigues justo donde lo dejaste.

---

## Modo no interactivo: una orden y listo

### Cómo se usa

```
mini chat "cuenta cuántos archivos .go hay y dime qué hace main.go"
```

`mini` ejecuta esa tarea de principio a fin y te muestra el resultado, sin
abrir una conversación. Cuando termina, vuelves a tu terminal normal.

### Para qué sirve

- **Tareas puntuales**: *"resume este archivo de log y dime si hay errores"*.
- **Automatizar**: meter el comando dentro de un script.
- **Respuestas rápidas** sin entrar a chatear.

### La diferencia de seguridad importante

En este modo **nadie está mirando la pantalla para dar permiso**. Por eso, por
defecto, `mini` **se niega** a hacer cosas arriesgadas (borrar, sobrescribir,
etc.) y solo hace lo seguro (leer, buscar, listar).

Si confías en la tarea y quieres que haga TODO sin frenar, añade `--yolo`:

```
mini chat --yolo "ordena los imports de todos los archivos .go y guárdalos"
```

> `--yolo` significa "hazlo sin pedirme permiso". Úsalo solo cuando estés
> seguro de lo que pides.

### Elegir otro cerebro de IA por una vez

```
mini chat -p openai -m gpt-5 "explica este error y propón una solución"
mini chat -p anthropic "revisa este texto y mejóralo"
```

- `-p` elige el proveedor (`gemini`, `openai`, `anthropic`)
- `-m` elige el modelo concreto

(Para OpenAI o Anthropic necesitas configurar tu clave; ver más abajo.)

---

## Comandos sueltos útiles (fuera de la conversación)

Estos los escribes directamente en tu terminal, no dentro del chat:

```
mini version        # muestra la versión instalada
mini whoami         # con qué cuenta y proveedor estás
mini tools          # lista todo lo que mini sabe hacer
mini help           # ayuda general
```

Para curiosear una herramienta concreta:

```
mini tools schema write     # qué información necesita la herramienta "write"
```

---

## Primera vez: conectar tu cuenta

Por defecto `mini` usa **Gemini** de Google, que tiene una capa gratuita. Solo
hay que autorizarlo una vez:

```
mini provider gemini auth
```

Te dará un enlace. Lo abres en tu navegador, das *Permitir*, y el navegador te
mostrará un error de "no se puede acceder" (es normal). Copias la dirección de
la barra del navegador y la pegas así:

```
mini provider gemini auth-complete "la-direccion-que-copiaste"
```

Y ya está. Para comprobar que quedó conectado:

```
mini whoami
```

### Si prefieres OpenAI o Anthropic

En vez de autorizar, defines tu clave una vez en tu terminal:

```
export OPENAI_API_KEY=tu-clave
mini chat -p openai "hola"

export ANTHROPIC_API_KEY=tu-clave
mini chat -p anthropic "hola"
```

---

## Cosas que conviene saber

- **Memoria a largo plazo**: puedes pedirle *"recuerda que prefiero respuestas
  en español"* y lo guardará para futuras conversaciones. Son notas estables
  sobre ti o tu proyecto, no apuntes de la tarea de hoy.

- **Procesos en segundo plano**: si le pides arrancar algo que no termina (un
  servidor, por ejemplo), lo deja corriendo y puedes consultarlo con `/jobs`.
  Cuando cierras `mini`, esos procesos se cierran solos.

- **Todo es auditable**: en cada paso `mini` te dice qué herramienta usó y con
  qué datos, para que nunca haya sorpresas.

- **Dónde guarda sus cosas**: en una carpeta oculta llamada `~/.mini`
  (tus sesiones guardadas, tu memoria, los registros de procesos).

---

## Resumen de un vistazo

- **¿Quieres trabajar codo a codo, paso a paso?** → escribe `mini` y conversa.
- **¿Quieres una tarea puntual o automatizar?** → `mini chat "tu orden"`.
- **¿Te preocupa que toque algo?** → en interactivo te pide permiso; en orden
  suelta rechaza lo riesgoso salvo que pongas `--yolo`.
- **¿La charla se hizo larga?** → `/compact`, o deja que lo haga solo.
- **¿Quieres continuar mañana?** → `/save` hoy, `/load` mañana.
