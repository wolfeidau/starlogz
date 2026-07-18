package server

import (
	"encoding/base64"
	"encoding/json"
	"math"
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
	revision := 1

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
		"history field":   {value: mutate(func(p *cursorPayload) { p.Revision = &revision }), projectID: projectID, tag: "go"},
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

func TestInsightHistoryCursorRoundTrip(t *testing.T) {
	projectID := uuid.New()
	insightID := uuid.New()
	want := &store.InsightHistoryCursor{Revision: 7}
	encoded, err := encodeInsightHistoryCursor(projectID, insightID, want)
	require.NoError(t, err)
	require.NotContains(t, encoded, "=")

	decoded, err := decodeInsightHistoryCursor(encoded, projectID, insightID)
	require.NoError(t, err)
	require.Equal(t, want, decoded)

	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	require.NoError(t, err)
	require.NotContains(t, string(payload), projectID.String())
	require.NotContains(t, string(payload), insightID.String())
}

func TestInsightHistoryCursorRejectsInvalidValues(t *testing.T) {
	projectID := uuid.New()
	insightID := uuid.New()
	encoded, err := encodeInsightHistoryCursor(projectID, insightID, &store.InsightHistoryCursor{Revision: 7})
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
	zero := 0
	tooLarge := store.MaxInsightRevision + 1
	timestamp := time.Now().UnixMicro()

	tests := map[string]struct {
		value     string
		projectID uuid.UUID
		insightID uuid.UUID
	}{
		"malformed":        {value: "not-base64", projectID: projectID, insightID: insightID},
		"too long":         {value: strings.Repeat("a", maxCursorLength+1), projectID: projectID, insightID: insightID},
		"changed project":  {value: encoded, projectID: uuid.New(), insightID: insightID},
		"changed insight":  {value: encoded, projectID: projectID, insightID: uuid.New()},
		"missing revision": {value: mutate(func(p *cursorPayload) { p.Revision = nil }), projectID: projectID, insightID: insightID},
		"zero revision":    {value: mutate(func(p *cursorPayload) { p.Revision = &zero }), projectID: projectID, insightID: insightID},
		"large revision":   {value: mutate(func(p *cursorPayload) { p.Revision = &tooLarge }), projectID: projectID, insightID: insightID},
		"wrong operation":  {value: mutate(func(p *cursorPayload) { p.Kind = insightListCursorKind }), projectID: projectID, insightID: insightID},
		"wrong version":    {value: mutate(func(p *cursorPayload) { p.Version++ }), projectID: projectID, insightID: insightID},
		"timestamp field":  {value: mutate(func(p *cursorPayload) { p.UpdatedAtUS = &timestamp }), projectID: projectID, insightID: insightID},
		"id field":         {value: mutate(func(p *cursorPayload) { p.ID = uuid.NewString() }), projectID: projectID, insightID: insightID},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := decodeInsightHistoryCursor(test.value, test.projectID, test.insightID)
			require.ErrorIs(t, err, errInvalidCursor)
		})
	}
}

func TestInsightHistoryCursorEmptyValueStartsFirstPage(t *testing.T) {
	decoded, err := decodeInsightHistoryCursor("", uuid.New(), uuid.New())
	require.NoError(t, err)
	require.Nil(t, decoded)
}

func TestInsightSearchCursorRoundTripPreservesRealRank(t *testing.T) {
	projectID := uuid.New()
	want := &store.InsightSearchCursor{
		Rank:      math.Float32frombits(0x3dcccccd),
		UpdatedAt: time.Date(2026, 7, 18, 12, 34, 56, 789123000, time.UTC),
		ID:        uuid.New(),
	}
	encoded, err := encodeInsightSearchCursor(projectID, "postgres query", store.SearchQueryModeAll, []string{"db", "go"}, store.SearchTagModeAny, want)
	require.NoError(t, err)
	require.NotContains(t, encoded, "=")

	decoded, err := decodeInsightSearchCursor(encoded, projectID, "postgres query", store.SearchQueryModeAll, []string{"go", "db", "db"}, store.SearchTagModeAny)
	require.NoError(t, err)
	require.Equal(t, math.Float32bits(want.Rank), math.Float32bits(decoded.Rank))
	require.Equal(t, want.UpdatedAt, decoded.UpdatedAt)
	require.Equal(t, want.ID, decoded.ID)

	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	require.NoError(t, err)
	require.NotContains(t, string(payload), projectID.String())
	require.NotContains(t, string(payload), "postgres query")
	require.NotContains(t, string(payload), "db")
}

