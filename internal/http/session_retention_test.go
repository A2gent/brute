package http

import (
	"testing"
	"time"
)

func TestRetentionPolicyFromSettings(t *testing.T) {
	p := retentionPolicyFromSettings(map[string]string{})
	if p.MaxBytes != 1<<30 || p.MaxAge != 0 || p.MaxFiles != 0 {
		t.Fatalf("defaults wrong: %+v", p)
	}
	p = retentionPolicyFromSettings(map[string]string{
		sessionsRetentionMaxMBSettingKey:    "0",
		sessionsRetentionMaxDaysSettingKey:  "30",
		sessionsRetentionMaxFilesSettingKey: "500",
	})
	if p.MaxBytes != 0 || p.MaxAge != 30*24*time.Hour || p.MaxFiles != 500 {
		t.Fatalf("custom wrong: %+v", p)
	}
}
