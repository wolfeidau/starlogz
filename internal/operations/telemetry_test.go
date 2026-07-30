package operations

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/stretchr/testify/require"
)

type testLogsClient struct {
	mu     sync.Mutex
	starts int
	rows   map[string][][]types.ResultField
}

type pendingLogsClient struct {
	stops int
}

func (c *pendingLogsClient) StartQuery(context.Context, *cloudwatchlogs.StartQueryInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StartQueryOutput, error) {
	return &cloudwatchlogs.StartQueryOutput{QueryId: aws.String("pending")}, nil
}

func (c *pendingLogsClient) GetQueryResults(context.Context, *cloudwatchlogs.GetQueryResultsInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetQueryResultsOutput, error) {
	return &cloudwatchlogs.GetQueryResultsOutput{Status: types.QueryStatusRunning}, nil
}

func (c *pendingLogsClient) StopQuery(context.Context, *cloudwatchlogs.StopQueryInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StopQueryOutput, error) {
	c.stops++
	return &cloudwatchlogs.StopQueryOutput{}, nil
}

func (c *testLogsClient) StartQuery(_ context.Context, input *cloudwatchlogs.StartQueryInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StartQueryOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.starts++
	query := aws.ToString(input.QueryString)
	queryID := "tools"
	if strings.Contains(query, "p95_duration_ms") {
		queryID = "latency"
	} else if strings.Contains(query, "ui.session.created") {
		queryID = "flows"
	}
	return &cloudwatchlogs.StartQueryOutput{QueryId: aws.String(queryID)}, nil
}

func (c *testLogsClient) GetQueryResults(_ context.Context, input *cloudwatchlogs.GetQueryResultsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetQueryResultsOutput, error) {
	return &cloudwatchlogs.GetQueryResultsOutput{
		Status:  types.QueryStatusComplete,
		Results: c.rows[aws.ToString(input.QueryId)],
	}, nil
}

func (c *testLogsClient) StopQuery(context.Context, *cloudwatchlogs.StopQueryInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StopQueryOutput, error) {
	return &cloudwatchlogs.StopQueryOutput{}, nil
}

func TestCloudWatchAggregatesAndCachesTelemetry(t *testing.T) {
	client := &testLogsClient{rows: map[string][][]types.ResultField{
		"tools": {
			row("bucket", "2026-07-29 10:00:00.000", "tool", "insight_search", "outcome", "success", "calls", "8"),
			row("bucket", "2026-07-29 10:00:00.000", "tool", "insight_search", "outcome", "failure", "calls", "2"),
			row("bucket", "2026-07-29 11:00:00.000", "tool", "insight_write", "outcome", "success", "calls", "4"),
		},
		"latency": {
			row("p95_duration_ms", "47.4"),
		},
		"flows": {
			row("event_name", "oauth.token_exchange.completed", "outcome", "success", "reason", "completed", "events", "5"),
			row("event_name", "oauth.token_exchange.completed", "outcome", "failure", "reason", "invalid_request", "events", "1"),
			row("event_name", "ui.session.created", "outcome", "success", "reason", "completed", "events", "3"),
		},
	}}
	provider := NewCloudWatch(client, "/aws/events/starlogz-dev")
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	provider.now = func() time.Time { return now }

	first, err := provider.Get(t.Context())
	require.NoError(t, err)
	second, err := provider.Get(t.Context())
	require.NoError(t, err)
	require.Same(t, first, second)

	require.Equal(t, 3, client.starts)
	require.Equal(t, 14, first.TotalToolCalls)
	require.Equal(t, 2, first.FailedToolCalls)
	require.Equal(t, int64(47), first.P95ToolDurationMS)
	require.Equal(t, 3, first.SuccessfulDashboardLogins)
	require.Len(t, first.ToolSeries, 25)
	require.Equal(t, TimeBucket{StartedAt: now.Add(-24 * time.Hour)}, first.ToolSeries[0])
	require.Equal(t, TimeBucket{
		StartedAt: now.Add(-2 * time.Hour), Success: 8, Failure: 2,
	}, first.ToolSeries[22])
	require.Equal(t, TimeBucket{
		StartedAt: now.Add(-time.Hour), Success: 4,
	}, first.ToolSeries[23])
	require.Equal(t, TimeBucket{StartedAt: now}, first.ToolSeries[24])
	require.Equal(t, "insight_search", first.Tools[0].Tool)
	require.Equal(t, 10, first.Tools[0].Calls)
	require.Equal(t, 2, first.Tools[0].Failures)
	require.Equal(t, FlowAggregate{
		EventName: "oauth.token_exchange.completed", Success: 5, Failure: 1,
	}, first.OAuthFlows[0])
	require.Equal(t, FailureAggregate{
		EventName: "oauth.token_exchange.completed", Reason: "invalid_request", Count: 1,
	}, first.OAuthFailures[0])
}

func TestCloudWatchRequiresConfiguration(t *testing.T) {
	_, err := NewCloudWatch(nil, "").Get(t.Context())
	require.ErrorIs(t, err, ErrUnavailable)
}

func TestCloudWatchStopsQueryWhenContextExpires(t *testing.T) {
	client := &pendingLogsClient{}
	provider := NewCloudWatch(client, "/aws/events/starlogz-dev")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()

	_, err := provider.runQuery(ctx, time.Now().Add(-time.Hour), time.Now(), toolSeriesQuery)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, 1, client.stops)
}

func row(fields ...string) []types.ResultField {
	result := make([]types.ResultField, 0, len(fields)/2)
	for i := 0; i < len(fields); i += 2 {
		result = append(result, types.ResultField{Field: aws.String(fields[i]), Value: aws.String(fields[i+1])})
	}
	return result
}
