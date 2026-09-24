package client

import (
	"context"
	"errors"
	"testing"
)

func TestToolError(t *testing.T) {
	tests := []struct {
		text string
		want error
	}{
		{"This Telegram user is not paired with an Obsidian agent", ErrNotPaired},
		{"No Obsidian agent connected", ErrAgentOffline},
		{"Obsidian agent disconnected before responding", ErrAgentOffline},
		{"timed out waiting for Obsidian agent response", ErrAgentTimeout},
		{"no folder selected: choose one with list_folders and select_folder", ErrNoFolderSelected},
		{"unknown folder_id: use list_folders to see the available folders", ErrUnknownFolder},
		{"invalid pairing code", ErrInvalidPairingCode},
		{"pairing code has expired", ErrExpiredPairingCode},
		{"pairing code has already been used", ErrUsedPairingCode},
	}
	for _, tt := range tests {
		if got := toolError("save_file", tt.text); !errors.Is(got, tt.want) {
			t.Errorf("toolError(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}

	got := toolError("save_file", "vault is read-only")
	for _, known := range []error{ErrNotPaired, ErrAgentOffline, ErrAgentTimeout, ErrNoFolderSelected, ErrUnknownFolder, ErrInvalidPairingCode, ErrExpiredPairingCode, ErrUsedPairingCode} {
		if errors.Is(got, known) {
			t.Errorf("unknown error mapped to %v", known)
		}
	}
}

func TestPairAgent(t *testing.T) {
	f := &fakeServer{}
	c := newTestClient(t, f)

	agentID, err := c.PairAgent(context.Background(), 42, "bucu-eu")
	if err != nil || agentID != "agent-1" {
		t.Fatalf("PairAgent = %q, %v", agentID, err)
	}
	// The code is sent as typed; the server normalizes it.
	if f.pairArgs != (pairInput{TelegramUserID: 42, PairingCode: "bucu-eu"}) {
		t.Errorf("pair_agent args = %+v", f.pairArgs)
	}
}

func TestPairAgentErrors(t *testing.T) {
	tests := map[string]error{
		"invalid pairing code":               ErrInvalidPairingCode,
		"pairing code has expired":           ErrExpiredPairingCode,
		"pairing code has already been used": ErrUsedPairingCode,
	}
	for text, want := range tests {
		c := newTestClient(t, &fakeServer{pairErr: errors.New(text)})
		if _, err := c.PairAgent(context.Background(), 42, "AAAA-AA"); !errors.Is(err, want) {
			t.Errorf("%q: err = %v, want %v", text, err, want)
		}
	}
}

func TestIsPaired(t *testing.T) {
	tests := []struct {
		listErr error
		paired  bool
		wantErr bool
	}{
		{nil, true, false},
		{errors.New("No Obsidian agent connected"), true, false},
		{errors.New("timed out waiting for Obsidian agent response"), true, false},
		{errors.New("This Telegram user is not paired with an Obsidian agent"), false, false},
		{errors.New("vault is read-only"), false, true},
	}
	for _, tt := range tests {
		c := newTestClient(t, &fakeServer{listErr: tt.listErr})
		paired, err := c.IsPaired(context.Background(), 42)
		if paired != tt.paired || (err != nil) != tt.wantErr {
			t.Errorf("listErr %v: IsPaired = %v, %v", tt.listErr, paired, err)
		}
	}
}
