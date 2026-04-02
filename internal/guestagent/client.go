package guestagent

import (
	"fmt"
	"io"
	"os"

	"github.com/mdlayher/vsock"
)

const (
	AgentPort  uint32 = 10000
	DefaultCID uint32 = 3
)

// Client connects to the guest agent over vsock.
type Client struct {
	conn *vsock.Conn
}

// Dial connects to the guest agent at the given CID.
func Dial(cid uint32) (*Client, error) {
	conn, err := vsock.Dial(cid, AgentPort, nil)
	if err != nil {
		return nil, fmt.Errorf("vsock dial cid=%d port=%d: %w", cid, AgentPort, err)
	}
	return &Client{conn: conn}, nil
}

// Close closes the connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// Ping checks if the guest agent is alive.
func (c *Client) Ping() error {
	if err := WriteMessage(c.conn, &Request{Type: "Ping"}); err != nil {
		return err
	}
	var resp Response
	if err := ReadMessage(c.conn, &resp); err != nil {
		return err
	}
	if resp.Type != "Pong" {
		return fmt.Errorf("unexpected response: %s", resp.Type)
	}
	return nil
}

// Exec runs a command in the guest and streams output to stdout/stderr.
// Returns the exit code.
func (c *Client) Exec(command []string, workdir string) (int, error) {
	req := Request{
		Type:    "Exec",
		Command: command,
		Workdir: workdir,
	}
	if err := WriteMessage(c.conn, &req); err != nil {
		return 1, fmt.Errorf("send exec request: %w", err)
	}

	for {
		var resp Response
		if err := ReadMessage(c.conn, &resp); err != nil {
			if err == io.EOF {
				return 1, fmt.Errorf("agent connection closed unexpectedly")
			}
			return 1, fmt.Errorf("read response: %w", err)
		}

		switch resp.Type {
		case "Output":
			switch resp.Stream {
			case "stderr":
				os.Stderr.Write(resp.Data)
			default:
				os.Stdout.Write(resp.Data)
			}
		case "Exit":
			return resp.ExitCode, nil
		case "Error":
			return 1, fmt.Errorf("agent error: %s", resp.Message)
		}
	}
}
