// Command orchestrator serves the browser chat UI. Its agent has no local
// tools: it delegates every task to the sandboxed worker over A2A.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"

	"github.com/nolabs-ai/a2ademo/internal/config"
	"github.com/nolabs-ai/a2ademo/internal/peer"
)

// webFS holds the browser UI. It is embedded rather than read from disk so the
// binary does not depend on its working directory.
//
//go:embed web
var webFS embed.FS

const (
	appName = "a2a_orchestrator"
	userID  = "web_user"
)

// instruction mirrors the other demos' prompt. The orchestrator has no tools of
// its own: anything a tool can answer goes to the worker, and the worker's
// answer is passed on in the same plain, friendly voice.
const instruction = "You are a friendly, concise assistant running locally on the user's machine. " +
	"When the user asks something a tool can answer (the current time, arithmetic, " +
	"listing a folder's contents, fetching or posting to an HTTP URL), " +
	"call delegate_to_worker with the request rather than guessing. " +
	"The worker has already summarised its tool results, so pass its answer on essentially as written, " +
	"keeping every specific it gives (names, numbers, paths, lengths, fingerprints) and adding nothing of your own. " +
	"If the worker reports an error, explain " +
	"what went wrong and how the user can fix their request. " +
	"If the worker's answer says an action is blocked by policy, repeat only that guidance. " +
	"Do not mention nono, a sandbox, A2A, the worker, or any implementation details. " +
	"Do not retry the operation or suggest workarounds."

type delegateArgs struct {
	Request string `json:"request" jsonschema:"the task for the worker, in full"`
}

type runtime struct {
	runner   *runner.Runner
	sessions session.Service

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	llm, err := config.NewModel(ctx)
	if err != nil {
		return err
	}

	workerURL := config.Getenv("WORKER_URL", "http://127.0.0.1:9001")
	// The worker is a remote A2A agent. The peer discovers its agent card and
	// talks to it with a2a-go's client; the model sees it as one delegation tool.
	worker := peer.New(workerURL)
	delegate, err := functiontool.New(functiontool.Config{
		Name:        "delegate_to_worker",
		Description: "Delegate a task to the remote worker agent over A2A. The worker can tell the time, do arithmetic, list local folders, fetch and post to URLs. Pass the user's request in full; the worker's answer is returned.",
	}, func(ctx agent.ToolContext, a delegateArgs) (map[string]any, error) {
		answer, err := worker.Ask(ctx, ctx.SessionID(), a.Request)
		if err != nil {
			log.Printf("delegation failed: %v", err)
			return map[string]any{"status": "error", "error": err.Error()}, nil
		}
		return map[string]any{"status": "ok", "worker_answer": answer}, nil
	})
	if err != nil {
		return err
	}

	orchestrator, err := llmagent.New(llmagent.Config{
		Name:                  appName,
		Model:                 llm,
		Description:           "Orchestrator that delegates to a sandboxed worker over A2A.",
		Instruction:           instruction,
		Tools:                 []tool.Tool{delegate},
		GenerateContentConfig: config.GenerateContentConfig(),
	})
	if err != nil {
		return err
	}

	sessions := session.InMemoryService()
	r, err := runner.New(runner.Config{
		AppName:        appName,
		Agent:          agent.Agent(orchestrator),
		SessionService: sessions,
	})
	if err != nil {
		return err
	}
	rt := &runtime{runner: r, sessions: sessions, locks: map[string]*sync.Mutex{}}

	web, err := fs.Sub(webFS, "web")
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(web)))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, req *http.Request) {
		http.ServeFileFS(w, req, web, "index.html")
	})
	mux.HandleFunc("GET /api/agent", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"name":       appName,
			"demo_title": "A2A Secured Chat Assistant",
			"framework":  "A2A · Go",
			"provider":   "gemini",
			"model":      config.Model(),
		})
	})
	mux.HandleFunc("POST /api/sessions", rt.createSession)
	mux.HandleFunc("POST /api/chat", rt.chat)

	addr := config.Getenv("ORCHESTRATOR_ADDR", "127.0.0.1:8000")
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	log.Printf("orchestrator UI on http://%s, delegating to worker at %s", addr, workerURL)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (rt *runtime) createSession(w http.ResponseWriter, req *http.Request) {
	id := uuid.NewString()
	if err := rt.ensureSession(req.Context(), id); err != nil {
		log.Printf("create session: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": "Could not create a session."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"session_id": id})
}

func (rt *runtime) ensureSession(ctx context.Context, id string) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if _, ok := rt.locks[id]; ok {
		return nil
	}
	if _, err := rt.sessions.Create(ctx, &session.CreateRequest{AppName: appName, UserID: userID, SessionID: id}); err != nil {
		return err
	}
	rt.locks[id] = &sync.Mutex{}
	return nil
}

type chatRequest struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

func (rt *runtime) chat(w http.ResponseWriter, req *http.Request) {
	req.Body = http.MaxBytesReader(w, req.Body, 64<<10)
	var in chatRequest
	if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": "Invalid request body."})
		return
	}
	if _, err := uuid.Parse(in.SessionID); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": "Invalid session_id."})
		return
	}
	msg := strings.TrimSpace(in.Message)
	if msg == "" || len(msg) > 20_000 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"detail": "Message cannot be blank or longer than 20,000 characters."})
		return
	}

	reply, err := rt.respond(req.Context(), in.SessionID, msg)
	if err != nil {
		// Keep credentials and provider internals out of browser-visible errors.
		log.Printf("agent request failed for session %s: %v", in.SessionID, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"detail": "The agent could not complete that request. Check the server log."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"reply": reply})
}

func (rt *runtime) respond(ctx context.Context, sessionID, text string) (string, error) {
	if err := rt.ensureSession(ctx, sessionID); err != nil {
		return "", err
	}
	rt.mu.Lock()
	lock := rt.locks[sessionID]
	rt.mu.Unlock()
	// Sessions are ordered conversations; don't interleave two submissions.
	lock.Lock()
	defer lock.Unlock()

	content := genai.NewContentFromText(text, genai.RoleUser)
	var replies []string
	for event, err := range rt.runner.Run(ctx, userID, sessionID, content, agent.RunConfig{}) {
		if err != nil {
			return "", err
		}
		if event == nil || !event.IsFinalResponse() || event.Content == nil {
			continue
		}
		var sb strings.Builder
		for _, p := range event.Content.Parts {
			sb.WriteString(p.Text)
		}
		if t := strings.TrimSpace(sb.String()); t != "" {
			replies = append(replies, t)
		}
	}
	if len(replies) == 0 {
		return "", errors.New("the agent completed without a text response")
	}
	return strings.Join(replies, "\n\n"), nil
}
