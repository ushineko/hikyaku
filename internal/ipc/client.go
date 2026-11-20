package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

// ErrNoService is returned when nothing is listening on the socket.
var ErrNoService = errors.New("the hikyaku service is not running; start it with `hikyaku serve`")

// dialTimeout bounds how long a CLI waits to learn there is no service (R9).
const dialTimeout = time.Second

// Call sends one request to the service at socketPath and returns its
// response. Cancelling ctx closes the connection, which the service reads as
// the client going away.
func Call(ctx context.Context, socketPath string, req Request) (Response, error) {
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, fmt.Errorf("connect to service: %w", ctx.Err())
		}
		return Response{}, ErrNoService
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	b, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("encode request: %w", err)
	}
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return Response{}, fmt.Errorf("send request: %w", err)
	}
	r := bufio.NewReaderSize(conn, 64<<10)
	line, err := readLine(r)
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, fmt.Errorf("wait for service: %w", ctx.Err())
		}
		return Response{}, fmt.Errorf("the service closed the connection: %w", err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return Response{}, fmt.Errorf("decode response: %w", err)
	}
	return resp, nil
}
