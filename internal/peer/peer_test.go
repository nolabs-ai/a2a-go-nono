package peer

import (
	"context"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

// echoExecutor answers every message as a completed task, or fails it when the
// text asks for that.
type echoExecutor struct{}

func (echoExecutor) Execute(_ context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		text := ""
		for _, p := range ec.Message.Parts {
			text += p.Text()
		}
		if ec.StoredTask == nil {
			if !yield(a2a.NewSubmittedTask(ec, ec.Message), nil) {
				return
			}
		}
		if strings.Contains(text, "fail") {
			yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateFailed, a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("it broke"))), nil)
			return
		}
		if !yield(a2a.NewArtifactEvent(ec, a2a.NewTextPart("echo: "+text)), nil) {
			return
		}
		yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateCompleted, nil), nil)
	}
}

func (echoExecutor) Cancel(_ context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateCanceled, nil), nil)
	}
}

func startEcho(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	card := &a2a.AgentCard{
		Name:    "echo",
		Version: "1.0.0",
		SupportedInterfaces: []*a2a.AgentInterface{{
			URL:             srv.URL + "/invoke",
			ProtocolBinding: a2a.TransportProtocolJSONRPC,
			ProtocolVersion: a2a.Version,
		}},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
	}
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	mux.Handle("/invoke", a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(echoExecutor{})))
	return srv.URL
}

func TestAskDiscoversCardAndReturnsArtifactText(t *testing.T) {
	p := New(startEcho(t))
	got, err := p.Ask(context.Background(), "s1", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if got != "echo: hello" {
		t.Errorf("got %q", got)
	}
	card, err := p.Card(context.Background())
	if err != nil || card.Name != "echo" {
		t.Errorf("card = %v, %v", card, err)
	}
}

func TestAskReusesContextPerSession(t *testing.T) {
	p := New(startEcho(t))
	if _, err := p.Ask(context.Background(), "s1", "one"); err != nil {
		t.Fatal(err)
	}
	first := p.contexts["s1"]
	if first == "" {
		t.Fatal("no A2A context recorded for the session")
	}
	if _, err := p.Ask(context.Background(), "s2", "two"); err != nil {
		t.Fatal(err)
	}
	if p.contexts["s2"] == first {
		t.Error("different sessions must not share an A2A context")
	}
}

func TestAskReportsFailedTask(t *testing.T) {
	p := New(startEcho(t))
	_, err := p.Ask(context.Background(), "s1", "please fail")
	if err == nil || !strings.Contains(err.Error(), "it broke") {
		t.Fatalf("err = %v, want the worker's failure message", err)
	}
}

func TestAskWhenWorkerIsDown(t *testing.T) {
	p := New("http://127.0.0.1:1")
	if _, err := p.Ask(context.Background(), "s1", "hi"); err == nil {
		t.Fatal("want error")
	}
}
