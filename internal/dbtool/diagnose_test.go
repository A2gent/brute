package dbtool

import (
	"context"
	"net"
	"testing"
)

func TestDiagnoseBadDSN(t *testing.T) {
	steps := Diagnose(context.Background(), Config{Engine: "postgres", DSN: "not a url"})
	if steps[0].Status != stepFail {
		t.Fatalf("expected DSN failure, got %+v", steps[0])
	}
	if len(steps) != 5 || steps[4].Status != stepSkip {
		t.Fatalf("remaining stages should be skipped: %+v", steps)
	}
}

func TestDiagnosePostgresNoSSLServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 8)
			_, _ = c.Read(buf)
			_, _ = c.Write([]byte("N"))
			c.Close()
		}
	}()
	dsn := "postgres://u:p@" + ln.Addr().String() + "/db"
	steps := Diagnose(context.Background(), Config{Engine: "postgres", DSN: dsn})
	byName := map[string]DiagnosticStep{}
	for _, s := range steps {
		byName[s.Name] = s
	}
	if byName["TCP"].Status != stepOK {
		t.Fatalf("tcp: %+v", byName["TCP"])
	}
	if byName["TLS"].Status != stepFail || byName["TLS"].Hint == "" {
		t.Fatalf("tls should fail with hint: %+v", byName["TLS"])
	}
}
