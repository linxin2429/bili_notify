package app

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDependenciesValidate(t *testing.T) {
	t.Parallel()
	listener := new(net.TCPListener)
	tests := []struct {
		name    string
		value   Dependencies
		wantErr string
	}{
		{name: "defaults"},
		{name: "complete base URLs", value: Dependencies{BilibiliAPIURL: "https://api.example", BilibiliPassportURL: "https://passport.example"}},
		{name: "complete listeners", value: Dependencies{AdminListener: listener, ObserveListener: listener}},
		{name: "missing passport URL", value: Dependencies{BilibiliAPIURL: "https://api.example"}, wantErr: "base URLs"},
		{name: "missing API URL", value: Dependencies{BilibiliPassportURL: "https://passport.example"}, wantErr: "base URLs"},
		{name: "missing observability listener", value: Dependencies{AdminListener: listener}, wantErr: "listeners"},
		{name: "missing admin listener", value: Dependencies{ObserveListener: listener}, wantErr: "listeners"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.value.validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
