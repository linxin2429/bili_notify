package web

import "testing"

func TestMicrosoftIdentityChanged(t *testing.T) {
	tests := []struct {
		name    string
		current map[string]string
		update  map[string]string
		want    bool
	}{
		{
			name:    "blank tenant equals common",
			current: map[string]string{"client_id": "client", "tenant": ""},
			update:  map[string]string{"client_id": "client", "tenant": "common"},
		},
		{
			name:    "client changed",
			current: map[string]string{"client_id": "old", "tenant": "common"},
			update:  map[string]string{"client_id": "new", "tenant": "common"},
			want:    true,
		},
		{
			name:    "tenant changed",
			current: map[string]string{"client_id": "client", "tenant": "common"},
			update:  map[string]string{"client_id": "client", "tenant": "consumers"},
			want:    true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := microsoftIdentityChanged(test.current, test.update); got != test.want {
				t.Fatalf("microsoftIdentityChanged() = %v, want %v", got, test.want)
			}
		})
	}
}
