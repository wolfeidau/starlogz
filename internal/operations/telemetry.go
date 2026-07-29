package operations

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
)

const (
	window       = 24 * time.Hour
	cacheTTL     = time.Minute
	queryTimeout = 5 * time.Second
	pollInterval = 100 * time.Millisecond

	resultTools    = "tools"
	resultLatency  = "latency"
	resultFlows    = "flows"
	outcomeSuccess = "success"
	outcomeFailure = "failure"
)

const toolSeriesQuery = `
fields detail.attributes.tool as tool, detail.outcome as outcome
| filter detail.event_name = "mcp.tool_call.completed"
| stats count() as calls by bin(1h) as bucket, tool, outcome
| sort bucket asc
| limit 1000`

const toolLatencyQuery = `
fields detail.duration_ms as duration_ms
| filter detail.event_name = "mcp.tool_call.completed"
| stats pct(duration_ms, 95) as p95_duration_ms`

const flowQuery = `
fields detail.event_name as event_name, detail.outcome as outcome, detail.reason as reason
| filter detail.event_name like /^oauth\./ or detail.event_name = "ui.session.created"
| stats count() as events by event_name, outcome, reason
| sort event_name asc
| limit 100`

var ErrUnavailable = errors.New("operations telemetry unavailable")

type Provider interface {
	Get(context.Context) (*Snapshot, error)
}

type Snapshot struct {
	GeneratedAt               time.Time
	WindowStartedAt           time.Time
	WindowEndedAt             time.Time
	TotalToolCalls            int
	FailedToolCalls           int
	P95ToolDurationMS         int64
	SuccessfulDashboardLogins int
	ToolSeries                []TimeBucket
	Tools                     []ToolAggregate
	OAuthFlows                []FlowAggregate
	OAuthFailures             []FailureAggregate
}

type TimeBucket struct {
	StartedAt time.Time
	Success   int
	Failure   int
}

type ToolAggregate struct {
	Tool     string
	Calls    int
	Failures int
}

type FlowAggregate struct {
	EventName string
	Success   int
	Failure   int
}

type FailureAggregate struct {
	EventName string
	Reason    string
	Count     int
}

type logsClient interface {
	StartQuery(context.Context, *cloudwatchlogs.StartQueryInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StartQueryOutput, error)
	GetQueryResults(context.Context, *cloudwatchlogs.GetQueryResultsInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetQueryResultsOutput, error)
	StopQuery(context.Context, *cloudwatchlogs.StopQueryInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StopQueryOutput, error)
}

type CloudWatch struct {
	client   logsClient
	logGroup string
	now      func() time.Time

	mu      sync.Mutex
	cached  *Snapshot
	expires time.Time
}

func NewCloudWatch(client logsClient, logGroup string) *CloudWatch {
	return &CloudWatch{client: client, logGroup: logGroup, now: time.Now}
}

