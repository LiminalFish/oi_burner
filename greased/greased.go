package greased

import (
	"crypto/aes"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"time"
)

// InitClient binds the local UDP port and resolves the STUN server address.
func InitClient(localPort int, stun string) (*net.UDPConn, *net.UDPAddr) {
	localAddr := &net.UDPAddr{
		IP:   net.ParseIP("0.0.0.0"),
		Port: localPort,
	}

	local, err := net.ListenUDP("udp", localAddr)
	if err != nil {
		log.Fatalln("Failed to bind UDP socket:", err)
	}

	stunAddr, err := net.ResolveUDPAddr("udp", stun)
	if err != nil {
		log.Fatalln(err)
	}

	return local, stunAddr
}

// RegisterRoom sends the initial registration/join payload to the STUN server (non-blocking for peer).
func RegisterRoom(local *net.UDPConn, stunAddr *net.UDPAddr, roomId string, password string) {
	payload, _ := json.Marshal(map[string]string{
		"room_id":  roomId,
		"password": password,
	})

	_, err := local.WriteToUDP(payload, stunAddr)
	if err != nil {
		log.Fatal(err)
	}
}

// WaitForPeer blocks until the STUN server responds with the peer address when a second client joins.
func WaitForPeer(local *net.UDPConn, stunAddr *net.UDPAddr) *net.UDPAddr {
	buf := make([]byte, 2048)
	n, remoteAddr, err := local.ReadFromUDP(buf)
	if err != nil {
		log.Fatalln(err)
	}

	if remoteAddr.String() != stunAddr.String() {
		log.Fatalln("Received packet from unknown source")
	}

	var response map[string]string
	if err := json.Unmarshal(buf[:n], &response); err != nil {
		log.Fatalln("Invalid JSON from server:", err)
	}

	peerAddrStr := response["peer_addr"]
	peerUDPAddr, err := net.ResolveUDPAddr("udp", peerAddrStr)
	if err != nil {
		log.Fatalln("Invalid peer address:", err)
	}

	return peerUDPAddr
}

// HolePunch takes the established connection and peer address,
// then performs UDP hole punching until a direct P2P connection is made.
func HolePunch(local *net.UDPConn, peerUDPAddr *net.UDPAddr) {

	buf := make([]byte, 2048)
	punch := make(chan struct{})

	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-punch:
				return
			case <-ticker.C:
				_, _ = local.WriteToUDP([]byte("PUNCH"), peerUDPAddr)
			}
		}
	}()

	// Listen for direct connection from the peer
	for {
		_, remoteAddr, err := local.ReadFromUDP(buf)
		if err != nil {
			continue
		}

		if remoteAddr.String() == peerUDPAddr.String() {
			close(punch) // Stop punching once heard from peer

			// msg := string(buf[:n])
			// if msg != "PUNCH" {
			// 	fmt.Printf("Peer: %s\n", msg)
			// }
			break
		}
	}
}

func EarlyDestruct(local *net.UDPConn, stun *net.UDPAddr, roomId string, password string) {
	command, _ := json.Marshal(map[string]string{
		"room_id":  roomId,
		"password": password,
		"action":   "DESTROY",
	})

	local.WriteToUDP(command, stun)
	fmt.Println("[*] Sent EarlyDestruct command to STUN server.")
}

func SendMessage(local *net.UDPConn, peer *net.UDPAddr, message string) {
	command, _ := json.Marshal(map[string]string{
		"action":  "MESSAGE",
		"message": message,
	})

	local.WriteToUDP(command, peer)
}

func PeerHeartBeat(local *net.UDPConn, peer *net.UDPAddr) {
	command, _ := json.Marshal(map[string]string{
		"action": "HEARTBEAT",
	})

	local.WriteToUDP(command, peer)
}

func DestroySession(local *net.UDPConn, peer *net.UDPAddr) {
	command, _ := json.Marshal(map[string]string{
		"action": "DESTROY",
	})

	local.WriteToUDP(command, peer)
}

func Encrypt(message string, password string) string {
	cipher, err := aes.NewCipher([]byte(password))
	if err != nil {
		log.Fatalln("Error in Encrypt:\t", err)
	}

	var dst []byte
	cipher.Encrypt(dst, []byte(message))

	return string(dst)
}

func Decrypt(message string, password string) string {
	cipher, err := aes.NewCipher([]byte(password))
	if err != nil {
		log.Fatalln("Error in Decrypt:\t", err)
	}

	var dst []byte
	cipher.Decrypt(dst, []byte(message))

	return string(dst)
}
