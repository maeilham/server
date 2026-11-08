package config

import (
	"testing"
	"time"
)

func TestLoad_SyncInterval(t *testing.T) {
	cases := []struct {
		env     string
		want    time.Duration
		wantErr bool
	}{
		{"", 0, false},
		{"1h", time.Hour, false},
		{"30m", 30 * time.Minute, false},
		{"abc", 0, true},
		{"-1h", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("MAEILHAM_SYNC_INTERVAL", tc.env)
			c, err := Load()
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && c.SyncInterval != tc.want {
				t.Fatalf("SyncInterval = %v, want %v", c.SyncInterval, tc.want)
			}
		})
	}
}

func TestLoad_NotifyAt(t *testing.T) {
	cases := []struct {
		env         string
		wantEnabled bool
		want        time.Duration
		wantErr     bool
	}{
		{"", false, 0, false},
		{"07:30", true, 7*time.Hour + 30*time.Minute, false},
		{"00:00", true, 0, false},
		{"7:30pm", false, 0, true},
		{"25:00", false, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("MAEILHAM_NOTIFY_AT", tc.env)
			c, err := Load()
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && (c.NotifyEnabled != tc.wantEnabled || c.NotifyAt != tc.want) {
				t.Fatalf("enabled=%v at=%v, want enabled=%v at=%v", c.NotifyEnabled, c.NotifyAt, tc.wantEnabled, tc.want)
			}
		})
	}
}
