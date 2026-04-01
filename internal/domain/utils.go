package domain

import (
	"database/sql"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Ptr returns a pointer to the given value.
func Ptr[T any](v T) *T { return &v }

// Deref returns the value pointed to, or the zero value if nil.
func Deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

// Deprecated aliases — kept for backward compatibility during migration.
func StrPtr(s string) *string { return Ptr(s) }
func DerefStr(p *string) string { return Deref(p) }
func DerefInt(p *int) int { return Deref(p) }

// Truncate shortens s to at most max runes, appending "..." if truncated.
// Safe for multi-byte UTF-8 content (Vietnamese, Japanese, etc.).
func Truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "..."
}

func NullTimeToString(t sql.NullTime) *string {
	if !t.Valid {
		return nil
	}
	s := t.Time.Format("2006-01-02T15:04:05Z07:00")
	return &s
}

func PtrCategoryToStr(c *CommentCategory) *string {
	if c == nil {
		return nil
	}
	s := string(*c)
	return &s
}

func BoolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func ParseCSV(s string) []string {
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func DerefCategory(c *CommentCategory) CommentCategory {
	if c == nil {
		return CategoryLogic
	}
	return *c
}

func GetIntOr(input ToolInput, key string, def int) int {
	v, ok := input[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		if i, err := strconv.Atoi(n); err == nil {
			return i
		}
	}
	return def
}
