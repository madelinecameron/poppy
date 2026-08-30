// Package mcp serves poppy's fleet state to agents over the Model Context
// Protocol. It is read-only: every tool answers a question about the fleet and
// none changes it, so an agent can be given the server without risk.
//
// The transport is newline-delimited JSON-RPC 2.0 over stdio, which the
// protocol specifies for local servers. No dependency is needed for it, so
// poppy stays a single self-contained binary.
package mcp

import "encoding/json"

// protocolVersion is the MCP revision poppy implements. When a client requests
// a version poppy understands, that version is echoed back instead.
const protocolVersion = "2024-11-05"

const (
	serverName    = "poppy"
	serverVersion = "0.1.0"
)

// request is an incoming JSON-RPC message. A message without an id is a
// notification and receives no response.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func (r request) isNotification() bool { return len(r.ID) == 0 }

// response is an outgoing JSON-RPC message. Exactly one of Result or Error is
// set.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// JSON-RPC 2.0 error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

func result(id json.RawMessage, v any) response {
	return response{JSONRPC: "2.0", ID: id, Result: v}
}

func failure(id json.RawMessage, code int, message string) response {
	return response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}}
}

// initializeParams is the handshake payload; only the client's requested
// protocol version matters to poppy.
type initializeParams struct {
	ProtocolVersion string `json:"protocolVersion"`
}

type initializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    capabilities `json:"capabilities"`
	ServerInfo      serverInfo   `json:"serverInfo"`
}

type capabilities struct {
	Tools *toolsCapability `json:"tools,omitempty"`
}

type toolsCapability struct{}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// toolDescriptor is one entry in a tools/list response.
type toolDescriptor struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	InputSchema map[string]any   `json:"inputSchema"`
	Annotations *toolAnnotations `json:"annotations,omitempty"`
}

type toolAnnotations struct {
	ReadOnlyHint   bool `json:"readOnlyHint"`
	IdempotentHint bool `json:"idempotentHint"`
}

type toolsListResult struct {
	Tools []toolDescriptor `json:"tools"`
}

type callToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// callToolResult carries a tool's output. Content is a list of blocks; poppy
// returns a single text block holding JSON or a narrative.
type callToolResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func textContent(text string) callToolResult {
	return callToolResult{Content: []contentBlock{{Type: "text", Text: text}}}
}

func errorContent(text string) callToolResult {
	return callToolResult{Content: []contentBlock{{Type: "text", Text: text}}, IsError: true}
}
