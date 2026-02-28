// Go-Zork Web Interface
// Serves a terminal and chat UI for playing Zork via WebSocket.

package main

import (
	"embed"
	"flag"
	"fmt"
	"go-zork/pkg/zork"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

//go:embed terminal.html chat.html
var staticFiles embed.FS

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func wsHandler(storyFile, saveFile string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		inputChan := make(chan string)
		outputChan := make(chan []byte, 64)

		z := &zork.ZMachine{}
		z.InputChan = inputChan
		z.OutputChan = outputChan
		z.RandomSeed = int32(time.Now().UnixNano())
		z.SaveFile = saveFile

		if err := z.LoadStory(storyFile); err != nil {
			conn.WriteMessage(websocket.TextMessage, []byte("error: "+err.Error()+"\n")) //nolint:errcheck
			return
		}

		done := z.Run()

		go func() {
			for chunk := range outputChan {
				conn.WriteMessage(websocket.TextMessage, chunk) //nolint:errcheck
			}
		}()

		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				close(inputChan)
				<-done
				return
			}
			line := string(msg)
			if !strings.HasSuffix(line, "\n") {
				line += "\n"
			}
			inputChan <- line
		}
	}
}

func main() {
	storyFile := flag.String("story", "zork1.dat", "path to story file")
	saveFile  := flag.String("save", "save.dat", "path to save file")
	port      := flag.String("port", ":8080", "listen address")
	flag.Parse()

	if _, err := os.Stat(*storyFile); err != nil {
		fmt.Fprintf(os.Stderr, "error: story file %q not found\n", *storyFile)
		os.Exit(1)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/terminal", http.StatusFound)
	})

	http.HandleFunc("/terminal", func(w http.ResponseWriter, r *http.Request) {
		data, _ := staticFiles.ReadFile("terminal.html")
		w.Header().Set("Content-Type", "text/html")
		w.Write(data) //nolint:errcheck
	})

	http.HandleFunc("/chat", func(w http.ResponseWriter, r *http.Request) {
		data, _ := staticFiles.ReadFile("chat.html")
		w.Header().Set("Content-Type", "text/html")
		w.Write(data) //nolint:errcheck
	})

	http.HandleFunc("/ws", wsHandler(*storyFile, *saveFile))

	fmt.Printf("Listening on %s\n", *port)
	fmt.Printf("Terminal UI: http://localhost%s/terminal\n", *port)
	fmt.Printf("Chat UI:     http://localhost%s/chat\n", *port)

	if err := http.ListenAndServe(*port, nil); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
