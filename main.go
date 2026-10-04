package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func main() {
	p := tea.NewProgram(login())
	final, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERR: %v\n", err)
	}
	// if a burn clear screen and scrollback buffer
	if c, ok := final.(chatModel); ok && c.burned {
		fmt.Print("\033[H\033[2J\033[3J")
	}
}

type chatModel struct {
	conn        *net.UDPConn
	peer        *net.UDPAddr
	incoming    chan tea.Msg
	viewport    viewport.Model
	messages    []string
	textarea    textarea.Model
	senderStyle lipgloss.Style
	peerStyle   lipgloss.Style
	burned      bool
	lastHeard   time.Time
	sendErr     error // last local write failure, nil while sends are leaving fine
}

// every datagram is one of these, his module uses the same shape
type packet struct {
	Action  string `json:"action"`
	Message string `json:"message,omitempty"`
}

const (
	actionMessage   = "MESSAGE"
	actionDestroy   = "DESTROY"
	actionHeartbeat = "HEARTBEAT"
)

const (
	heartbeatEvery = 5 * time.Second
	peerTimeout    = 15 * time.Second // three missed beats
)

func send(conn *net.UDPConn, to *net.UDPAddr, p packet) error {
	b, _ := json.Marshal(p)
	_, err := conn.WriteToUDP(b, to)
	return err
}

// what the reader goroutine hands back to the update loop
type peerMsg string

type peerBurnMsg struct{}

type peerHeartbeatMsg struct{}

// fires on a timer, tells us to send our own heartbeat
type beatMsg struct{}

func beat() tea.Cmd {
	return tea.Tick(heartbeatEvery, func(time.Time) tea.Msg { return beatMsg{} })
}

type connLostMsg struct{}

// sits on the socket forever and feeds whatever arrives into ch
func readLoop(conn *net.UDPConn, peer *net.UDPAddr, ch chan<- tea.Msg) {
	buf := make([]byte, 1500)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			close(ch) // socket died or got closed, tell the ui
			return
		}
		// anyone who knows the ip:port could blast packets at client, so only
		// render the ones that came from the peer the server paired us with
		if !from.IP.Equal(peer.IP) || from.Port != peer.Port {
			continue
		}

		var p packet
		if json.Unmarshal(buf[:n], &p) == nil {
			switch p.Action {

			case actionMessage:
				ch <- peerMsg(p.Message)

			case actionDestroy:
				ch <- peerBurnMsg{}

			case actionHeartbeat:
				ch <- peerHeartbeatMsg{}
			}
		}
		clear(buf) // don't leave the last datagram sitting
	}
}

// blocks until the next message shows up. has to be re-issued after every
// delivery, a cmd only ever produces one msg
func recv(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return connLostMsg{}
		}
		return msg
	}
}

// conn is our socket, peer is who we're talking to
func chat(conn *net.UDPConn, peer *net.UDPAddr) chatModel {
	ta := textarea.New()
	ta.Placeholder = "Send a message..."
	ta.SetVirtualCursor(false)
	ta.Focus()

	ta.Prompt = "┃ "
	ta.CharLimit = 280

	ta.SetWidth(30)
	ta.SetHeight(3)

	// Remove cursor line styling
	s := ta.Styles()
	s.Focused.CursorLine = lipgloss.NewStyle()
	ta.SetStyles(s)

	ta.ShowLineNumbers = false

	vp := viewport.New(viewport.WithWidth(30), viewport.WithHeight(5))
	vp.SetContent(`Type a message and press Enter to send.`)
	// left/right belong to the text cursor, don't let them scroll the viewport
	vp.KeyMap.Left.SetEnabled(false)
	vp.KeyMap.Right.SetEnabled(false)

	// enter will send the message instead of adding a newline
	ta.KeyMap.InsertNewline.SetEnabled(false)

	lastHeard := time.Now()
	incoming := make(chan tea.Msg, 16)
	go readLoop(conn, peer, incoming)

	return chatModel{
		conn:        conn,
		peer:        peer,
		incoming:    incoming,
		lastHeard:   lastHeard,
		textarea:    ta,
		messages:    []string{},
		viewport:    vp,
		senderStyle: lipgloss.NewStyle().Foreground(lipgloss.Color("#4024f5")),
		peerStyle:   lipgloss.NewStyle().Foreground(lipgloss.Color("#f52482")),
	}
}

