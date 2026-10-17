// Package tools defines the worker agent's function tools. Filesystem and
// network access are deliberately unchecked at the application level: nono
// enforces the boundary, and these tools just report what the OS says.
package tools

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	"github.com/nolabs-ai/a2ademo/internal/config"
)

// Result is a loosely typed tool response, mirroring the dicts in the Python demo.
type Result map[string]any

func inNono() bool { return os.Getenv("NONO_CAP_FILE") != "" }

func blocked(what string) string {
	return fmt.Sprintf("Access to %s is blocked by corporate policy. If you believe this access should be allowed, please submit an access request.", what)
}

// New returns every worker tool.
func New() ([]tool.Tool, error) {
	var out []tool.Tool
	add := func(t tool.Tool, err error) error {
		if err != nil {
			return err
		}
		out = append(out, t)
		return nil
	}
	if err := add(functiontool.New(functiontool.Config{
		Name:        "get_current_time",
		Description: `Return the current date and time for an IANA timezone such as "UTC" or "Europe/London".`,
	}, currentTime)); err != nil {
		return nil, err
	}
	if err := add(functiontool.New(functiontool.Config{
		Name:        "calculate",
		Description: "Safely evaluate a basic arithmetic expression using + - * / % ** and parentheses over numbers.",
	}, calculate)); err != nil {
		return nil, err
	}
	if err := add(functiontool.New(functiontool.Config{
		Name:        "list_folder",
		Description: `List files and subfolders in a directory. Defaults to the current directory; a leading "~" is expanded. Permission denials carry sandbox="nono" and a message_for_user.`,
	}, listFolder)); err != nil {
		return nil, err
	}
	if err := add(functiontool.New(functiontool.Config{
		Name:        "fetch_url",
		Description: "Fetch a public HTTP or HTTPS URL through nono's supervised proxy. Returns the status and the first 4 KiB of text. URLs blocked by nono return sandbox=\"nono\" and a message_for_user.",
	}, fetchURL)); err != nil {
		return nil, err
	}
	if err := add(functiontool.New(functiontool.Config{
		Name:        "post_url",
		Description: "POST a JSON body to a public HTTP or HTTPS URL through nono's supervised proxy. Returns the status and the first 4 KiB of the response. Requests blocked by nono return sandbox=\"nono\" and a message_for_user.",
	}, postURL)); err != nil {
		return nil, err
	}
	return out, nil
}

// --- get_current_time ---

type timeArgs struct {
	Timezone string `json:"timezone,omitempty" jsonschema:"IANA timezone name; defaults to UTC"`
}

func currentTime(_ agent.ToolContext, a timeArgs) (Result, error) {
	name := a.Timezone
	if name == "" {
		name = "UTC"
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return Result{"status": "error", "error": fmt.Sprintf("Unknown timezone: %q. Use an IANA name like 'Europe/London'.", name)}, nil
	}
	now := time.Now().In(loc)
	return Result{
		"status":         "ok",
		"timezone":       name,
		"datetime":       now.Format(time.RFC3339),
		"human_readable": now.Format("Monday, 02 January 2006 at 15:04:05 MST"),
	}, nil
}

// --- calculate ---

type calcArgs struct {
	Expression string `json:"expression" jsonschema:"e.g. 2 + 2 * 10 or (3 ** 4) / 9"`
}

func calculate(_ agent.ToolContext, a calcArgs) (Result, error) {
	v, err := evalExpr(a.Expression)
	if err != nil {
		return Result{"status": "error", "error": fmt.Sprintf("Could not evaluate %q. Use numbers and + - * / %% ** ( ).", a.Expression)}, nil
	}
	return Result{"status": "ok", "expression": a.Expression, "result": v}, nil
}

// --- list_folder ---

type listArgs struct {
	Path string `json:"path,omitempty" jsonschema:"directory to list; defaults to ."`
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func permissionDenied(path, resolved string) Result {
	r := Result{"status": "error", "error": fmt.Sprintf("Permission denied: %q", path)}
	if inNono() {
		r["sandbox"] = "nono"
		r["message_for_user"] = blocked(fmt.Sprintf("%q", resolved))
	}
	return r
}

func isPermission(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES)
}

