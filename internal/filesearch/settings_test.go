package filesearch

import "testing"

func TestIsIndexingEnabledForProjectPrefersProjectSetting(t *testing.T) {
	global := map[string]string{IndexingEnabledSettingKey: "false"}
	project := map[string]string{ProjectIndexingEnabledSettingKey: "true"}
	if !IsIndexingEnabledForProject(project, global) {
		t.Fatal("expected project setting to enable indexing")
	}
}

func TestIsIndexingEnabledForProjectFallsBackToGlobal(t *testing.T) {
	global := map[string]string{IndexingEnabledSettingKey: "true"}
	if !IsIndexingEnabledForProject(map[string]string{}, global) {
		t.Fatal("expected global setting fallback")
	}
}

func TestIsIndexingEnabledForProjectDefaultsToDisabled(t *testing.T) {
	SetIndexingEnabled(false)
	t.Cleanup(func() { SetIndexingEnabled(false) })
	if IsIndexingEnabledForProject(map[string]string{}, map[string]string{}) {
		t.Fatal("expected indexing to stay disabled by default")
	}
}
