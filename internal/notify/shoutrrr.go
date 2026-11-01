package notify

import (
	"context"
	"crypto/tls"
	"net/http"
	"time"

	"github.com/nicholas-fedor/shoutrrr/pkg/router"
	"github.com/nicholas-fedor/shoutrrr/pkg/types"
)

// sendTimeout bounds one delivery to one service.
const sendTimeout = 20 * time.Second

// Shoutrrr sends through github.com/nicholas-fedor/shoutrrr.
type Shoutrrr struct {
	client *http.Client
}

// NewShoutrrr returns the default sender. Like the Telegram bot it offers
// classic TLS key exchange only: some networks drop the two-packet
// ClientHello of the post-quantum share (see telegram.defaultClient).
func NewShoutrrr() *Shoutrrr {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{
		MinVersion:       tls.VersionTLS12,
		CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384},
	}
	return &Shoutrrr{client: &http.Client{Timeout: sendTimeout, Transport: tr}}
}

func (s *Shoutrrr) locate(rawURL string) (types.Service, error) {
	r, err := router.NewWithOptions(nil, types.SenderOptions{HTTPClient: s.client, Timeout: sendTimeout})
	if err != nil {
		return nil, err
	}
	return r.Locate(rawURL)
}

// Check parses the URL and initializes its service without sending.
func (s *Shoutrrr) Check(rawURL string) error {
	_, err := s.locate(rawURL)
	return err
}

// Send delivers one message.
func (s *Shoutrrr) Send(ctx context.Context, rawURL, title, body string) error {
	svc, err := s.locate(rawURL)
	if err != nil {
		return err
	}
	params := &types.Params{types.TitleKey: title}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	if cs, ok := svc.(types.ContextSender); ok {
		return cs.SendContext(ctx, body, params)
	}
	done := make(chan error, 1)
	go func() { done <- svc.Send(body, params) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Services lists the URL schemes this build supports.
func Services() []string { return router.SupportedSchemas() }
