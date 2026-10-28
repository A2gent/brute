package dbtool

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// DiagnosticStep is one stage of a connection check (parse, dns, tcp, tls, login).
type DiagnosticStep struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | fail | warn | skip
	Detail string `json:"detail,omitempty"`
	Hint   string `json:"hint,omitempty"`
}

const (
	stepOK   = "ok"
	stepFail = "fail"
	stepWarn = "warn"
	stepSkip = "skip"

	diagnoseStepTimeout = 8 * time.Second
)

// Diagnose checks a connection stage by stage so the user can tell a firewall
// timeout from a TLS or credentials problem. It stops at the first failure and
// marks the remaining stages as skipped.
func Diagnose(ctx context.Context, cfg Config) []DiagnosticStep {
	engine := strings.ToLower(strings.TrimSpace(cfg.Engine))
	cfg.Engine = engine
	var steps []DiagnosticStep

	if engine == "sqlite" {
		steps = append(steps, diagnoseSQLiteFile(cfg.DSN))
		if steps[0].Status == stepFail {
			return steps
		}
		return append(steps, diagnoseLogin(ctx, cfg))
	}

	target, sslMode, parseStep := parseDiagnoseTarget(engine, cfg.DSN)
	steps = append(steps, parseStep)
	if parseStep.Status == stepFail {
		return skipRemaining(steps, "DNS", "TCP", "TLS", "Login")
	}

	host, port, _ := net.SplitHostPort(target)
	dnsStep, ips := diagnoseDNS(ctx, host)
	steps = append(steps, dnsStep)
	if dnsStep.Status == stepFail {
		return skipRemaining(steps, "TCP", "TLS", "Login")
	}

	tcpStep := diagnoseTCP(ctx, net.JoinHostPort(host, port), ips)
	steps = append(steps, tcpStep)
	if tcpStep.Status == stepFail {
		return skipRemaining(steps, "TLS", "Login")
	}

	steps = append(steps, diagnoseTLSSupport(ctx, engine, target, sslMode))
	return append(steps, diagnoseLogin(ctx, cfg))
}

func skipRemaining(steps []DiagnosticStep, names ...string) []DiagnosticStep {
	for _, name := range names {
		steps = append(steps, DiagnosticStep{Name: name, Status: stepSkip})
	}
	return steps
}

// parseDiagnoseTarget extracts host:port and the requested SSL mode from the DSN.
func parseDiagnoseTarget(engine, dsn string) (string, string, DiagnosticStep) {
	step := DiagnosticStep{Name: "DSN", Status: stepOK}
	fail := func(err error, hint string) (string, string, DiagnosticStep) {
		step.Status, step.Detail, step.Hint = stepFail, err.Error(), hint
		return "", "", step
	}
	if strings.TrimSpace(dsn) == "" {
		return fail(errors.New("DSN is empty"), "")
	}

	switch engine {
	case "postgres", "redis", "elasticsearch":
		if engine == "elasticsearch" && !strings.Contains(dsn, "://") {
			// A bare host:port is a valid Elasticsearch DSN; normalise it before parsing
			// ("localhost:9200" would otherwise parse as scheme "localhost").
			dsn = "http://" + strings.TrimSpace(dsn)
		}
		u, err := url.Parse(dsn)
		if err != nil || u.Host == "" {
			if err == nil {
				err = errors.New("host is missing")
			}
			return fail(err, "Expected scheme://user:password@host:port/dbname. Special characters in the password must be percent-encoded.")
		}
		port := u.Port()
		if port == "" {
			port = map[string]string{"postgres": "5432", "redis": "6379", "elasticsearch": "9200"}[engine]
		}
		mode := u.Query().Get("sslmode")
		if engine == "redis" && u.Scheme == "rediss" {
			mode = "require"
		}
		if engine == "elasticsearch" && u.Scheme == "https" {
			mode = "require"
		}
		step.Detail = fmt.Sprintf("host=%s port=%s", u.Hostname(), port)
		if engine == "postgres" {
			if mode == "" {
				// lib/pq defaults to sslmode=require when none is given.
				step.Detail += " sslmode=require (default)"
			} else {
				step.Detail += " sslmode=" + mode
			}
		}
		return net.JoinHostPort(u.Hostname(), port), mode, step
	case "mysql":
		c, err := mysql.ParseDSN(dsn)
		if err != nil {
			return fail(err, "Expected user:password@tcp(host:3306)/dbname")
		}
		if c.Net != "tcp" {
			return fail(fmt.Errorf("unsupported network %q", c.Net), "Use the tcp(host:port) form.")
		}
		host, port, err := net.SplitHostPort(c.Addr)
		if err != nil {
			host, port = c.Addr, "3306"
		}
		step.Detail = fmt.Sprintf("host=%s port=%s", host, port)
		return net.JoinHostPort(host, port), c.TLSConfig, step
	}
	return fail(fmt.Errorf("unsupported database engine: %s", engine), "")
}

func diagnoseSQLiteFile(path string) DiagnosticStep {
	step := DiagnosticStep{Name: "File", Status: stepOK, Detail: path}
	if _, err := os.Stat(path); err != nil {
		step.Status, step.Detail = stepFail, err.Error()
		step.Hint = "The path is resolved on the machine where Brute runs."
	}
	return step
}

