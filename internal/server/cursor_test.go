package server

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/wolfeidau/starlogz/internal/store"
)

func TestInsightListCursorRoundTrip(t *testing.T) {
	projectID := uuid.New()
	timestamps := map[string]time.Time{
		"current":   time.Date(2026, 7, 18, 12, 34, 56, 789123000, time.UTC),
		"epoch":     time.Unix(0, 0).UTC(),
		"pre-epoch": time.Date(1960, 1, 2, 3, 4, 5, 678901000, time.UTC),
	}

	for name, timestamp := range timestamps {
		t.Run(name, func(t *testing.T) {
			want := &store.InsightListCursor{UpdatedAt: timestamp, ID: uuid.New()}
			encoded, err := encodeInsightListCursor(projectID, "postgres", want)
			require.NoError(t, err)
			require.NotContains(t, encoded, "=")

			decoded, err := decodeInsightListCursor(encoded, projectID, "postgres")
			require.NoError(t, err)
			require.Equal(t, want, decoded)

			payload, err := base64.RawURLEncoding.DecodeString(encoded)
			require.NoError(t, err)
			require.NotContains(t, string(payload), projectID.String())
			require.NotContains(t, string(payload), "postgres")
		})
	}
}

func TestInsightListCursorRejectsInvalidValues(t *testing.T) {
	projectID := uuid.New()
	cursor := &store.InsightListCursor{UpdatedAt: time.Now().UTC(), ID: uuid.New()}
	encoded, err := encodeInsightListCursor(projectID, "go", cursor)
	require.NoError(t, err)
	mutate := func(change func(*cursorPayload)) string {
		t.Helper()
		decoded, err := base64.RawURLEncoding.DecodeString(encoded)
		require.NoError(t, err)
		var payload cursorPayload
		require.NoError(t, json.Unmarshal(decoded, &payload))
		change(&payload)
		decoded, err = json.Marshal(payload)
		require.NoError(t, err)
		return base64.RawURLEncoding.EncodeToString(decoded)
	}

	tests := map[string]struct {
		value     string
		projectID uuid.UUID
		tag       string
	}{
		"malformed":       {value: "not-base64", projectID: projectID, tag: "go"},
		"too long":        {value: strings.Repeat("a", maxCursorLength+1), projectID: projectID, tag: "go"},
		"changed project": {value: encoded, projectID: uuid.New(), tag: "go"},
		"changed tag":     {value: encoded, projectID: projectID, tag: "rust"},
		"missing time":    {value: mutate(func(p *cursorPayload) { p.UpdatedAtUS = nil }), projectID: projectID, tag: "go"},
		"wrong operation": {value: mutate(func(p *cursorPayload) { p.Kind = "insight_search" }), projectID: projectID, tag: "go"},
		"wrong version":   {value: mutate(func(p *cursorPayload) { p.Version++ }), projectID: projectID, tag: "go"},
		"trailing object": {value: base64.RawURLEncoding.EncodeToString([]byte(`{"v":1}{}`)), projectID: projectID, tag: "go"},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := decodeInsightListCursor(test.value, test.projectID, test.tag)
			require.ErrorIs(t, err, errInvalidCursor)
			require.Equal(t, "invalid_cursor", err.Error())
		})
	}
}

func TestInsightListCursorEmptyValueStartsFirstPage(t *testing.T) {
	decoded, err := decodeInsightListCursor("", uuid.New(), strings.Repeat("x", 20))
	require.NoError(t, err)
	require.Nil(t, decoded)
}
