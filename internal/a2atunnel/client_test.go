package a2atunnel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

type noopHandler struct{}

func (noopHandler) Handle(context.Context, *AgentRequest) ([]byte, error) {
	return []byte(`{"result":"ok"}`), nil
}

func waitConnected(t *testing.T, client *TunnelClient) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if client.IsConnected() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("tunnel client did not connect in time; last state=%s", client.Status().State)
}

func TestTunnelClientCallGRPC(t *testing.T) {
	t.Parallel()

	received := make(chan CallPayload, 4)
	apiKey := "sq_test_key"

	server := grpc.NewServer()
	svc := &grpcTestService{apiKey: apiKey, received: received}
	server.RegisterService(&grpc.ServiceDesc{
		ServiceName: "square.tunnel.v1.TunnelService",
		HandlerType: (*grpcTunnelService)(nil),
		Streams: []grpc.StreamDesc{
			{
				StreamName:    "Connect",
				Handler:       grpcConnectHandler,
				ServerStreams: true,
				ClientStreams: true,
			},
		},
	}, svc)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer lis.Close()

	go server.Serve(lis)
	defer server.Stop()

	client := NewWithTransport(lis.Addr().String(), apiKey, noopHandler{}, TransportGRPC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go client.Run(ctx)

	waitConnected(t, client)

	payload1, _ := json.Marshal(CallPayload{
		Task:           "hello one",
		SourceAgentID:  "caller-1",
		ConversationID: "conv-123",
	})
	if _, err := client.Call(context.Background(), "target-a", payload1); err != nil {
		t.Fatalf("call 1 failed: %v", err)
	}
	payload2, _ := json.Marshal(CallPayload{
		Task:           "hello two",
		SourceAgentID:  "caller-1",
		ConversationID: "conv-123",
	})
	if _, err := client.Call(context.Background(), "target-a", payload2); err != nil {
		t.Fatalf("call 2 failed: %v", err)
	}

	first := <-received
	second := <-received
	if first.ConversationID != "conv-123" || second.ConversationID != "conv-123" {
		t.Fatalf("conversation continuity lost: got %q and %q", first.ConversationID, second.ConversationID)
	}
}

func TestTunnelClientCallWebSocket(t *testing.T) {
	t.Parallel()

	received := make(chan CallPayload, 4)
	apiKey := "sq_test_key_ws"

	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+apiKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "bye")
		for {
			var msg AgentResponse
			if err := wsjson.Read(r.Context(), conn, &msg); err != nil {
				if errors.Is(err, io.EOF) {
					return
				}
				return
			}
			if msg.Kind != KindCallRequest {
				continue
			}
			var p CallPayload
			_ = json.Unmarshal(msg.Payload, &p)
			received <- p
			_ = wsjson.Write(r.Context(), conn, AgentRequest{
				Kind:    KindCallResponse,
				CallID:  msg.CallID,
				Payload: []byte(`{"result":"ok"}`),
			})
		}
	}))
	defer wsServer.Close()

	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
	client := NewWithTransport(wsURL, apiKey, noopHandler{}, TransportWebSocket)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go client.Run(ctx)

	waitConnected(t, client)

	payload1, _ := json.Marshal(CallPayload{
		Task:           "ws one",
		SourceAgentID:  "caller-ws",
		ConversationID: "conv-ws-42",
	})
	if _, err := client.Call(context.Background(), "target-a", payload1); err != nil {
		t.Fatalf("ws call 1 failed: %v", err)
	}
	payload2, _ := json.Marshal(CallPayload{
		Task:           "ws two",
		SourceAgentID:  "caller-ws",
		ConversationID: "conv-ws-42",
	})
	if _, err := client.Call(context.Background(), "target-a", payload2); err != nil {
		t.Fatalf("ws call 2 failed: %v", err)
	}

	first := <-received
	second := <-received
	if first.ConversationID != "conv-ws-42" || second.ConversationID != "conv-ws-42" {
		t.Fatalf("conversation continuity lost over websocket: got %q and %q", first.ConversationID, second.ConversationID)
	}
}

type grpcTunnelService interface {
	Connect(grpcTunnelConnectServer) error
}

type grpcTunnelConnectServer interface {
	Send(*AgentRequest) error
	Recv() (*AgentResponse, error)
	grpc.ServerStream
}

type grpcConnectServer struct{ grpc.ServerStream }

func (s *grpcConnectServer) Send(m *AgentRequest) error { return s.ServerStream.SendMsg(m) }
func (s *grpcConnectServer) Recv() (*AgentResponse, error) {
	m := new(AgentResponse)
	if err := s.ServerStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

func grpcConnectHandler(srv interface{}, stream grpc.ServerStream) error {
	return srv.(grpcTunnelService).Connect(&grpcConnectServer{stream})
}

type grpcTestService struct {
	apiKey   string
	received chan<- CallPayload
}

func (s *grpcTestService) Connect(stream grpcTunnelConnectServer) error {
	md, ok := metadata.FromIncomingContext(stream.Context())
	if !ok {
		return status.Error(codes.Unauthenticated, "missing metadata")
	}
	keys := md.Get("x-api-key")
	if len(keys) == 0 || keys[0] != s.apiKey {
		return status.Error(codes.Unauthenticated, "invalid api key")
	}

	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		if msg.Kind != KindCallRequest {
			continue
		}
		var p CallPayload
		_ = json.Unmarshal(msg.Payload, &p)
		s.received <- p
		if err := stream.Send(&AgentRequest{
			Kind:    KindCallResponse,
			CallID:  msg.CallID,
			Payload: []byte(`{"result":"ok"}`),
		}); err != nil {
			return err
		}
	}
}
