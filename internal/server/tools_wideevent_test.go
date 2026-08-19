package server

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/auth"
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
	userID := uuid.New().String()
	request := &mcp.CallToolRequest{Extra: &mcp.RequestExtra{TokenInfo: &auth.TokenInfo{
		UserID: userID,
		Extra:  map[string]any{"client_id": "test-client"},
	}}}

	success := trackTool(ms, wideevent.ToolWhoami, func(context.Context, *mcp.CallToolRequest, whoamiInput) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})
	_, _, err = success(t.Context(), request, whoamiInput{Telemetry: telemetryInput{Context: "Recording the tool purpose to understand progress toward the caller's stated project goal and improve service analytics safely."}})
	require.NoError(t, err)

	counted := trackTool(ms, wideevent.ToolInsightSearch, func(context.Context, *mcp.CallToolRequest, whoamiInput) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, toolEventMetadata{resultCount: 7}, nil
	})
	_, output, err := counted(t.Context(), request, whoamiInput{Telemetry: telemetryInput{Context: "Recording the tool purpose to understand progress toward the caller's stated project goal and improve service analytics safely."}})
	require.NoError(t, err)
	require.Nil(t, output)
	history := trackTool(ms, wideevent.ToolInsightHistory, func(context.Context, *mcp.CallToolRequest, whoamiInput) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, toolEventMetadata{resultCount: 51}, nil
	})
	_, output, err = history(t.Context(), request, whoamiInput{Telemetry: telemetryInput{Context: "Recording the tool purpose to understand progress toward the caller's stated project goal and improve service analytics safely."}})
	require.NoError(t, err)
	require.Nil(t, output)

	get := trackTool(ms, wideevent.ToolInsightGet, func(context.Context, *mcp.CallToolRequest, whoamiInput) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})
	_, _, err = get(t.Context(), request, whoamiInput{Telemetry: telemetryInput{Context: "Recording the tool purpose to understand progress toward the caller's stated project goal and improve service analytics safely."}})
	require.NoError(t, err)
	restore := trackTool(ms, wideevent.ToolInsightRestore, func(context.Context, *mcp.CallToolRequest, whoamiInput) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})
	_, _, err = restore(t.Context(), request, whoamiInput{Telemetry: telemetryInput{Context: "Recording the tool purpose to understand progress toward the caller's stated project goal and improve service analytics safely."}})
	require.NoError(t, err)

	toolFailure := trackTool(ms, wideevent.ToolInsightUpdate, func(context.Context, *mcp.CallToolRequest, whoamiInput) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{IsError: true}, nil, nil
	})
	_, _, err = toolFailure(t.Context(), request, whoamiInput{Telemetry: telemetryInput{Context: "Recording the tool purpose to understand progress toward the caller's stated project goal and improve service analytics safely."}})
	require.NoError(t, err)

	failure := trackTool(ms, wideevent.ToolInsightSearch, func(context.Context, *mcp.CallToolRequest, whoamiInput) (*mcp.CallToolResult, any, error) {
		return nil, nil, errors.New("failed")
	})
	_, _, err = failure(t.Context(), request, whoamiInput{Telemetry: telemetryInput{Context: "Recording the tool purpose to understand progress toward the caller's stated project goal and improve service analytics safely."}})
	require.EqualError(t, err, "failed")

	require.Len(t, publisher.events, 7)
	for _, event := range publisher.events {
		require.Equal(t, userID, event.UserID)
		require.Equal(t, "test-client", event.ClientID)
	}
	require.Equal(t, wideevent.OutcomeSuccess, publisher.events[0].Outcome)
	require.Equal(t, "Recording the tool purpose to understand progress toward the caller's stated project goal and improve service analytics safely.", publisher.events[0].Telemetry.Context)
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

func TestValidateTelemetryContext(t *testing.T) {
	tests := map[string]struct {
		context   string
		wantError string
	}{
		"valid":            {context: "Recording the tool purpose to understand progress toward the caller's stated project goal and improve service analytics safely."},
		"too short":        {context: "Recording the purpose of this tool call for analytics and project progress.", wantError: "15-25 meaningful words"},
		"first person":     {context: "We record this tool purpose to understand progress toward the caller's stated project goal and improve service analytics safely.", wantError: "third-person"},
		"punctuation only": {context: "Purposeful analysis supports project planning effectively with trusted goals, ........ ........ ........ ........ ........ ........ ........ ........", wantError: "15-25 meaningful words"},
		"oversized":        {context: strings.Repeat("a", telemetryContextMaxBytes+1), wantError: "must not exceed"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateTelemetryContext(test.context)
			if test.wantError == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, test.wantError)
		})
	}
}

func TestTrackToolEmitsFailureWithoutInvalidTelemetry(t *testing.T) {
	publisher := &toolEventPublisher{}
	emitter, err := wideevent.NewEmitter(publisher, "test", "devel", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	ms := &mcpServer{events: emitter}
	request := &mcp.CallToolRequest{Extra: &mcp.RequestExtra{TokenInfo: &auth.TokenInfo{UserID: uuid.New().String()}}}
	called := false
	handler := trackTool(ms, wideevent.ToolWhoami, func(context.Context, *mcp.CallToolRequest, whoamiInput) (*mcp.CallToolResult, any, error) {
		called = true
		return &mcp.CallToolResult{}, nil, nil
	})

	_, _, err = handler(t.Context(), request, whoamiInput{Telemetry: telemetryInput{Context: "Too few words for a valid telemetry context."}})
	require.ErrorContains(t, err, "15-25 meaningful words")
	require.False(t, called)
	require.Len(t, publisher.events, 1)
	require.Equal(t, wideevent.OutcomeFailure, publisher.events[0].Outcome)
	require.Equal(t, wideevent.ReasonFailed, publisher.events[0].Reason)
	require.Nil(t, publisher.events[0].Telemetry)
}

func TestTrackToolOmitsLegacyClientID(t *testing.T) {
	publisher := &toolEventPublisher{}
	emitter, err := wideevent.NewEmitter(publisher, "test", "devel", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	ms := &mcpServer{events: emitter}
	userID := uuid.New().String()
	request := &mcp.CallToolRequest{Extra: &mcp.RequestExtra{TokenInfo: &auth.TokenInfo{UserID: userID}}}
	handler := trackTool(ms, wideevent.ToolWhoami, func(context.Context, *mcp.CallToolRequest, whoamiInput) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})

	_, _, err = handler(t.Context(), request, whoamiInput{Telemetry: telemetryInput{Context: "Recording the tool purpose to understand progress toward the caller's stated project goal and improve service analytics safely."}})
	require.NoError(t, err)
	require.Len(t, publisher.events, 1)
	require.Equal(t, userID, publisher.events[0].UserID)
	require.Empty(t, publisher.events[0].ClientID)
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
