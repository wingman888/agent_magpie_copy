package gateway

import "context"

// claudeDirKey carries the config folder a remembered Claude account runs from
// down to the binary's start-up.
type claudeDirKey struct{}

// withClaudeConfigDir returns ctx carrying the Claude config folder dir; "" leaves ctx as it is.
func withClaudeConfigDir(ctx context.Context, dir string) context.Context {
	if dir == "" {
		return ctx
	}
	return context.WithValue(ctx, claudeDirKey{}, dir)
}

// claudeConfigDirOf is the Claude config folder carried by ctx, or "".
func claudeConfigDirOf(ctx context.Context) string {
	dir, _ := ctx.Value(claudeDirKey{}).(string)
	return dir
}
