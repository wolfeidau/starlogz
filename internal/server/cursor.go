package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/wolfeidau/starlogz/internal/store"
)

var errInvalidCursor = errors.New("invalid_cursor")

const (
	cursorVersion           = 1
	maxCursorLength         = 1024
	insightListCursorKind   = "insight_list"
	insightSearchCursorKind = "insight_search"
)

type cursorPayload struct {
	Version     int     `json:"v"`
	Kind        string  `json:"o"`
	FilterHash  string  `json:"f"`
	RankBits    *uint32 `json:"r,omitempty"`
	UpdatedAtUS *int64  `json:"u"`
	ID          string  `json:"i"`
}

type decodedCursor struct {
	Payload   cursorPayload
	UpdatedAt time.Time
	ID        uuid.UUID
}

func encodeInsightListCursor(projectID uuid.UUID, tag string, cursor *store.InsightListCursor) (string, error) {
	updatedAtUS := cursor.UpdatedAt.UnixMicro()
	payload := cursorPayload{
		Version:     cursorVersion,
		Kind:        insightListCursorKind,
		FilterHash:  insightListFilterHash(projectID, tag),
		UpdatedAtUS: &updatedAtUS,
		ID:          cursor.ID.String(),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeInsightListCursor(value string, projectID uuid.UUID, tag string) (*store.InsightListCursor, error) {
	decoded, err := decodeCursor(value, insightListCursorKind, insightListFilterHash(projectID, tag))
	if err != nil || decoded == nil {
		return nil, err
	}
	return &store.InsightListCursor{UpdatedAt: decoded.UpdatedAt, ID: decoded.ID}, nil
}

func insightListFilterHash(projectID uuid.UUID, tag string) string {
	filter := make([]byte, 0, len(insightListCursorKind)+1+len(projectID)+len(tag))
	filter = append(filter, insightListCursorKind...)
	filter = append(filter, 0)
	filter = append(filter, projectID[:]...)
	filter = append(filter, tag...)
	sum := sha256.Sum256(filter)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func encodeInsightSearchCursor(projectID uuid.UUID, query string, queryMode store.SearchQueryMode, tags []string, tagMode store.SearchTagMode, cursor *store.InsightSearchCursor) (string, error) {
	updatedAtUS := cursor.UpdatedAt.UnixMicro()
	rankBits := math.Float32bits(cursor.Rank)
	payload := cursorPayload{
		Version:     cursorVersion,
		Kind:        insightSearchCursorKind,
		FilterHash:  insightSearchFilterHash(projectID, query, queryMode, tags, tagMode),
		RankBits:    &rankBits,
		UpdatedAtUS: &updatedAtUS,
		ID:          cursor.ID.String(),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeInsightSearchCursor(value string, projectID uuid.UUID, query string, queryMode store.SearchQueryMode, tags []string, tagMode store.SearchTagMode) (*store.InsightSearchCursor, error) {
	decoded, err := decodeCursor(value, insightSearchCursorKind, insightSearchFilterHash(projectID, query, queryMode, tags, tagMode))
	if err != nil || decoded == nil {
		return nil, err
	}
	if decoded.Payload.RankBits == nil {
		return nil, errInvalidCursor
	}
	rank := math.Float32frombits(*decoded.Payload.RankBits)
	if math.IsNaN(float64(rank)) || math.IsInf(float64(rank), 0) || rank < 0 {
		return nil, errInvalidCursor
	}
	return &store.InsightSearchCursor{Rank: rank, UpdatedAt: decoded.UpdatedAt, ID: decoded.ID}, nil
}

func decodeCursor(value, kind, filterHash string) (*decodedCursor, error) {
	if value == "" {
		return nil, nil
	}
	if len(value) > maxCursorLength {
		return nil, errInvalidCursor
	}

	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, errInvalidCursor
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	var payload cursorPayload
	if err := decoder.Decode(&payload); err != nil {
		return nil, errInvalidCursor
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errInvalidCursor
	}
	if payload.Version != cursorVersion || payload.Kind != kind || payload.FilterHash != filterHash || payload.UpdatedAtUS == nil {
		return nil, errInvalidCursor
	}
	id, err := uuid.Parse(payload.ID)
	if err != nil || id == uuid.Nil {
		return nil, errInvalidCursor
	}
	return &decodedCursor{Payload: payload, UpdatedAt: time.UnixMicro(*payload.UpdatedAtUS).UTC(), ID: id}, nil
}

func canonicalSearchTags(tags []string) []string {
	normalised := make([]string, len(tags))
	copy(normalised, tags)
	slices.Sort(normalised)
	return slices.Compact(normalised)
}

func insightSearchFilterHash(projectID uuid.UUID, query string, queryMode store.SearchQueryMode, tags []string, tagMode store.SearchTagMode) string {
	filter, _ := json.Marshal(struct {
		Kind      string                `json:"operation"`
		ProjectID uuid.UUID             `json:"project_id"`
		Query     string                `json:"query"`
		QueryMode store.SearchQueryMode `json:"query_mode"`
		Tags      []string              `json:"tags"`
		TagMode   store.SearchTagMode   `json:"tag_mode"`
	}{
		Kind: insightSearchCursorKind, ProjectID: projectID,
		Query: query, QueryMode: queryMode,
		Tags: canonicalSearchTags(tags), TagMode: tagMode,
	})
	sum := sha256.Sum256(filter)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
