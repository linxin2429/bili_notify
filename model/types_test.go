package model

import "testing"

func TestMicrosoftChannelValidationAndMasking(t *testing.T) {
	channel := Channel{
		Name: "outlook", Type: ChannelMicrosoft,
		Settings: map[string]string{
			"client_id": "11111111-2222-3333-4444-555555555555",
			"tenant":    "common", "to": "one@example.com,Two <two@example.com>",
			"access_token": "access", "refresh_token": "refresh",
		},
	}
	if err := channel.Validate(); err != nil {
		t.Fatal(err)
	}
	masked := channel.Masked()
	if masked.Settings["access_token"] != "********" || masked.Settings["refresh_token"] != "********" {
		t.Fatalf("tokens were not masked: %#v", masked.Settings)
	}
	if masked.Settings["client_id"] != channel.Settings["client_id"] {
		t.Fatal("client ID should remain visible")
	}
}

func TestMicrosoftChannelRejectsInvalidTenant(t *testing.T) {
	channel := Channel{
		Name: "outlook", Type: ChannelMicrosoft,
		Settings: map[string]string{
			"client_id": "11111111-2222-3333-4444-555555555555",
			"tenant":    "../token", "to": "one@example.com",
		},
	}
	if err := channel.Validate(); err == nil {
		t.Fatal("invalid tenant was accepted")
	}
}

func TestMicrosoftChannelMustBeAuthorizedBeforeEnable(t *testing.T) {
	channel := Channel{
		Name: "outlook", Type: ChannelMicrosoft, Enabled: true,
		Settings: map[string]string{
			"client_id": "11111111-2222-3333-4444-555555555555",
			"tenant":    "common", "to": "one@example.com",
		},
	}
	if err := channel.Validate(); err == nil {
		t.Fatal("unauthorized Microsoft channel was enabled")
	}
}
