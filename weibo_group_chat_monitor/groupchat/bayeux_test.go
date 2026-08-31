package groupchat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
)

func TestBayeuxReconnectAdviceSemantics(t *testing.T) {
	client, err := NewBayeuxClient("https://example.com/im", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	handled, err := client.handleReconnectAdvice(context.Background(), "/im/42", &BayeuxAdvice{Reconnect: "retry"})
	if !handled || err != nil {
		t.Fatalf("retry advice: handled=%v err=%v", handled, err)
	}
	handled, err = client.handleReconnectAdvice(context.Background(), "/im/42", &BayeuxAdvice{Reconnect: "none"})
	if !handled || !errors.Is(err, ErrBayeuxReconnectNone) {
		t.Fatalf("none advice: handled=%v err=%v", handled, err)
	}
}

func TestBayeuxListenWithErrorStopsBeforeNextConnect(t *testing.T) {
	callbackErr := errors.New("persist failed")
	connects := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/im/handshake":
			_, _ = w.Write([]byte(`[{"channel":"/meta/handshake","successful":true,"clientId":"client-1"}]`))
		case "/im/subscribe":
			_, _ = w.Write([]byte(`[{"channel":"/meta/subscribe","successful":true}]`))
		case "/im/connect":
			connects++
			_, _ = w.Write([]byte(`[{"channel":"/im/42","data":{"sub_type":321}},{"channel":"/meta/connect","successful":true}]`))
		}
	}))
	defer server.Close()
	client, err := NewBayeuxClient(server.URL+"/im", "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	err = client.ListenWithError(context.Background(), "/im/42", func(BayeuxMessage) error { return callbackErr })
	if !errors.Is(err, callbackErr) || connects != 1 {
		t.Fatalf("err=%v connects=%d", err, connects)
	}
}

func TestBayeuxConnectCarriesAckSequence(t *testing.T) {
	var connectAcks []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/im/handshake":
			_, _ = w.Write([]byte(`[{"channel":"/meta/handshake","successful":true,"clientId":"client-ack","ext":{"ack":true}}]`))
		case "/im/subscribe":
			_, _ = w.Write([]byte(`[{"channel":"/meta/subscribe","successful":true}]`))
		case "/im/connect":
			var requests []struct {
				Ext struct {
					Ack int `json:"ack"`
				} `json:"ext"`
			}
			if err := json.NewDecoder(r.Body).Decode(&requests); err != nil {
				t.Errorf("decode connect request: %v", err)
			}
			connectAcks = append(connectAcks, requests[0].Ext.Ack)
			next := 7
			if len(connectAcks) > 1 {
				next = 8
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`[{"channel":"/meta/connect","successful":true,"ext":{"ack":%d}}]`, next)))
		}
	}))
	defer server.Close()

	client, err := NewBayeuxClient(server.URL+"/im", "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := client.Handshake(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.Subscribe(ctx, "/im/42"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if got, want := connectAcks, []int{0, 7}; !reflect.DeepEqual(got, want) {
		t.Fatalf("connect ACK = %v, want %v", got, want)
	}
}

func TestBayeuxReceiveRepeatsHandshakeWhenAdvised(t *testing.T) {
	var mu sync.Mutex
	handshakes := 0
	subscriptions := 0
	connects := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/im/handshake":
			handshakes++
			_, _ = w.Write([]byte(fmt.Sprintf(`[{"channel":"/meta/handshake","successful":true,"clientId":"client-%d"}]`, handshakes)))
		case "/im/subscribe":
			subscriptions++
			_, _ = w.Write([]byte(`[{"channel":"/meta/subscribe","successful":true}]`))
		case "/im/connect":
			connects++
			if connects == 1 {
				_, _ = w.Write([]byte(`[{"channel":"/meta/connect","successful":false,"error":"402::unknown client","advice":{"reconnect":"handshake","interval":0}}]`))
				return
			}
			_, _ = w.Write([]byte(`[{"channel":"/im/42","data":{"sub_type":321}},{"channel":"/meta/connect","successful":true}]`))
		}
	}))
	defer server.Close()

	client, err := NewBayeuxClient(server.URL+"/im", "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	err = client.Listen(ctx, "/im/42", func(BayeuxMessage) { cancel() })
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if handshakes != 2 || subscriptions != 2 || connects != 2 {
		t.Fatalf("handshakes=%d subscriptions=%d connects=%d", handshakes, subscriptions, connects)
	}
}

func TestBayeuxListenHandshakeSubscribeAndReceive(t *testing.T) {
	var mu sync.Mutex
	var actions []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		actions = append(actions, r.URL.Path)
		mu.Unlock()

		if r.Header.Get("Origin") != "https://api.weibo.com" {
			t.Errorf("unexpected origin: %q", r.Header.Get("Origin"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/im/handshake":
			w.Header().Add("Set-Cookie", "BAYEUX_BROWSER=test; Path=/")
			_, _ = w.Write([]byte(`[{"channel":"/meta/handshake","successful":true,"clientId":"client-1","supportedConnectionTypes":["long-polling"]}]`))
		case "/im/subscribe":
			if cookie, err := r.Cookie("BAYEUX_BROWSER"); err != nil || cookie.Value != "test" {
				t.Errorf("handshake cookie was not retained")
			}
			_, _ = w.Write([]byte(`[{"channel":"/meta/subscribe","successful":true,"subscription":"/im/42"}]`))
		case "/im/connect":
			_, _ = w.Write([]byte(`[{"channel":"/im/42","data":{"type":"group","gid":"123"}},{"channel":"/meta/connect","successful":true}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewBayeuxClient(server.URL+"/im", "SUB=secret", server.Client())
	if err != nil {
		t.Fatalf("NewBayeuxClient failed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var received BayeuxMessage
	err = client.Listen(ctx, "/im/42", func(message BayeuxMessage) {
		received = message
		cancel()
	})
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	if received.Channel != "/im/42" {
		t.Fatalf("unexpected received channel: %q", received.Channel)
	}
	var data map[string]any
	if err := json.Unmarshal(received.Data, &data); err != nil {
		t.Fatalf("decode data failed: %v", err)
	}
	if data["gid"] != "123" {
		t.Fatalf("unexpected data: %#v", data)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"/im/handshake", "/im/subscribe", "/im/connect"}
	if len(actions) != len(want) {
		t.Fatalf("unexpected actions: %#v", actions)
	}
	for i := range want {
		if actions[i] != want[i] {
			t.Fatalf("unexpected actions: %#v", actions)
		}
	}
}

func TestBayeuxSubscribeRequiresHandshake(t *testing.T) {
	client, err := NewBayeuxClient("https://example.com/im", "", nil)
	if err != nil {
		t.Fatalf("NewBayeuxClient failed: %v", err)
	}
	if err := client.Subscribe(context.Background(), "/im/42"); err == nil {
		t.Fatal("expected handshake requirement error")
	}
}
