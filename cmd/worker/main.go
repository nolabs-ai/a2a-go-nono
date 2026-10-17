// Command worker runs the tool-using agent as an A2A server. It is the process
// that nono sandboxes: the orchestrator reaches it only over the A2A protocol.
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/server/adka2a/v2"
	"google.golang.org/adk/session"

	"github.com/nolabs-ai/a2ademo/internal/audit"
	"github.com/nolabs-ai/a2ademo/internal/config"
	"github.com/nolabs-ai/a2ademo/internal/tools"
)

const (
	agentName = "local_worker"
	rpcPath   = "/invoke"
)

// instruction is the same prompt the Python and TypeScript demos use, so the
// model behaves identically wherever the tools run.
const instruction = "You are a friendly, concise assistant running locally on the user's machine. " +
	"When the user asks something a tool can answer (the current time, arithmetic, " +
	"listing a folder's contents, fetching or posting to an HTTP URL), " +
	"call the appropriate tool rather than guessing. When listing a folder, present the results " +
	"readably (e.g. directories first, then files). " +
	"Summarise tool results in plain language. If a tool returns an error, explain " +
	"what went wrong and how the user can fix their request. " +
	`If a tool result contains "sandbox": "nono", respond using only the ` +
	`guidance in "message_for_user". Do not mention nono, a sandbox, or any ` +
	"implementation details. Do not retry the operation or suggest workarounds."

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
	toolset, err := tools.New()
	if err != nil {
		return err
	}
	worker, err := llmagent.New(llmagent.Config{
		Name:                  agentName,
		Model:                 llm,
		Description:           "Worker agent that can tell the time, do arithmetic, list local folders, fetch and post to URLs.",
		Instruction:           instruction,
		Tools:                 toolset,
		AfterToolCallbacks:    []llmagent.AfterToolCallback{audit.AfterTool(audit.Logger())},
		GenerateContentConfig: config.GenerateContentConfig(),
	})
	if err != nil {
		return err
	}

	addr := config.Getenv("WORKER_ADDR", "127.0.0.1:9001")
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	base := &url.URL{Scheme: "http", Host: listener.Addr().String()}

	card := &a2a.AgentCard{
		Name:        worker.Name(),
		Description: worker.Description(),
		SupportedInterfaces: []*a2a.AgentInterface{{
			URL:             base.JoinPath(rpcPath).String(),
			ProtocolBinding: a2a.TransportProtocolJSONRPC,
			ProtocolVersion: a2a.Version,
		}},
		Version:            "1.0.0",
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills:             adka2a.BuildAgentSkills(worker),
		Capabilities:       a2a.AgentCapabilities{Streaming: true},
	}

	executor := adka2a.NewExecutor(adka2a.ExecutorConfig{
		RunnerConfig: runner.Config{
			AppName:        worker.Name(),
			Agent:          agent.Agent(worker),
			SessionService: session.InMemoryService(),
		},
	})

	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	mux.Handle(rpcPath, a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(executor)))

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	log.Printf("worker A2A server on %s (card: %s%s, model: %s)", base, base, a2asrv.WellKnownAgentCardPath, config.Model())
	if err := srv.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
