package llmbackend

import "strings"

// PayloadString reads a string field out of a native event payload, "" when it is not one.
// Both harnesses map provider payloads, so it lives here rather than in either of them.
func PayloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	if s, ok := payload[key].(string); ok {
		return s
	}
	return ""
}

// FirstNonEmptyString returns the first value that is not blank ("" when all are): the
// shape both harnesses want when a provider offers the same fact under two names.
func FirstNonEmptyString(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// WithUsageKeys copies the provider's usage numbers into the neutral keys,
// looking at both the nested usage object and the top-level payload.
func WithUsageKeys(payload map[string]any, usage map[string]any) map[string]any {
	get := func(keys ...string) (any, bool) {
		if v, ok := PayloadNumber(usage, keys...); ok {
			return int64(v), true
		}
		if v, ok := PayloadNumber(payload, keys...); ok {
			return int64(v), true
		}
		return nil, false
	}
	out := payload
	add := func(key string, keys ...string) {
		if v, ok := get(keys...); ok {
			out = WithNeutralKV(out, key, v)
		}
	}
	add(KeyInputTokens, "input_tokens", "inputTokens", "total_input_tokens", "totalInputTokens")
	add(KeyOutputTokens, "output_tokens", "outputTokens", "total_output_tokens", "totalOutputTokens")
	add(KeyCacheReadTokens, "cache_read_tokens", "cacheReadTokens", "total_cache_read_tokens", "totalCacheReadTokens")
	add(KeyCacheWriteTokens, "cache_write_tokens", "cacheWriteTokens", "total_cache_write_tokens", "totalCacheWriteTokens")
	add(KeyTotalTokens, "total_tokens", "totalTokens")
	// Cost is deliberately not normalized here: the unit depends on the provider
	// (Cline reports USD, llmbackend.Cursor's stream does not report cost at all), so each
	// adapter fills KeyCostUSD when it knows the unit.
	return out
}

// normalizeToolStatus maps a provider's tool status onto running/completed/failed.
