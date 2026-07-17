package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/wolfeidau/starlogz/internal/store"
)

var errInvalidCursor = errors.New("invalid_cursor")

const (
	cursorVersion         = 1
	maxCursorLength       = 1024
	insightListCursorKind = "insight_list"
)

type cursorPayload struct {
	Version     int    `json:"v"`
	Kind        string `json:"o"`
	FilterHash  string `json:"f"`
	UpdatedAtUS *int64 `json:"u"`
	ID          string `json:"i"`
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
	if payload.Version != cursorVersion || payload.Kind != insightListCursorKind || payload.FilterHash != insightListFilterHash(projectID, tag) || payload.UpdatedAtUS == nil {
		return nil, errInvalidCursor
	}
	id, err := uuid.Parse(payload.ID)
	if err != nil || id == uuid.Nil {
		return nil, errInvalidCursor
	}

	return &store.InsightListCursor{UpdatedAt: time.UnixMicro(*payload.UpdatedAtUS).UTC(), ID: id}, nil
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
