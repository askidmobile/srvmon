// Package selfmon registers the monitoring host itself as a server, so the
// agent shipped in the server's compose stack shows up without manual steps.
package selfmon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/askidmobile/srvmon/internal/auth"
	"github.com/askidmobile/srvmon/internal/i18n"
	"github.com/askidmobile/srvmon/internal/store"
)

// Ensure makes sure a server named name exists and tokenFile holds a live
// agent token for it. An existing valid token is kept; a missing or unknown
// one (fresh install, restored database) is replaced. It returns true when a
// new token was written.
func Ensure(ctx context.Context, st *store.Store, name, tokenFile string) (bool, error) {
	if name == "" || tokenFile == "" {
		return false, errors.New("selfmon: name and token file are required")
	}
	if b, err := os.ReadFile(tokenFile); err == nil {
		if tok := strings.TrimSpace(string(b)); tok != "" {
			ok, err := st.AgentTokenValid(ctx, auth.HashToken(tok))
			if err != nil {
				return false, err
			}
			if ok {
				return false, nil
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("selfmon: read token: %w", err)
	}

	id, err := st.ServerIDByName(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		id, err = st.CreateServer(ctx, store.Server{
			Name:        name,
			Group:       "infra",
			Description: i18n.T("monitoring server"),
			Tags:        []string{"srvmon"},
		})
	}
	if err != nil {
		return false, fmt.Errorf("selfmon: server: %w", err)
	}
	tok, err := auth.NewToken("a_")
	if err != nil {
		return false, err
	}
	if err := st.AddAgentToken(ctx, id, auth.HashToken(tok)); err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(tokenFile), 0o700); err != nil {
		return false, err
	}
	tmp := tokenFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(tok+"\n"), 0o600); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, tokenFile)
}
