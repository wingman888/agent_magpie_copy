# dial

One small dial for every coding agent's model.

`dial` is a single screen that lists each AI coding agent on your machine and
the model it is set to. Click a value, pick a model. That is the whole app.

It lives in the menu bar: click the icon and a panel drops down; the same
screen also opens as a normal window (`dial`, or *Open dial* in the tray menu),
and there is a terminal version (`dial tui`) and a plain CLI.

```
  ◉ dial

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

- **One small binary.** About 11 MB with the desktop app (it uses the system
  webview through [Wails](https://wails.io), nothing bundled), 7 MB for the
  terminal-only build. macOS, Linux and Windows.
- **Edits config files surgically.** Only the one key you change is touched;
  comments, ordering and indentation in your `settings.json`, `config.toml`,
  `opencode.jsonc` or `config.yaml` survive intact. Writes are atomic.
- **One endpoint for every agent.** dial runs a local gateway that speaks
  OpenAI chat completions, OpenAI Responses and the Anthropic Messages API,
  and forwards to whichever vendor serves the model. Codex, Claude Code,
  OpenCode and the rest all point at `http://127.0.0.1:3425/v1` and pick
  from one catalog; the translation between APIs happens in dial, streaming
  and tool calls included.
- **Providers with one field.** Pick a preset (Anthropic, OpenAI, Gemini,
  DeepSeek, Kimi, GLM, MiniMax, Qwen, Mistral, Groq, xAI, OpenRouter,
  Together, Fireworks, SiliconFlow, AiHubMix, 302.AI, Ollama, LM Studio…),
  paste a key, done. Custom vendors need a name and a base URL. dial never
  reads keys from your shell environment.
- **Real model lists.** With a key in hand dial asks the vendor which models
  it serves and offers exactly those; the [models.dev](https://models.dev)
  catalog fills in names, reasoning efforts and the list for vendors that
  have none. Choose which models each provider exposes, or expose them all.
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
dial's gateway, so agents never hold vendor keys or vendor URLs. Add a
provider, and its models appear in every agent's picker:

```sh
dial presets                          # the vendors dial knows, grouped: vendors, relays, local
dial provider add deepseek sk-…       # a preset needs only the key
dial provider add ollama              # local servers need none
dial provider add "My Relay" url=https://relay.example.com/v1 key=sk-… models=gpt-5.5,claude-sonnet-5
dial providers                        # host, key, exposed models, who uses what
dial provider deepseek                # one provider in detail
dial provider models deepseek         # re-fetch the vendor's list (add ids to choose which to expose)
dial provider test deepseek           # one tiny request per API, with latency
dial provider key deepseek sk-…       # replace the key
dial provider rm deepseek
dial models                           # the catalog agents see
dial claude deepseek/deepseek-chat    # use it
```

Custom providers take `url=` (an OpenAI-compatible base), `anthropic=` (an
Anthropic-compatible base), or both, plus `responses=` when the vendor has a
separate Responses endpoint, `catalog=` to borrow a models.dev list, and
`models=` to name the models to expose. Anything a preset does not know can
be overridden the same way.

The gateway listens on `127.0.0.1:3425` (`DIAL_ADDR` changes it) and starts
with the app; `dial serve` runs it alone. It exposes:

| Path                     | API                        |
| ------------------------ | -------------------------- |
| `/v1/chat/completions`   | OpenAI chat completions    |
| `/v1/responses`          | OpenAI Responses           |
| `/v1/messages`           | Anthropic Messages         |
| `/v1/messages/count_tokens` | Anthropic token counting |
| `/v1/models`             | the catalog                |

Requests pass straight through when the vendor speaks the agent's API and
are translated otherwise, streaming, tool calls and reasoning included. The
bearer token is `dial`; the gateway only listens on loopback. `DIAL_DEBUG=1`
logs every call, and the *Activity* toggle in the app shows the recent ones.

**Claude Code** gets `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN` and the
model variables in the `env` block of `settings.json`; picking a native
model (`opus`, `sonnet`…) removes them and restores whatever was there.

**Codex** gets a `[model_providers.dial]` table, `model_catalog_json`
pointing at `~/.codex/dial-models.json` (written from the catalog, so the
models show in Codex's own list) and a valid `model`/`effort`; picking a
native model removes all of that. Your ChatGPT sign-in is never touched.
Codex reads its model list at start-up, so restart it after a switch.

**OpenCode, Pi, Crush** get a `dial` provider entry and `dial/provider/model`.

**Gemini CLI** switches `auth` between API key, Google account and Vertex;
the API key goes to `~/.gemini/.env`.

## Install

```sh
go install github.com/yetone/dial@latest
```

or build locally:

```sh
make build            # ./dial with the desktop app (needs cgo + the platform webview)
make app              # macOS: dial.app, a menu bar app with no Dock icon
make cli              # terminal-only build, no cgo, cross-compiles anywhere
make release          # dist/: native app build + cli builds for every platform
```

Linux needs `libgtk-3-dev` and `libwebkit2gtk-4.1-dev` for the app build;
Windows uses the WebView2 runtime that ships with the OS.

### Developing

```sh
make dev
```

builds with `-tags dev` and opens the app with the UI served straight from
`internal/gui/assets`: save `app.css`, `app.js` or `index.html` and the window
reloads itself. With `fswatch` installed (`brew install fswatch`), a change to a
Go file rebuilds and relaunches the app too. The dev build uses its own gateway
port (`DEV_ADDR`, default 127.0.0.1:3426), so a dial you already run keeps
serving your agents. Point it at a scratch home to keep your real agent
configs out of it:

```sh
HOME=/tmp/dial-home XDG_CONFIG_HOME=/tmp/dial-home/.config make dev
```

`DIAL_THEME=light|dark` forces the palette and `DIAL_DEBUG=1` prints what the
gateway translates.

## Use

```sh
dial                          # open the app: a window plus the menu bar icon
dial tray                     # menu bar icon only (use this in your login items)
dial tui                      # the same dial, in the terminal
dial ls                       # list every agent and its current settings
dial claude opus              # set a model (agent names accept prefixes: cc, oc, gem …)
dial codex gpt-5.6-sol
dial codex effort high        # other fields
dial codex xhigh              # bare effort levels are recognised too
dial codex deepseek/deepseek-chat   # any catalog model, through the gateway
dial claude moonshot/kimi-k2.5
dial gemini auth api-key
dial opencode anthropic/claude-sonnet-5
dial oc small anthropic/claude-haiku-4-5

dial save work                # snapshot everything as a profile
dial use work                 # switch back
dial profiles
dial rm work

dial sync                     # refresh the models.dev catalog and every live model list
```

In the app, click any value to open a filtered list; type to search or to
enter something that is not listed; `esc` closes the panel. Profiles are the
chips at the bottom: click to apply, `×` to delete, *+ save current* to add.
The *Providers* tab of the window lists your providers with the agents on
each; click a row to change the key or the exposed models, *Test* it, or
click an agent icon to point that agent at one of its models. *Add
provider* shows the presets as tiles: pick one, paste the key.

Keys in the terminal dial:

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

- `~/.config/dial/profiles.json` — saved profiles
- `~/.config/dial/providers.json` — your providers, keys included (0600)
- `~/.config/dial/stash.json` — values dial replaced, restored on switch-back
- `~/.cache/dial/models.json` — models.dev catalog (OpenCode's cache at
  `~/.cache/opencode/models.json` is used when present)
- `~/.cache/dial/models/<provider>.json` — model lists fetched from vendors

`XDG_CONFIG_HOME` and `XDG_CACHE_HOME` are respected.
