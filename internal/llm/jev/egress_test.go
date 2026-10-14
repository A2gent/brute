package jev

import "testing"

func TestSafeEgressTextRejectsDSNCredentials(t *testing.T) {
	for _, text := range []string{
		"connect to postgres://admin:hunter2@db.example.com/app",
		"MONGO=mongodb+srv://u:p@cluster.example.com",
	} {
		if SafeEgressText(text, nil) {
			t.Errorf("credential DSN allowed: %q", text)
		}
	}
	if !SafeEgressText("open example.com and summarize postgres docs", nil) {
		t.Error("plain prompt rejected")
	}
}
