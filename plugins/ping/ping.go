package ping

import (
	"context"

	"github.com/go-logr/logr"
	. "github.com/minekube/gate-plugin-template/util"
	"github.com/robinbraemer/event"
	"go.minekube.com/gate/pkg/edition/java/proxy"
)

// Plugin is a ping plugin that handles ping events.
var Plugin = proxy.Plugin{
	Name: "Ping",
	Init: func(ctx context.Context, p *proxy.Proxy) error {
		log := logr.FromContextOrDiscard(ctx)
		log.Info("Hello from Ping plugin!")

		event.Subscribe(p.Event(), 0, onPing())

		return nil
	},
}

func onPing() func(*proxy.PingEvent) {
	return func(e *proxy.PingEvent) {
		line1 := Text("                 &dMultiverse Mystery &a[1.21.4]")
		line2 := Text("\n              &6&lBlockHunt &7- &b&lParkour")

		p := e.Ping()
		p.Description = Join(line1, line2)
		p.Players.Max = p.Players.Online + 1
	}
}
