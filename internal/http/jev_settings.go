package http

import (
	"os"
	"strconv"
	"strings"
)

const (
	jevAdmissionSettingKey   = "A2GENT_JEV_ADMISSION_ENABLED"
	jevToolsetsSettingKey    = "A2GENT_JEV_TOOLSETS_ENABLED"
	jevScreenshotsSettingKey = "A2GENT_JEV_SCREENSHOTS_ENABLED"
)

// App settings override legacy config flags. Environment overrides remain an
// operator escape hatch; absent keys preserve explicit config.json opt-outs.
func (s *Server) jevAutomationEnabled(key string, fallback bool) bool {
	if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
		return !strings.EqualFold(raw, "false")
	}
	if s != nil && s.store != nil {
		if settings, err := s.store.GetSettings(); err == nil {
			if raw := strings.TrimSpace(settings[key]); raw != "" {
				return !strings.EqualFold(raw, "false")
			}
		}
	}
	return fallback
}

func (s *Server) settingsResponseWithJev(settings, customEnv map[string]string) SettingsResponse {
	response := settingsResponse(settings, customEnv)
	if s != nil && s.config != nil {
		response.Settings[jevAdmissionSettingKey] = strconv.FormatBool(s.jevAutomationEnabled(jevAdmissionSettingKey, s.config.Tools.JevAdmission.Enabled))
		response.Settings[jevToolsetsSettingKey] = strconv.FormatBool(s.jevAutomationEnabled(jevToolsetsSettingKey, s.config.Tools.JevToolsets.Enabled))
		response.Settings[jevScreenshotsSettingKey] = strconv.FormatBool(s.jevAutomationEnabled(jevScreenshotsSettingKey, s.config.Tools.JevScreenshots.Enabled))
	}
	return response
}
