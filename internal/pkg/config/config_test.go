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
