package sources

import (
	"context"
	"errors"

	"github.com/linxin2429/bili_notify/model"
)

// Repository is the persistence surface owned by source administration.
type Repository interface {
	CreateSource(model.Source) error
	DeleteSource(string) error
	ListSources(model.Platform) ([]model.Source, error)
	PutSource(model.Source) error
	Source(string) (model.Source, error)
}

type RepositoryFactory func(context.Context) Repository

type Admin struct {
	repository          RepositoryFactory
	onBilibiliChanged   func()
	onSourcesChanged    func()
	onSourceDataDeleted func()
}

func NewAdmin(repository RepositoryFactory, onBilibiliChanged, onSourcesChanged, onSourceDataDeleted func()) (*Admin, error) {
	if repository == nil || onBilibiliChanged == nil || onSourcesChanged == nil || onSourceDataDeleted == nil {
		return nil, errors.New("source admin dependencies are required")
	}
	return &Admin{repository: repository, onBilibiliChanged: onBilibiliChanged,
		onSourcesChanged: onSourcesChanged, onSourceDataDeleted: onSourceDataDeleted}, nil
}

func (a *Admin) List(ctx context.Context, platform model.Platform) ([]model.Source, error) {
	return a.repository(ctx).ListSources(platform)
}

func (a *Admin) Create(ctx context.Context, source model.Source) error {
	if err := source.Validate(); err != nil {
		return err
	}
	if err := a.repository(ctx).CreateSource(source); err != nil {
		return err
	}
	if source.Platform == model.PlatformBilibili {
		a.onBilibiliChanged()
	}
	a.onSourcesChanged()
	return nil
}

func (a *Admin) Update(ctx context.Context, id, name, note string, enabled bool) (model.Source, error) {
	repository := a.repository(ctx)
	source, err := repository.Source(id)
	if err != nil {
		return model.Source{}, err
	}
	source.Note, source.Enabled = note, enabled
	if name != "" {
		source.Name = name
	}
	if source.Enabled && source.BaselineState == model.BaselineFailed {
		source.BaselineState = model.BaselineRunning
	}
	if err := source.Validate(); err != nil {
		return model.Source{}, err
	}
	if err := repository.PutSource(source); err != nil {
		return model.Source{}, err
	}
	if source.Platform == model.PlatformBilibili {
		a.onBilibiliChanged()
	}
	a.onSourcesChanged()
	return source, nil
}

func (a *Admin) Delete(ctx context.Context, id string) error {
	repository := a.repository(ctx)
	source, err := repository.Source(id)
	if err != nil {
		return err
	}
	if err := repository.DeleteSource(id); err != nil {
		return err
	}
	if source.Platform == model.PlatformBilibili {
		a.onBilibiliChanged()
	}
	a.onSourceDataDeleted()
	return nil
}
