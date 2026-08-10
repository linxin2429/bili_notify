package zsxq

import (
	"testing"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWatermarkOrdersByTimestampThenStableID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content model.Content
		mark    string
		want    bool
	}{
		{name: "fractional second is newer", content: model.Content{ID: "b", PublishedAt: time.Date(2026, 8, 10, 0, 0, 0, 100, time.UTC)}, mark: "2026-08-10T00:00:00Z|a", want: true},
		{name: "larger ID breaks equal timestamp", content: model.Content{ID: "b", PublishedAt: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)}, mark: "2026-08-10T00:00:00Z|a", want: true},
		{name: "older tuple is baseline", content: model.Content{ID: "z", PublishedAt: time.Date(2026, 8, 9, 23, 59, 59, 0, time.UTC)}, mark: "2026-08-10T00:00:00Z|a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isAfterWatermark(tt.content, tt.mark))
			stamp, id, ok := decodeWatermark(encodeWatermark(tt.content))
			require.True(t, ok)
			assert.Equal(t, tt.content.ID, id)
			assert.True(t, stamp.Equal(tt.content.PublishedAt))
		})
	}
}
