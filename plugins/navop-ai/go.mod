module navop-ai

go 1.25.0

require github.com/dbcore/dbcore v0.1.0

// W-A1: the AI plugin. It provides the `ai.provider` capability and speaks the
// OpenAI-compatible chat-completions shape (so a local llama.cpp/ollama gateway
// works as well as a hosted endpoint). The API key arrives through AD-4's lend —
// a raw stdin body, never a JSON field.

replace github.com/dbcore/dbcore => ../../../dbcore
