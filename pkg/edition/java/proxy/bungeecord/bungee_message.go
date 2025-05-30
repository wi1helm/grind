package bungeecord

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"go.minekube.com/common/minecraft/component"
	"go.minekube.com/common/minecraft/component/codec"
	"go.minekube.com/common/minecraft/component/codec/legacy"
	"go.minekube.com/common/minecraft/key"
	"go.minekube.com/gate/pkg/edition/java/proto/packet/plugin"
	"go.minekube.com/gate/pkg/edition/java/proto/util"
	"go.minekube.com/gate/pkg/edition/java/proto/version"
	"go.minekube.com/gate/pkg/edition/java/proxy/message"
	"go.minekube.com/gate/pkg/gate/proto"
	"go.minekube.com/gate/pkg/util/netutil"
	"go.minekube.com/gate/pkg/util/uuid"
)

// MessageResponder is a message responder for BungeeCord plugin channels.
type MessageResponder interface {
	// Process processes the given plugin message.
	// Returns true if the message is a BungeeCord plugin message and was processed.
	Process(message *plugin.Message) bool
}

// NopMessageResponder is a MessageResponder that does not process messages.
var NopMessageResponder MessageResponder = &nopMessageResponder{}

// Dependencies required by NewMessageResponder.
type (
	Providers interface {
		PlayerProvider
		ServerProvider
		ServerConnectionProvider
	}
	// PlayerProvider provides a player by its name.
	PlayerProvider interface {
		PlayerByName(username string) Player
		PlayerCount() int // Total number of players online
		Players() []Player
		BroadcastMessage(component.Component)
	}
	// ServerProvider provides servers.
	ServerProvider interface {
		Server(name string) Server
		Servers() []Server
		AddJoinableServer(name string)
		RemoveJoinableServer(name string)
	}
	// ServerConnectionProvider provides the currently connected server connection for a player.
	ServerConnectionProvider interface {
		ConnectedServer() ServerConnection
	}
	// ServerConnection represents a server connection for a player.
	ServerConnection interface {
		Name() string // Name of the server.
		Protocol() proto.Protocol
		proto.PacketWriter
	}
	Player interface {
		ID() uuid.UUID
		Username() string
		RemoteAddr() net.Addr
		Disconnect(reason component.Component)
		Protocol() proto.Protocol
	}
	Server interface {
		Name() string
		PlayerCount() int
		BroadcastPluginMessage(message.ChannelIdentifier, []byte)
		Connect(Player)
		Players() []Player
		BroadcastMessage(component.Component)
		Addr() net.Addr
	}
)

// NewMessageResponder returns a new MessageResponder.
func NewMessageResponder(
	player Player,
	providers Providers,
) MessageResponder {
	return &bungeeCordMessageResponder{
		player:    player,
		Providers: providers,
	}
}

type bungeeCordMessageResponder struct {
	player Player // The player of this responder.
	Providers
}

var (
	bungeeCordModernChannel = (&message.MinecraftChannelIdentifier{Key: key.New("bungeecord", "main")}).ID()
	bungeeCordLegacyChannel = message.LegacyChannelIdentifier("BungeeCord")
)

// Channel returns the BungeeCord plugin channel identifier for the given protocol.
func Channel(protocol proto.Protocol) string {
	if protocol.GreaterEqual(version.Minecraft_1_13) {
		return bungeeCordModernChannel
	}
	return bungeeCordLegacyChannel.ID()
}

func IsBungeeCordMessage(message *plugin.Message) bool {
	return strings.EqualFold(bungeeCordModernChannel, message.Channel) ||
		strings.EqualFold(bungeeCordLegacyChannel.ID(), message.Channel)
}

