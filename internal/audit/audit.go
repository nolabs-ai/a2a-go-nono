// Package audit logs every tool call the worker agent makes, marking the ones
// nono blocked. It is a readable view of the agent's behaviour, not the source
// of truth: nono's own audit trail records what the sandbox actually enforced.
package audit

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/tool"
)

const maxValueLen = 120

// Outcomes recorded for each call.
const (
	Allowed = "ALLOWED"
	Denied  = "DENIED"
	Failed  = "FAILED"
)

// Logger returns a text logger on stderr for the worker's audit lines.
func Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				a.Value = slog.StringValue(a.Value.Time().Format("15:04:05"))
			}
			return a
		},
	}))
}

// AfterTool is an llmagent AfterToolCallback that logs each call and its
// outcome. It never alters the result.
func AfterTool(log *slog.Logger) func(agent.ToolContext, tool.Tool, map[string]any, map[string]any, error) (map[string]any, error) {
	return func(ctx agent.ToolContext, t tool.Tool, args, result map[string]any, err error) (map[string]any, error) {
		outcome, detail := Classify(result, err)
		attrs := []any{
			"tool", t.Name(),
			"args", FormatArgs(args),
			"session", ctx.SessionID(),
		}
		if detail != "" {
			attrs = append(attrs, "detail", detail)
		}
		switch outcome {
		case Denied:
			log.Warn(Denied, attrs...)
		case Failed:
			log.Error(Failed, attrs...)
		default:
			log.Info(Allowed, attrs...)
		}
		return nil, nil
	}
}

// Classify decides the outcome of a tool call from its result. A result marked
// sandbox="nono" is a block by the sandbox; any other error status is a plain
// failure, such as a missing file.
func Classify(result map[string]any, err error) (outcome, detail string) {
	if err != nil {
		return Failed, truncate(err.Error())
	}
	if result["sandbox"] == "nono" {
		return Denied, truncate(firstString(result, "detail", "error"))
	}
	if result["status"] == "error" {
		return Failed, truncate(firstString(result, "error"))
	}
	return Allowed, ""
}

// FormatArgs renders arguments as stable, truncated key=value pairs.
func FormatArgs(args map[string]any) string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v, _ := json.Marshal(args[k])
		parts = append(parts, fmt.Sprintf("%s=%s", k, truncate(string(v))))
	}
	return strings.Join(parts, " ")
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func truncate(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxValueLen {
		return s[:maxValueLen] + "…"
	}
	return s
}
