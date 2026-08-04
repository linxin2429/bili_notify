package vault

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRejectsInvalidKeyLength(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		key  []byte
	}{
		{name: "nil", key: nil},
		{name: "empty", key: []byte{}},
		{name: "too short", key: bytes.Repeat([]byte{1}, 16)},
		{name: "too long", key: bytes.Repeat([]byte{1}, 64)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := New(tt.key)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "32 bytes")
		})
	}
}

func TestRoundTripAndAdditionalData(t *testing.T) {
	t.Parallel()
	v, err := New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)

	sealed, err := v.Seal([]byte("secret"), []byte("record-a"))
	require.NoError(t, err)

	got, err := v.Open(sealed, []byte("record-a"))
	require.NoError(t, err)
	assert.Equal(t, "secret", string(got))

	_, err = v.Open(sealed, []byte("record-b"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication failed")
}

func TestOpenRejectsMalformedCiphertext(t *testing.T) {
	t.Parallel()
	v, err := New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)

	tests := []struct {
		name    string
		sealed  []byte
		wantMsg string
	}{
		{name: "empty", sealed: nil, wantMsg: "invalid encrypted value format"},
		{name: "too short for nonce", sealed: []byte{1, 2, 3}, wantMsg: "invalid encrypted value nonce"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := v.Open(tt.sealed, []byte("aad"))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantMsg)
		})
	}
}
