package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync"
	"time"
)

type Request struct {
	RoomID   string `json:"room_id"`
	Password string `json:"password"`
	Action   string `json:"action,omitempty"` // "DESTROY", "HEARTBEAT", or empty for join/register
}

type ClientInfo struct {
	Addr     string
	LastSeen time.Time
}

type Room struct {
	RoomID   string
	Password string
	Clients  []ClientInfo
	LastSeen time.Time
}

var rooms = struct {
	sync.RWMutex
	m map[string]*Room
}{m: make(map[string]*Room)}

// RoomTimeout defines how long a room stays alive without activity before expiring
const RoomTimeout = 30 * time.Second

func main() {
	con, err := net.ListenPacket("udp", "0.0.0.0:2121")
	if err != nil {
		log.Fatalln(err)
	}
	defer con.Close()

	fmt.Println("STUN/Signaling server listening on 0.0.0.0:2121")

	// Start background routine to clean up inactive/abandoned rooms
	go cleanupInactiveRooms()

	for {
		buf := make([]byte, 2048)
		n, addr, err := con.ReadFrom(buf)
		if err != nil {
			log.Println(err)
			continue
		}

		go handlePacket(con, addr, buf[:n])
	}
}

func handlePacket(con net.PacketConn, addr net.Addr, payload []byte) {
	var req Request
	if err := json.Unmarshal(payload, &req); err != nil {
		log.Println("Invalid packet JSON:", err)
		return
	}

	if req.RoomID == "" {
		return
	}

	clientStr := addr.String()

	rooms.Lock()
	defer rooms.Unlock()

	room, exists := rooms.m[req.RoomID]

	// 1. Handle Early Destruction Request
	if req.Action == "DESTROY" {
		if exists && room.Password == req.Password {
			delete(rooms.m, req.RoomID)
			log.Printf("Room %s explicitly destroyed by %s", req.RoomID, clientStr)
		}
		return
	}

	// 2. Handle Heartbeat Request
	if req.Action == "HEARTBEAT" {
		if exists && room.Password == req.Password {
			room.LastSeen = time.Now()
			for i := range room.Clients {
				if room.Clients[i].Addr == clientStr {
					room.Clients[i].LastSeen = time.Now()
				}
			}
			log.Printf("Heartbeat received for room %s from %s", req.RoomID, clientStr)
		}
		return
	}

	// 3. Room Registration (First Connection / Host)
	if !exists {
		rooms.m[req.RoomID] = &Room{
			RoomID:   req.RoomID,
			Password: req.Password,
			Clients: []ClientInfo{
				{Addr: clientStr, LastSeen: time.Now()},
			},
			LastSeen: time.Now(),
		}
		log.Printf("Room registered %s by host %s", req.RoomID, clientStr)
		return
	}

	// Validate password for joining clients
	if room.Password != req.Password {
		log.Println("Password mismatch for room:", req.RoomID)
		return
	}

	room.LastSeen = time.Now()

	// Check if client is already registered
	for _, c := range room.Clients {
		if c.Addr == clientStr {
			return
		}
	}

	// 4. Second Client Joins (Full Room)
	room.Clients = append(room.Clients, ClientInfo{Addr: clientStr, LastSeen: time.Now()})

	if len(room.Clients) == 2 {
		client0 := room.Clients[0].Addr
		client1 := room.Clients[1].Addr
		delete(rooms.m, req.RoomID)

		log.Printf("Room %s full. Exchanging endpoints between %s and %s", req.RoomID, client0, client1)
		sendPeerInfo(con, client0, client1)
		sendPeerInfo(con, client1, client0)
	}
}

func sendPeerInfo(con net.PacketConn, targetStr, peerStr string) {
	targetAddr, err := net.ResolveUDPAddr("udp", targetStr)
	if err != nil {
		return
	}
	msg, _ := json.Marshal(map[string]string{
		"peer_addr": peerStr,
	})
	con.WriteTo(msg, targetAddr)
}

func cleanupInactiveRooms() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		rooms.Lock()
		now := time.Now()
		for roomID, room := range rooms.m {
			if now.Sub(room.LastSeen) > RoomTimeout {
				delete(rooms.m, roomID)
				log.Printf("Room %s expired due to inactivity", roomID)
			}
		}
		rooms.Unlock()
	}
}
