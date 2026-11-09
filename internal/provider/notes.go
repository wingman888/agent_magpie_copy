package provider

// Notes are things about the agent logins dial sees that the user should
// know but that are not providers. They carry no credentials.
func Notes() []string {
	var out []string
	if ClaudeSignedIn() {
		out = append(out, "Claude Code 已登录，但不提供为 Provider。原因：其登录凭据仅供 Claude Code 自身使用，dial 不读取也不转发。")
	}
	return out
}
