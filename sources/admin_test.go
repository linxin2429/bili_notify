package sources

import (
	"context"
	"errors"
	"testing"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errMissingSource = errors.New("missing source")

type memoryRepository struct {
	sources map[string]model.Source
}

func (r *memoryRepository) CreateSource(source model.Source) error {
	if _, exists := r.sources[source.ID]; exists {
		return errors.New("duplicate")
	}
	r.sources[source.ID] = source
	return nil
}
func (r *memoryRepository) DeleteSource(id string) error {
	if _, exists := r.sources[id]; !exists {
		return errMissingSource
	}
	delete(r.sources, id)
	return nil
}
func (r *memoryRepository) ListSources(platform model.Platform) ([]model.Source, error) {
	items := make([]model.Source, 0)
	for _, source := range r.sources {
		if platform == "" || source.Platform == platform {
			items = append(items, source)
		}
	}
	return items, nil
}
func (r *memoryRepository) PutSource(source model.Source) error {
	r.sources[source.ID] = source
	return nil
}
func (r *memoryRepository) Source(id string) (model.Source, error) {
	source, exists := r.sources[id]
	if !exists {
		return model.Source{}, errMissingSource
	}
	return source, nil
}

func TestAdminOwnsSourceMutationSideEffects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		run        func(context.Context, *Admin, model.Source) error
		wantBili   int
		wantChange int
		wantDelete int
	}{
		{name: "create", run: func(ctx context.Context, admin *Admin, source model.Source) error { return admin.Create(ctx, source) }, wantBili: 1, wantChange: 1},
		{name: "update", run: func(ctx context.Context, admin *Admin, source model.Source) error {
			if err := admin.Create(ctx, source); err != nil {
				return err
			}
			_, err := admin.Update(ctx, source.ID, "renamed", "note", true, "", nil)
			return err
		}, wantBili: 2, wantChange: 2},
		{name: "delete", run: func(ctx context.Context, admin *Admin, source model.Source) error {
			if err := admin.Create(ctx, source); err != nil {
				return err
			}
			return admin.Delete(ctx, source.ID)
		}, wantBili: 2, wantChange: 1, wantDelete: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repository := &memoryRepository{sources: make(map[string]model.Source)}
			bili, changed, deleted := 0, 0, 0
			admin, err := NewAdmin(func(context.Context) Repository { return repository },
				func() { bili++ }, func() { changed++ }, func() { deleted++ })
			require.NoError(t, err)
			source := model.Source{ID: model.SourceID(model.PlatformBilibili, "42"), Platform: model.PlatformBilibili, Type: model.SourceBilibiliUP, ExternalID: "42", Name: "UP", BaselineState: model.BaselinePending}
			require.NoError(t, tt.run(t.Context(), admin, source))
			assert.Equal(t, tt.wantBili, bili)
			assert.Equal(t, tt.wantChange, changed)
			assert.Equal(t, tt.wantDelete, deleted)
		})
	}
}
