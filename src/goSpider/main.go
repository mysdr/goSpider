package main

import (
	"encoding/binary"
	"errors"
	"io"
	"log"
	"math/rand"
	"net"
	"strconv"
	"time"

	ss "github.com/shadowsocks/shadowsocks-go/shadowsocks"
	"os"
	"fmt"
	"golang.org/x/net/proxy"
	"net/http"
	//"io/ioutil"
	"context"
)

var (
	//  ss password
	ssPassword []string = []string{
		"",
		"",
	}
	// domain.com:3308
	ssServer []string = []string{
		"",
		"",
	}
	// :1080  127.0.0.1:1080
	localPort []string = []string{
		"",
		"",
	}
)

var debug ss.DebugLog

var (
	errAddrType      = errors.New("socks addr type not supported")
	errVer           = errors.New("socks version not supported")
	errMethod        = errors.New("socks only support 1 method now")
	errAuthExtraData = errors.New("socks authentication get extra data")
	errReqExtraData  = errors.New("socks request get extra data")
	errCmd           = errors.New("socks command not supported")
)

const (
	socksVer5       = 5
	socksCmdConnect = 1
)

func init() {
	rand.Seed(time.Now().Unix())
}

func handShake(conn net.Conn) (err error) {
	const (
		idVer     = 0
		idNmethod = 1
	)
	// version identification and method selection message in theory can have
	// at most 256 methods, plus version and nmethod field in total 258 bytes
	// the current rfc defines only 3 authentication methods (plus 2 reserved),
	// so it won't be such long in practice

	buf := make([]byte, 258)

	var n int
	ss.SetReadTimeout(conn)
	// make sure we get the nmethod field
	if n, err = io.ReadAtLeast(conn, buf, idNmethod+1); err != nil {
		return
	}
	if buf[idVer] != socksVer5 {
		return errVer
	}
	nmethod := int(buf[idNmethod])
	msgLen := nmethod + 2
	if n == msgLen { // handshake done, common case
		// do nothing, jump directly to send confirmation
	} else if n < msgLen { // has more methods to read, rare case
		if _, err = io.ReadFull(conn, buf[n:msgLen]); err != nil {
			return
		}
	} else { // error, should not get extra data
		return errAuthExtraData
	}
	// send confirmation: version 5, no authentication required
	_, err = conn.Write([]byte{socksVer5, 0})
	return
}

func getRequest(conn net.Conn) (rawaddr []byte, host string, err error) {
	const (
		idVer   = 0
		idCmd   = 1
		idType  = 3 // address type index
		idIP0   = 4 // ip addres start index
		idDmLen = 4 // domain address length index
		idDm0   = 5 // domain address start index

		typeIPv4 = 1 // type is ipv4 address
		typeDm   = 3 // type is domain address
		typeIPv6 = 4 // type is ipv6 address

		lenIPv4   = 3 + 1 + net.IPv4len + 2 // 3(ver+cmd+rsv) + 1addrType + ipv4 + 2port
		lenIPv6   = 3 + 1 + net.IPv6len + 2 // 3(ver+cmd+rsv) + 1addrType + ipv6 + 2port
		lenDmBase = 3 + 1 + 1 + 2           // 3 + 1addrType + 1addrLen + 2port, plus addrLen
	)
	// refer to getRequest in server.go for why set buffer size to 263
	buf := make([]byte, 263)
	var n int
	ss.SetReadTimeout(conn)
	// read till we get possible domain length field
	if n, err = io.ReadAtLeast(conn, buf, idDmLen+1); err != nil {
		return
	}
	// check version and cmd
	if buf[idVer] != socksVer5 {
		err = errVer
		return
	}
	if buf[idCmd] != socksCmdConnect {
		err = errCmd
		return
	}

	reqLen := -1
	switch buf[idType] {
	case typeIPv4:
		reqLen = lenIPv4
	case typeIPv6:
		reqLen = lenIPv6
	case typeDm:
		reqLen = int(buf[idDmLen]) + lenDmBase
	default:
		err = errAddrType
		return
	}

	if n == reqLen {
		// common case, do nothing
	} else if n < reqLen { // rare case
		if _, err = io.ReadFull(conn, buf[n:reqLen]); err != nil {
			return
		}
	} else {
		err = errReqExtraData
		return
	}

	rawaddr = buf[idType:reqLen]

	if debug {
		switch buf[idType] {
		case typeIPv4:
			host = net.IP(buf[idIP0: idIP0+net.IPv4len]).String()
		case typeIPv6:
			host = net.IP(buf[idIP0: idIP0+net.IPv6len]).String()
		case typeDm:
			host = string(buf[idDm0: idDm0+buf[idDmLen]])
		}
		port := binary.BigEndian.Uint16(buf[reqLen-2: reqLen])
		host = net.JoinHostPort(host, strconv.Itoa(int(port)))
	}

	return
}

