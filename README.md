# magpie

One place to pick every agent's model: Codex on DeepSeek, Claude Code
on Kimi, Gemini CLI on GLM, from the menu bar. [usemagpie.ai](https://usemagpie.ai)

`magpie` is a single screen that lists each AI agent on your machine and
the model it is set to. Click a value, pick a model. That is the whole app.

It lives in the menu bar: click the icon and a panel drops down; the same
screen also opens as a normal window (`magpie`, or *Open magpie* in the tray menu),
and there is a terminal version (`magpie tui`) and a plain CLI.

```
  ◉ magpie

  ▸ Claude Code   claude-fable-5-1[1m]                        ~/.claude/settings.json
    Codex         gpt-6-astra   effort medium
    Gemini CLI    gemini-3.1-pro
    OpenCode      anthropic/claude-sonnet-5   small anthropic/claude-haiku-4-5
    Pi            openrouter/z-ai/glm-5.2:batch
    Goose         anthropic/claude-sonnet-5
    Cursor        auto
    Copilot CLI   claude-fable-5

  ↑↓ agent  ·  ←→ field  ·  ↵ change  ·  s save profile  ·  p profiles  ·  q quit
```

- **One small binary.** Under 15 MB with the desktop app (it uses the system
  webview through [Wails](https://wails.io), nothing bundled), 7 MB for the
  terminal-only build. macOS, Linux and Windows.
- **Edits config files surgically.** Only the one key you change is touched;
  comments, ordering and indentation in your `settings.json`, `config.toml`,
  `opencode.jsonc` or `config.yaml` survive intact. Writes are atomic.
- **One endpoint for every agent.** magpie runs a local gateway that speaks
  OpenAI chat completions, OpenAI Responses and the Anthropic Messages API,
  and forwards to whichever vendor serves the model. Codex, Claude Code,
  OpenCode and the rest all point at `http://127.0.0.1:3425/v1` and pick
  from one catalog; the translation between APIs happens in magpie, streaming
  and tool calls included.
- **Your subscriptions, shared.** Sign in to Claude Code, Codex (ChatGPT)
  or Copilot and that login shows up as a provider: every other agent can
  use its models through the gateway, with nothing copied and no key to
  paste.
- **Providers with one field.** Pick a preset (Anthropic, OpenAI, Gemini,
  DeepSeek, Kimi, GLM, MiniMax, Qwen, Mistral, Groq, xAI, OpenRouter,
  Together, Fireworks, SiliconFlow, AiHubMix, 302.AI, Ollama, LM Studio…),
  paste a key, done. Custom vendors need a name and a base URL. magpie never
  reads keys from your shell environment.
- **Real model lists, nothing compiled in.** With a key in hand magpie asks
  the vendor which models it serves and offers exactly those; the
  [models.dev](https://models.dev) catalog fills in names, reasoning efforts
  and the list for vendors that have none, and refreshes itself in the
  background once it goes stale. Choose which models each provider exposes,
  or expose them all — a model released this morning is in the picker on
  the next refresh.
- **Profiles.** Snapshot every agent's settings under a name and switch all of
  them back in one move.
- **Real logos, no framework.** Plain HTML over the system webview; brand
  icons from [lobehub/icons](https://github.com/lobehub/lobe-icons).

## Agents

| Agent        | File                              | Fields          |
| ------------ | --------------------------------- | --------------- |
| Claude Code  | `~/.claude/settings.json`         | provider, model |
| Codex        | `~/.codex/config.toml`            | provider, model, effort |
| Gemini CLI   | `~/.gemini/settings.json`, `~/.gemini/.env` | auth, model |
| OpenCode     | `~/.config/opencode/opencode.json(c)` | model, small |
| Pi           | `~/.pi/agent/settings.json`       | model           |
| Goose        | `~/.config/goose/config.yaml`     | model           |
| Cursor CLI   | `~/.cursor/cli-config.json`       | model           |
| Copilot CLI  | `~/.copilot/settings.json`        | model           |
| Crush        | `~/.config/crush/crush.json`      | large, small    |

Provider-scoped agents (OpenCode, Pi, Goose, Crush) take `provider/model`.
Only agents that are installed or configured are shown.

## Providers and the gateway

Every model an agent can pick is spelled `provider/model` and served by
magpie's gateway, so agents never hold vendor keys or vendor URLs. Add a
provider, and its models appear in every agent's picker:

```sh
magpie presets                          # the vendors magpie knows, grouped: vendors, relays, local
magpie provider add deepseek sk-…       # a preset needs only the key
magpie provider add ollama              # local servers need none
magpie provider add "My Relay" url=https://relay.example.com/v1 key=sk-… models=gpt-5.5,claude-sonnet-5
magpie providers                        # host, key, exposed models, who uses what
magpie provider deepseek                # one provider in detail
magpie provider models deepseek         # re-fetch the vendor's list (add ids to choose which to expose)
magpie provider test deepseek           # one tiny request per API, with latency
magpie provider key deepseek sk-…       # replace the key
magpie provider rm deepseek
magpie models                           # the catalog agents see
magpie claude deepseek/deepseek-chat    # use it
```

Custom providers take `url=` (an OpenAI-compatible base), `anthropic=` (an
Anthropic-compatible base), or both, plus `responses=` when the vendor has a
separate Responses endpoint, `catalog=` to borrow a models.dev list, and
`models=` to name the models to expose. Anything a preset does not know can
be overridden the same way.

### Signed-in agents as providers

An agent you have signed in to is a subscription with models behind it, so
magpie offers it as a provider too. Claude Code (an OAuth login in the macOS
Keychain or `~/.claude/.credentials.json`), Codex (a ChatGPT login in
`~/.codex/auth.json`) and Copilot (a GitHub login in
`~/.config/github-copilot/apps.json`) appear in `magpie providers` and in the
Providers tab as *signed in as …*, with their models spelled
`claude/claude-sonnet-5`, `codex/gpt-5.5` or `copilot/claude-sonnet-4.5` in
every other agent's picker. magpie reads the agent's own credentials each
time, refreshes tokens the way the agent does — writing a rotated token
back where the agent will find it — and stores nothing but your model
picks; sign out of the agent and the provider is gone. The model list is
the vendor's own too: magpie asks Anthropic's, Copilot's or Codex's API with
that same sign-in, so a model added upstream appears on the next refresh.
The ChatGPT backend only streams and rejects a few parameters, so magpie
translates non-streaming requests and drops what it would refuse.
Claude subscriptions are different: Anthropic classifies another agent's
system prompt as third-party traffic even when the OAuth request otherwise
looks like Claude Code. magpie therefore drives the genuine local `claude`
binary for every Claude subscription generation. The caller's tools are
bridged into that live turn over MCP, and tool results resume the same Claude
Code process; Pi, OpenCode and every other agent use this path automatically.
The generated harness stays out of Anthropic's system-prompt classifier while
its instructions remain part of the user context. This requires Claude Code
to be installed and signed in. A Gemini CLI Google login is planned.

### Connecting anything else

The gateway listens on `127.0.0.1:3425` (`MAGPIE_ADDR` changes it) and starts
with the app; `magpie serve` runs it alone. It exposes:

| Path                     | API                        |
| ------------------------ | -------------------------- |
| `/v1/chat/completions`   | OpenAI chat completions    |
| `/v1/responses`          | OpenAI Responses           |
| `/v1/messages`           | Anthropic Messages         |
| `/v1/messages/count_tokens` | Anthropic token counting |
| `/v1beta/models/{model}:generateContent` | Google Gemini (also `:streamGenerateContent`, `:countTokens`) |
| `/v1/models`, `/v1beta/models` | the catalog            |

Requests pass straight through when the vendor speaks the agent's API and
are translated otherwise, streaming, tool calls and reasoning included. The
key is `magpie` (any value works; the gateway only listens on loopback), and
models are named `provider/model`. Anything with a base-URL setting can use
it:

| Tool speaks | Base URL                   | Environment                                   |
| ----------- | -------------------------- | --------------------------------------------- |
| OpenAI      | `http://127.0.0.1:3425/v1` | `OPENAI_BASE_URL`, `OPENAI_API_KEY=magpie`      |
| Anthropic   | `http://127.0.0.1:3425`    | `ANTHROPIC_BASE_URL`, `ANTHROPIC_API_KEY=magpie` |
| Gemini      | `http://127.0.0.1:3425`    | `GOOGLE_GEMINI_BASE_URL`, `GEMINI_API_KEY=magpie` |

The *Gateway* tab in the app has this as copy buttons and ready-made
snippets (shell, curl, Python, Node) for each API, the list of model ids,
and the recent calls; `MAGPIE_DEBUG=1` logs every call to the terminal.

**Claude Code** gets `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN` and the
model variables in the `env` block of `settings.json`; picking a native
model (`opus`, `sonnet`…) removes them and restores whatever was there.

**Codex** gets a `[model_providers.magpie]` table, `model_catalog_json`
pointing at `~/.codex/magpie-models.json` (written from the catalog, so the
models show in Codex's own list) and a valid `model`/`effort`; picking a
native model removes all of that. Your ChatGPT sign-in is never touched.
Codex reads its model list at start-up, so restart it after a switch.

**OpenCode, Pi, Crush** get a `magpie` provider entry and `magpie/provider/model`.

**Gemini CLI** switches `auth` between API key, Google account and Vertex;
the API key goes to `~/.gemini/.env`. Picking a catalog model points
`GOOGLE_GEMINI_BASE_URL` at the gateway (which speaks the Gemini API), sets
`auth` to API key with the gateway token, and names the model in
`settings.json`; a native model puts the previous auth back.

## Import links

A provider can be shared as a single link. `magpie://import?<parameters>`
describes a provider, either from a preset or fully custom, and every value
must be URL-encoded:

```sh
magpie import 'magpie://import?preset=deepseek&key=sk-…'
magpie import 'magpie://import?name=Acme%20Relay&chat=https://api.acme.example/v1&anthropic=https://api.acme.example&key=sk-…&models=gpt-5.5,claude-sonnet-5'
```

These are equivalent spellings and are all accepted: `magpie:import?…`,
`magpie:///import?…` and `MAGPIE://import/?…` (the scheme is case-insensitive,
and `import` may have a trailing slash).

| Parameter | Meaning |
| --- | --- |
| `preset` | A preset id (see `magpie presets`). Its endpoints are used. |
| `region` | Which region of a preset with several to use. An unknown region is an error. |
| `name` | Provider name. Required without `preset`; with a preset it overrides the preset's name. Names longer than 80 characters are truncated. |
| `id` | Provider id. Defaults to a slug of the name; a preset link defaults to the preset's own id. Give `id` explicitly if the name has no ASCII letters or digits, because the id would otherwise be empty. |
| `key` | API key. If omitted, you add it later (see below). |
| `chat` | OpenAI Chat Completions base URL, usually ending in `/v1`. |
| `responses` | OpenAI Responses base URL, usually ending in `/v1`. |
| `anthropic` | Anthropic Messages base URL. This is the root, without `/v1`. |
| `models` | Model ids to expose, comma-separated. Empty entries and surrounding spaces are ignored. Up to 200. |
| `catalog` | A models.dev provider id, used for model names and reasoning efforts. An unknown value is ignored. |
| `website`, `keys` | The vendor's website and its API-key page. Only `https` is accepted; any other value is ignored. |

With `preset`, the endpoints come from the preset (and the chosen region), so
`chat`, `responses`, `anthropic`, `catalog`, `website` and `keys` have no
effect. Without `preset`, give at least one of `chat`, `responses` or
`anthropic`.

Base URLs must be `https`. The exception is a local or LAN address: `localhost`,
a loopback address, a private-network IP, or a `.local` name may use `http`.
A base URL must not contain a username or password, a query string or a
fragment, and must not use any other scheme such as `file:`. A trailing `/` is
removed. Any link that breaks these rules is rejected with an error, and nothing
is saved.

### Confirmation

A link never saves anything by itself. `magpie import` first prints a summary
of the provider that would be added: its name, its id, the endpoint hosts that
will receive your prompts and your key, and its models. It then asks
`Add it? [y/N]`. Only `y` or `yes` saves it; anything else exits with
"nothing added".

- If the link's id matches a provider you already have, the summary includes a
  warning that this will replace the existing provider. The confirmation still
  comes first.
- `magpie import -y '<link>'` skips the question. Use it in scripts only.
- When standard input is not a terminal and `-y` is not given, `magpie import`
  refuses to run. It never assumes yes.
- If the link has no `key`, or the provider is still unusable after saving, the
  output tells you to add the key with `magpie provider key <id> <key>`.

On a desktop build, the operating system passes `magpie://` links to the app
as its first command-line argument, and the running app receives them. The
terminal-only build behaves like `magpie import`.

### Sharing through a web page

Put the link's parameters after `#` on a web page:

```
https://usemagpie.ai/import#<the same parameters>
```

The browser does not send the part after `#` to the server, so the web host
never receives the key or the prompts. The page itself can still read the
fragment, so link only to pages you trust.

## Install

Download the app for macOS, Windows or Linux from
[usemagpie.ai](https://usemagpie.ai), or install it from a terminal (on
Linux, the desktop app when WebKitGTK 4.1 is installed, the command
otherwise):

```sh
curl -fsSL https://usemagpie.ai/install.sh | sh
```

Mac releases are signed and notarised; the Windows and Linux builds are not
signed yet (Windows SmartScreen may ask before the first run). Every build
keeps itself current: the app
downloads a new version in the background and installs it when you restart
(*Restart to Update* in the menu) or quit; `magpie update` does the same from
a terminal. Every release is on
[yetone/magpie-releases](https://github.com/yetone/magpie-releases/releases).

From source:

```sh
go install github.com/yetone/magpie@latest
```

or build locally:

```sh
make build            # ./magpie with the desktop app (needs cgo + the platform webview)
make app              # macOS: magpie.app, a menu bar app with no Dock icon
make cli              # terminal-only build, no cgo, cross-compiles anywhere
make release          # dist/: native app build + cli builds for every platform
make release-windows  # dist/: the Windows app, amd64 and arm64 (cross-compiles)
make release-linux    # dist/: the Linux app for this machine's arch
```

Linux needs `libgtk-3-dev` and `libwebkit2gtk-4.1-dev` for the app build
(the Makefile adds the `gtk3` tag; with plain `go build`, pass `-tags gtk3`);
Windows uses the WebView2 runtime that ships with the OS.

### Developing

```sh
make dev
```

builds with `-tags dev` and opens the app with the UI served straight from
`internal/gui/assets`: save `app.css`, `app.js` or `index.html` and the window
reloads itself. With `fswatch` installed (`brew install fswatch`), a change to a
Go file rebuilds and relaunches the app too. The dev build uses its own gateway
port (`DEV_ADDR`, default 127.0.0.1:3426), so a magpie you already run keeps
serving your agents. Point it at a scratch home to keep your real agent
configs out of it:

```sh
HOME=/tmp/magpie-home XDG_CONFIG_HOME=/tmp/magpie-home/.config make dev
```

`MAGPIE_THEME=light|dark` forces the palette and `MAGPIE_DEBUG=1` prints what the
gateway translates.

## Use

```sh
magpie                          # open the app: a window plus the menu bar icon
magpie tray                     # menu bar icon only (use this in your login items)
magpie tui                      # the same thing, in the terminal
magpie ls                       # list every agent and its current settings
magpie claude opus              # set a model (agent names accept prefixes: cc, oc, gem …)
magpie codex gpt-5.6-sol
magpie codex effort high        # other fields
magpie codex xhigh              # bare effort levels are recognised too
magpie codex deepseek/deepseek-chat   # any catalog model, through the gateway
magpie claude moonshot/kimi-k2.5
magpie gemini auth api-key
magpie opencode anthropic/claude-sonnet-5
magpie oc small anthropic/claude-haiku-4-5

magpie save work                # snapshot everything as a profile
magpie use work                 # switch back
magpie profiles
magpie rm work

magpie sync                     # refresh the models.dev catalog and every live model list
```

In the app, click any value to open a filtered list; type to search or to
enter something that is not listed; `esc` closes the panel. Profiles are the
chips at the bottom: click to apply, `×` to delete, *+ save current* to add.
The *Providers* tab of the window lists your providers with the agents on
each; click a row to change the key or the exposed models, *Test* it, or
click an agent icon to point that agent at one of its models. *Add
provider* shows the presets as tiles: pick one, paste the key.

Keys in the terminal version:

| Key        | Action                                |
| ---------- | ------------------------------------- |
| `↑` `↓`    | choose agent                          |
| `←` `→`    | choose field (model, effort, small …) |
| `↵`        | open the picker                       |
| type       | filter; enter accepts custom values   |
| `s`        | save current setup as a profile       |
| `p`        | apply or delete (`ctrl+d`) a profile  |
| `S`        | sync the model catalog                |
| `q`        | quit                                  |

Agents read their config at startup, so a running session keeps its model
until you start a new one.

## Files

- `~/.config/magpie/profiles.json` — saved profiles
- `~/.config/magpie/providers.json` — your providers, keys included (0600)
- `~/.config/magpie/stash.json` — values magpie replaced, restored on switch-back
- `~/.cache/magpie/models.json` — models.dev catalog (OpenCode's cache at
  `~/.cache/opencode/models.json` is used when present)
- `~/.cache/magpie/models/<provider>.json` — model lists fetched from vendors

`XDG_CONFIG_HOME` and `XDG_CACHE_HOME` are respected.

## License

MIT. See [LICENSE](LICENSE).
