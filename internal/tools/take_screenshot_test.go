package tools

import "testing"

func TestNormalizeScreenshotDisplayIndex(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		params   TakeScreenshotParams
		want     int
		wantErr  bool
	}{
		{
			name:   "uses display alias",
			params: TakeScreenshotParams{Display: 2},
			want:   2,
		},
		{
			name:   "uses monitor alias",
			params: TakeScreenshotParams{Monitor: 3},
			want:   3,
		},
		{
			name:   "uses monitor_index alias",
			params: TakeScreenshotParams{MonitorIndex: 4},
			want:   4,
		},
		{
			name:   "keeps display_index when alias matches",
			params: TakeScreenshotParams{DisplayIndex: 2, Monitor: 2},
			want:   2,
		},
		{
			name:    "rejects conflicting aliases",
			params:  TakeScreenshotParams{DisplayIndex: 1, Monitor: 2},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := tc.params
			err := normalizeScreenshotDisplayIndex(&p)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p.DisplayIndex != tc.want {
				t.Fatalf("DisplayIndex=%d, want %d", p.DisplayIndex, tc.want)
			}
		})
	}
}

