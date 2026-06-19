package dbtool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

// WHY: Key previews need type-specific pagination rules; isolating them keeps
// Redis query dispatch small and the preview behavior easier to test.
func previewRedisKey(ctx context.Context, client *redisClient, key string, limit, offset int) ([]map[string]interface{}, error) {
	keyType, err := redisStringCommand(ctx, client, "TYPE", key)
	if err != nil {
		return nil, err
	}
	ttl, _ := redisIntCommand(ctx, client, "TTL", key)
	base := func() map[string]interface{} {
		return map[string]interface{}{
			"key":  key,
			"type": keyType,
			"ttl":  ttl,
		}
	}

	switch keyType {
	case "none":
		row := base()
		row["value"] = nil
		return []map[string]interface{}{row}, nil
	case "string":
		value, err := client.command(ctx, "GET", key)
		if err != nil {
			return nil, err
		}
		size, _ := redisIntCommand(ctx, client, "STRLEN", key)
		row := base()
		row["value"] = value
		row["size"] = size
		return []map[string]interface{}{row}, nil
	case "hash":
		resp, err := client.command(ctx, "HGETALL", key)
		if err != nil {
			return nil, err
		}
		items := redisStringSlice(resp)
		rows := make([]map[string]interface{}, 0, len(items)/2)
		start, end := boundedRange(len(items)/2, limit, offset)
		for pairIndex := start; pairIndex < end; pairIndex++ {
			row := base()
			row["field"] = items[pairIndex*2]
			if pairIndex*2+1 < len(items) {
				row["value"] = items[pairIndex*2+1]
			}
			rows = append(rows, row)
		}
		return rows, nil
	case "list":
		start := offset
		stop := offset + limit - 1
		resp, err := client.command(ctx, "LRANGE", key, strconv.Itoa(start), strconv.Itoa(stop))
		if err != nil {
			return nil, err
		}
		values := redisStringSlice(resp)
		rows := make([]map[string]interface{}, 0, len(values))
		for index, value := range values {
			row := base()
			row["index"] = offset + index
			row["value"] = value
			rows = append(rows, row)
		}
		return rows, nil
	case "set":
		resp, err := client.command(ctx, "SMEMBERS", key)
		if err != nil {
			return nil, err
		}
		values := redisStringSlice(resp)
		sort.Strings(values)
		start, end := boundedRange(len(values), limit, offset)
		rows := make([]map[string]interface{}, 0, end-start)
		for index, value := range values[start:end] {
			row := base()
			row["index"] = start + index
			row["value"] = value
			rows = append(rows, row)
		}
		return rows, nil
	case "zset":
		start := offset
		stop := offset + limit - 1
		resp, err := client.command(ctx, "ZRANGE", key, strconv.Itoa(start), strconv.Itoa(stop), "WITHSCORES")
		if err != nil {
			return nil, err
		}
		values := redisStringSlice(resp)
		rows := make([]map[string]interface{}, 0, len(values)/2)
		for pairIndex := 0; pairIndex < len(values); pairIndex += 2 {
			row := base()
			row["index"] = offset + pairIndex/2
			row["value"] = values[pairIndex]
			if pairIndex+1 < len(values) {
				row["score"] = values[pairIndex+1]
			}
			rows = append(rows, row)
		}
		return rows, nil
	case "stream":
		resp, err := client.command(ctx, "XRANGE", key, "-", "+", "COUNT", strconv.Itoa(limit))
		if err != nil {
			return nil, err
		}
		return redisStreamRows(base, resp), nil
	default:
		row := base()
		row["value"] = fmt.Sprintf("Preview for Redis type %q is not implemented yet", keyType)
		return []map[string]interface{}{row}, nil
	}
}

func redisStringCommand(ctx context.Context, client *redisClient, args ...string) (string, error) {
	resp, err := client.command(ctx, args...)
	if err != nil {
		return "", err
	}
	value, ok := resp.(string)
	if !ok {
		return "", fmt.Errorf("unexpected redis string response: %#v", resp)
	}
	return value, nil
}

func redisIntCommand(ctx context.Context, client *redisClient, args ...string) (int64, error) {
	resp, err := client.command(ctx, args...)
	if err != nil {
		return 0, err
	}
	value, ok := resp.(int64)
	if !ok {
		return 0, fmt.Errorf("unexpected redis integer response: %#v", resp)
	}
	return value, nil
}

func redisStringSlice(resp interface{}) []string {
	values, ok := resp.([]interface{})
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func boundedRange(total, limit, offset int) (int, int) {
	if offset > total {
		return total, total
	}
	return offset, min(total, offset+limit)
}

func redisStreamRows(base func() map[string]interface{}, resp interface{}) []map[string]interface{} {
	entries, ok := resp.([]interface{})
	if !ok {
		return nil
	}
	rows := make([]map[string]interface{}, 0, len(entries))
	for index, entry := range entries {
		entryParts, ok := entry.([]interface{})
		if !ok || len(entryParts) != 2 {
			continue
		}
		row := base()
		row["index"] = index
		row["id"], _ = entryParts[0].(string)
		fields := map[string]string{}
		fieldValues := redisStringSlice(entryParts[1])
		for fieldIndex, fieldValue := range fieldValues {
			if fieldIndex%2 == 0 {
				fields[fieldValue] = ""
				continue
			}
			fields[fieldValues[fieldIndex-1]] = fieldValue
		}
		encoded, _ := json.Marshal(fields)
		row["value"] = string(encoded)
		rows = append(rows, row)
	}
	return rows
}

func redisResponseRows(command string, resp interface{}, limit, offset int) []map[string]interface{} {
	if values, ok := resp.([]interface{}); ok {
		start, end := boundedRange(len(values), limit, offset)
		rows := make([]map[string]interface{}, 0, end-start)
		for index, value := range values[start:end] {
			rows = append(rows, map[string]interface{}{
				"command": command,
				"index":   start + index,
				"result":  redisDisplayValue(value),
			})
		}
		return rows
	}
	return []map[string]interface{}{{
		"command": command,
		"result":  redisDisplayValue(resp),
	}}
}

func redisDisplayValue(value interface{}) interface{} {
	if value == nil {
		return nil
	}
	switch typed := value.(type) {
	case string, int64:
		return typed
	case []interface{}:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprintf("%#v", typed)
		}
		return string(encoded)
	default:
		return fmt.Sprintf("%#v", typed)
	}
}
