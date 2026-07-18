package server

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"github.com/wolfeidau/starlogz/internal/wideevent"
)

type toolEventPublisher struct {
	events []wideevent.Event
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
	history := trackTool(ms, wideevent.ToolInsightHistory, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, toolEventMetadata{resultCount: 51}, nil
	})
	_, output, err = history(t.Context(), &mcp.CallToolRequest{}, struct{}{})
	require.NoError(t, err)
	require.Nil(t, output)

	get := trackTool(ms, wideevent.ToolInsightGet, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})
	_, _, err = get(t.Context(), &mcp.CallToolRequest{}, struct{}{})
	require.NoError(t, err)
	restore := trackTool(ms, wideevent.ToolInsightRestore, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})
	_, _, err = restore(t.Context(), &mcp.CallToolRequest{}, struct{}{})
	require.NoError(t, err)

	toolFailure := trackTool(ms, wideevent.ToolInsightUpdate, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{IsError: true}, nil, nil
	})
	_, _, err = toolFailure(t.Context(), &mcp.CallToolRequest{}, struct{}{})
	require.NoError(t, err)

	failure := trackTool(ms, wideevent.ToolInsightSearch, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return nil, nil, errors.New("failed")
	})
	_, _, err = failure(t.Context(), &mcp.CallToolRequest{}, struct{}{})
	require.EqualError(t, err, "failed")

	require.Len(t, publisher.events, 7)
	require.Equal(t, wideevent.OutcomeSuccess, publisher.events[0].Outcome)
	require.Equal(t, map[string]string{wideevent.AttributeTool: wideevent.ToolWhoami}, publisher.events[0].Attributes)
	require.Equal(t, wideevent.OutcomeSuccess, publisher.events[1].Outcome)
	require.Equal(t, map[string]string{
		wideevent.AttributeTool:              wideevent.ToolInsightSearch,
		wideevent.AttributeResultCountBucket: wideevent.ResultCountOneToTen,
	}, publisher.events[1].Attributes)
	require.Equal(t, map[string]string{
		wideevent.AttributeTool:              wideevent.ToolInsightHistory,
		wideevent.AttributeResultCountBucket: wideevent.ResultCount51To100,
	}, publisher.events[2].Attributes)
	require.Equal(t, map[string]string{wideevent.AttributeTool: wideevent.ToolInsightGet}, publisher.events[3].Attributes)
	require.Equal(t, map[string]string{wideevent.AttributeTool: wideevent.ToolInsightRestore}, publisher.events[4].Attributes)
	require.Equal(t, wideevent.OutcomeFailure, publisher.events[5].Outcome)
	require.Equal(t, wideevent.ReasonFailed, publisher.events[5].Reason)
	require.Equal(t, map[string]string{wideevent.AttributeTool: wideevent.ToolInsightUpdate}, publisher.events[5].Attributes)
	require.Equal(t, wideevent.OutcomeFailure, publisher.events[6].Outcome)
	require.Equal(t, wideevent.ReasonFailed, publisher.events[6].Reason)
	require.Equal(t, map[string]string{wideevent.AttributeTool: wideevent.ToolInsightSearch}, publisher.events[6].Attributes)
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