func diagnoseDNS(ctx context.Context, host string) (DiagnosticStep, []string) {
	step := DiagnosticStep{Name: "DNS", Status: stepOK}
	if ip := net.ParseIP(host); ip != nil {
		step.Detail = "IP address, no lookup needed"
		return step, []string{host}
	}
	cctx, cancel := context.WithTimeout(ctx, diagnoseStepTimeout)
	defer cancel()
	ips, err := net.DefaultResolver.LookupHost(cctx, host)
	if err != nil {
		step.Status, step.Detail = stepFail, err.Error()
		step.Hint = "Check the host name for typos and that this machine has working DNS/VPN."
		return step, nil
	}
	step.Detail = strings.Join(ips, ", ")
	return step, ips
}

func diagnoseTCP(ctx context.Context, addr string, _ []string) DiagnosticStep {
	step := DiagnosticStep{Name: "TCP", Status: stepOK}
	start := time.Now()
	conn, err := (&net.Dialer{Timeout: diagnoseStepTimeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		step.Status, step.Detail = stepFail, err.Error()
		var nerr net.Error
		if errors.As(err, &nerr) && nerr.Timeout() {
			step.Hint = "Timed out: the port is filtered. Typical cause is a firewall / allow-list (e.g. DigitalOcean Trusted Sources, AWS security group) that does not include this machine's public IP, or a VPN/private-network-only host."
		} else {
			step.Hint = "Connection refused or unreachable: check the port and that the server is running."
		}
		return step
	}
	conn.Close()
	step.Detail = fmt.Sprintf("%s reachable in %d ms", addr, time.Since(start).Milliseconds())
	return step
}

// diagnoseTLSSupport asks a PostgreSQL server whether it accepts SSL (SSLRequest
// handshake). Other engines have no cheap probe, so the stage is reported as skipped.
func diagnoseTLSSupport(ctx context.Context, engine, addr, sslMode string) DiagnosticStep {
	step := DiagnosticStep{Name: "TLS", Status: stepSkip}
	if engine != "postgres" {
		return step
	}
	conn, err := (&net.Dialer{Timeout: diagnoseStepTimeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		step.Status, step.Detail = stepFail, err.Error()
		return step
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(diagnoseStepTimeout))
	// SSLRequest: int32 length (8) + int32 code 80877103.
	if _, err := conn.Write([]byte{0, 0, 0, 8, 4, 210, 22, 47}); err != nil {
		step.Status, step.Detail = stepFail, err.Error()
		return step
	}
	reply := make([]byte, 1)
	if _, err := conn.Read(reply); err != nil {
		step.Status, step.Detail = stepFail, err.Error()
		return step
	}
	switch reply[0] {
	case 'S':
		step.Status, step.Detail = stepOK, "server supports SSL"
		if sslMode == "disable" {
			step.Status = stepWarn
			step.Hint = "Server supports SSL but sslmode=disable is set; managed providers usually reject unencrypted connections."
		}
	case 'N':
		step.Status, step.Detail = stepWarn, "server does not support SSL"
		if sslMode == "" || sslMode == "require" || sslMode == "verify-full" || sslMode == "verify-ca" {
			step.Status = stepFail
			step.Hint = "Server has no SSL but the DSN requires it. Set sslmode=disable."
		}
	default:
		step.Status, step.Detail = stepWarn, fmt.Sprintf("unexpected reply %q", reply[0])
	}
	return step
}

func diagnoseLogin(ctx context.Context, cfg Config) DiagnosticStep {
	step := DiagnosticStep{Name: "Login", Status: stepOK}
	cctx, cancel := context.WithTimeout(ctx, 2*diagnoseStepTimeout)
	defer cancel()

	var err error
	if cfg.Engine == "redis" {
		var client *redisClient
		client, err = connectRedis(cctx, cfg.DSN)
		if err == nil {
			client.close()
		}
	} else if cfg.Engine == "elasticsearch" {
		var detail string
		_, detail, err = connectElasticsearch(cctx, cfg.DSN)
		if err == nil {
			// WHY: keep the caveat ("no cluster monitor permission") instead of a flat
			// "authenticated", so the user knows listing is what will actually work.
			step.Detail = detail
			return step
		}
	} else {
		var db *sql.DB
		db, err = connectContext(cctx, cfg)
		if err == nil {
			db.Close()
		}
	}
	if err != nil {
		step.Status, step.Detail = stepFail, err.Error()
		step.Hint = loginHint(err.Error())
		return step
	}
	step.Detail = "authenticated"
	return step
}

func loginHint(msg string) string {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "password authentication failed"), strings.Contains(lower, "access denied"), strings.Contains(lower, "wrongpass"), strings.Contains(lower, "unauthorized"):
		return "Wrong user or password."
	case strings.Contains(lower, "does not exist"):
		return "The database or user does not exist; check the database name."
	case strings.Contains(lower, "certificate"), strings.Contains(lower, "x509"), strings.Contains(lower, "self signed"), strings.Contains(lower, "self-signed"):
		// WHY: Elasticsearch clusters commonly use self-signed certs; for them the fix is
		// ?insecure=true, not a Postgres sslmode. Mention both so either engine is helped.
		return "SSL problem: verify the CA/certificate. Postgres: try sslmode=require, or sslmode=disable if the server has no SSL. Elasticsearch: add ?insecure=true for a self-signed cluster."
	case strings.Contains(lower, "ssl"), strings.Contains(lower, "tls"):
		return "SSL problem: try sslmode=require (encrypted, no CA check) or sslmode=disable if the server has no SSL. verify-full needs a trusted CA. Elasticsearch: use https:// and ?insecure=true for a self-signed cluster."
	case strings.Contains(lower, "pg_hba.conf"):
		return "The server rejected this client address or SSL setting (pg_hba.conf)."
	}
	return ""
}
