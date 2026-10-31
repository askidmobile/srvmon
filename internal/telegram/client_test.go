package telegram

import (
	"crypto/tls"
	"net/http"
	"testing"
)

// The default client must not offer post-quantum key shares: DPI drops the
// resulting two-packet ClientHello on some networks.
func TestDefaultClientClassicCurves(t *testing.T) {
	tr, ok := defaultClient().Transport.(*http.Transport)
	if !ok || tr.TLSClientConfig == nil {
		t.Fatal("default client has no TLS config")
	}
	curves := tr.TLSClientConfig.CurvePreferences
	if len(curves) == 0 {
		t.Fatal("curve preferences are empty, Go would add X25519MLKEM768")
	}
	for _, c := range curves {
		if c == tls.X25519MLKEM768 {
			t.Fatal("X25519MLKEM768 is offered")
		}
	}
}
