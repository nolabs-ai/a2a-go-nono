// Package peer is the orchestrator's A2A client for one remote agent. It uses
// a2a-go directly: it discovers the agent card, opens a client from it, sends
// messages and follows the resulting task to a terminal state.
package peer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
)

const pollInterval = 500 * time.Millisecond

// Peer talks to a single remote A2A agent.
type Peer struct {
	baseURL string
	http    *http.Client

	mu       sync.Mutex
	card     *a2a.AgentCard
	client   *a2aclient.Client
	contexts map[string]string // caller session -> A2A contextId
}

// New returns a Peer for the agent served at baseURL. It does not connect yet:
// the card is fetched on first use so the worker may start after the
// orchestrator.
func New(baseURL string) *Peer {
	return &Peer{
		baseURL:  baseURL,
		http:     &http.Client{Timeout: 5 * time.Minute},
		contexts: map[string]string{},
	}
}

// Card returns the remote agent's card, discovering it on first use.
func (p *Peer) Card(ctx context.Context) (*a2a.AgentCard, error) {
	_, card, err := p.connect(ctx)
	return card, err
}

func (p *Peer) connect(ctx context.Context) (*a2aclient.Client, *a2a.AgentCard, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil {
		return p.client, p.card, nil
	}
	card, err := agentcard.NewResolver(p.http).Resolve(ctx, p.baseURL)
	if err != nil {
		return nil, nil, fmt.Errorf("discover agent card at %s: %w", p.baseURL, err)
	}
	client, err := a2aclient.NewFromCard(ctx, card, a2aclient.WithJSONRPCTransport(p.http))
	if err != nil {
		return nil, nil, fmt.Errorf("create A2A client for %q: %w", card.Name, err)
	}
	log.Printf("A2A discovered agent %q v%s with %d skill(s) at %s", card.Name, card.Version, len(card.Skills), p.baseURL)
	p.card, p.client = card, client
	return client, card, nil
}

// Ask sends text to the remote agent and returns its text answer. Messages
// sharing a session reuse the same A2A context so the remote agent keeps the
// conversation.
func (p *Peer) Ask(ctx context.Context, session, text string) (string, error) {
	client, card, err := p.connect(ctx)
	if err != nil {
		return "", err
	}

	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(text))
	p.mu.Lock()
	msg.ContextID = p.contexts[session]
	p.mu.Unlock()

	log.Printf("A2A → %s SendMessage context=%q", card.Name, msg.ContextID)
	res, err := client.SendMessage(ctx, &a2a.SendMessageRequest{Message: msg})
	if err != nil {
		return "", fmt.Errorf("send message to %q: %w", card.Name, err)
	}

	switch r := res.(type) {
	case *a2a.Message:
		p.remember(session, r.ContextID)
		log.Printf("A2A ← %s message", card.Name)
		return joinText(r.Parts), nil
	case *a2a.Task:
		p.remember(session, r.ContextID)
		return p.finish(ctx, client, card.Name, r)
	default:
		return "", fmt.Errorf("unexpected A2A result %T", res)
	}
}

func (p *Peer) remember(session, contextID string) {
	if contextID == "" {
		return
	}
	p.mu.Lock()
	p.contexts[session] = contextID
	p.mu.Unlock()
}

// finish follows a task until it reaches a terminal state, then extracts the
// worker's answer from its artifacts or final status message.
func (p *Peer) finish(ctx context.Context, client *a2aclient.Client, name string, task *a2a.Task) (string, error) {
	for !task.Status.State.Terminal() {
		log.Printf("A2A ← %s task=%s state=%s", name, task.ID, task.Status.State)
		select {
		case <-ctx.Done():
			// Best effort: tell the worker to stop, even though we are giving up.
			cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_, _ = client.CancelTask(cancelCtx, &a2a.CancelTaskRequest{ID: task.ID})
			return "", ctx.Err()
		case <-time.After(pollInterval):
		}
		t, err := client.GetTask(ctx, &a2a.GetTaskRequest{ID: task.ID})
		if err != nil {
			return "", fmt.Errorf("get task %s: %w", task.ID, err)
		}
		task = t
	}
	log.Printf("A2A ← %s task=%s state=%s", name, task.ID, task.Status.State)
	return taskResult(task)
}

func taskResult(task *a2a.Task) (string, error) {
	var statusText string
	if task.Status.Message != nil {
		statusText = joinText(task.Status.Message.Parts)
	}
	switch task.Status.State {
	case a2a.TaskStateCompleted:
		var parts []string
		for _, art := range task.Artifacts {
			if t := joinText(art.Parts); t != "" {
				parts = append(parts, t)
			}
		}
		if len(parts) == 0 && statusText != "" {
			parts = append(parts, statusText)
		}
		if len(parts) == 0 {
			return "", errors.New("worker completed the task without a text result")
		}
		return strings.Join(parts, "\n\n"), nil
	default:
		if statusText == "" {
			statusText = "no details provided"
		}
		return "", fmt.Errorf("worker task ended in state %s: %s", task.Status.State, statusText)
	}
}

func joinText(parts a2a.ContentParts) string {
	var out []string
	for _, part := range parts {
		if t := strings.TrimSpace(part.Text()); t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, "\n")
}
