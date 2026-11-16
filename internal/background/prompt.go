package background

// Config holds the configuration for generating an agent system prompt.
type Config struct {
	// Base is the base prompt text.
	Base string
}

// AskCwInstruction is the instruction for agents on how to access the web.
const AskCwInstruction = "Use ask_webchat for all web access. It is the only way to reach the web."

const defaultBasePrompt = "You are a helpful assistant."

// SystemPrompt generates the full system prompt by combining the base prompt
// with the ask_webchat instruction. If the base prompt is empty, a default is used.
// The result is never empty.
func SystemPrompt(cfg Config) string {
	base := cfg.Base
	if base == "" {
		base = defaultBasePrompt
	}
	return base + "\n\n" + AskCwInstruction
}
