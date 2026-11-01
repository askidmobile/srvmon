package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"

	"github.com/askidmobile/srvmon/internal/sshgw"
	"github.com/askidmobile/srvmon/internal/store"
)

// shellServer is an sshd stand-in whose shell prints a banner, echoes input
// and exits on "q".
func shellServer(t *testing.T, client ssh.PublicKey) (addr string, hostKey ssh.PublicKey) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if string(k.Marshal()) == string(client.Marshal()) {
				return nil, nil
			}
			return nil, io.EOF
		},
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					ch, creqs, _ := nc.Accept()
					go func() {
						defer ch.Close()
						for req := range creqs {
							switch req.Type {
							case "pty-req", "window-change":
								req.Reply(true, nil)
							case "shell":
								req.Reply(true, nil)
								io.WriteString(ch, "welcome привет\r\n$ ")
								buf := make([]byte, 64)
								for {
									n, err := ch.Read(buf)
									if err != nil || strings.Contains(string(buf[:n]), "q") {
										break
									}
									ch.Write(buf[:n])
								}
								ch.SendRequest("exit-status", false, ssh.Marshal(struct{ S uint32 }{0}))
								return
							default:
								req.Reply(false, nil)
							}
						}
					}()
				}
			}()
		}
	}()
	return ln.Addr().String(), signer.PublicKey()
}

func TestTerminalIsRecorded(t *testing.T) {
	e := recEnv(t)
	kp, err := sshgw.GenerateKey("srvmon")
	if err != nil {
		t.Fatal(err)
	}
	pub, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(kp.Public))
	addr, hostKey := shellServer(t, pub)
	host, portS, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portS)
	ctx := context.Background()
	id, _ := e.st.CreateServer(ctx, store.Server{Name: "web-1", Host: host})
	enc, _ := e.srv.secrets.Seal(kp.Private, keyAAD(id))
	if err := e.st.SetServerSSH(ctx, id, host, port, "srvmon", sshgw.FormatHostKey(hostKey), enc); err != nil {
		t.Fatal(err)
	}

	wsURL := "ws" + strings.TrimPrefix(e.ts.URL, "http") + "/api/servers/" + itoa(id) + "/terminal?cols=100&rows=30"
	h := http.Header{"Origin": {e.ts.URL}}
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: e.client, HTTPHeader: h})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var sawRec bool
	var out strings.Builder
	// The rec control frame and the shell prompt race: the SSH output pump
	// may start before the handler announces the recording.
	for !sawRec || !strings.Contains(out.String(), "$ ") {
		typ, data, err := conn.Read(rctx)
		if err != nil {
			t.Fatalf("read: %v (got %q)", err, out.String())
		}
		if typ == websocket.MessageText {
			var c struct {
				Type string `json:"type"`
				On   bool   `json:"on"`
			}
			json.Unmarshal(data, &c)
			sawRec = sawRec || (c.Type == "rec" && c.On)
			continue
		}
		out.Write(data)
	}
	if !sawRec {
		t.Fatal("browser not told about the recording")
	}
	conn.Write(rctx, websocket.MessageText, []byte(`{"type":"resize","cols":120,"rows":40}`))
	conn.Write(rctx, websocket.MessageBinary, []byte("ls\r"))
	// Wait for the echo: "q" in the same read would end the shell first.
	for echo := ""; !strings.Contains(echo, "ls"); {
		typ, data, err := conn.Read(rctx)
		if err != nil {
			t.Fatalf("echo: %v", err)
		}
		if typ == websocket.MessageBinary {
			echo += string(data)
		}
	}
	conn.Write(rctx, websocket.MessageBinary, []byte("q"))
	for {
		if _, _, err := conn.Read(rctx); err != nil {
			break
		}
	}

	var list []store.Recording
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		list, _ = e.st.ListRecordings(ctx, id, 0, 10)
		if len(list) == 1 && list[0].EndedAt != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(list) != 1 || list[0].EndedAt == nil || list[0].Target != "srvmon@web-1" || list[0].Cols != 100 {
		t.Fatalf("recordings %+v", list)
	}
	code, body := e.raw(t, "GET", "/api/recordings/"+itoa(list[0].ID)+"/cast", nil)
	if code != http.StatusOK || !strings.Contains(string(body), "welcome привет") ||
		!strings.Contains(string(body), `"r", "120x40"`) || !strings.Contains(string(body), `"ls\r"`) {
		t.Fatalf("cast %d %s", code, body)
	}
	// The echo is output; keystrokes themselves are never written.
	if strings.Contains(string(body), `"i",`) {
		t.Fatal("input events recorded")
	}
}
