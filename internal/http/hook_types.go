// hook_types.go holds API fields for event-triggered loops ("hooks"). They are
// embedded into the job request/response types so loops and hooks share one API.
package http

// JobTriggerFields is embedded in CreateJobRequest and JobResponse.
type JobTriggerFields struct {
	TriggerType      string `json:"trigger_type,omitempty"` // "schedule" (default) | "webhook"
	TriggerSource    string `json:"trigger_source,omitempty"`
	TriggerEvents    string `json:"trigger_events,omitempty"`
	TriggerBranches  string `json:"trigger_branches,omitempty"`
	TriggerCondition string `json:"trigger_condition,omitempty"`
}

// JobTriggerUpdateFields is embedded in UpdateJobRequest; nil leaves a field unchanged.
type JobTriggerUpdateFields struct {
	TriggerType      *string `json:"trigger_type,omitempty"`
	TriggerSource    *string `json:"trigger_source,omitempty"`
	TriggerEvents    *string `json:"trigger_events,omitempty"`
	TriggerBranches  *string `json:"trigger_branches,omitempty"`
	TriggerCondition *string `json:"trigger_condition,omitempty"`
}

// JobWebhookFields exposes the Square connection of a webhook-triggered loop.
type JobWebhookFields struct {
	WebhookConnected     bool   `json:"webhook_connected"`
	WebhookURL           string `json:"webhook_url,omitempty"`
	WebhookSigningSecret string `json:"webhook_signing_secret,omitempty"`
}
