package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/net/websocket"
)

var (
	ngmr      io.ReadCloser
	ngmw      io.WriteCloser
	wscs      []*websocket.Conn
	mu        sync.Mutex
	connected = make(chan struct{})
	clientswg sync.WaitGroup
	appMode   bool
	quit      = make(chan struct{})
)

// Config is struct for server_config.json
type Config struct {
	Port       string   `json:"port"`
	RootURI    string   `json:"root_uri"`
	RootDir    string   `json:"root_dir"`
	NagomeExec []string `json:"nagome_exec"`
	AppMode    bool     `json:"app_mode"`
}

func utf8SafeWrite(src io.Reader) error {
	var utf8tmp []byte
	buf := make([]byte, 32*1024)

read:
	for {
		// append utf8tmp to the top of buf
		copy(buf, utf8tmp)
		// read into the rest buf
		nr, err := src.Read(buf[len(utf8tmp):])
		nr += len(utf8tmp)
		utf8tmp = utf8tmp[0:0]
		if nr > 0 {
			rb := buf[0:nr]
			if !utf8.Valid(rb) {
				for i := 1; ; i++ {
					if i > 4 {
						return fmt.Errorf("invalid data (not utf-8)")
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
			for _, c := range wscs {
				if c == nil {
					continue
				}
				nw, ew := c.Write(rb)
				if ew != nil {
					log.Println(ew)
					continue
				}
				if nr != nw {
					err := io.ErrShortWrite
					log.Println(err)
					continue
				}
			}
			mu.Unlock()
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("websocket read: %s", err.Error())
		}
	}

	return nil
}

// BridgeServer receive a connection
func BridgeServer(wsc *websocket.Conn) {
	if appMode {
		clientswg.Add(1)
		defer clientswg.Done()
		select {
		case connected <- struct{}{}:
		default:
		}
	}

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
		log.Println(err)
		return
	}
}

func loadConfig() (*Config, error) {
	// Parse flags
	f := flag.NewFlagSet("nagome-webapp-server", flag.ContinueOnError)
	configPath := f.String("c", "./server_config.json", "Path to the config file")
	printHelp := f.Bool("h", false, "Print this help.")

	err := f.Parse(os.Args[1:])
	if err != nil {
		return nil, err
	}

	if *printHelp {
		f.Usage()
		return nil, nil
	}

	// Load config file
	file, err := os.Open(*configPath)
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(file)
	var c Config
	if err = d.Decode(&c); err != nil {
		return nil, fmt.Errorf("server config error %s", err)
	}

	return &c, nil
}

func cli() error {
	c, err := loadConfig()
	if err != nil {
		return err
	} else if c == nil {
		// Exit for printting help
		return nil
	}

	// connect to Nagome
	if len(c.NagomeExec) == 0 {
		return fmt.Errorf("no command in NagomeExec")
	}
	cmd := exec.Command(c.NagomeExec[0], c.NagomeExec[1:]...)
	ngmw, err = cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer ngmw.Close()
	ngmr, err = cmd.StdoutPipe()
	if err != nil {
		return err
	}
	defer ngmr.Close()
	ngme, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	defer ngme.Close()
	err = cmd.Start()
	if err != nil {
		return err
	}

	go func() {
		_, _ = io.Copy(os.Stderr, ngme)
	}()

	go func() {
		_, _ = io.Copy(ioutil.Discard, os.Stdin)
		close(quit)
	}()

	// wait for quit
	go func() {
		<-quit
		err := ngmw.Close()
		if err != nil {
			log.Println("nagome connection close: ", err)
			os.Exit(1)
		}
		err = cmd.Wait()
		if err != nil {
			log.Println("nagome wait: ", err)
			os.Exit(1)
		}
		fmt.Println("cmd end")
		os.Exit(0)
	}()

	go func() {
		err = utf8SafeWrite(ngmr)
		if err != nil {
			log.Println(err)
		}
	}()

	if c.AppMode {
		appMode = true
		go func() {
			// quit if not connected a while
			select {
			case <-time.After(time.Minute):
				fmt.Println("closing...\nNot connected a while")
				close(quit)
			case <-connected:
			}
			// quit when all clients are disconnected
			for {
				clientswg.Wait()
				select {
				case <-time.After(2 * time.Second):
					fmt.Println("closing...\nAll clients are disconnected")
					close(quit)
				case <-connected:
				}
			}
		}()
	}

	// serve
	http.Handle("/ws", websocket.Handler(BridgeServer))
	http.Handle("/app/", http.StripPrefix("/app/", http.FileServer(http.Dir(c.RootDir))))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic("tcp listen: " + err.Error())
	}
	fmt.Println("http://" + path.Join(listener.Addr().String(), "app", c.RootURI))
	err = http.Serve(listener, nil)
	if err != nil {
		panic("http serve: " + err.Error())
	}

	return nil
}

// This example demonstrates a trivial echo server.
func main() {
	err := cli()
	if err != nil {
		log.Println(err)
		os.Exit(1)
	}
}
