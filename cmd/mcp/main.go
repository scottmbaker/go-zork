// Go-Zork MCP Server
// Exposes Zork verbs as MCP tools so an AI agent can play the game.
// Supports stdio (default) and SSE transports.

/*
Example prompt:

You're going to play zork. Do not use raw command mode because I want you to show your moves.
Do not use my walkthrough.txt file as that would be cheating and you're not a cheater.
I want you to find a three nearby treasures on your own and return it to the trophy chest.
Reset the game using the restart command before starting so we know it is clear.
*/

package main

import (
	"context"
	"flag"
	"fmt"
	"go-zork/pkg/zork"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Game manages a single ZMachine session, bridging synchronous MCP tool
// calls to the channel-based ZMachine interface.
type Game struct {
	inputChan   chan string
	outputChan  chan []byte
	mu          sync.Mutex
	storyFile   string
	saveFile    string
	StartupText string
	Log         bool // if true, print all input/output to stdout
}

func newGame(storyFile, saveFile string) (*Game, error) {
	g := &Game{storyFile: storyFile, saveFile: saveFile}
	if err := g.start(); err != nil {
		return nil, err
	}
	return g, nil
}

// start launches a fresh ZMachine and captures the intro text.
// Must be called with mu held (or before the Game is shared).
func (g *Game) start() error {
	inputChan := make(chan string)
	outputChan := make(chan []byte, 64)

	z := &zork.ZMachine{}
	z.InputChan = inputChan
	z.OutputChan = outputChan
	z.RandomSeed = int32(time.Now().UnixNano())
	z.SaveFile = g.saveFile

	if err := z.LoadStory(g.storyFile); err != nil {
		return err
	}

	g.inputChan = inputChan
	g.outputChan = outputChan
	z.Run()
	g.StartupText = g.drainOutput()
	return nil
}

// restart shuts down the current ZMachine and starts a fresh game.
func (g *Game) restart() (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Closing inputChan signals the ZMachine to quit (ok==false branch).
	close(g.inputChan)
	// Drain outputChan until the ZMachine closes it (deferred in Run).
	for range g.outputChan {
	}

	if err := g.start(); err != nil {
		return "", err
	}
	if g.Log {
		fmt.Fprintf(os.Stdout, "[game restarted]\n%s", g.StartupText)
	}
	return g.StartupText, nil
}

// drainOutput collects ZMachine output until it goes quiet for 300 ms,
// indicating the interpreter is waiting for the next input command.
func (g *Game) drainOutput() string {
	var buf strings.Builder
	timer := time.NewTimer(300 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case chunk, ok := <-g.outputChan:
			if !ok {
				return buf.String()
			}
			buf.Write(chunk)
			// Reset the idle timer after each chunk.
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(300 * time.Millisecond)
		case <-timer.C:
			return buf.String()
		}
	}
}

// send delivers a command to the ZMachine and returns its textual response.
func (g *Game) send(cmd string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !strings.HasSuffix(cmd, "\n") {
		cmd += "\n"
	}
	var result string
	select {
	case g.inputChan <- cmd:
		result = g.drainOutput()
	case <-time.After(5 * time.Second):
		result = "(error: game not responding — it may have ended; try the 'quit' command or restart the server)"
	}
	if g.Log {
		fmt.Fprintf(os.Stdout, " %s%s", cmd, result)
	}
	return result
}

// toolFn is a shorthand type for MCP tool handlers.
type toolFn func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)

// text wraps a string in a successful MCP tool result.
func text(s string) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultText(s), nil
}

