//go:build linux

package daemon

import (
	"testing"

	v1 "github.com/pm0/pm0/api/v1"
	"github.com/pm0/pm0/internal/config"
)

// The rotation knobs' three-state wire encoding (divergence 12): 0 =
// default, -1 = off, >0 = bytes. specToConfig must map all three onto the
// in-memory model (0 = off, >0 = bytes, default pre-filled by
// config.Default).
func TestSpecToConfigRotateKnobs(t *testing.T) {
	cases := []struct {
		name    string
		wireMax int64
		wireRet int32
		wantMax int64
		wantRet int
	}{
		{"unset = default", 0, 0, config.DefaultRotateMax, config.DefaultRotateRetain},
		{"explicit off", -1, 0, 0, config.DefaultRotateRetain},
		{"override max", 4096, 0, 4096, config.DefaultRotateRetain},
		{"override retain", 0, 5, config.DefaultRotateMax, 5},
		{"override both", 4096, 5, 4096, 5},
		{"retain 0 = default", 0, 0, config.DefaultRotateMax, config.DefaultRotateRetain},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := specToConfig(&v1.ProcessSpec{
				Name:              "x",
				Script:            "/bin/x",
				LogRotateMaxBytes: tc.wireMax,
				LogRotateRetain:   tc.wireRet,
			})
			if cfg.LogRotateMaxBytes != tc.wantMax {
				t.Errorf("LogRotateMaxBytes = %d, want %d", cfg.LogRotateMaxBytes, tc.wantMax)
			}
			if cfg.LogRotateRetain != tc.wantRet {
				t.Errorf("LogRotateRetain = %d, want %d", cfg.LogRotateRetain, tc.wantRet)
			}
		})
	}
}