func TestInsightSearchCursorRejectsInvalidValuesAndChangedFilters(t *testing.T) {
	projectID := uuid.New()
	want := &store.InsightSearchCursor{Rank: 0.25, UpdatedAt: time.Now().UTC(), ID: uuid.New()}
	encoded, err := encodeInsightSearchCursor(projectID, "postgres", store.SearchQueryModeAll, []string{"db"}, store.SearchTagModeAll, want)
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
	bits := func(value float32) *uint32 {
		valueBits := math.Float32bits(value)
		return &valueBits
	}

	tests := map[string]struct {
		value     string
		projectID uuid.UUID
		query     string
		queryMode store.SearchQueryMode
		tags      []string
		tagMode   store.SearchTagMode
	}{
		"malformed":          {value: "not-base64", projectID: projectID, query: "postgres", queryMode: store.SearchQueryModeAll, tags: []string{"db"}, tagMode: store.SearchTagModeAll},
		"too long":           {value: strings.Repeat("a", maxCursorLength+1), projectID: projectID, query: "postgres", queryMode: store.SearchQueryModeAll, tags: []string{"db"}, tagMode: store.SearchTagModeAll},
		"changed project":    {value: encoded, projectID: uuid.New(), query: "postgres", queryMode: store.SearchQueryModeAll, tags: []string{"db"}, tagMode: store.SearchTagModeAll},
		"changed query":      {value: encoded, projectID: projectID, query: "redis", queryMode: store.SearchQueryModeAll, tags: []string{"db"}, tagMode: store.SearchTagModeAll},
		"changed query mode": {value: encoded, projectID: projectID, query: "postgres", queryMode: store.SearchQueryModeWeb, tags: []string{"db"}, tagMode: store.SearchTagModeAll},
		"changed tags":       {value: encoded, projectID: projectID, query: "postgres", queryMode: store.SearchQueryModeAll, tags: []string{"operations"}, tagMode: store.SearchTagModeAll},
		"changed tag mode":   {value: encoded, projectID: projectID, query: "postgres", queryMode: store.SearchQueryModeAll, tags: []string{"db"}, tagMode: store.SearchTagModeAny},
		"missing rank":       {value: mutate(func(p *cursorPayload) { p.RankBits = nil }), projectID: projectID, query: "postgres", queryMode: store.SearchQueryModeAll, tags: []string{"db"}, tagMode: store.SearchTagModeAll},
		"nan rank":           {value: mutate(func(p *cursorPayload) { p.RankBits = bits(float32(math.NaN())) }), projectID: projectID, query: "postgres", queryMode: store.SearchQueryModeAll, tags: []string{"db"}, tagMode: store.SearchTagModeAll},
		"infinite rank":      {value: mutate(func(p *cursorPayload) { p.RankBits = bits(float32(math.Inf(1))) }), projectID: projectID, query: "postgres", queryMode: store.SearchQueryModeAll, tags: []string{"db"}, tagMode: store.SearchTagModeAll},
		"negative rank":      {value: mutate(func(p *cursorPayload) { p.RankBits = bits(-0.1) }), projectID: projectID, query: "postgres", queryMode: store.SearchQueryModeAll, tags: []string{"db"}, tagMode: store.SearchTagModeAll},
		"missing time":       {value: mutate(func(p *cursorPayload) { p.UpdatedAtUS = nil }), projectID: projectID, query: "postgres", queryMode: store.SearchQueryModeAll, tags: []string{"db"}, tagMode: store.SearchTagModeAll},
		"wrong operation":    {value: mutate(func(p *cursorPayload) { p.Kind = insightListCursorKind }), projectID: projectID, query: "postgres", queryMode: store.SearchQueryModeAll, tags: []string{"db"}, tagMode: store.SearchTagModeAll},
		"wrong version":      {value: mutate(func(p *cursorPayload) { p.Version++ }), projectID: projectID, query: "postgres", queryMode: store.SearchQueryModeAll, tags: []string{"db"}, tagMode: store.SearchTagModeAll},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := decodeInsightSearchCursor(test.value, test.projectID, test.query, test.queryMode, test.tags, test.tagMode)
			require.ErrorIs(t, err, errInvalidCursor)
		})
	}
}

func TestInsightSearchCursorEmptyValueStartsFirstPage(t *testing.T) {
	decoded, err := decodeInsightSearchCursor("", uuid.New(), "postgres", store.SearchQueryModeAll, []string{"db"}, store.SearchTagModeAll)
	require.NoError(t, err)
	require.Nil(t, decoded)
}

func TestInsightSearchCursorTreatsNilAndEmptyTagsAsEquivalent(t *testing.T) {
	projectID := uuid.New()
	want := &store.InsightSearchCursor{Rank: 0.25, UpdatedAt: time.Date(2026, 7, 18, 12, 34, 56, 789123000, time.UTC), ID: uuid.New()}
	encoded, err := encodeInsightSearchCursor(projectID, "postgres", store.SearchQueryModeAll, nil, store.SearchTagModeAll, want)
	require.NoError(t, err)

	decoded, err := decodeInsightSearchCursor(encoded, projectID, "postgres", store.SearchQueryModeAll, []string{}, store.SearchTagModeAll)
	require.NoError(t, err)
	require.Equal(t, want, decoded)
}
