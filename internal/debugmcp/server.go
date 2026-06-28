package debugmcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

type Server struct {
	config Config
	client *mastClient
}

func NewServer(config Config) *Server {
	return &Server{
		config: config,
		client: newMastClient(config.BaseURL),
	}
}

func (s *Server) Serve(in io.Reader, out io.Writer) error {
	decoder := json.NewDecoder(bufio.NewReader(in))
	encoder := json.NewEncoder(out)
	ctx := context.Background()

	for {
		var req rpcRequest
		if err := decoder.Decode(&req); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		resp, ok := s.handleRPC(ctx, req)
		if !ok {
			continue
		}
		if err := encoder.Encode(resp); err != nil {
			return err
		}
	}
}

func (s *Server) handleRPC(ctx context.Context, req rpcRequest) (rpcResponse, bool) {
	if req.ID == nil {
		return rpcResponse{}, false
	}

	switch req.Method {
	case "initialize":
		return success(req.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo": map[string]any{
				"name":    "mast-debug-mcp",
				"version": "0.1.0",
			},
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
		}), true
	case "tools/list":
		return success(req.ID, map[string]any{"tools": s.toolDefinitions()}), true
	case "tools/call":
		var params callToolParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return failure(req.ID, -32602, "invalid params: "+err.Error()), true
		}
		result := s.CallTool(ctx, params.Name, params.Arguments)
		return success(req.ID, result), true
	default:
		return failure(req.ID, -32601, fmt.Sprintf("method not found: %s", req.Method)), true
	}
}

func (s *Server) CallTool(ctx context.Context, name string, args map[string]any) mcpToolResult {
	result, err := s.callTool(ctx, name, args)
	if err != nil {
		return mcpErrorResult(err)
	}
	return mcpJSONResult(result)
}

type rpcRequest struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Result  any              `json:"result,omitempty"`
	Error   *rpcError        `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type callToolParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type mcpToolResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func success(id *json.RawMessage, result any) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func failure(id *json.RawMessage, code int, message string) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}}
}

func mcpJSONResult(value any) mcpToolResult {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return mcpErrorResult(err)
	}
	return mcpToolResult{
		Content: []mcpContent{{Type: "text", Text: string(encoded)}},
	}
}

func mcpErrorResult(err error) mcpToolResult {
	return mcpToolResult{
		IsError: true,
		Content: []mcpContent{{Type: "text", Text: err.Error()}},
	}
}
