package server

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"github.com/wolfeidau/starlogz/internal/clientclass"
	"github.com/wolfeidau/starlogz/internal/wideevent"
)

type toolEventPublisher struct {
	events []wideevent.Event
}

func toolEventAttributes(values map[string]string) map[string]string {
	attributes := wideevent.ClientIdentityAttributes(clientclass.Unknown())
	for key, value := range values {
		attributes[key] = value
	}
	return attributes
}

func (p *toolEventPublisher) Publish(_ context.Context, event wideevent.Event) error {
	p.events = append(p.events, event)
	return nil
}

func TestTrackToolEmitsSuccessAndFailure(t *testing.T) {
	publisher := &toolEventPublisher{}
	emitter, err := wideevent.NewEmitter(publisher, "test", "devel", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	ms := &mcpServer{events: emitter}

	success := trackTool(ms, wideevent.ToolWhoami, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})
	_, _, err = success(t.Context(), &mcp.CallToolRequest{}, struct{}{})
	require.NoError(t, err)

	counted := trackTool(ms, wideevent.ToolInsightSearch, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, toolEventMetadata{resultCount: 7}, nil
	})
	_, output, err := counted(t.Context(), &mcp.CallToolRequest{}, struct{}{})
	require.NoError(t, err)
	require.Nil(t, output)

	get := trackTool(ms, wideevent.ToolInsightGet, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})
	_, _, err = get(t.Context(), &mcp.CallToolRequest{}, struct{}{})
	require.NoError(t, err)

	failure := trackTool(ms, wideevent.ToolInsightSearch, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return nil, nil, errors.New("failed")
	})
	_, _, err = failure(t.Context(), &mcp.CallToolRequest{}, struct{}{})
	require.EqualError(t, err, "failed")

	require.Len(t, publisher.events, 4)
	require.Equal(t, wideevent.OutcomeSuccess, publisher.events[0].Outcome)
	require.Equal(t, toolEventAttributes(map[string]string{wideevent.AttributeTool: wideevent.ToolWhoami}), publisher.events[0].Attributes)
	require.Equal(t, wideevent.OutcomeSuccess, publisher.events[1].Outcome)
	require.Equal(t, toolEventAttributes(map[string]string{
		wideevent.AttributeTool:              wideevent.ToolInsightSearch,
		wideevent.AttributeResultCountBucket: wideevent.ResultCountOneToTen,
	}), publisher.events[1].Attributes)
	require.Equal(t, toolEventAttributes(map[string]string{wideevent.AttributeTool: wideevent.ToolInsightGet}), publisher.events[2].Attributes)
	require.Equal(t, wideevent.OutcomeFailure, publisher.events[3].Outcome)
	require.Equal(t, wideevent.ReasonFailed, publisher.events[3].Reason)
	require.Equal(t, toolEventAttributes(map[string]string{wideevent.AttributeTool: wideevent.ToolInsightSearch}), publisher.events[3].Attributes)
}

func TestResultCountBucket(t *testing.T) {
	tests := map[int]string{
		0:   wideevent.ResultCountZero,
		1:   wideevent.ResultCountOneToTen,
		10:  wideevent.ResultCountOneToTen,
		11:  wideevent.ResultCountElevenTo50,
		50:  wideevent.ResultCountElevenTo50,
		51:  wideevent.ResultCount51To100,
		100: wideevent.ResultCount51To100,
		101: wideevent.ResultCount101To200,
		200: wideevent.ResultCount101To200,
	}

	for count, expected := range tests {
		require.Equal(t, expected, resultCountBucket(count))
	}
}

func TestTrackClientInitializeEmitsDeclaredIdentity(t *testing.T) {
	publisher := &toolEventPublisher{}
	emitter, err := wideevent.NewEmitter(publisher, "test", "devel", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	ms := &mcpServer{events: emitter}
	middleware := trackClientInitialize(ms)
	handler := middleware(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		return &mcp.InitializeResult{}, nil
	})
	req := &mcp.InitializeRequest{
		Params: &mcp.InitializeParams{
			ClientInfo: &mcp.Implementation{Name: "codex-mcp-client", Version: "144.4.0"},
		},
	}

	_, err = handler(t.Context(), "initialize", req)
	require.NoError(t, err)
	require.Len(t, publisher.events, 1)
	require.Equal(t, wideevent.MCPClientInitialized, publisher.events[0].EventName)
	require.Equal(t, wideevent.ClientIdentityAttributes(clientclass.FromMCP("codex-mcp-client", "144.4.0")), publisher.events[0].Attributes)
}

func TestTrackClientInitializeEmitsOneFailureAndRepanics(t *testing.T) {
	for _, test := range []struct {
		name    string
		handler mcp.MethodHandler
		panic   bool
	}{
		{name: "error", handler: func(context.Context, string, mcp.Request) (mcp.Result, error) {
			return nil, errors.New("failed")
		}},
		{name: "panic", panic: true, handler: func(context.Context, string, mcp.Request) (mcp.Result, error) {
			panic("failed")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			publisher := &toolEventPublisher{}
			emitter, err := wideevent.NewEmitter(publisher, "test", "devel", slog.New(slog.DiscardHandler))
			require.NoError(t, err)
			ms := &mcpServer{events: emitter}
			handler := trackClientInitialize(ms)(test.handler)
			req := &mcp.InitializeRequest{Params: &mcp.InitializeParams{
				ClientInfo: &mcp.Implementation{Name: "private-client", Version: "secret-version"},
			}}

			call := func() { _, _ = handler(t.Context(), "initialize", req) }
			if test.panic {
				require.Panics(t, call)
			} else {
				call()
			}

			require.Len(t, publisher.events, 1)
			require.Equal(t, wideevent.OutcomeFailure, publisher.events[0].Outcome)
			require.Equal(t, wideevent.ReasonFailed, publisher.events[0].Reason)
			require.Equal(t, wideevent.ClientIdentityAttributes(clientclass.Unknown()), publisher.events[0].Attributes)
		})
	}
}

func TestTrackToolPrefersTokenIdentity(t *testing.T) {
	publisher := &toolEventPublisher{}
	emitter, err := wideevent.NewEmitter(publisher, "test", "devel", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	ms := &mcpServer{events: emitter}
	handler := trackTool(ms, wideevent.ToolWhoami, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})
	req := &mcp.CallToolRequest{Extra: &mcp.RequestExtra{TokenInfo: &auth.TokenInfo{Extra: map[string]any{
		clientclass.TokenInfoProductKey:    clientclass.ProductCodex,
		clientclass.TokenInfoMajorKey:      144,
		clientclass.TokenInfoConfidenceKey: clientclass.ConfidenceDeclared,
	}}}}

	_, _, err = handler(t.Context(), req, struct{}{})
	require.NoError(t, err)
	require.Len(t, publisher.events, 1)
	want := wideevent.ClientIdentityAttributes(clientclass.Classification{
		Product: clientclass.ProductCodex, Major: 144, HasMajor: true,
		Source: clientclass.SourceOAuthRegistration, Confidence: clientclass.ConfidenceDeclared,
	})
	want[wideevent.AttributeTool] = wideevent.ToolWhoami
	require.Equal(t, want, publisher.events[0].Attributes)
}