func handleConnection(conn net.Conn, serverId int) {
	if debug {
		debug.Printf("socks connect from %s\n", conn.RemoteAddr().String())
	}
	closed := false
	defer func() {
		if !closed {
			conn.Close()
		}
	}()

	var err error = nil
	// borrow & ss-local 握手
	if err = handShake(conn); err != nil {
		log.Println("socks handshake:", err)
		return
	}

	// 从socket中获取请求地址
	rawaddr, addr, err := getRequest(conn)
	if err != nil {
		log.Println("error getting request:", err)
		return
	}
	// Sending connection established message immediately to client.
	// This some round trip time for creating socks connection with the client.
	// But if connection failed, the client will get connection reset error.
	_, err = conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x08, 0x43})
	if err != nil {
		debug.Println("send connection confirmation:", err)
		return
	}

	cipher, err = ss.NewCipher("rc4-md5", ssPassword[serverId])
	if err != nil {
		os.Exit(100)
	}

	//fmt.Println(rand.Intn(2))

	//remote, err := connectToServer(0, rawaddr, addr)
	remote, err := ss.DialWithRawAddr(rawaddr, ssServer[serverId], cipher.Copy())
	if err != nil {
		log.Println("error connecting to shadowsocks server:", err)
		return
	}
	defer func() {
		if !closed {
			remote.Close()
		}
	}()

	go ss.PipeThenClose(conn, remote)
	ss.PipeThenClose(remote, conn)
	closed = true
	debug.Println("closed connection to", addr)
}

func run(listenAddr string, serverId int) {
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("starting local socks5 server at %v ...\n", listenAddr)
	for {
		// 等待每次请求
		conn, err := ln.Accept()
		if err != nil {
			log.Println("accept:", err)
			continue
		}
		//异步处理每次请求
		go handleConnection(conn, serverId)
	}
}

var cipher *ss.Cipher
var dialer proxy.Dialer
var netTransport *http.Transport
var httpClient *http.Client
var httpReq *http.Request

func main() {
	ch := make(chan int)
	go ssLocal(localPort[0], 0, ch)
	go ssLocal(localPort[1], 1, ch)
	time.Sleep(1e9)

	//connect socks5 proxy
	httpClientArr := httpClients()
	fmt.Println(httpClientArr)
	var err error

	count := 10
	chs := make([]chan int, count)

	for {
		currentInt := rand.Intn(len(localPort))
		fmt.Println(currentInt)
		httpReq, err = http.NewRequest("GET", "https://ss.flysay.com/", nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, "can't create request:", err)
			os.Exit(2)
		}

		for i := 0; i < count; i++ {
			chs[i] = make(chan int)
			go getHttp(chs[i], i, count, httpClientArr[currentInt])
		}
		for _, ch1 := range chs {
			<-ch1
		}
		time.Sleep(5e9)
	}
	<-ch
	fmt.Println(2222)
}

func getHttp(ch chan int, i int, len int, httpClient *http.Client) {
	var startTimeF float64 = float64(time.Now().UnixNano() / 1000)
	// request
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		fmt.Fprintln(os.Stderr, "can't GET page:", err)
	} else {
		defer resp.Body.Close()
	}
	fmt.Println((float64(time.Now().UnixNano()/1000)-startTimeF)/1000.0, "ms")
	ch <- i
}

func httpClients() []*http.Client {
	httpClientArr := make([]*http.Client, len(localPort))
	for v, _ := range localPort {
		dialer, err := proxy.SOCKS5("tcp", localPort[v], nil, proxy.Direct)
		if err != nil {
			fmt.Fprintln(os.Stderr, "can't connect to the proxy:", err)
			os.Exit(1)
		}

		netTransport = &http.Transport{
			// Go version < 1.6
			//Dial:dialer.Dial,

			// Go version > 1.6
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			},
			TLSHandshakeTimeout: 10 * time.Second,
		}

		httpClientArr[v] = &http.Client{Transport: netTransport}
	}
	return httpClientArr
}

func ssLocal(localPort string, serverId int, ch chan int, ) {
	run(localPort, serverId)
}
