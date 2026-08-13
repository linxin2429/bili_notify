package zsxq

import (
	"context"

	"github.com/linxin2429/bili_notify/model"
	platformcontract "github.com/linxin2429/bili_notify/platform"
)

func NewModule(runner platformcontract.Runner, disconnect func(context.Context) error) platformcontract.Module {
	meta, ok := platformcontract.BuiltinMeta(model.PlatformZSXQ)
	if !ok {
		panic("missing Knowledge Planet platform metadata")
	}
	return platformcontract.Module{Meta: meta, Runner: runner, Accounts: platformcontract.AccountRoutes{Disconnect: disconnect}}
}
