package config

import (
	"reflect"
	"testing"
)

func TestNormalizeCORSAllowedOrigins(t *testing.T) {
	t.Parallel()

	got := normalizeCORSAllowedOrigins(" https://app.example.com , https://app.example.com, http://localhost:5173 ,, ")
	want := []string{"https://app.example.com", "http://localhost:5173"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeCORSAllowedOrigins mismatch\nwant: %#v\ngot:  %#v", want, got)
	}
}

func TestNormalizeCORSAllowedOriginsOrDefault(t *testing.T) {
	t.Parallel()

	if got := normalizeCORSAllowedOriginsOrDefault(nil); !reflect.DeepEqual(got, []string{"*"}) {
		t.Fatalf("expected wildcard fallback for nil list, got %#v", got)
	}
	if got := normalizeCORSAllowedOriginsOrDefault([]string{" ", ""}); !reflect.DeepEqual(got, []string{"*"}) {
		t.Fatalf("expected wildcard fallback for empty list, got %#v", got)
	}
	if got := normalizeCORSAllowedOriginsOrDefault([]string{"https://blog.example.com"}); !reflect.DeepEqual(got, []string{"https://blog.example.com"}) {
		t.Fatalf("expected explicit origin, got %#v", got)
	}
}

func TestEffectiveCORSAllowedOrigins(t *testing.T) {
	t.Parallel()

	cfg := &Config{CORSAllowedOrigins: []string{"https://blog.example.com", "https://blog.example.com"}}
	got := cfg.EffectiveCORSAllowedOrigins()
	want := []string{"https://blog.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("effective origins mismatch\nwant: %#v\ngot:  %#v", want, got)
	}

	var nilCfg *Config
	if got := nilCfg.EffectiveCORSAllowedOrigins(); !reflect.DeepEqual(got, []string{"*"}) {
		t.Fatalf("nil config should fallback to wildcard, got %#v", got)
	}
}