func listFolder(_ agent.ToolContext, a listArgs) (Result, error) {
	path := a.Path
	if path == "" {
		path = "."
	}
	expanded := expandHome(path)
	resolved, err := filepath.Abs(expanded)
	if err != nil {
		resolved = expanded
	}

	info, err := os.Stat(expanded)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Result{"status": "error", "error": fmt.Sprintf("Path does not exist: %q", path)}, nil
	case err != nil && isPermission(err):
		return permissionDenied(path, resolved), nil
	case err != nil:
		return Result{"status": "error", "error": fmt.Sprintf("Could not access %q: %v", path, err)}, nil
	case !info.IsDir():
		return Result{"status": "error", "error": fmt.Sprintf("Not a directory: %q", path)}, nil
	}

	dirents, err := os.ReadDir(expanded)
	if err != nil {
		if isPermission(err) {
			return permissionDenied(path, resolved), nil
		}
		return Result{"status": "error", "error": fmt.Sprintf("Could not read %q: %v", path, err)}, nil
	}
	type entry struct {
		Name string `json:"name"`
		Type string `json:"type"`
		Size *int64 `json:"size_bytes"`
	}
	entries := make([]entry, 0, len(dirents))
	for _, d := range dirents {
		e := entry{Name: d.Name(), Type: "dir"}
		if !d.IsDir() {
			e.Type = "file"
			if fi, err := d.Info(); err == nil {
				size := fi.Size()
				e.Size = &size
			}
		}
		entries = append(entries, e)
	}
	// Directories first, then files, each alphabetical (case-insensitive).
	sort.Slice(entries, func(i, j int) bool {
		if (entries[i].Type == "dir") != (entries[j].Type == "dir") {
			return entries[i].Type == "dir"
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return Result{"status": "ok", "path": resolved, "count": len(entries), "entries": entries}, nil
}

// --- fetch_url ---

type fetchArgs struct {
	URL string `json:"url" jsonschema:"absolute http or https URL, e.g. https://example.com"`
}

var httpClient = config.HTTPClient(10 * time.Second)

func isNonoNetworkDenial(detail string) bool {
	d := strings.ToLower(detail)
	for _, m := range []string{"is in the deny list", "is not in the allowlist", "is in the link-local range"} {
		if strings.Contains(d, m) {
			return true
		}
	}
	return false
}

// l7DenialBody is what nono's proxy returns when an endpoint rule rejects a
// request. It carries no nono-specific marker, so an identical body from a real
// server would be misread; that is acceptable for a demo tool.
const l7DenialBody = `{"error":"Forbidden"}`

// proxyDenial reports whether a response is nono's proxy refusing the request.
// For plain HTTP the proxy names the reason in the status line ("403
// Forbidden: host x is in the deny list"); some paths put it in the body.
func proxyDenial(code int, status string, body []byte) (string, bool) {
	if code != http.StatusForbidden {
		return "", false
	}
	// L7 endpoint rules on a TLS-intercepted domain give no reason on the wire,
	// only this fixed body; nono's log holds the method, path and rule detail.
	if strings.TrimSpace(string(body)) == l7DenialBody {
		return "request blocked by an endpoint rule (method/path not allowed for this host)", true
	}
	for _, candidate := range []string{status, string(body)} {
		if isNonoNetworkDenial(candidate) {
			return strings.TrimSpace(candidate), true
		}
	}
	return "", false
}

func fetchURL(_ agent.ToolContext, a fetchArgs) (Result, error) {
	return request(http.MethodGet, a.URL, nil), nil
}

// --- post_url ---

type postArgs struct {
	URL  string `json:"url" jsonschema:"absolute http or https URL to POST to"`
	Body string `json:"body,omitempty" jsonschema:"JSON request body"`
}

func postURL(_ agent.ToolContext, a postArgs) (Result, error) {
	return request(http.MethodPost, a.URL, strings.NewReader(a.Body)), nil
}

// request performs one HTTP call and maps nono's refusals to a sandbox result.
func request(method, rawURL string, body io.Reader) Result {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return Result{"status": "error", "error": "Use an absolute http:// or https:// URL."}
	}
	if u.User != nil {
		return Result{"status": "error", "error": "URLs containing credentials are not accepted."}
	}
	req, err := http.NewRequest(method, rawURL, body)
	if err != nil {
		return Result{"status": "error", "error": err.Error()}
	}
	req.Header.Set("User-Agent", "a2ademo/0.1")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fetchFailure(method, rawURL, err.Error())
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if detail, ok := proxyDenial(resp.StatusCode, resp.Status, respBody); ok {
		return fetchFailure(method, rawURL, detail)
	}
	return Result{
		"status":       "ok",
		"url":          resp.Request.URL.String(),
		"http_status":  resp.StatusCode,
		"content_type": resp.Header.Get("Content-Type"),
		"body":         strings.ToValidUTF8(string(respBody), "\uFFFD"),
	}
}

func fetchFailure(method, rawURL, detail string) Result {
	if isNonoNetworkDenial(detail) || strings.Contains(detail, "endpoint rule") {
		return Result{
			"status":           "error",
			"error":            fmt.Sprintf("Network access denied for %s %q", method, rawURL),
			"sandbox":          "nono",
			"message_for_user": blocked(fmt.Sprintf("%s %q", method, rawURL)),
			"detail":           detail,
		}
	}
	return Result{"status": "error", "error": fmt.Sprintf("Could not fetch %q", rawURL), "detail": detail}
}
