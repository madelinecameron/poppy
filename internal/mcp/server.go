package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/madelinecameron/poppy/internal/config"
	"github.com/madelinecameron/poppy/internal/model"
)

// Source supplies fleet state to the server. hub.Store satisfies it; tests
// provide a fake.
type Source interface {
	Fleet() (model.Fleet, error)
	Pull(ctx context.Context) error
}

// Server answers MCP requests about the fleet.
type Server struct {
	cfg    config.Config
	source Source
	tools  []tool
	byName map[string]tool
}

// tool is one callable capability.
type tool struct {
	descriptor toolDescriptor
	handle     func(ctx context.Context, s *Server, args map[string]any) (string, error)
}

// New returns a server reading fleet state from source.
func New(cfg config.Config, source Source) *Server {
	s := &Server{cfg: cfg, source: source}
	s.tools = registry()
	s.byName = make(map[string]tool, len(s.tools))
	for _, t := range s.tools {
		s.byName[t.descriptor.Name] = t
	}
	return s
}

// Serve runs the JSON-RPC loop over in and out until in reaches EOF.
//
// Reads are line-delimited; the scan buffer is enlarged because a fleet
// document, though never received here, keeps request handling uniform and a
// generous limit costs nothing. Writes are serialized so a future concurrent
// handler cannot interleave a partial line.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var writeMu sync.Mutex
	write := func(resp response) {
		writeMu.Lock()
		defer writeMu.Unlock()
		data, err := json.Marshal(resp)
		if err != nil {
			return
		}
		out.Write(append(data, '\n'))
	}

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			write(failure(nil, codeParse, "parse error: "+err.Error()))
			continue
		}

		resp, respond := s.dispatch(ctx, req)
		if respond {
			write(resp)
		}
	}
	return scanner.Err()
}

// dispatch handles one message, reporting whether a response should be sent
// (notifications receive none).
func (s *Server) dispatch(ctx context.Context, req request) (response, bool) {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req), true
	case "tools/list":
		return result(req.ID, toolsListResult{Tools: s.descriptors()}), true
	case "tools/call":
		return s.handleCall(ctx, req), true
	case "ping":
		return result(req.ID, map[string]any{}), true
	case "notifications/initialized", "notifications/cancelled":
		return response{}, false
	default:
		if req.isNotification() {
			return response{}, false
		}
		return failure(req.ID, codeMethodNotFound, "unknown method: "+req.Method), true
	}
}

func (s *Server) handleInitialize(req request) response {
	version := protocolVersion
	if len(req.Params) > 0 {
		var p initializeParams
		if json.Unmarshal(req.Params, &p) == nil && p.ProtocolVersion != "" {
			version = p.ProtocolVersion
		}
	}
	return result(req.ID, initializeResult{
		ProtocolVersion: version,
		Capabilities:    capabilities{Tools: &toolsCapability{}},
		ServerInfo:      serverInfo{Name: serverName, Version: serverVersion},
	})
}

func (s *Server) handleCall(ctx context.Context, req request) response {
	var p callToolParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return failure(req.ID, codeInvalidParams, "invalid params: "+err.Error())
	}
	t, ok := s.byName[p.Name]
	if !ok {
		return failure(req.ID, codeMethodNotFound, "unknown tool: "+p.Name)
	}

	args := map[string]any{}
	if len(p.Arguments) > 0 {
		if err := json.Unmarshal(p.Arguments, &args); err != nil {
			return failure(req.ID, codeInvalidParams, "invalid arguments: "+err.Error())
		}
	}

	// A tool that fails is reported inside the result, not as a protocol
	// error: the call succeeded, and the model should see what went wrong so
	// it can adjust rather than treating it as a transport fault.
	text, err := t.handle(ctx, s, args)
	if err != nil {
		return result(req.ID, errorContent(err.Error()))
	}
	return result(req.ID, textContent(text))
}

func (s *Server) descriptors() []toolDescriptor {
	out := make([]toolDescriptor, len(s.tools))
	for i, t := range s.tools {
		out[i] = t.descriptor
	}
	return out
}

// fleet returns the current fleet, refreshing from the hub first so an agent
// sees other machines' latest state. A refresh failure is tolerated: stale
// state still answers most questions and is better than none.
func (s *Server) fleet(ctx context.Context) (model.Fleet, error) {
	_ = s.source.Pull(ctx)
	return s.source.Fleet()
}

// jsonText renders a value as indented JSON for a tool result.
func jsonText(v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode result: %w", err)
	}
	return string(data), nil
}

// argString reads a string argument, returning the empty string when absent.
func argString(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}