func (r *bungeeCordMessageResponder) Process(message *plugin.Message) bool {
	if !IsBungeeCordMessage(message) {
		return false
	}

	in := bytes.NewReader(message.Data)
	subChannel, err := util.ReadUTF(in) // read first sequence
	if err != nil {
		return false
	}
	switch subChannel {
	case "ForwardToPlayer":
		r.processForwardToPlayer(in)
	case "Forward":
		r.processForwardToServer(in)
	case "Connect":
		r.processConnect(in)
	case "ConnectOther":
		r.processConnectOther(in)
	case "IP":
		r.processIP()
	case "IPOther":
		r.processIPOther(in)
	case "UUID":
		r.processUUID()
	case "UUIDOther":
		r.processUUIDOther(in)
	case "PlayerCount":
		r.processPlayerCount(in)
	case "PlayerList":
		r.processPlayerList(in)
	case "GetServers":
		r.processGetServers()
	case "GetServer":
		r.processGetServer()
	case "Message":
		r.processMessage(in)
	case "MessageRaw":
		r.processMessageRaw(in)
	case "ServerIP":
		r.processServerIP(in)
	case "KickPlayer":
		r.processKick(in)
	case "KickPlayerRaw":
		r.processKickRaw(in)
	case "AddJoinableServer":
		r.processAddJoinableServer(in)
	case "RemoveJoinableServer":
		r.processRemoveJoinableServer(in)
	case "ServerSelectorInfo":
		r.processServerSelectorInfo()

	default:
		// Unknown sub-channel, do nothing
	}
	return true
}

func (r *bungeeCordMessageResponder) prepareForwardMessage(in io.Reader) (forward []byte) {
	channel, err := util.ReadUTF(in)
	if err != nil {
		return
	}
	messageLen, err := util.ReadInt16(in)
	if err != nil {
		return
	}
	msg := make([]byte, messageLen)
	_, err = io.ReadFull(in, msg)
	if err != nil {
		return
	}

	forwarded := new(bytes.Buffer)
	forwarded.WriteString(channel)
	_ = util.WriteInt16(forwarded, messageLen)
	forwarded.Write(msg)
	return forwarded.Bytes()
}

func (r *bungeeCordMessageResponder) sendServerResponse(in []byte) {
	if len(in) == 0 {
		return
	}
	serverConn := r.ConnectedServer()
	if serverConn == nil {
		return
	}
	ch := Channel(serverConn.Protocol())
	_ = serverConn.WritePacket(&plugin.Message{Channel: ch, Data: in})
}

func (r *bungeeCordMessageResponder) processForwardToPlayer(in io.Reader) {
	r.readPlayer(in, func(player Player) {
		r.sendServerResponse(r.prepareForwardMessage(in))
	})
}

func (r *bungeeCordMessageResponder) processForwardToServer(in io.Reader) {
	target, err := util.ReadUTF(in)
	if err != nil {
		return
	}
	forward := r.prepareForwardMessage(in)
	if strings.EqualFold(target, "ALL") || strings.EqualFold(target, "ONLINE") {
		var currentUserServer string
		if s := r.ConnectedServer(); s != nil {
			currentUserServer = s.Name()
		}
		// Broadcast message to players on all servers except the current one
		for _, server := range r.Servers() {
			if server.Name() == currentUserServer {
				continue // skip current server
			}
			server.BroadcastPluginMessage(bungeeCordLegacyChannel, forward)
		}
	} else {
		if server := r.Server(target); server != nil {
			server.BroadcastPluginMessage(bungeeCordLegacyChannel, forward)
		}
	}
}

func (r *bungeeCordMessageResponder) processConnect(in io.Reader) {
	r.readServer(in, func(server Server) {
		r.connect(r.player, server)
	})
}
func (r *bungeeCordMessageResponder) processConnectOther(in io.Reader) {
	r.readPlayer(in, func(player Player) {
		r.readServer(in, func(server Server) {
			r.connect(player, server)
		})
	})
}
func (r *bungeeCordMessageResponder) connect(cr Player, server Server) {
	server.Connect(cr)
}

func (r *bungeeCordMessageResponder) processIP() {
	host, port := netutil.HostPort(r.player.RemoteAddr())
	b := new(bytes.Buffer)
	_ = util.WriteUTF(b, "IP")
	_ = util.WriteUTF(b, host)
	_ = util.WriteInt32(b, int32(port))
	r.sendServerResponse(b.Bytes())
}

func (r *bungeeCordMessageResponder) processIPOther(in io.Reader) {
	r.readPlayer(in, func(player Player) {
		host, port := netutil.HostPort(player.RemoteAddr())
		b := new(bytes.Buffer)
		_ = util.WriteUTF(b, "IPOther")
		_ = util.WriteUTF(b, player.Username())
		_ = util.WriteUTF(b, host)
		_ = util.WriteInt32(b, int32(port))
		r.sendServerResponse(b.Bytes())
	})
}

