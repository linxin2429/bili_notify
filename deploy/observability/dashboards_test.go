package observability

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A syntactically valid Grafana dashboard can silently hide panel definitions
// under links. Check the renderable structure, not just JSON validity.
func TestSessionPanelsAreRenderable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		panels map[int]string
	}{
		{name: "bili-notify-overview.json", panels: map[int]string{11: "stat", 12: "timeseries"}},
		{name: "bili-notify-logs.json", panels: map[int]string{6: "table", 7: "logs"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(filepath.Join("grafana", "provisioning", "dashboards", "json", tt.name))
			require.NoError(t, err)
			var dashboard struct {
				Links  []map[string]json.RawMessage `json:"links"`
				Panels []struct {
					ID      int               `json:"id"`
					Type    string            `json:"type"`
					Targets []json.RawMessage `json:"targets"`
					GridPos struct {
						W, H int
						X, Y int
					} `json:"gridPos"`
				} `json:"panels"`
			}
			require.NoError(t, json.Unmarshal(raw, &dashboard))
			for _, link := range dashboard.Links {
				assert.NotContains(t, link, "gridPos")
				assert.NotContains(t, link, "targets")
			}
			seen := make(map[int]string)
			for i, panel := range dashboard.Panels {
				assert.NotContains(t, seen, panel.ID, "duplicate panel id")
				seen[panel.ID] = panel.Type
				assert.NotEmpty(t, panel.Targets)
				r := panel.GridPos
				assert.Positive(t, r.W)
				assert.Positive(t, r.H)
				assert.GreaterOrEqual(t, r.X, 0)
				assert.LessOrEqual(t, r.X+r.W, 24)
				for _, other := range dashboard.Panels[i+1:] {
					s := other.GridPos
					assert.True(t, r.X+r.W <= s.X || s.X+s.W <= r.X || r.Y+r.H <= s.Y || s.Y+s.H <= r.Y, "panels %d and %d overlap", panel.ID, other.ID)
				}
			}
			for id, kind := range tt.panels {
				assert.Equal(t, kind, seen[id], "missing renderable session panel %d", id)
			}
		})
	}
}
