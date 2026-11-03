package server

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

// A call-scoped driver distinguishes a missing binding from a failed lookup.
// It needs no PostgreSQL, shared schema mutation, network, or global test hook.
type bindingReadConnector struct {
	fail          bool
	bindingReads  atomic.Int32
	fallbackReads atomic.Int32
}

func (c *bindingReadConnector) Connect(context.Context) (driver.Conn, error) {
	return bindingReadConn{c}, nil
}
func (c *bindingReadConnector) Driver() driver.Driver { return bindingReadDriver{c} }

type bindingReadDriver struct{ fixture *bindingReadConnector }

func (d bindingReadDriver) Open(string) (driver.Conn, error) {
	return bindingReadConn{d.fixture}, nil
}

type bindingReadConn struct{ fixture *bindingReadConnector }

func (bindingReadConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unsupported fixture prepare")
}
func (bindingReadConn) Close() error { return nil }
func (bindingReadConn) Begin() (driver.Tx, error) {
	return nil, errors.New("unsupported fixture transaction")
}
func (c bindingReadConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, " FROM agents WHERE key=") {
		c.fixture.bindingReads.Add(1)
		if c.fixture.fail {
			return nil, errors.New("synthetic private binding backend details")
		}
		return bindingReadEmptyRows{}, nil
	}
	c.fixture.fallbackReads.Add(1)
	return nil, errors.New("fallback profile must not be queried after a binding failure")
}

type bindingReadEmptyRows struct{}

func (bindingReadEmptyRows) Columns() []string         { return []string{"unused"} }
func (bindingReadEmptyRows) Close() error              { return nil }
func (bindingReadEmptyRows) Next([]driver.Value) error { return io.EOF }

func bindingReadServer(t *testing.T, fail, local bool) (*Server, *bindingReadConnector) {
	t.Helper()
	fixture := &bindingReadConnector{fail: fail}
	connection := sql.OpenDB(fixture)
	t.Cleanup(func() { _ = connection.Close() })
	s := &Server{m: &Manager{pg: &db.DB{DB: connection}}, profChatAgents: map[int64]*agent.ChatAgent{}}
	if local {
		s.localResolver = localTestResolver(t, "http://127.0.0.1:11434/v1")
	}
	return s, fixture
}

func TestLocalOnlyBindingReadFailureStopsTaskFallback(t *testing.T) {
	s, fixture := bindingReadServer(t, true, true)
	provider, _, ok := s.agentBindingProvider("worker")
	if !ok || provider == nil {
		t.Fatal("binding lookup failure was treated as an unbound role")
	}
	_, _, _, err := provider.Complete(context.Background(), llm.CompletionRequest{})
	if err != ErrLocalLLMProfile {
		t.Fatalf("binding failure did not return the fixed local profile error: %v", err)
	}
	if fixture.bindingReads.Load() != 1 || fixture.fallbackReads.Load() != 0 {
		t.Fatalf("binding=%d fallback=%d", fixture.bindingReads.Load(), fixture.fallbackReads.Load())
	}
}

func TestLocalOnlyBindingReadFailureStopsChatFallback(t *testing.T) {
	s, fixture := bindingReadServer(t, true, true)
	id := int64(7)
	s.profChatAgents[id] = &agent.ChatAgent{}
	if s.resolveChatAgent(&db.Conversation{AgentKey: "worker", LLMProfileID: &id}) != nil {
		t.Fatal("binding lookup failure selected a pinned or global chat agent")
	}
	if fixture.bindingReads.Load() != 1 || fixture.fallbackReads.Load() != 0 {
		t.Fatalf("binding=%d fallback=%d", fixture.bindingReads.Load(), fixture.fallbackReads.Load())
	}
}

func TestLocalOnlyMissingBindingStoreStopsFallback(t *testing.T) {
	s := &Server{m: &Manager{}, localResolver: localTestResolver(t, "http://127.0.0.1:11434/v1")}
	provider, _, ok := s.agentBindingProvider("worker")
	if _, blocked := provider.(blockedLocalProvider); !ok || !blocked {
		t.Fatal("unavailable binding source permitted fallback")
	}
	if s.resolveChatAgent(&db.Conversation{AgentKey: "worker"}) != nil {
		t.Fatal("unavailable binding source permitted global chat")
	}
}

func TestKnownAbsentBindingStillPermitsTaskFallback(t *testing.T) {
	s, fixture := bindingReadServer(t, false, true)
	if provider, _, ok := s.agentBindingProvider("worker"); ok || provider != nil {
		t.Fatal("a successful lookup with no binding must remain unbound")
	}
	if fixture.bindingReads.Load() != 1 || fixture.fallbackReads.Load() != 0 {
		t.Fatalf("binding=%d fallback=%d", fixture.bindingReads.Load(), fixture.fallbackReads.Load())
	}
}

func TestExternalBindingReadFailureKeepsLegacyFallback(t *testing.T) {
	s, fixture := bindingReadServer(t, true, false)
	if provider, _, ok := s.agentBindingProvider("worker"); ok || provider != nil {
		t.Fatal("external mode changed its legacy task fallback")
	}
	id := int64(7)
	expected := &agent.ChatAgent{}
	s.profChatAgents[id] = expected
	if s.resolveChatAgent(&db.Conversation{AgentKey: "worker", LLMProfileID: &id}) != expected {
		t.Fatal("external mode changed its legacy pinned chat fallback")
	}
	if fixture.bindingReads.Load() != 2 || fixture.fallbackReads.Load() != 0 {
		t.Fatalf("binding=%d fallback=%d", fixture.bindingReads.Load(), fixture.fallbackReads.Load())
	}
}