func (r *bungeeCordMessageResponder) processPlayerCount(in io.Reader) {
	target, err := util.ReadUTF(in)
	if err != nil {
		return
	}
	var (
		count int
		name  = "ALL"
	)
	if strings.EqualFold(target, name) {
		count = r.PlayerCount()
	} else {
		s := r.Server(target)
		if s == nil {
			return
		}
		name = s.Name()
		count = s.PlayerCount()
	}

	b := new(bytes.Buffer)
	_ = util.WriteUTF(b, "PlayerCount")
	_ = util.WriteUTF(b, name)
	_ = util.WriteInt32(b, int32(count))

	r.sendServerResponse(b.Bytes())
}

func (r *bungeeCordMessageResponder) processPlayerList(in io.Reader) {
	target, err := util.ReadUTF(in)
	if err != nil {
		return
	}
	var (
		name    = "ALL"
		players []Player
	)
	if target == name {
		players = r.Players()
	} else {
		server := r.Server(target)
		if server == nil {
			return
		}
		name = server.Name()
		players = server.Players()
	}

	list := joiner{split: ", "}
	for _, player := range players {
		list.write(player.Username())
	}

	b := new(bytes.Buffer)
	_ = util.WriteUTF(b, "PlayerList")
	_ = util.WriteUTF(b, name)
	_ = util.WriteUTF(b, list.String())

	r.sendServerResponse(b.Bytes())
}

func (r *bungeeCordMessageResponder) processGetServers() {
	list := joiner{split: ", "}
	for _, server := range r.Servers() {
		list.write(server.Name())
	}
	b := new(bytes.Buffer)
	_ = util.WriteUTF(b, "GetServers")
	_ = util.WriteUTF(b, list.String())
	r.sendServerResponse(b.Bytes())
}

func (r *bungeeCordMessageResponder) processMessage0(in io.Reader, decoder codec.Unmarshaler) {
	target, err := util.ReadUTF(in)
	if err != nil {
		return
	}
	msg, err := util.ReadUTF(in)
	if err != nil {
		return
	}

	comp, err := decoder.Unmarshal([]byte(msg))
	if err != nil {
		return
	}
	if target == "ALL" {
		r.BroadcastMessage(comp)
	} else {
		r.Server(target).BroadcastMessage(comp)
	}
}
func (r *bungeeCordMessageResponder) processMessage(in io.Reader) {
	r.processMessage0(in, &legacy.Legacy{})
}
func (r *bungeeCordMessageResponder) processMessageRaw(in io.Reader) {
	r.processMessage0(in, util.DefaultJsonCodec())
}

func (r *bungeeCordMessageResponder) processGetServer() {
	s := r.ConnectedServer()
	if s == nil {
		return
	}
	b := new(bytes.Buffer)
	_ = util.WriteUTF(b, "GetServer")
	_ = util.WriteUTF(b, s.Name())
	r.sendServerResponse(b.Bytes())
}

func (r *bungeeCordMessageResponder) processUUID() {
	b := new(bytes.Buffer)
	_ = util.WriteUTF(b, "UUID")
	_ = util.WriteUTF(b, r.player.ID().Undashed())
	r.sendServerResponse(b.Bytes())
}
func (r *bungeeCordMessageResponder) processUUIDOther(in io.Reader) {
	r.readPlayer(in, func(player Player) {
		b := new(bytes.Buffer)
		_ = util.WriteUTF(b, "UUIDOther")
		_ = util.WriteUTF(b, player.Username())
		_ = util.WriteUTF(b, player.ID().Undashed())
		r.sendServerResponse(b.Bytes())
	})
}

func (r *bungeeCordMessageResponder) processServerIP(in io.Reader) {
	r.readServer(in, func(server Server) {
		host, port := netutil.HostPort(server.Addr())
		b := new(bytes.Buffer)
		_ = util.WriteUTF(b, "ServerIP")
		_ = util.WriteUTF(b, server.Name())
		_ = util.WriteUTF(b, host)
		_ = util.WriteInt16(b, int16(port))
		r.sendServerResponse(b.Bytes())
	})
}