func registerTools(s *server.MCPServer, g *Game) {
	// ── Meta ──────────────────────────────────────────────────────────────────

	s.AddTool(mcp.NewTool("get_intro",
		mcp.WithDescription("Return the game's introduction text. "+
			"Call this first to see the opening scene and starting room description."),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return text(g.StartupText)
	})

	s.AddTool(mcp.NewTool("restart",
		mcp.WithDescription("Restart the game from the beginning, resetting all progress. "+
			"Returns the introduction text of the fresh game."),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		intro, err := g.restart()
		if err != nil {
			return nil, err
		}
		return text(intro)
	})

	// ── No-parameter verbs ────────────────────────────────────────────────────

	type simple struct{ name, desc, cmd string }
	simpleTools := []simple{
		{"look", "Look around and get a full description of the current location.", "look"},
		{"inventory", "List all items you are currently carrying.", "inventory"},
		{"score", "Display your current score and number of moves.", "score"},
		{"wait", "Wait one turn without doing anything (z).", "wait"},
		{"verbose", "Enable verbose mode: full room descriptions on every visit.", "verbose"},
		{"brief", "Enable brief mode: abbreviated room descriptions after the first visit.", "brief"},
		{"superbrief", "Enable superbrief mode: only room names, no descriptions.", "superbrief"},
		// Directions
		{"north", "Move north.", "north"},
		{"south", "Move south.", "south"},
		{"east", "Move east.", "east"},
		{"west", "Move west.", "west"},
		{"northeast", "Move northeast.", "northeast"},
		{"northwest", "Move northwest.", "northwest"},
		{"southeast", "Move southeast.", "southeast"},
		{"southwest", "Move southwest.", "southwest"},
		{"up", "Move up.", "up"},
		{"down", "Move down.", "down"},
	}
	for _, t := range simpleTools {
		t := t
		s.AddTool(mcp.NewTool(t.name, mcp.WithDescription(t.desc)),
			func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return text(g.send(t.cmd))
			})
	}

	// ── Single-object verbs ───────────────────────────────────────────────────

	type objTool struct{ name, desc, verb string }
	objectTools := []objTool{
		{"examine", "Examine an object closely to get more detail.", "examine"},
		{"take", "Pick up an object and add it to your inventory.", "take"},
		{"drop", "Drop a carried object in the current location.", "drop"},
		{"open", "Open a door, container, or other openable object.", "open"},
		{"close", "Close a door, container, or other closeable object.", "close"},
		{"read", "Read text on or in an object.", "read"},
		{"eat", "Eat something.", "eat"},
		{"drink", "Drink something.", "drink"},
		{"turn_on", "Turn something on (e.g. a lamp or lantern).", "turn on"},
		{"turn_off", "Turn something off.", "turn off"},
		{"push", "Push or press an object.", "push"},
		{"pull", "Pull an object.", "pull"},
		{"climb", "Climb an object or surface.", "climb"},
		{"move", "Move or shift an object to look beneath or behind it.", "move"},
		{"smell", "Smell something.", "smell"},
		{"listen", "Listen to something.", "listen"},
		{"wave", "Wave an object.", "wave"},
		{"ring", "Ring something (e.g. a bell).", "ring"},
		{"rub", "Rub or polish an object.", "rub"},
		{"light", "Light an object such as a candle or torch.", "light"},
		{"extinguish", "Extinguish a burning object.", "extinguish"},
		{"enter", "Enter something such as a boat or passage.", "enter"},
		{"exit", "Exit or leave something you are inside.", "exit"},
	}
	for _, t := range objectTools {
		t := t
		paramName := strings.ReplaceAll(t.name, "_", " ")
		tool := mcp.NewTool(t.name,
			mcp.WithDescription(t.desc),
			mcp.WithString("object",
				mcp.Required(),
				mcp.Description("The object to "+paramName),
			),
		)
		s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			obj := req.GetString("object", "")
			return text(g.send(t.verb + " " + obj))
		})
	}

	// ── Multi-object verbs ────────────────────────────────────────────────────

	// put <item> in/on <container>
	s.AddTool(mcp.NewTool("put",
		mcp.WithDescription("Put an item into or onto a container or surface."),
		mcp.WithString("item", mcp.Required(), mcp.Description("The item to place")),
		mcp.WithString("container", mcp.Required(), mcp.Description("The container or surface to place it in/on")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return text(g.send(fmt.Sprintf("put %s in %s", req.GetString("item", ""), req.GetString("container", ""))))
	})

	// attack <target> [with <weapon>]
	s.AddTool(mcp.NewTool("attack",
		mcp.WithDescription("Attack a creature or target, optionally with a specific weapon."),
		mcp.WithString("target", mcp.Required(), mcp.Description("The creature or target to attack")),
		mcp.WithString("weapon", mcp.Description("The weapon to use (optional)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		cmd := "attack " + req.GetString("target", "")
		if w := req.GetString("weapon", ""); w != "" {
			cmd += " with " + w
		}
		return text(g.send(cmd))
	})

	// throw <item> at <target>
	s.AddTool(mcp.NewTool("throw",
		mcp.WithDescription("Throw an item at a target."),
		mcp.WithString("item", mcp.Required(), mcp.Description("The item to throw")),
		mcp.WithString("target", mcp.Required(), mcp.Description("The target to throw it at")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return text(g.send(fmt.Sprintf("throw %s at %s", req.GetString("item", ""), req.GetString("target", ""))))
	})

	// give <item> to <recipient>
	s.AddTool(mcp.NewTool("give",
		mcp.WithDescription("Give an item to a character in the game."),
		mcp.WithString("item", mcp.Required(), mcp.Description("The item to give")),
		mcp.WithString("recipient", mcp.Required(), mcp.Description("The character to give it to")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return text(g.send(fmt.Sprintf("give %s to %s", req.GetString("item", ""), req.GetString("recipient", ""))))
	})

	// unlock <object> with <key>
	s.AddTool(mcp.NewTool("unlock",
		mcp.WithDescription("Unlock an object using a key."),
		mcp.WithString("object", mcp.Required(), mcp.Description("The object to unlock")),
		mcp.WithString("key", mcp.Required(), mcp.Description("The key to use")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return text(g.send(fmt.Sprintf("unlock %s with %s", req.GetString("object", ""), req.GetString("key", ""))))
	})

	// lock <object> with <key>
	s.AddTool(mcp.NewTool("lock",
		mcp.WithDescription("Lock an object using a key."),
		mcp.WithString("object", mcp.Required(), mcp.Description("The object to lock")),
		mcp.WithString("key", mcp.Required(), mcp.Description("The key to use")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return text(g.send(fmt.Sprintf("lock %s with %s", req.GetString("object", ""), req.GetString("key", ""))))
	})

	// tie <item> to <target>
	s.AddTool(mcp.NewTool("tie",
		mcp.WithDescription("Tie an item to something."),
		mcp.WithString("item", mcp.Required(), mcp.Description("The item to tie")),
		mcp.WithString("target", mcp.Required(), mcp.Description("The thing to tie it to")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return text(g.send(fmt.Sprintf("tie %s to %s", req.GetString("item", ""), req.GetString("target", ""))))
	})

	// insert <item> into <container>  (alias for put, some games prefer this phrasing)
	s.AddTool(mcp.NewTool("insert",
		mcp.WithDescription("Insert an item into a container (alternative to 'put')."),
		mcp.WithString("item", mcp.Required(), mcp.Description("The item to insert")),
		mcp.WithString("container", mcp.Required(), mcp.Description("The container to insert it into")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return text(g.send(fmt.Sprintf("insert %s into %s", req.GetString("item", ""), req.GetString("container", ""))))
	})

	// say <text> [to <character>]
	s.AddTool(mcp.NewTool("say",
		mcp.WithDescription("Say something, optionally directed at a character."),
		mcp.WithString("text", mcp.Required(), mcp.Description("What to say")),
		mcp.WithString("to", mcp.Description("The character to say it to (optional)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		cmd := fmt.Sprintf(`say "%s"`, req.GetString("text", ""))
		if who := req.GetString("to", ""); who != "" {
			cmd = fmt.Sprintf(`say "%s" to %s`, req.GetString("text", ""), who)
		}
		return text(g.send(cmd))
	})

	// ── Catch-all ─────────────────────────────────────────────────────────────

	s.AddTool(mcp.NewTool("command",
		mcp.WithDescription("Send any raw text command to the game. "+
			"Use this for verbs or phrasings not covered by other tools."),
		mcp.WithString("text", mcp.Required(), mcp.Description("The raw command text to send to the game")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return text(g.send(req.GetString("text", "")))
	})
}

func main() {
	storyFile := flag.String("story", "zork1.dat", "path to story file")
	saveFile := flag.String("save", "save.dat", "path to save file")
	mode := flag.String("mode", "stdio", "transport mode: stdio or sse")
	port := flag.String("port", ":8081", "listen address (SSE mode only)")
	logFlag := flag.Bool("log", false, "print all game input and output to stderr")
	flag.Parse()

	if _, err := os.Stat(*storyFile); err != nil {
		fmt.Fprintf(os.Stderr, "error: story file %q not found\n", *storyFile)
		os.Exit(1)
	}

	game, err := newGame(*storyFile, *saveFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error initializing game: %v\n", err)
		os.Exit(1)
	}
	game.Log = *logFlag
	if *logFlag {
		fmt.Fprintf(os.Stdout, "%s", game.StartupText)
	}

	s := server.NewMCPServer("Zork", "1.0.0",
		server.WithInstructions(
			"This server lets you play the classic text adventure Zork 1. "+
				"Call get_intro first to see the opening scene. "+
				"Use directional tools (north, south, …) to move, look to survey your surroundings, "+
				"and inventory to check what you carry. "+
				"The goal is to collect treasures and return them to the trophy case. "+
				"Your maximum score is 350 points.",
		),
	)

	registerTools(s, game)

	switch *mode {
	case "stdio":
		if err := server.ServeStdio(s); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	case "sse":
		addr := fmt.Sprintf(":%s", *port)
		sseServer := server.NewSSEServer(s, server.WithBaseURL(addr))
		fmt.Fprintf(os.Stderr, "Zork MCP SSE server listening on %s\n", *port)
		if err := sseServer.Start(*port); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q: use stdio or sse\n", *mode)
		os.Exit(1)
	}
}
