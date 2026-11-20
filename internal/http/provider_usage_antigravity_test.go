package http

import (
	"testing"
	"time"

	"github.com/A2gent/brute/internal/llm/antigravitycli"
)

func TestAntigravityUsageBarsMapsWeeklyAndFiveHourBuckets(t *testing.T) {
	resetAt := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	summary := antigravitycli.QuotaSummary{Groups: []antigravitycli.QuotaGroup{{
		DisplayName: "Claude and GPT models",
		Buckets: []antigravitycli.QuotaBucket{
			{Window: "weekly", RemainingFraction: 0.25, ResetTime: resetAt.Format(time.RFC3339)},
			{Window: "5h", RemainingFraction: 0, ResetTime: resetAt.Format(time.RFC3339)},
			{Window: "monthly", RemainingFraction: 0.5},
		},
	}}}

	bars := antigravityUsageBars(summary)
	if len(bars) != 2 {
		t.Fatalf("bars = %+v, want 2 (unknown windows skipped)", bars)
	}
	weekly := bars[0]
	if weekly.Label != "Claude and GPT models weekly" || weekly.LeftPercent != 25 || weekly.UsedPercent != 75 {
		t.Fatalf("weekly bar = %+v", weekly)
	}
	if !weekly.LongTerm || weekly.WindowSeconds != int64((7*24*time.Hour)/time.Second) {
		t.Fatalf("weekly bar must be long-term with 7d window: %+v", weekly)
	}
	if weekly.ResetsAt != resetAt.Format(time.RFC3339) {
		t.Fatalf("resets_at = %q, want %q", weekly.ResetsAt, resetAt.Format(time.RFC3339))
	}
	fiveHour := bars[1]
	if fiveHour.LongTerm || fiveHour.Status != "limit_reached" || fiveHour.LeftPercent != 0 {
		t.Fatalf("5h bar = %+v", fiveHour)
	}
}

func TestAntigravityUsageBarsFeedWeeklyReserveCheck(t *testing.T) {
	summary := antigravitycli.QuotaSummary{Groups: []antigravitycli.QuotaGroup{{
		DisplayName: "Claude and GPT models",
		Buckets:     []antigravitycli.QuotaBucket{{Window: "weekly", RemainingFraction: 0.05, ResetTime: "2099-01-01T00:00:00Z"}},
	}}}
	usage := ProviderUsageResponse{Status: providerUsageStatusAvailable, UsageBars: antigravityUsageBars(summary)}

	below, detail := providerUsageBelowMinBalance(usage, 10, time.Now())
	if !below || detail == "" {
		t.Fatalf("expected weekly bar below 10%% reserve, got below=%t detail=%q", below, detail)
	}
}
