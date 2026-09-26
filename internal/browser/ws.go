package browser

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
)

type wsConn struct {
	conn    net.Conn
	r       *bufio.Reader
	writeMu sync.Mutex
}

func dialWS(raw string) (*wsConn, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "ws" {
		return nil, fmt.Errorf("unsupported websocket scheme %q", u.Scheme)
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}
	c, err := net.Dial("tcp", host)
	if err != nil {
		return nil, err
	}
	keyRaw := make([]byte, 16)
	if _, err := rand.Read(keyRaw); err != nil {
		c.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyRaw)
	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", path, u.Host, key)
	r := bufio.NewReader(c)
	status, err := r.ReadString('\n')
	if err != nil {
		c.Close()
		return nil, err
	}
	if !strings.Contains(status, " 101 ") {
		c.Close()
		return nil, fmt.Errorf("websocket upgrade failed: %s", strings.TrimSpace(status))
	}
	headers := map[string]string{}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			c.Close()
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if i := strings.Index(line, ":"); i >= 0 {
			headers[strings.ToLower(strings.TrimSpace(line[:i]))] = strings.TrimSpace(line[i+1:])
		}
	}
	accept := base64.StdEncoding.EncodeToString(func() []byte {
		h := sha1.New()
		h.Write([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		return h.Sum(nil)
	}())
	if headers["sec-websocket-accept"] != accept {
		c.Close()
		return nil, errors.New("invalid websocket accept")
	}
	return &wsConn{conn: c, r: r}, nil
}

func (w *wsConn) Close() error { return w.conn.Close() }

func (w *wsConn) WriteText(payload []byte) error {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	var h bytes.Buffer
	h.WriteByte(0x81)
	n := len(payload)
	if n < 126 {
		h.WriteByte(byte(n) | 0x80)
	} else if n <= 65535 {
		h.WriteByte(126 | 0x80)
		binary.Write(&h, binary.BigEndian, uint16(n))
	} else {
		h.WriteByte(127 | 0x80)
		binary.Write(&h, binary.BigEndian, uint64(n))
	}
	mask := make([]byte, 4)
	if _, err := rand.Read(mask); err != nil {
		return err
	}
	h.Write(mask)
	masked := make([]byte, n)
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	if _, err := w.conn.Write(h.Bytes()); err != nil {
		return err
	}
	_, err := w.conn.Write(masked)
	return err
}

func (w *wsConn) writeControl(op byte, payload []byte) error {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	if len(payload) > 125 {
		payload = payload[:125]
	}
	var h bytes.Buffer
	h.WriteByte(0x80 | op)
	h.WriteByte(byte(len(payload)) | 0x80)
	mask := make([]byte, 4)
	rand.Read(mask)
	h.Write(mask)
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	_, err := w.conn.Write(append(h.Bytes(), payload...))
	return err
}

func (w *wsConn) ReadText() ([]byte, error) {
	var assembled bytes.Buffer
	var started bool
	for {
		b1, err := w.r.ReadByte()
		if err != nil {
			return nil, err
		}
		b2, err := w.r.ReadByte()
		if err != nil {
			return nil, err
		}
		fin := b1&0x80 != 0
		op := b1 & 0x0f
		masked := b2&0x80 != 0
		n := uint64(b2 & 0x7f)
		if n == 126 {
			var x uint16
			if err := binary.Read(w.r, binary.BigEndian, &x); err != nil {
				return nil, err
			}
			n = uint64(x)
		} else if n == 127 {
			if err := binary.Read(w.r, binary.BigEndian, &n); err != nil {
				return nil, err
			}
		}
		if n > 64*1024*1024 {
			return nil, errors.New("websocket frame too large")
		}
		var mask [4]byte
		if masked {
			if _, err := io.ReadFull(w.r, mask[:]); err != nil {
				return nil, err
			}
		}
		p := make([]byte, int(n))
		if _, err := io.ReadFull(w.r, p); err != nil {
			return nil, err
		}
		if masked {
			for i := range p {
				p[i] ^= mask[i%4]
			}
		}
		switch op {
		case 0x8:
			return nil, io.EOF
		case 0x9:
			_ = w.writeControl(0xA, append([]byte(nil), p...))
			continue
		case 0xA:
			continue
		case 0x1:
			started = true
			assembled.Write(p)
		case 0x0:
			if started {
				assembled.Write(p)
			}
		default:
			continue
		}
		if fin && started {
			return assembled.Bytes(), nil
		}
	}
}
