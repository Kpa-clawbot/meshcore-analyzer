package main

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// fakeToken is a minimal mqtt.Token whose Error() is resolvable immediately
// without calling Wait(), matching how paho's real ConnectToken behaves on
// both of its synchronous return paths (the "already retrying, treated as a
// safe no-op" success case and any error case) — see buildForceReconnectFn's
// doc comment for why the caller must not need to Wait() before reading
// Error() (doing so would block indefinitely on a genuine, still in-flight
// reconnect attempt).
type fakeToken struct{ err error }

func (t *fakeToken) Wait() bool                     { return true }
func (t *fakeToken) WaitTimeout(time.Duration) bool { return true }
func (t *fakeToken) Done() <-chan struct{}          { ch := make(chan struct{}); close(ch); return ch }
func (t *fakeToken) Error() error                   { return t.err }

// fakeClient implements mqtt.Client, recording calls to the three methods
// buildForceReconnectFn actually uses (IsConnectionOpen, Disconnect,
// Connect). Every other method panics — buildForceReconnectFn must never
// touch subscriptions, publishes, or options, so a call there indicates the
// fix drifted from its intended scope.
type fakeClient struct {
	isConnectionOpen bool
	connectErr       error

	disconnectCalled bool
	connectCalled    bool
	callOrder        []string
}

func (c *fakeClient) IsConnected() bool { panic("not used by buildForceReconnectFn") }
func (c *fakeClient) IsConnectionOpen() bool {
	c.callOrder = append(c.callOrder, "IsConnectionOpen")
	return c.isConnectionOpen
}
func (c *fakeClient) Connect() mqtt.Token {
	c.connectCalled = true
	c.callOrder = append(c.callOrder, "Connect")
	return &fakeToken{err: c.connectErr}
}
func (c *fakeClient) Disconnect(quiesce uint) {
	c.disconnectCalled = true
	c.callOrder = append(c.callOrder, "Disconnect")
}
func (c *fakeClient) Publish(topic string, qos byte, retained bool, payload any) mqtt.Token {
	panic("not used by buildForceReconnectFn")
}
func (c *fakeClient) Subscribe(topic string, qos byte, callback mqtt.MessageHandler) mqtt.Token {
	panic("not used by buildForceReconnectFn")
}
func (c *fakeClient) SubscribeMultiple(filters map[string]byte, callback mqtt.MessageHandler) mqtt.Token {
	panic("not used by buildForceReconnectFn")
}
func (c *fakeClient) Unsubscribe(topics ...string) mqtt.Token {
	panic("not used by buildForceReconnectFn")
}
func (c *fakeClient) AddRoute(topic string, callback mqtt.MessageHandler) {
	panic("not used by buildForceReconnectFn")
}
func (c *fakeClient) OptionsReader() mqtt.ClientOptionsReader {
	panic("not used by buildForceReconnectFn")
}

// Case 1 (#1335, the genuine half-open-TCP case): paho reports the
// connection as strictly open. This is the one case where Disconnect() is
// safe — status==connected means Disconnecting() does not need to wait on
// any in-flight retry loop, so it completes well within the quiesce window.
// buildForceReconnectFn must still Disconnect before Connect here.
func TestBuildForceReconnectFn_DisconnectsWhenConnectionOpen(t *testing.T) {
	c := &fakeClient{isConnectionOpen: true}
	fn := buildForceReconnectFn(c, "half-open-source")
	fn()

	if !c.disconnectCalled {
		t.Error("expected Disconnect to be called when IsConnectionOpen()==true (half-open TCP case, #1335)")
	}
	if !c.connectCalled {
		t.Error("expected Connect to be called")
	}
	want := []string{"IsConnectionOpen", "Disconnect", "Connect"}
	if len(c.callOrder) != len(want) {
		t.Fatalf("expected call order %v, got %v", want, c.callOrder)
	}
	for i, name := range want {
		if c.callOrder[i] != name {
			t.Errorf("expected call order %v, got %v", want, c.callOrder)
			break
		}
	}
}

// Case 2 (the race this fix closes): paho is already mid-retry
// (reconnecting, or ConnectRetry's connecting), so IsConnectionOpen() is
// false even though the looser IsConnected() (used elsewhere for liveness
// classification) would report true. Calling Disconnect() here is what
// caused the original bug: it races paho's internal status transition and
// can permanently kill an in-flight retry loop via a botched, silently
// swallowed reconnect. buildForceReconnectFn must skip Disconnect and just
// call Connect(), which paho treats as a safe no-op if a retry is already
// under way.
func TestBuildForceReconnectFn_SkipsDisconnectWhenAlreadyRetrying(t *testing.T) {
	c := &fakeClient{isConnectionOpen: false}
	fn := buildForceReconnectFn(c, "mid-retry-source")
	fn()

	if c.disconnectCalled {
		t.Error("Disconnect must NOT be called while paho is already retrying (IsConnectionOpen()==false) — this races paho's status machine and can kill the in-flight retry loop")
	}
	if !c.connectCalled {
		t.Error("expected Connect to still be called (safe no-op per paho when already retrying, or a real reconnect if paho had actually settled to disconnected)")
	}
}

// Case 3: Connect() returns an error token (e.g. the errStatusMustBeDisconnected
// class this whole bug hinged on — Connect() called while paho's status is
// transitionally "disconnecting"). The old code discarded the returned token
// entirely; the fix must surface it via log output instead of silently
// dropping it.
func TestBuildForceReconnectFn_LogsConnectError(t *testing.T) {
	c := &fakeClient{isConnectionOpen: false, connectErr: errors.New("status can only transition to connecting from disconnected")}
	fn := buildForceReconnectFn(c, "erroring-source")

	var buf bytes.Buffer
	origOut := log.Writer()
	origFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(origOut)
		log.SetFlags(origFlags)
	}()

	fn()

	logged := buf.String()
	if !strings.Contains(logged, "erroring-source") || !strings.Contains(logged, "status can only transition") {
		t.Errorf("expected Connect() error to be logged with the source tag, got: %q", logged)
	}
}