func (r *bungeeCordMessageResponder) processKick(in io.Reader) {
	r.readPlayer(in, func(player Player) {
		msg, err := util.ReadUTF(in)
		if err != nil {
			return
		}
		kickReason, err := (&legacy.Legacy{}).Unmarshal([]byte(msg))
		if err != nil {
			kickReason = &component.Text{} // fallback to blank reason
		}
		player.Disconnect(kickReason)
	})
}

func (r *bungeeCordMessageResponder) processKickRaw(in io.Reader) {
	r.readPlayer(in, func(player Player) {
		msg, err := util.ReadUTF(in)
		if err != nil {
			return
		}
		kickReason, err := util.JsonCodec(r.player.Protocol()).Unmarshal([]byte(msg))
		if err != nil {
			kickReason = &component.Text{} // fallback to blank reason
		}
		player.Disconnect(kickReason)
	})
}

func (r *bungeeCordMessageResponder) processAddJoinableServer(in io.Reader) {
	serverName, err := util.ReadUTF(in)
	if err != nil {
		return
	}
	r.AddJoinableServer(serverName)
}

func (r *bungeeCordMessageResponder) processRemoveJoinableServer(in io.Reader) {
	serverName, err := util.ReadUTF(in)
	if err != nil {
		return
	}
	r.RemoveJoinableServer(serverName)
}

func (r *bungeeCordMessageResponder) processServerSelectorInfo() {
	// Create buffer for the response message
	b := new(bytes.Buffer)
	_ = util.WriteUTF(b, "ServerSelectorInfo")

	// Collect server names and player counts
	serverNames := joiner{split: " "}
	playerCounts := joiner{split: " "}
	offlineServers := joiner{split: " "}
	for _, server := range r.Servers() {
		// Check if the server is online before including it
		if !serverIsOnline(server) {
			// Skip this server
			offlineServers.write(server.Name())
		}

		serverNames.write(server.Name())
		playerCounts.write(fmt.Sprintf("%d", server.PlayerCount()))
	}

	// Include the list of server names
	_ = util.WriteUTF(b, serverNames.String())

	// Include the list of player counts as space-separated values
	_ = util.WriteUTF(b, playerCounts.String())

	// Include the list of offline servers as space-separated values
	_ = util.WriteUTF(b, offlineServers.String())

	// Include the player's current server name
	if currentServer := r.ConnectedServer(); currentServer != nil {
		_ = util.WriteUTF(b, currentServer.Name())
	} else {
		_ = util.WriteUTF(b, "") // Empty if no current server
	}
	// Send the constructed message as a response
	r.sendServerResponse(b.Bytes())
}

func serverIsOnline(server Server) bool {
	// Attempt to establish a TCP connection to the server's address
	conn, err := net.DialTimeout("tcp", server.Addr().String(), 500*time.Millisecond)
	if err != nil {
		// Connection failed; server is likely offline
		return false
	}
	conn.Close()
	return true
}

//
//
//

type (
	playerFn func(p Player)
	serverFn func(s Server)
)

func (r *bungeeCordMessageResponder) readServer(in io.Reader, fn serverFn) {
	name, err := util.ReadUTF(in)
	if err != nil {
		return
	}
	server := r.Server(name)
	if server != nil {
		fn(server)
	}
}
func (r *bungeeCordMessageResponder) readPlayer(in io.Reader, fn playerFn) {
	name, err := util.ReadUTF(in)
	if err != nil {
		return
	}
	player := r.PlayerByName(name)
	if player != nil {
		fn(player)
	}
}

// joiner joins strings with a spliterator
type joiner struct {
	split string
	b     strings.Builder
}

func (j *joiner) write(s string) {
	if j.b.Len() != 0 {
		j.b.WriteString(j.split)
	}
	j.b.WriteString(s)
}

func (j *joiner) String() string {
	return j.b.String()
}

type nopMessageResponder struct{}

func (n *nopMessageResponder) Process(*plugin.Message) bool { return false }

var _ MessageResponder = (*nopMessageResponder)(nil)
