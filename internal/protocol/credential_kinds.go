package protocol

// CredentialOperationKind maps the legacy shared credential command's scopes.
// Gemini and Claude use separate commands and are not scopes of this command.
func CredentialOperationKind(scope string) (string, bool) {
	switch scope {
	case "":
		return OpCredentialsConfigure, true
	case "codex":
		return OpCodexCredentialsConfigure, true
	case "git":
		return OpGitCredentialsConfigure, true
	default:
		return "", false
	}
}
