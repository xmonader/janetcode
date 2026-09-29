package acp

import "encoding/json"

// The wire types of the Agent Client Protocol, in the shapes the editor
// sends and expects. Only what this adapter uses is modelled; unknown
// fields are ignored on the way in and omitted on the way out.

type initializeParams struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities clientCapabilities `json:"clientCapabilities"`
	ClientInfo         *implInfo          `json:"clientInfo,omitempty"`
}

type clientCapabilities struct {
	FS       fsCapability `json:"fs"`
	Terminal bool         `json:"terminal,omitempty"`
}

type fsCapability struct {
	ReadTextFile  bool `json:"readTextFile,omitempty"`
	WriteTextFile bool `json:"writeTextFile,omitempty"`
}

type implInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
}

type initializeResult struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities agentCapabilities `json:"agentCapabilities"`
	AgentInfo         implInfo          `json:"agentInfo"`
	AuthMethods       []authMethod      `json:"authMethods"`
}

type agentCapabilities struct {
	LoadSession        bool               `json:"loadSession"`
	PromptCapabilities promptCapabilities `json:"promptCapabilities"`
	MCPCapabilities    mcpCapabilities    `json:"mcpCapabilities"`
}

type promptCapabilities struct {
	Image           bool `json:"image"`
	Audio           bool `json:"audio"`
	EmbeddedContext bool `json:"embeddedContext"`
}

type authMethod struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type newSessionParams struct {
	Cwd                   string          `json:"cwd"`
	AdditionalDirectories []string        `json:"additionalDirectories,omitempty"`
	MCPServers            []mcpServerSpec `json:"mcpServers,omitempty"`
}

type newSessionResult struct {
	SessionID string `json:"sessionId"`
}

type loadSessionParams struct {
	SessionID  string          `json:"sessionId"`
	Cwd        string          `json:"cwd"`
	MCPServers []mcpServerSpec `json:"mcpServers,omitempty"`
}

type promptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []contentBlock `json:"prompt"`
}

type promptResult struct {
	StopReason string      `json:"stopReason"`
	Meta       *promptMeta `json:"_meta,omitempty"`
}

// promptMeta is janetcode's extension of a prompt's result, under the
// protocol's _meta key: what the turn cost and what it demonstrated. An
// editor that does not know it ignores it; the Go SDK reads it.
type promptMeta struct {
	Janetcode turnReport `json:"janetcode"`
}

type turnReport struct {
	// Verdict is what the turn demonstrated, computed by the harness
	// (the same line -p prints as [verdict]); empty when there was none.
	Verdict string `json:"verdict,omitempty"`
	// Usage is the turn's token usage, summed over its model calls.
	Usage     turnUsage `json:"usage"`
	ToolCalls int       `json:"toolCalls"`
}

type turnUsage struct {
	InputTokens      int     `json:"inputTokens"`
	OutputTokens     int     `json:"outputTokens"`
	CacheReadTokens  int     `json:"cacheReadTokens"`
	CacheWriteTokens int     `json:"cacheWriteTokens"`
	CostUSD          float64 `json:"costUsd,omitempty"`
}

type cancelParams struct {
	SessionID string `json:"sessionId"`
}

// contentBlock is the ACP content union. Only the fields of the variants
// this adapter reads or writes are present.
type contentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	URI      string `json:"uri,omitempty"`
	Name     string `json:"name,omitempty"`
	// Resource carries an embedded resource's own fields.
	Resource *embeddedResource `json:"resource,omitempty"`
}

type embeddedResource struct {
	URI      string `json:"uri,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
}

// sessionNotification is the envelope of every session/update.
type sessionNotification struct {
	SessionID string `json:"sessionId"`
	Update    any    `json:"update"`
}

// A session/update payload is one of two shapes. They are separate types
// because both name their payload "content" — a message chunk carries one
// content block, a tool call carries a list of tool content — and one Go
// struct cannot hold the same JSON tag twice.

// msgUpdate is agent_message_chunk, agent_thought_chunk or
// user_message_chunk.
type msgUpdate struct {
	SessionUpdate string        `json:"sessionUpdate"`
	MessageID     string        `json:"messageId,omitempty"`
	Content       *contentBlock `json:"content,omitempty"`
	// Meta marks a chunk that is the harness speaking (a notice, a
	// discarded stream), not the model, for a client that tells them
	// apart; an editor shows it inline like any other chunk.
	Meta *chunkMeta `json:"_meta,omitempty"`
}

type chunkMeta struct {
	Janetcode struct {
		Notice bool `json:"notice"`
		// Discard says the message streamed before it was cut off and
		// is void: its retry follows in full.
		Discard bool `json:"discard,omitempty"`
	} `json:"janetcode"`
}

func noticeMeta() *chunkMeta {
	m := &chunkMeta{}
	m.Janetcode.Notice = true
	return m
}

func discardMeta() *chunkMeta {
	m := noticeMeta()
	m.Janetcode.Discard = true
	return m
}

// toolUpdate is tool_call or tool_call_update.
type toolUpdate struct {
	SessionUpdate string             `json:"sessionUpdate"`
	ToolCallID    string             `json:"toolCallId"`
	Title         string             `json:"title,omitempty"`
	Kind          string             `json:"kind,omitempty"`
	Status        string             `json:"status,omitempty"`
	Content       []toolCallContent  `json:"content,omitempty"`
	Locations     []toolCallLocation `json:"locations,omitempty"`
	RawInput      json.RawMessage    `json:"rawInput,omitempty"`
}

// toolCallContent is the content union of a tool call: rendered text, a
// file diff, or a terminal handle. Only text and diff are produced here.
type toolCallContent struct {
	Type    string        `json:"type"`
	Content *contentBlock `json:"content,omitempty"`
	Path    string        `json:"path,omitempty"`
	OldText *string       `json:"oldText,omitempty"`
	NewText string        `json:"newText,omitempty"`
}

type toolCallLocation struct {
	Path string `json:"path"`
	Line *int   `json:"line,omitempty"`
}

// Tool call kinds and statuses, as the protocol names them.
const (
	kindRead    = "read"
	kindEdit    = "edit"
	kindDelete  = "delete"
	kindSearch  = "search"
	kindExecute = "execute"
	kindThink   = "think"
	kindFetch   = "fetch"
	kindOther   = "other"

	statusPending    = "pending"
	statusInProgress = "in_progress"
	statusCompleted  = "completed"
	statusFailed     = "failed"
)

// Stop reasons, as the protocol defines them. "completed" is not among
// them — the published schema says end_turn — and an agent that sends it
// has every finished turn rejected by a validating client.
const (
	stopEndTurn   = "end_turn"
	stopCancelled = "cancelled"
	stopRefusal   = "refusal"
	stopMaxTurns  = "max_turn_requests"
	stopMaxTokens = "max_tokens"
)

type requestPermissionParams struct {
	SessionID string             `json:"sessionId"`
	ToolCall  toolUpdate         `json:"toolCall"`
	Options   []permissionOption `json:"options"`
}

type permissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// Permission option ids and kinds. The outcome is matched on the id, so a
// client that renames kinds still routes correctly.
const (
	optAllowOnce    = "allow_once"
	optAllowAlways  = "allow_always"
	optRejectOnce   = "reject_once"
	optRejectAlways = "reject_always"
)

type requestPermissionResult struct {
	Outcome permissionOutcome `json:"outcome"`
}

type permissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}
