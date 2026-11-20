package service

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connectproto"

	wv1 "github.com/openbeehive-org/openbeehive-app/server/internal/gen/openbeehive/v1"
)

// Request headers with which a client says what it is (see sync.proto).
const (
	HeaderSyncProtocol = "Openbeehive-Sync-Protocol"
	HeaderAppVersion   = "Openbeehive-App-Version"
)

// protocolSnapshots is the first protocol version: snapshots, reset signals
// and cursors bound to a database. A client that sends no protocol header
// (version 0) is an app of release 0.4.x.
const protocolSnapshots = 1

// syncClient is what a request says about the app that sent it.
type syncClient struct {
	protocol   int
	appVersion string
}

// legacy reports a 0.4.x app, which is served exactly what release 0.4.1
// would have sent it.
func (c syncClient) legacy() bool { return c.protocol < protocolSnapshots }

type syncClientKey struct{}

// WithSyncProtocol marks ctx as a call from a client speaking the given
// protocol version, for callers that do not come in over HTTP.
func WithSyncProtocol(ctx context.Context, protocol int) context.Context {
	return context.WithValue(ctx, syncClientKey{}, syncClient{protocol: protocol})
}

// clientOf reads the client's protocol and app version from the request
// headers. A missing or unreadable protocol header means a legacy client.
func clientOf(ctx context.Context) syncClient {
	if c, ok := ctx.Value(syncClientKey{}).(syncClient); ok {
		return c
	}
	info, ok := connect.CallInfoForServerContext(ctx)
	if !ok {
		return syncClient{}
	}
	h := info.RequestHeader()
	c := syncClient{appVersion: clip(h.Get(HeaderAppVersion))}
	if n, err := strconv.Atoi(strings.TrimSpace(h.Get(HeaderSyncProtocol))); err == nil && n > 0 {
		c.protocol = n
	}
	return c
}

// admit identifies the client and refuses it when it speaks an older
// protocol than this server is configured to accept.
func (s *SyncService) admit(ctx context.Context) (syncClient, error) {
	c := clientOf(ctx)
	if c.protocol < s.MinProtocol {
		// One log line per protocol version, not one per request: a refused
		// app keeps asking.
		if _, logged := s.refused.LoadOrStore(c.protocol, true); !logged {
			log.Printf("sync: refusing app version %q speaking sync protocol %d; BEEHIVE_SYNC_MIN_PROTOCOL is %d",
				c.appVersion, c.protocol, s.MinProtocol)
		}
		return c, upgradeRequired(s.MinProtocol, fmt.Sprintf(
			"this server accepts sync protocol %d and newer; the app speaks %d", s.MinProtocol, c.protocol))
	}
	return c, nil
}

// upgradeRequired is the failure of a sync call that an older app cannot
// make any more. Apps that know the UpgradeRequired detail tell the user to
// update; older ones see the message in their log and keep retrying.
func upgradeRequired(minProtocol int, reason string) error {
	err := connect.NewError(connect.CodeFailedPrecondition, "upgrade required: "+reason+". Update the app.")
	if detail, derr := connectproto.NewErrorDetail(&wv1.UpgradeRequired{MinProtocol: i32(int64(minProtocol))}); derr == nil {
		err = err.WithDetail(detail)
	}
	return err
}
