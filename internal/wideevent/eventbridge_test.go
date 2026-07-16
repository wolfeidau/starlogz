package wideevent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fakeEventBridgeClient struct {
	input  *eventbridge.PutEventsInput
	output *eventbridge.PutEventsOutput
	err    error
}

func (c *fakeEventBridgeClient) PutEvents(_ context.Context, input *eventbridge.PutEventsInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error) {
	c.input = input
	return c.output, c.err
}

func validEvent() Event {
	return Event{
		SchemaVersion: SchemaVersion, EventID: uuid.New().String(), EventName: OAuthRefreshCompleted,
		OccurredAt: time.Now().UTC().Format(time.RFC3339Nano), Environment: "dev",
		ServiceVersion: "v1.2.3", Outcome: OutcomeSuccess, Reason: ReasonCompleted,
		Attributes: withTestIdentity(nil),
	}
}

func TestEventBridgePublisherUsesScopedBusAndBoundedDetail(t *testing.T) {
	client := &fakeEventBridgeClient{output: &eventbridge.PutEventsOutput{Entries: []types.PutEventsResultEntry{{EventId: aws.String("event-id")}}}}
	publisher := NewEventBridgePublisher(client, "starlogz-dev")
	event := validEvent()

	require.NoError(t, publisher.Publish(t.Context(), event))
	require.Len(t, client.input.Entries, 1)
	entry := client.input.Entries[0]
	require.Equal(t, "starlogz-dev", aws.ToString(entry.EventBusName))
	require.Equal(t, eventSource, aws.ToString(entry.Source))
	require.Equal(t, string(event.EventName), aws.ToString(entry.DetailType))
	require.Equal(t, event.OccurredAt, aws.ToTime(entry.Time).UTC().Format(time.RFC3339Nano))
	var detail Event
	require.NoError(t, json.Unmarshal([]byte(aws.ToString(entry.Detail)), &detail))
	require.Equal(t, event, detail)
}

func TestEventBridgePublisherReturnsTransportAndEntryFailures(t *testing.T) {
	t.Run("validation", func(t *testing.T) {
		client := &fakeEventBridgeClient{}
		err := NewEventBridgePublisher(client, "starlogz-dev").Publish(t.Context(), Event{})
		require.ErrorContains(t, err, "validate wide event")
		require.Nil(t, client.input)
	})
	t.Run("transport", func(t *testing.T) {
		client := &fakeEventBridgeClient{err: errors.New("unavailable")}
		err := NewEventBridgePublisher(client, "starlogz-dev").Publish(t.Context(), validEvent())
		require.ErrorContains(t, err, "put EventBridge event")
	})
	t.Run("entry", func(t *testing.T) {
		client := &fakeEventBridgeClient{output: &eventbridge.PutEventsOutput{
			FailedEntryCount: 1,
			Entries:          []types.PutEventsResultEntry{{ErrorCode: aws.String("InternalFailure")}},
		}}
		err := NewEventBridgePublisher(client, "starlogz-dev").Publish(t.Context(), validEvent())
		require.EqualError(t, err, "put EventBridge event failed: InternalFailure")
	})
}
