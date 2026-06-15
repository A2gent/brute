package dbtool

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// WHY: Redis command validation and dispatch are independent from connection
// handling and result formatting, so they live in this focused file.
func executeRedisQuery(ctx context.Context, cfg Config, query string, limit, offset int, format string) (string, error) {
	args, err := parseRedisCommand(query)
	if err != nil {
		return "", err
	}
	if len(args) == 0 {
		return "", fmt.Errorf("redis command is required")
	}
	if cfg.IsReadOnly {
		if err := ensureRedisReadOnlyCommand(args[0]); err != nil {
			return "", err
		}
	}
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	client, err := connectRedis(ctx, cfg.DSN)
	if err != nil {
		return "", fmt.Errorf("failed to connect: %w", err)
	}
	defer client.close()

	command := strings.ToUpper(args[0])
	if command == "GET" && len(args) == 2 {
		rows, err := previewRedisKey(ctx, client, args[1], limit, offset)
		if err != nil {
			return "", err
		}
		return formatRedisRows(rows, format)
	}

	resp, err := client.command(ctx, args...)
	if err != nil {
		return "", err
	}
	return formatRedisRows(redisResponseRows(command, resp, limit, offset), format)
}

func scanRedisKeys(ctx context.Context, client *redisClient, pattern string, maxKeys int) ([]string, error) {
	if maxKeys <= 0 {
		maxKeys = redisMaxExplorerKeys
	}
	cursor := "0"
	keys := make([]string, 0)
	for {
		resp, err := client.command(ctx, "SCAN", cursor, "MATCH", pattern, "COUNT", strconv.Itoa(redisScanCount))
		if err != nil {
			return nil, err
		}
		nextCursor, batch, err := parseRedisScanResponse(resp)
		if err != nil {
			return nil, err
		}
		keys = append(keys, batch...)
		if len(keys) >= maxKeys {
			keys = keys[:maxKeys]
			break
		}
		cursor = nextCursor
		if cursor == "0" {
			break
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func parseRedisScanResponse(resp interface{}) (string, []string, error) {
	values, ok := resp.([]interface{})
	if !ok || len(values) != 2 {
		return "", nil, fmt.Errorf("unexpected SCAN response: %#v", resp)
	}
	cursor, ok := values[0].(string)
	if !ok {
		return "", nil, fmt.Errorf("unexpected SCAN cursor: %#v", values[0])
	}
	keyValues, ok := values[1].([]interface{})
	if !ok {
		return "", nil, fmt.Errorf("unexpected SCAN key list: %#v", values[1])
	}
	keys := make([]string, 0, len(keyValues))
	for _, keyValue := range keyValues {
		key, ok := keyValue.(string)
		if ok {
			keys = append(keys, key)
		}
	}
	return cursor, keys, nil
}

func parseRedisCommand(query string) ([]string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("redis command is required")
	}

	args := make([]string, 0)
	var current strings.Builder
	var quote rune
	escaped := false
	inToken := false
	for _, r := range query {
		if escaped {
			current.WriteRune(r)
			escaped = false
			inToken = true
			continue
		}
		if r == '\\' {
			escaped = true
			inToken = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			current.WriteRune(r)
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			inToken = true
			continue
		}
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if inToken {
				args = append(args, current.String())
				current.Reset()
				inToken = false
			}
			continue
		}
		current.WriteRune(r)
		inToken = true
	}
	if escaped {
		current.WriteRune('\\')
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quoted redis argument")
	}
	if inToken {
		args = append(args, current.String())
	}
	return args, nil
}

var redisReadOnlyCommands = map[string]struct{}{
	"DBSIZE":    {},
	"EXISTS":    {},
	"GET":       {},
	"HGET":      {},
	"HGETALL":   {},
	"HLEN":      {},
	"KEYS":      {},
	"LLEN":      {},
	"LRANGE":    {},
	"MGET":      {},
	"PTTL":      {},
	"SCARD":     {},
	"SCAN":      {},
	"SCOMMAND":  {},
	"SMEMBERS":  {},
	"SSCAN":     {},
	"STRLEN":    {},
	"TTL":       {},
	"TYPE":      {},
	"XINFO":     {},
	"XLEN":      {},
	"XRANGE":    {},
	"XREVRANGE": {},
	"ZCARD":     {},
	"ZRANGE":    {},
	"ZREVRANGE": {},
}

func ensureRedisReadOnlyCommand(command string) error {
	command = strings.ToUpper(strings.TrimSpace(command))
	if _, ok := redisReadOnlyCommands[command]; ok {
		return nil
	}
	return fmt.Errorf("redis command %q is not allowed on a read-only connection", command)
}
