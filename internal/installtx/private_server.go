package installtx

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"sync"
	"time"
)

// ServePrivate serves only the separate maintenance listener. It cannot set a
// control emitter, create a GUI session or displace the business listener.
func ServePrivate(ctx context.Context, listener net.Listener, endpoint PrivateEndpoint) error {
	return servePrivate(ctx, listener, endpoint, privateRootPeer)
}
func servePrivate(ctx context.Context, listener net.Listener, endpoint PrivateEndpoint, authorize func(net.Conn) bool) error {
	if listener == nil || authorize == nil || !protocolID(endpoint.TxID, "it1_") || endpoint.Info == nil || endpoint.Admit == nil {
		return ErrProtocol
	}
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	var workers sync.WaitGroup
	defer workers.Wait()
	slots := make(chan struct{}, 8)
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if !authorize(conn) {
			conn.Close()
			continue
		}
		select {
		case slots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-slots }()
			defer conn.Close()
			one, stop := context.WithTimeout(ctx, 2*time.Second)
			defer stop()
			end, _ := one.Deadline()
			if conn.SetDeadline(end) != nil {
				return
			}
			closeOnCancel := context.AfterFunc(one, func() { conn.Close() })
			defer closeOnCancel()
			line, err := bufio.NewReaderSize(conn, MaxMetadataBytes+1).ReadSlice('\n')
			if err != nil || len(line) > MaxMetadataBytes {
				return
			}
			request, err := DecodePrivateRequest(line)
			if err != nil {
				return
			}
			reply, err := endpoint.handle(one, request)
			if err != nil {
				// Never return raw callback/path/storage errors on this wire.
				conn.Write([]byte("{\"schema\":1,\"status\":\"refused\"}\n"))
				return
			}
			data, err := json.Marshal(reply)
			if err != nil {
				return
			}
			conn.Write(append(data, '\n'))
		}()
	}
}