func (m *chatModel) burn(notify bool) {
	if m.conn != nil {
		if notify {
			// udp may drops packets so say it three times
			for range 3 {
				// ===== PENDING MODULE =====
				// swap for: DestroySession(m.conn, m.peer)
				send(m.conn, m.peer, packet{Action: actionDestroy})
			}
		}
		m.conn.Close()
	}
	m.messages = nil
	m.textarea.Reset()
	m.viewport.SetContent("")
	m.burned = true
}

// wraps the backlog to the current width and pins us to the newest line
func (m *chatModel) render() {
	m.viewport.SetContent(lipgloss.NewStyle().Width(m.viewport.Width()).Render(strings.Join(m.messages, "\n")))
	m.viewport.GotoBottom()
}

// every new line goes through here so the wrapping and scrolling stay in one place
func (m *chatModel) appendMsg(line string) {
	m.messages = append(m.messages, line)
	m.render()
}

func (m chatModel) Init() tea.Cmd {
	return tea.Batch(recv(m.incoming), beat())
}

// the footer. only redraws when something happens, so the elapsed time steps
// along with the heartbeat rather than ticking every second
func (m chatModel) statusLine() string {
	color, text := "#3ddc84", "\u25cf connected"

	switch {
	// our own write failed, so the packet never left this machine. known
	// straight away, no waiting on a timeout
	case m.sendErr != nil:
		color, text = "#f5245e", "\u2715 offline | messages cannot send"

	// sends are leaving fine but nothing comes back, so it's them or the path
	case time.Since(m.lastHeard) > peerTimeout:
		color = "#f5a524"
		text = "\u25cb disconnected? time since last ping: " + time.Since(m.lastHeard).Round(time.Second).String()
	}

	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(text)
}

func (m chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case beatMsg:
		m.sendErr = send(m.conn, m.peer, packet{Action: actionHeartbeat})
		return m, beat() // the tick is also what refreshes the footer

	case peerHeartbeatMsg:
		m.lastHeard = time.Now()
		return m, recv(m.incoming)

	case peerMsg:
		m.lastHeard = time.Now()
		m.appendMsg(m.peerStyle.Render("0xDEADBEEF: ") + string(msg))
		return m, recv(m.incoming) // re arm for the next one

	case peerBurnMsg:
		// they burned it, so do the same here. notify false, don't echo it back
		m.burn(false)
		return m, tea.Quit

	case connLostMsg:
		m.appendMsg("!!! connection closed !!!")
		return m, nil // nothing left to listen to so don't re arm it

	case tea.WindowSizeMsg:
		m.viewport.SetWidth(msg.Width)
		m.textarea.SetWidth(msg.Width)
		m.viewport.SetHeight(msg.Height - m.textarea.Height() - 1) // -1 for the status line

		m.render() // rewrap the backlog at the new width
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			// esc just ends the connection, it isn't a burn
			if m.conn != nil {
				m.conn.Close()
			}
			return m, tea.Quit
		case "ctrl+b":
			// same as typing /burn, for when you need it now
			m.burn(true)
			return m, tea.Quit
		case "enter":
			text := m.textarea.Value()
			if text == "" {
				return m, nil
			}
			// literal match only, so you can still talk about burning
			if text == "/burn" {
				m.burn(true)
				return m, tea.Quit
			}
			// one message per datagram, no framing. the write just hands the
			// bytes to the kernel, so it's cheap enough to do inline

			// ===== PENDING MODULE =====
			// swap for: SendMessage(m.conn, m.peer, text), once it returns error
			m.sendErr = send(m.conn, m.peer, packet{Action: actionMessage, Message: text})
			if m.sendErr != nil {
				m.appendMsg("!!ERR!! send failed: " + m.sendErr.Error())
				return m, nil
			}

			m.appendMsg(m.senderStyle.Render("You: ") + text)
			m.textarea.Reset()

			return m, nil
		default:
			// Sends all other keypresses to the textarea.
			var cmd tea.Cmd
			m.textarea, cmd = m.textarea.Update(msg)

			return m, cmd
		}

	}

	return m, nil
}

