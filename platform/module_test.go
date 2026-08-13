package platform

import (
	"context"
	"testing"

	"github.com/linxin2429/bili_notify/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testRunner struct{}

func (testRunner) Run(context.Context) error { return nil }

func TestBuiltinMetaTriggersRegisteredRoles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		platform model.Platform
		role     model.AuthorRole
		want     bool
	}{
		{name: "Bilibili UP", platform: model.PlatformBilibili, role: model.RoleUP, want: true},
		{name: "Bilibili member", platform: model.PlatformBilibili, role: model.RoleMember},
		{name: "Knowledge Planet owner", platform: model.PlatformZSXQ, role: model.RoleOwner, want: true},
		{name: "Knowledge Planet admin", platform: model.PlatformZSXQ, role: model.RoleAdmin},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			meta, ok := BuiltinMeta(tt.platform)
			require.True(t, ok)
			require.NoError(t, meta.Validate())
			assert.Equal(t, tt.want, meta.Triggers(tt.role))
		})
	}
}

func TestModuleRequiresRunner(t *testing.T) {
	t.Parallel()
	meta, ok := BuiltinMeta(model.PlatformBilibili)
	require.True(t, ok)
	require.Error(t, (Module{Meta: meta}).Validate())
	require.NoError(t, (Module{Meta: meta, Runner: testRunner{}, Accounts: AccountRoutes{Disconnect: func(context.Context) error { return nil }}}).Validate())
}
