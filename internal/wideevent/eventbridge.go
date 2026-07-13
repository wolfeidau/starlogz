package wideevent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
)

const eventSource = "starlogz.service"

type eventBridgeClient interface {
	PutEvents(context.Context, *eventbridge.PutEventsInput, ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error)
}

type EventBridgePublisher struct {
	client  eventBridgeClient
	busName string
}

func NewEventBridgePublisher(client eventBridgeClient, busName string) *EventBridgePublisher {
	return &EventBridgePublisher{client: client, busName: busName}
}

func (p *EventBridgePublisher) Publish(ctx context.Context, event Event) error {
	if err := event.Validate(); err != nil {
		return fmt.Errorf("validate wide event: %w", err)
	}
	detail, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal wide event: %w", err)
	}
	occurredAt, _ := time.Parse(time.RFC3339Nano, event.OccurredAt)
	output, err := p.client.PutEvents(ctx, &eventbridge.PutEventsInput{Entries: []types.PutEventsRequestEntry{{
		EventBusName: aws.String(p.busName),
		Source:       aws.String(eventSource),
		DetailType:   aws.String(string(event.EventName)),
		Detail:       aws.String(string(detail)),
		Time:         aws.Time(occurredAt),
	}}})
	if err != nil {
		return fmt.Errorf("put EventBridge event: %w", err)
	}
	if output.FailedEntryCount > 0 {
		code := "unknown"
		if len(output.Entries) > 0 {
			code = aws.ToString(output.Entries[0].ErrorCode)
		}
		return fmt.Errorf("put EventBridge event failed: %s", code)
	}
	return nil
}
