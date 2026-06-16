package funcs

import "fmt"

// Helpers to extract typed values from a map[string]any (the shape we get from
// JSON-decoded tool arguments). All helpers accept a default for missing keys
// and treat type mismatches as errors.

func argString(args map[string]any, key, defaultVal string) (string, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return defaultVal, nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("arg %q must be string, got %T", key, v)
	}
	return s, nil
}

func argRequiredString(args map[string]any, key string) (string, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return "", fmt.Errorf("arg %q is required", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("arg %q must be string, got %T", key, v)
	}
	if s == "" {
		return "", fmt.Errorf("arg %q must be non-empty", key)
	}
	return s, nil
}

func argInt(args map[string]any, key string, defaultVal int) (int, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return defaultVal, nil
	}
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		return int(n), nil
	case float32:
		return int(n), nil
	default:
		return 0, fmt.Errorf("arg %q must be int, got %T", key, v)
	}
}

func argBool(args map[string]any, key string, defaultVal bool) (bool, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return defaultVal, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("arg %q must be bool, got %T", key, v)
	}
	return b, nil
}
