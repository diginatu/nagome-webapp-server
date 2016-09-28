package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"sync"
	"unicode/utf8"

	"golang.org/x/net/websocket"
)

const (
	defaultPort = "8753"
)

var (
	ngmr      io.ReadCloser
	ngmw      io.WriteCloser
	wscs      []*websocket.Conn
	mu        sync.Mutex
	connected = make(chan struct{})
)

func utf8SafeWrite(src io.Reader) error {
	var utf8tmp []byte
	buf := make([]byte, 32*1024)

read:
	for {
		// append utf8tmp to the top of buf
		copy(buf, utf8tmp)
		// read into the rest buf
		nr, er := src.Read(buf[len(utf8tmp):])
		nr += len(utf8tmp)
		utf8tmp = utf8tmp[0:0]
		if nr > 0 {
			rb := buf[0:nr]
			if !utf8.Valid(rb) {
				for i := 1; ; i++ {
					if i > 4 {
						panic("invalid data (not utf-8)")
					}
					if i == nr {
						utf8tmp = make([]byte, i)
						copy(utf8tmp, buf[nr-i:nr])
						break read
					}
					rb = buf[0 : nr-i]
					if utf8.Valid(rb) {
						utf8tmp = append(utf8tmp, buf[nr-i:nr]...)
						nr -= i
						break
					}
				}
			}

			mu.Lock()
			fmt.Println(wscs)
			for _, c := range wscs {
				if c == nil {
					continue
				}
				nw, ew := c.Write(rb)
				if ew != nil {
					fmt.Println(ew)
					continue
				}
				if nr != nw {
					err := io.ErrShortWrite
					fmt.Println(err)
					continue
				}
			}
			mu.Unlock()
		}
		if er == io.EOF {
			return nil
		}
		if er != nil {
			return er
		}
	}

	return nil
}

// BridgeServer receive a connection
func BridgeServer(wsc *websocket.Conn) {
	fmt.Println("connected to a client")
	var no int

	mu.Lock()
	bl := false
	for i, v := range wscs {
		if v == nil {
			no = i
			wscs[i] = wsc
			bl = true
			break
		}
	}
	if !bl {
		no = len(wscs)
		wscs = append(wscs, wsc)
	}
	mu.Unlock()
	defer func() {
		mu.Lock()
		wscs[no] = nil
		mu.Unlock()
	}()

	_, err := io.Copy(ngmw, wsc)
	if err != nil {
		fmt.Println(err)
		return
	}
}

// This example demonstrates a trivial echo server.
func main() {
	var err error

	// connect to Nagome
	cmd := exec.Command("nagome")
	ngmw, err = cmd.StdinPipe()
	if err != nil {
		log.Println(err)
		return
	}
	defer ngmw.Close()
	ngmr, err = cmd.StdoutPipe()
	if err != nil {
		log.Println(err)
		return
	}
	defer ngmr.Close()
	err = cmd.Start()
	if err != nil {
		log.Println(err)
		return
	}

	fmt.Println("http://localhost:" + defaultPort + "/app")

	go func() {
		err = utf8SafeWrite(ngmr)
		if err != nil {
			fmt.Println(err)
		}
	}()

	// serve
	http.Handle("/ws", websocket.Handler(BridgeServer))
	http.Handle("/app/", http.StripPrefix("/app/", http.FileServer(http.Dir("./app"))))
	err = http.ListenAndServe(":"+defaultPort, nil)
	if err != nil {
		panic("ListenAndServe: " + err.Error())
	}
}
