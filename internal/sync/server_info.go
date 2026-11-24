package sync

import (
	"context"

	"github.com/emersion/go-imap/v2"
	imapadapter "github.com/zhecho/m9s/internal/mail/imap"
)

// serverIDText renders an IMAP ID answer as "name / version": the name falls
// back to the vendor, a missing version is "-". Empty when there is no name.
func serverIDText(id *imap.IDData) string {
	if id == nil {
		return ""
	}
	name := id.Name
	if name == "" {
		name = id.Vendor
	}
	if name == "" {
		return ""
	}
	version := id.Version
	if version == "" {
		version = "-"
	}
	return name + " / " + version
}

// sendServerInfo asks the cmd connection's server who it is, once per engine
// run: after a success, reconnects do not ask again. A failure is ignored;
// the session goes on and the next connect tries again.
func (e *Engine) sendServerInfo(ctx context.Context) {
	if e.serverInfoSent.Load() {
		return
	}
	ider, ok := e.cmdConn.(imapadapter.ServerIdentifier)
	if !ok {
		return
	}
	id, err := ider.ServerID(ctx)
	if err != nil {
		return
	}
	e.serverInfoSent.Store(true)
	if text := serverIDText(id); text != "" {
		e.emit(ctx, ServerInfo{AccountName: e.cfg.Name, Text: text})
	}
}
