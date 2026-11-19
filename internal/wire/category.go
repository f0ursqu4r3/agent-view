package wire

var claudeCategories = map[string]string{
	"Read":         "read",
	"Edit":         "edit",
	"MultiEdit":    "edit",
	"NotebookEdit": "edit",
	"Write":        "write",
	"Bash":         "command",
	"Grep":         "search",
	"Glob":         "search",
	"Agent":        "agent",
	"Task":         "agent",
}

var codexCategories = map[string]string{
	"apply_patch":         "edit",
	"FileChange":          "edit",
	"fileChange":          "edit",
	"shell":               "command",
	"exec_command":        "command",
	"local_shell":         "command",
	"CommandExecution":    "command",
	"commandExecution":    "command",
	"spawn_agent":         "agent",
	"CollabAgentToolCall": "agent",
	"collabAgentToolCall": "agent",
	"webSearch":           "search",
}

// CategoryFor maps a provider's raw tool name to a category.
// An unknown provider or an unknown name gives "other".
func CategoryFor(provider, rawName string) string {
	var table map[string]string
	switch provider {
	case "claude":
		table = claudeCategories
	case "codex":
		table = codexCategories
	}
	if c, ok := table[rawName]; ok {
		return c
	}
	return "other"
}
