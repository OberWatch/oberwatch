package alert

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// generateTestTLSCert builds a self-signed certificate valid for 127.0.0.1,
// along with a root pool that trusts it. It exists so STARTTLS/implicit TLS
// tests can verify a real handshake instead of skipping verification.
func generateTestTLSCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate() error = %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey() error = %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair() error = %v", err)
	}
	pool := x509.NewCertPool()
	parsed, err := x509.ParseCertificate(derBytes)
	if err != nil {
		t.Fatalf("ParseCertificate() error = %v", err)
	}
	pool.AddCert(parsed)
	return cert, pool
}

// recordedSMTPSession captures one completed DATA transaction for assertions.
//
//nolint:govet // keep test session fields explicit.
type recordedSMTPSession struct {
	from     string
	to       []string
	data     string
	usedTLS  bool
	usedAuth bool
}

//nolint:govet // keep fake server fields grouped by role.
type fakeSMTPServer struct {
	t        *testing.T
	listener net.Listener
	tlsConf  *tls.Config
	implicit bool

	supportsSTARTTLS bool
	supportsAuth     bool
	allowAuthNoTLS   bool
	authUser         string
	authPass         string
	rejectAuth       bool
	rejectRecipients map[string]int
	hang             bool

	mu       sync.Mutex
	sessions []recordedSMTPSession
	conns    []net.Conn
}

func newFakeSMTPServer(t *testing.T) *fakeSMTPServer {
	t.Helper()
	return newFakeSMTPServerWithHang(t, false)
}

// newFakeSMTPServerWithHang fixes the server behavior before its accept loop
// starts. That makes the timeout fixture deterministic under `go test -race`:
// a connection handler can never observe `hang` concurrently with a test
// mutating it.
func newFakeSMTPServerWithHang(t *testing.T, hang bool) *fakeSMTPServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	server := &fakeSMTPServer{
		t:                t,
		listener:         listener,
		hang:             hang,
		rejectRecipients: map[string]int{},
	}
	t.Cleanup(server.Close)
	go server.acceptLoop(listener)
	return server
}

func newFakeImplicitTLSSMTPServer(t *testing.T, tlsConf *tls.Config) *fakeSMTPServer {
	t.Helper()
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	listener := tls.NewListener(raw, tlsConf)
	server := &fakeSMTPServer{t: t, listener: listener, implicit: true, rejectRecipients: map[string]int{}}
	t.Cleanup(server.Close)
	go server.acceptLoop(listener)
	return server
}

func (s *fakeSMTPServer) addr() string {
	return s.listener.Addr().String()
}

func (s *fakeSMTPServer) hostPort() (string, int) {
	host, portStr, err := net.SplitHostPort(s.addr())
	if err != nil {
		s.t.Fatalf("SplitHostPort() error = %v", err)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		s.t.Fatalf("parse port %q: %v", portStr, err)
	}
	return host, port
}

func (s *fakeSMTPServer) Close() {
	_ = s.listener.Close()
	s.mu.Lock()
	for _, conn := range s.conns {
		_ = conn.Close()
	}
	s.mu.Unlock()
}

func (s *fakeSMTPServer) recordConn(conn net.Conn) {
	s.mu.Lock()
	s.conns = append(s.conns, conn)
	s.mu.Unlock()
}

func (s *fakeSMTPServer) record(session recordedSMTPSession) {
	s.mu.Lock()
	s.sessions = append(s.sessions, session)
	s.mu.Unlock()
}

func (s *fakeSMTPServer) recordedSessions() []recordedSMTPSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recordedSMTPSession(nil), s.sessions...)
}

func (s *fakeSMTPServer) acceptLoop(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		s.recordConn(conn)
		go s.serveConn(conn)
	}
}

func (s *fakeSMTPServer) serveConn(conn net.Conn) {
	if s.hang {
		// Never speak. The client sits waiting for a greeting until its
		// context deadline closes the connection out from under it.
		return
	}

	reader := bufio.NewReader(conn)
	write := func(msg string) {
		_, _ = conn.Write([]byte(msg + "\r\n"))
	}
	write("220 fake.smtp ESMTP")

	tlsActive := s.implicit
	authenticated := false
	var session recordedSMTPSession

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(line)

		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			write("250-fake.smtp greets you")
			if s.supportsSTARTTLS && !tlsActive {
				write("250-STARTTLS")
			}
			if s.supportsAuth && (tlsActive || s.allowAuthNoTLS) {
				write("250-AUTH PLAIN")
			}
			write("250 8BITMIME")

		case upper == "STARTTLS":
			write("220 Go ahead")
			tlsConn := tls.Server(conn, s.tlsConf)
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			conn = tlsConn
			reader = bufio.NewReader(conn)
			tlsActive = true

		case strings.HasPrefix(upper, "AUTH PLAIN"):
			if s.rejectAuth {
				write("535 5.7.8 Authentication failed")
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 3 {
				write("334 ")
				authLine, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				fields = []string{"AUTH", "PLAIN", strings.TrimSpace(authLine)}
			}
			decoded, err := base64.StdEncoding.DecodeString(fields[2])
			if err != nil {
				write("535 5.7.8 Authentication failed")
				continue
			}
			parts := strings.Split(string(decoded), "\x00")
			if len(parts) != 3 || parts[1] != s.authUser || parts[2] != s.authPass {
				write("535 5.7.8 Authentication failed")
				continue
			}
			authenticated = true
			write("235 2.7.0 Authentication successful")

		case strings.HasPrefix(upper, "MAIL FROM:"):
			session.from = extractSMTPAddr(line)
			write("250 2.1.0 OK")

		case strings.HasPrefix(upper, "RCPT TO:"):
			addr := extractSMTPAddr(line)
			if code, reject := s.rejectRecipients[addr]; reject {
				write(fmt.Sprintf("%d 5.1.1 rejected", code))
				continue
			}
			session.to = append(session.to, addr)
			write("250 2.1.5 OK")

		case upper == "DATA":
			write("354 Start mail input; end with <CRLF>.<CRLF>")
			var data strings.Builder
			for {
				dataLine, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if dataLine == ".\r\n" || dataLine == ".\n" {
					break
				}
				data.WriteString(dataLine)
			}
			session.data = data.String()
			session.usedTLS = tlsActive
			session.usedAuth = authenticated
			s.record(session)
			session = recordedSMTPSession{}
			write("250 2.0.0 OK")

		case upper == "QUIT":
			write("221 2.0.0 Bye")
			return

		case upper == "NOOP":
			write("250 2.0.0 OK")

		case upper == "RSET":
			session = recordedSMTPSession{}
			write("250 2.0.0 OK")

		default:
			write("500 5.5.1 unrecognized command")
		}
	}
}

// extractSMTPAddr pulls the bracketed address out of a MAIL FROM/RCPT TO line.
func extractSMTPAddr(line string) string {
	start := strings.Index(line, "<")
	end := strings.Index(line, ">")
	if start < 0 || end < 0 || end < start {
		return strings.TrimSpace(line)
	}
	return line[start+1 : end]
}
