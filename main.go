package main

import (
	"crypto/tls"
	"errors"
	"flag"
	"io"
	"log"
	"math"
	"net"
	"sync"

	"github.com/PsyChip/revsock/bufferpool"
	"github.com/PsyChip/revsock/mux"
	"github.com/PsyChip/revsock/statute"
	   "time"
//	"fmt"
  //  "io/ioutil"
    //"net/http"
)

var (
	bufferPool = bufferpool.NewPool(math.MaxUint16)
	connected = false
)

func main() {
//	const addr = "54.167.58.47:4242"; // this line is hardcoded into application
//	const pass = "";
//	log.Println("Connecting to " + addr)
//	ReverseSocksAgent(addr, pass, false);
 //  return ;
	
	listen := flag.String("listen", ":10443", "Listen address for socks agents address:port")
	socks := flag.String("socks", "127.0.0.1:1080", "Listen address for socks server address:port")
	psk := flag.String("psk", "password", "Pre-shared key for encryption and authentication between the agent and server")
	connect := flag.String("connect", "", "Connect address for socks agent address:port")
	connectTLS := flag.Bool("tls", false, "Connect with TLS instead of TCP, the server must be using certificates")
	username := flag.String("username", "", "Username used for SOCKS5 authentication")
	password := flag.String("password", "", "Password used for SOCKS5 authentication. No authentication required if not configured.")
	cert := flag.String("cert", "", "certificate file")
	key := flag.String("key", "", "private key file")
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	if *connect == "" {
		ReverseSocksServer(*listen, *socks, *psk, *cert, *key, *username, *password)
	} else {
		for {
		ReverseSocksAgent(*connect, *psk, *connectTLS);
		log.Println("-- attempting to reconnect..");
		time.Sleep(1 * time.Second)
		}
	}
}

func ReverseSocksAgent(serverAddress, psk string, useTLS bool) {
	log.Println("Connecting to socks server at " + serverAddress)

	var conn net.Conn
	var err error

	if useTLS {
		conn, err = tls.Dial("tcp", serverAddress, nil)
	} else {
		conn, err = net.Dial("tcp", serverAddress)
	}

	if err != nil {
	//	log.Fatalln(err.Error())
log.Println(err.Error());
return;	
}

	log.Println("Connected")

	session := mux.Server(conn, psk)

	for {
		stream, err := session.AcceptStream()
		if err != nil {
			log.Println(err.Error())
			break
		}
		go func() {
			// Note ServeConn() will take overship of stream and close it.
			if err := ServeConn(stream); err != nil && err != mux.ErrPeerClosedStream {
				log.Println(err.Error())
			}
		}()
	}
	session.Close()
}

func ReverseSocksServer(agentListenAddress, socksListenAddress, psk, certFile, keyFile, username, password string) {
	usingTLS := false
	var cert tls.Certificate
	var err error

	if certFile != "" && keyFile != "" {
		cert, err = tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			log.Println("Certificate and/or private key not provided, using TCP listener")
		} else {
			usingTLS = true
		}
	}

	if len(password) == 0 {
//		log.Println("WARNING: No password configured, anyone will be able to connect to the SOCKS5 server.")
	}

	log.Println("Listening for socks agents on " + agentListenAddress)

	var ln net.Listener
	if usingTLS {
		config := &tls.Config{
			PreferServerCipherSuites: true,
			CurvePreferences:         []tls.CurveID{tls.X25519, tls.CurveP256},
			Certificates:             []tls.Certificate{cert},
		}
		ln, err = tls.Listen("tcp", agentListenAddress, config)
	} else {
		ln, err = net.Listen("tcp", agentListenAddress)
	}

	if err != nil {
		log.Fatalln(err.Error())
	}
	defer ln.Close()

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Println(err.Error())
			continue
		}
		session := mux.Client(conn, psk)
		TunnelServer(socksListenAddress, username, password, session)
		session.Close()
	}
}

// Accepts connections and tunnels the traffic to the SOCKS server running on the client.
func TunnelServer(listenAddress, username, password string, session *mux.Mux) {
	log.Println("Listening for socks clients on " + listenAddress)

	ln, err := net.Listen("tcp", listenAddress)
	if err != nil {
		log.Fatalln(err.Error())
	}
	defer ln.Close()

	authMethod := Authenticator(&NoAuthAuthenticator{})

	if len(password) > 0 {
		authMethod = &UserPassAuthenticator{username, password}
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				break
			} else {
				log.Println(err.Error())
				continue
			}
		}

		stream, err := session.OpenStream()
		if err != nil {
			conn.Close()
			log.Println(err.Error())
			// This is unrecoverable as the socks server is not opening connections.
			break
		}

		go handleSocksClient(conn, stream, authMethod)

	}
}

func doauth(conn net.Conn, authMethod Authenticator) error {

	// Check its a really SOCKS5 connection
	mr, err := statute.ParseMethodRequest(conn)
	if err != nil {
		return err
	}
	if mr.Ver != statute.VersionSocks5 {
		return statute.ErrNotSupportVersion
	}

	// Select a usable method
	for _, method := range mr.Methods {
		if authMethod.GetCode() == method {
			return authMethod.Authenticate(conn)

		}
	}

	// No usable method found
	conn.Write([]byte{statute.VersionSocks5, statute.MethodNoAcceptable}) //nolint: errcheck
	return statute.ErrNoSupportedAuth
}

func handleSocksClient(conn net.Conn, stream net.Conn, authMethod Authenticator) {
	defer conn.Close()
	defer stream.Close()

	if err := doauth(conn, authMethod); err != nil {
		log.Printf("failed to authenticate: %v", err.Error())
		return
	}

	// Proxy the data
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		buf := bufferPool.Get()
		defer bufferPool.Put(buf)
		io.CopyBuffer(conn, stream, buf[:cap(buf)])
		// io.Copy(conn, stream)
		wg.Done()
	}()
	go func() {
		buf := bufferPool.Get()
		defer bufferPool.Put(buf)
		io.CopyBuffer(stream, conn, buf[:cap(buf)])
		wg.Done()
	}()

	wg.Wait()
}