func (c *CloudWatch) Get(ctx context.Context) (*Snapshot, error) {
	if c == nil || c.client == nil || c.logGroup == "" {
		return nil, ErrUnavailable
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now().UTC()
	if c.cached != nil && now.Before(c.expires) {
		return c.cached, nil
	}

	queryCtx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	startedAt := now.Add(-window)

	type queryResult struct {
		name string
		rows []map[string]string
		err  error
	}
	results := make(chan queryResult, 3)
	for name, query := range map[string]string{
		resultTools: toolSeriesQuery, resultLatency: toolLatencyQuery, resultFlows: flowQuery,
	} {
		go func() {
			rows, err := c.runQuery(queryCtx, startedAt, now, query)
			results <- queryResult{name: name, rows: rows, err: err}
		}()
	}

	rows := make(map[string][]map[string]string, 3)
	for range 3 {
		result := <-results
		if result.err != nil {
			return nil, result.err
		}
		rows[result.name] = result.rows
	}

	snapshot, err := buildSnapshot(now, startedAt, rows[resultTools], rows[resultLatency], rows[resultFlows])
	if err != nil {
		return nil, err
	}
	c.cached = snapshot
	c.expires = now.Add(cacheTTL)
	return snapshot, nil
}

func (c *CloudWatch) runQuery(ctx context.Context, start, end time.Time, query string) ([]map[string]string, error) {
	output, err := c.client.StartQuery(ctx, &cloudwatchlogs.StartQueryInput{
		LogGroupName: aws.String(c.logGroup),
		StartTime:    aws.Int64(start.Unix()),
		EndTime:      aws.Int64(end.Unix()),
		QueryString:  aws.String(query),
		Limit:        aws.Int32(1000),
	})
	if err != nil {
		return nil, fmt.Errorf("start CloudWatch query: %w", err)
	}
	queryID := aws.ToString(output.QueryId)
	if queryID == "" {
		return nil, fmt.Errorf("start CloudWatch query: empty query ID")
	}
	complete := false
	defer func() {
		if complete {
			return
		}
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		_, _ = c.client.StopQuery(stopCtx, &cloudwatchlogs.StopQueryInput{QueryId: aws.String(queryID)})
		cancel()
	}()

	for {
		queryOutput, queryErr := c.client.GetQueryResults(ctx, &cloudwatchlogs.GetQueryResultsInput{
			QueryId: aws.String(queryID),
		})
		if queryErr != nil {
			return nil, fmt.Errorf("get CloudWatch query results: %w", queryErr)
		}
		switch queryOutput.Status {
		case types.QueryStatusComplete:
			complete = true
			return resultRows(queryOutput.Results), nil
		case types.QueryStatusScheduled, types.QueryStatusRunning:
		default:
			return nil, fmt.Errorf("CloudWatch query ended with status %s", queryOutput.Status)
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for CloudWatch query: %w", ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

func resultRows(results [][]types.ResultField) []map[string]string {
	rows := make([]map[string]string, 0, len(results))
	for _, result := range results {
		row := make(map[string]string, len(result))
		for _, field := range result {
			row[aws.ToString(field.Field)] = aws.ToString(field.Value)
		}
		rows = append(rows, row)
	}
	return rows
}

func buildSnapshot(now, startedAt time.Time, toolRows, latencyRows, flowRows []map[string]string) (*Snapshot, error) {
	snapshot := &Snapshot{GeneratedAt: now, WindowStartedAt: startedAt, WindowEndedAt: now}
	timeBuckets := map[time.Time]*TimeBucket{}
	for bucket := startedAt.Truncate(time.Hour); !bucket.After(now); bucket = bucket.Add(time.Hour) {
		timeBuckets[bucket] = &TimeBucket{StartedAt: bucket}
	}
	tools := map[string]*ToolAggregate{}
	for _, row := range toolRows {
		count, err := requiredInt(row, "calls")
		if err != nil {
			return nil, err
		}
		bucket, err := parseBucket(row["bucket"])
		if err != nil {
			return nil, err
		}
		tool := row["tool"]
		outcome := row["outcome"]
		if tool == "" || (outcome != outcomeSuccess && outcome != outcomeFailure) {
			return nil, fmt.Errorf("invalid CloudWatch tool aggregate")
		}
		snapshot.TotalToolCalls += count
		timeBucket := timeBuckets[bucket]
		if timeBucket == nil {
			timeBucket = &TimeBucket{StartedAt: bucket}
			timeBuckets[bucket] = timeBucket
		}
		toolAggregate := tools[tool]
		if toolAggregate == nil {
			toolAggregate = &ToolAggregate{Tool: tool}
			tools[tool] = toolAggregate
		}
		toolAggregate.Calls += count
		if outcome == outcomeFailure {
			snapshot.FailedToolCalls += count
			timeBucket.Failure += count
			toolAggregate.Failures += count
		} else {
			timeBucket.Success += count
		}
	}
	if len(latencyRows) > 0 && latencyRows[0]["p95_duration_ms"] != "" {
		value, err := strconv.ParseFloat(latencyRows[0]["p95_duration_ms"], 64)
		if err != nil {
			return nil, fmt.Errorf("parse p95 tool duration: %w", err)
		}
		if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("invalid p95 tool duration: %q", latencyRows[0]["p95_duration_ms"])
		}
		snapshot.P95ToolDurationMS = int64(value + 0.5)
	}

	flows := map[string]*FlowAggregate{}
	for _, row := range flowRows {
		count, err := requiredInt(row, "events")
		if err != nil {
			return nil, err
		}
		eventName, outcome, reason := row["event_name"], row["outcome"], row["reason"]
		if eventName == "ui.session.created" {
			if outcome == outcomeSuccess {
				snapshot.SuccessfulDashboardLogins += count
			}
			continue
		}
		if eventName == "" || (outcome != outcomeSuccess && outcome != outcomeFailure) {
			return nil, fmt.Errorf("invalid CloudWatch flow aggregate")
		}
		flow := flows[eventName]
		if flow == nil {
			flow = &FlowAggregate{EventName: eventName}
			flows[eventName] = flow
		}
		if outcome == outcomeFailure {
			flow.Failure += count
			snapshot.OAuthFailures = append(snapshot.OAuthFailures, FailureAggregate{
				EventName: eventName, Reason: reason, Count: count,
			})
		} else {
			flow.Success += count
		}
	}

	for _, bucket := range timeBuckets {
		snapshot.ToolSeries = append(snapshot.ToolSeries, *bucket)
	}
	for _, tool := range tools {
		snapshot.Tools = append(snapshot.Tools, *tool)
	}
	for _, flow := range flows {
		snapshot.OAuthFlows = append(snapshot.OAuthFlows, *flow)
	}
	sortSnapshot(snapshot)
	return snapshot, nil
}

func requiredInt(row map[string]string, field string) (int, error) {
	value, err := strconv.Atoi(row[field])
	if err != nil || value < 0 {
		return 0, fmt.Errorf("parse CloudWatch field %s: %q", field, row[field])
	}
	return value, nil
}

func parseBucket(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05.000", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("parse CloudWatch bucket: %q", value)
}

func sortSnapshot(snapshot *Snapshot) {
	sort.Slice(snapshot.ToolSeries, func(i, j int) bool {
		return snapshot.ToolSeries[i].StartedAt.Before(snapshot.ToolSeries[j].StartedAt)
	})
	sort.Slice(snapshot.Tools, func(i, j int) bool {
		if snapshot.Tools[i].Calls != snapshot.Tools[j].Calls {
			return snapshot.Tools[i].Calls > snapshot.Tools[j].Calls
		}
		return snapshot.Tools[i].Tool < snapshot.Tools[j].Tool
	})
	sort.Slice(snapshot.OAuthFlows, func(i, j int) bool {
		return snapshot.OAuthFlows[i].EventName < snapshot.OAuthFlows[j].EventName
	})
	sort.Slice(snapshot.OAuthFailures, func(i, j int) bool {
		if snapshot.OAuthFailures[i].Count != snapshot.OAuthFailures[j].Count {
			return snapshot.OAuthFailures[i].Count > snapshot.OAuthFailures[j].Count
		}
		if snapshot.OAuthFailures[i].EventName != snapshot.OAuthFailures[j].EventName {
			return snapshot.OAuthFailures[i].EventName < snapshot.OAuthFailures[j].EventName
		}
		return snapshot.OAuthFailures[i].Reason < snapshot.OAuthFailures[j].Reason
	})
}
