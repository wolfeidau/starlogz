package server

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/wolfeidau/starlogz/internal/store"
)

func TestToProtoInsightIncludesRevision(t *testing.T) {
	now := time.Now()
	insight, err := toProtoInsight(&store.Insight{
		ID: uuid.New(), Content: "content", CreatedAt: now, UpdatedAt: now, Revision: 7,
	}, "project")
	require.NoError(t, err)
	require.Equal(t, int32(7), insight.GetRevision())
}