func (m chatModel) View() tea.View {
	viewportView := m.viewport.View()
	// status goes below the textarea so it doesn't shift the cursor
	v := tea.NewView(viewportView + "\n" + m.textarea.View() + "\n" + m.statusLine())

	// textarea reports its cursor relative to itself, so push it down past
	// the viewport to get where it actually sits on screen
	c := m.textarea.Cursor()
	if c != nil {
		c.Y += lipgloss.Height(viewportView)
	}
	v.Cursor = c
	v.AltScreen = true
	return v
}

type loginModel struct {
	inputs     []textinput.Model
	focused    int
	connecting bool
	err        error
}

type connectedMsg struct {
	conn *net.UDPConn
	peer *net.UDPAddr
}

type connectErrMsg struct{ err error }

// this will be the UDP handshake logic later I think
// ===== PENDING HIS MODULE =====
// this whole body is a stub. his punch/connect call replaces everything inside
// and has to hand back the SAME socket it handshook on, a fresh one loses the
// nat hole. the STUN heartbeat while waiting for the other peer lives in there
// too, not out here
// =============================================================================
func connect(room, pass string) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(3000 * time.Millisecond)
		// temp logic to uhh show the uhh fail screen
		if pass == "fail" {
			return connectErrMsg{errors.New("wrong room code or password")}
		}
		// until the server exists, talk to ourselves on loopback so conn and
		// peer are real and nothing will be nil
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			return connectErrMsg{err}
		}
		return connectedMsg{conn: conn, peer: conn.LocalAddr().(*net.UDPAddr)}
	}
}

func login() loginModel {
	room := textinput.New()
	room.Placeholder = "room code"
	room.SetWidth(20)
	room.Focus()

	pass := textinput.New()
	pass.Placeholder = "password"
	pass.SetWidth(20)
	pass.EchoMode = textinput.EchoPassword

	return loginModel{inputs: []textinput.Model{room, pass}}
}

func (m loginModel) Init() tea.Cmd { return textinput.Blink }

func (m loginModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case connectedMsg:
		// cancelled while this was still in flight, throw the socket away
		if !m.connecting {
			if msg.conn != nil {
				msg.conn.Close()
			}
			return m, nil
		}
		// swapping models skips the new one's Init, so i run the startup cmds here
		// RequestWindowSize because the real WindowSizeMsg already happened on login
		c := chat(msg.conn, msg.peer)
		return c, tea.Batch(c.Init(), tea.RequestWindowSize)

	case connectErrMsg:
		if !m.connecting {
			return m, nil // already cancelled, nothing to report
		}
		m.connecting = false
		m.err = msg.err
		return m, nil

	case tea.KeyPressMsg:
		if m.connecting {
			switch msg.String() {
			case "ctrl+c":
				return m, tea.Quit
			case "esc":
				// ===== PENDING HIS MODULE =====
				// EarlyDestruct(conn, stun, room, pass) goes here so the server
				// stops holding a half matched room. blocked: we don't have the
				// stun address or the conn on this screen yet
				m.connecting = false
				m.err = errors.New("Connection attempt revoked, room closed.")
			}
			return m, nil
		}
		switch msg.String() {
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "tab", "shift+tab", "up", "down":
			m.inputs[m.focused].Blur()
			m.focused = (m.focused + 1) % len(m.inputs)
			return m, m.inputs[m.focused].Focus()
		case "enter":
			room, pass := m.inputs[0].Value(), m.inputs[1].Value()
			if room == "" || pass == "" {
				m.err = errors.New("room code and password required")
				return m, nil
			}
			m.connecting, m.err = true, nil
			return m, connect(room, pass)
		}
	}

	// Feed everything (keys, blinks) to every input; only the focused one reacts.
	var cmds []tea.Cmd
	for i := range m.inputs {
		var cmd tea.Cmd
		m.inputs[i], cmd = m.inputs[i].Update(msg)
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

func (m loginModel) View() tea.View {
	status := "tab to switch \u00b7 enter to join \u00b7 esc to quit"
	switch {
	case m.connecting:
		status = "Waiting for peer\u2026"
	case m.err != nil:
		status = "!! " + m.err.Error()
	}

	v := tea.NewView("Join a room\n\n" +
		m.inputs[0].View() + "\n" +
		m.inputs[1].View() + "\n\n" +
		status)
	v.AltScreen = true
	return v
}
