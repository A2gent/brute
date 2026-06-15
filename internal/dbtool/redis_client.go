package dbtool

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// WHY: Redis support contains its own wire protocol client; keeping it separate
// from SQL entry points makes the database tool package easier to navigate.
const (
	redisDefaultAddress = "127.0.0.1:6379"
	redisCommandTimeout = 5 * time.Second
	// WHY: The existing explorer endpoint returns a single list without pagination.
	// Cap Redis SCAN results to keep projects with huge keyspaces responsive.
	redisMaxExplorerKeys = 1000
	redisScanCount       = 250
)

type redisClient struct {
	conn   net.Conn
	reader *bufio.Reader
	writer *bufio.Writer
}

type redisConnConfig struct {
	address  string
	username string
	password string
	database int
	useTLS   bool
}

type redisError string

func (e redisError) Error() string { return string(e) }

func connectRedis(ctx context.Context, dsn string) (*redisClient, error) {
	cfg, err := parseRedisDSN(dsn)
	if err != nil {
		return nil, err
	}

	dialer := &net.Dialer{Timeout: redisCommandTimeout}
	var conn net.Conn
	if cfg.useTLS {
		tlsDialer := tls.Dialer{NetDialer: dialer, Config: &tls.Config{MinVersion: tls.VersionTLS12}}
		conn, err = tlsDialer.DialContext(ctx, "tcp", cfg.address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", cfg.address)
	}
	if err != nil {
		return nil, err
	}

	client := &redisClient{
		conn:   conn,
		reader: bufio.NewReader(conn),
		writer: bufio.NewWriter(conn),
	}

	if cfg.password != "" {
		if cfg.username != "" {
			_, err = client.command(ctx, "AUTH", cfg.username, cfg.password)
		} else {
			_, err = client.command(ctx, "AUTH", cfg.password)
		}
		if err != nil {
			client.close()
			return nil, err
		}
	}

	if cfg.database > 0 {
		if _, err := client.command(ctx, "SELECT", strconv.Itoa(cfg.database)); err != nil {
			client.close()
			return nil, err
		}
	}

	if _, err := client.command(ctx, "PING"); err != nil {
		client.close()
		return nil, err
	}

	return client, nil
}

func parseRedisDSN(dsn string) (redisConnConfig, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return redisConnConfig{}, fmt.Errorf("redis DSN is required")
	}
	if !strings.Contains(dsn, "://") {
		return redisConnConfig{address: withRedisDefaultPort(dsn)}, nil
	}

	parsed, err := url.Parse(dsn)
	if err != nil {
		return redisConnConfig{}, err
	}
	if parsed.Scheme != "redis" && parsed.Scheme != "rediss" {
		return redisConnConfig{}, fmt.Errorf("unsupported redis DSN scheme: %s", parsed.Scheme)
	}

	cfg := redisConnConfig{
		address: withRedisDefaultPort(parsed.Host),
		useTLS:  parsed.Scheme == "rediss",
	}
	if cfg.address == "" {
		cfg.address = redisDefaultAddress
	}
	if parsed.User != nil {
		cfg.username = parsed.User.Username()
		cfg.password, _ = parsed.User.Password()
		// redis://:password@host encodes an empty username. If only one token was
		// provided as redis://password@host, treat it as the password for compatibility.
		if cfg.password == "" && cfg.username != "" {
			cfg.password = cfg.username
			cfg.username = ""
		}
	}
	if path := strings.Trim(parsed.Path, "/"); path != "" {
		database, err := strconv.Atoi(path)
		if err != nil || database < 0 {
			return redisConnConfig{}, fmt.Errorf("invalid redis database index: %s", path)
		}
		cfg.database = database
	}
	return cfg, nil
}

func withRedisDefaultPort(address string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return redisDefaultAddress
	}
	if _, _, err := net.SplitHostPort(address); err == nil {
		return address
	}
	return net.JoinHostPort(address, "6379")
}

func (c *redisClient) close() {
	_ = c.conn.Close()
}

func (c *redisClient) command(ctx context.Context, args ...string) (interface{}, error) {
	deadline := time.Now().Add(redisCommandTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok {
		deadline = ctxDeadline
	}
	_ = c.conn.SetDeadline(deadline)

	if _, err := fmt.Fprintf(c.writer, "*%d\r\n", len(args)); err != nil {
		return nil, err
	}
	for _, arg := range args {
		if _, err := fmt.Fprintf(c.writer, "$%d\r\n%s\r\n", len([]byte(arg)), arg); err != nil {
			return nil, err
		}
	}
	if err := c.writer.Flush(); err != nil {
		return nil, err
	}
	resp, err := readRedisRESP(c.reader)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func readRedisRESP(reader *bufio.Reader) (interface{}, error) {
	prefix, err := reader.ReadByte()
	if err != nil {
		return nil, err
	}
	switch prefix {
	case '+':
		return readRedisLine(reader)
	case '-':
		line, err := readRedisLine(reader)
		if err != nil {
			return nil, err
		}
		return nil, redisError(line)
	case ':':
		line, err := readRedisLine(reader)
		if err != nil {
			return nil, err
		}
		return strconv.ParseInt(line, 10, 64)
	case '$':
		line, err := readRedisLine(reader)
		if err != nil {
			return nil, err
		}
		length, err := strconv.Atoi(line)
		if err != nil {
			return nil, err
		}
		if length < 0 {
			return nil, nil
		}
		buf := make([]byte, length+2)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return nil, err
		}
		return string(buf[:length]), nil
	case '*':
		line, err := readRedisLine(reader)
		if err != nil {
			return nil, err
		}
		length, err := strconv.Atoi(line)
		if err != nil {
			return nil, err
		}
		if length < 0 {
			return nil, nil
		}
		values := make([]interface{}, 0, length)
		for i := 0; i < length; i++ {
			value, err := readRedisRESP(reader)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		return values, nil
	default:
		return nil, fmt.Errorf("unsupported redis response prefix %q", prefix)
	}
}

func readRedisLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}
