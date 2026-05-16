package main

// Message is one conversational turn between the agent and the LLM.
// In browser-use, the user message is the task description; the assistant
// message is the LLM's choice of action; the tool/result message is the
// browser state after the action runs.
type Message struct {
	Role    string         // "system" | "user" | "assistant" | "tool"
	Content []ContentBlock // a turn can have multiple blocks (text + tool_use, etc.)
}

// ContentBlock is a tagged union over Type. Only the fields relevant to
// the type are non-zero. This shape mirrors what real LLM APIs accept and
// what `browser_use/llm/messages.py` defines for unified message handling.
type ContentBlock struct {
	Type  string // "text" | "tool_use" | "tool_result"
	Text  string // when Type == "text"
	Name  string // when Type == "tool_use" — the action name
	Input string // when Type == "tool_use" — JSON-encoded args, simple string here
	// Result fields, when Type == "tool_result"
	Result string
}

// Response is what the (fake) Provider returns each turn. It tells the loop:
// (a) what the model said (text), (b) which action(s) to dispatch.
type Response struct {
	Text       string
	Actions    []ActionCall
	StopReason string // "end_turn" | "tool_use" | "max_tokens"
}

// ActionCall is one tool/action dispatch decided by the LLM.
type ActionCall struct {
	Name  string
	Input string
}
