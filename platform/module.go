// Package platform defines the composition-root contract for data-source
// adapters. It deliberately contains no protocol or persistence abstractions.
package platform

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/linxin2429/bili_notify/model"
)

type Runner interface {
	Run(context.Context) error
}

type PlatformMeta struct {
	Platform     model.Platform
	DisplayName  string
	ContentNoun  string
	TriggerRoles []model.AuthorRole
	TriggerLabel string
	AuthorLabel  string
	ManualSource bool
	SourceSync   bool
}

func (meta PlatformMeta) Validate() error {
	if err := meta.Platform.Validate(); err != nil {
		return err
	}
	if meta.DisplayName == "" || meta.ContentNoun == "" || meta.TriggerLabel == "" || meta.AuthorLabel == "" || len(meta.TriggerRoles) == 0 {
		return errors.New("platform metadata is incomplete")
	}
	return nil
}

func (meta PlatformMeta) Triggers(role model.AuthorRole) bool {
	return slices.Contains(meta.TriggerRoles, role)
}

// Optional capabilities are consumer-owned small contracts. A nil field means
// that the platform does not expose that capability.
type SourceSync interface{ SyncSources(context.Context) error }
type ManualSource interface{ NotifySourceChanged() }
type AIEligibility interface {
	Eligible(model.Content) (*model.AIContentSnapshot, bool)
}
type MediaAuth interface{ AuthorizeMedia(*http.Request) }

type AccountRoutes struct {
	Disconnect func(context.Context) error
}

type Module struct {
	Meta          PlatformMeta
	Runner        Runner
	Accounts      AccountRoutes
	SourceSync    SourceSync
	ManualSource  ManualSource
	AIEligibility AIEligibility
	MediaAuth     MediaAuth
}

func (module Module) Validate() error {
	if err := module.Meta.Validate(); err != nil {
		return err
	}
	if module.Runner == nil {
		return errors.New("platform runner is required")
	}
	if module.Accounts.Disconnect == nil {
		return errors.New("platform account routes are required")
	}
	return nil
}

func BuiltinMeta(platform model.Platform) (PlatformMeta, bool) {
	switch platform {
	case model.PlatformBilibili:
		return PlatformMeta{Platform: platform, DisplayName: "B站", ContentNoun: "动态", TriggerRoles: []model.AuthorRole{model.RoleUP}, TriggerLabel: "UP主", AuthorLabel: "UP主", ManualSource: true}, true
	case model.PlatformZSXQ:
		return PlatformMeta{Platform: platform, DisplayName: "知识星球", ContentNoun: "内容", TriggerRoles: []model.AuthorRole{model.RoleOwner}, TriggerLabel: "星球主", AuthorLabel: "作者", SourceSync: true}, true
	default:
		return PlatformMeta{}, false
	}
}

func BuiltinPlatforms() []model.Platform {
	return []model.Platform{model.PlatformBilibili, model.PlatformZSXQ}
}
