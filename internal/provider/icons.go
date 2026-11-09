package provider

// builtinIcons are the icons that ship with magpie: the files in
// internal/gui/assets/icons, by name without the extension. The CLI cannot
// read that folder (it is embedded in the GUI build only), so the names are
// listed here; a test checks the list against the folder.
var rsiBuiltinIcons = map[string]bool{
	"ai302-color": true, "aihubmix-color": true, "anthropic": true, "claude-color": true,
	"claudecode-color": true, "codex-color": true, "crush": true, "cursor": true,
	"deepseek-color": true, "fireworks-color": true, "gemini-color": true, "geminicli-color": true,
	"githubcopilot": true, "googlecloud-color": true, "goose": true, "groq": true, "kimi": true,
	"lmstudio": true, "minimax-color": true, "mistral-color": true, "moonshot": true, "ollama": true,
	"openai": true, "opencode": true, "openrouter": true, "pi": true, "qwen-color": true,
	"siliconcloud-color": true, "together-color": true, "xai": true, "yylx": true, "zai": true,
	"zhipu-color": true,
}

// IsBuiltinIcon reports whether name is one of the icons that ship with magpie.
func IsBuiltinIcon(name string) bool { return rsiBuiltinIcons[name] }
