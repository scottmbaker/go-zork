package zork

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

const walkthroughSeed = 372

func loadStory(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../zork1.dat")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func walkthrough(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("../../walkthrough.txt")
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(strings.ReplaceAll(string(b), "\r", "")), "\n")
}

func start(t *testing.T) (*ZMachine, string) {
	t.Helper()
	z, err := NewMachine(loadStory(t), walkthroughSeed)
	if err != nil {
		t.Fatal(err)
	}
	res, err := z.Start()
	if err != nil {
		t.Fatal(err)
	}
	return z, string(res.Output)
}

func step(t *testing.T, z *ZMachine, line string) string {
	t.Helper()
	res, err := z.Step(line)
	if err != nil {
		t.Fatalf("step %q: %v", line, err)
	}
	return string(res.Output)
}

const perfect = "Your score is 350 (total of 350 points)"

func stepped(t *testing.T) string {
	z, out := start(t)
	for _, line := range walkthrough(t) {
		out += step(t, z, line)
	}
	return out
}

func TestWalkthroughStep(t *testing.T) {
	if out := stepped(t); !strings.Contains(out, perfect) {
		t.Fatal("walkthrough did not reach 350")
	}
}

// The channel API must produce the same transcript as stepping.
func TestWalkthroughChannels(t *testing.T) {
	in, out := make(chan string), make(chan []byte, 64)
	z := &ZMachine{InputChan: in, OutputChan: out, RandomSeed: walkthroughSeed, InitialSeed: walkthroughSeed}
	if err := z.LoadStory("../../zork1.dat"); err != nil {
		t.Fatal(err)
	}
	done := z.Run()
	lines := walkthrough(t)
	go func() {
		for _, l := range lines {
			in <- l + "\n"
		}
		close(in)
	}()
	var buf bytes.Buffer
	for b := range out {
		buf.Write(b)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if buf.String() != stepped(t) {
		t.Fatal("channel and step transcripts differ")
	}
}

// A server that forgets everything between turns must not change the game.
func TestSnapshotEveryTurn(t *testing.T) {
	want := stepped(t)
	story := loadStory(t)
	z, out := start(t)
	for i, line := range walkthrough(t) {
		snap, err := z.Snapshot()
		if err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
		z, err = NewMachine(story, 99999)
		if err != nil {
			t.Fatal(err)
		}
		if err := z.Restore(snap); err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
		// Exercising a clone must not disturb the original either.
		c := z.Clone()
		step(t, c, "look")
		step(t, c, "north")
		out += step(t, z, line)
	}
	if out != want {
		t.Fatal("transcript diverged from the uninterrupted run")
	}
	if !strings.Contains(out, perfect) {
		t.Fatal("did not reach 350")
	}
}

func TestSaveRestoreHandlers(t *testing.T) {
	z, _ := start(t)
	var slot []byte
	z.SaveHandler = func(b []byte) bool { slot = b; return true }
	z.RestoreHandler = func() ([]byte, bool) { return slot, slot != nil }

	if out := step(t, z, "restore"); !strings.Contains(out, "Failed") {
		t.Fatalf("restore with no save: %q", out)
	}
	step(t, z, "open mailbox")
	if out := step(t, z, "save"); !strings.Contains(out, "Ok") {
		t.Fatalf("save: %q", out)
	}
	step(t, z, "north")
	step(t, z, "north")
	if out := step(t, z, "restore"); !strings.Contains(out, "Ok") {
		t.Fatalf("restore: %q", out)
	}
	if out := step(t, z, "look"); !strings.Contains(out, "West of House") || !strings.Contains(out, "leaflet") {
		t.Fatalf("after restore: %q", out)
	}
	if err := z.Restore(slot); err == nil {
		t.Fatal("Restore accepted an in-game save")
	}
}

func TestRestoreRejectsDamage(t *testing.T) {
	z, _ := start(t)
	snap, err := z.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < len(snap); n += 37 {
		if err := z.Restore(snap[:n]); err == nil {
			t.Fatalf("accepted a snapshot truncated to %d bytes", n)
		}
	}
	other := append([]byte(nil), snap...)
	other[7]++ // release number
	if err := z.Restore(other); err == nil {
		t.Fatal("accepted a snapshot from another story")
	}
	if out := step(t, z, "look"); !strings.Contains(out, "West of House") {
		t.Fatalf("a rejected snapshot disturbed the game: %q", out)
	}
}

func TestRestartAndQuit(t *testing.T) {
	z, _ := start(t)
	step(t, z, "open mailbox")
	step(t, z, "take leaflet")
	step(t, z, "restart")
	if out := step(t, z, "y"); !strings.Contains(out, "West of House") {
		t.Fatalf("restart: %q", out)
	}
	if out := step(t, z, "inventory"); !strings.Contains(out, "empty-handed") {
		t.Fatalf("after restart: %q", out)
	}
	step(t, z, "quit")
	res, err := z.Step("y")
	if err != nil || res.Status != Halted {
		t.Fatalf("quit: %v %v", res.Status, err)
	}
	if res, err := z.Step("look"); err != nil || res.Status != Halted {
		t.Fatal("stepping a halted machine should stay halted")
	}
}

func TestParsing(t *testing.T) {
	for _, c := range []struct{ line, want string }{
		{"open  mailbox", "leaflet"},
		{"  open mailbox  ", "leaflet"},
		{"open mailbox. take leaflet", "Taken"},
		{"open mailbox.take leaflet", "Taken"},
		{"OPEN MAILBOX", "leaflet"},
		{"look, look", ""},
		{"abcdefghijk1", "don't know the word"},
		{"a\x00b\xff\xfe", ""},
		{strings.Repeat("x", 5000), ""},
		{strings.Repeat("x ", 500), ""},
		{"", "beg your pardon"},
		{"   ", "beg your pardon"},
		{"\r\n", "beg your pardon"},
	} {
		z, _ := start(t)
		if out := step(t, z, c.line); !strings.Contains(out, c.want) {
			t.Errorf("%.40q: got %q, want %q", c.line, out, c.want)
		}
		if out := step(t, z, "look"); !strings.Contains(out, "West of House") {
			t.Errorf("%.40q damaged the game: %q", c.line, out)
		}
	}
}

func TestInspect(t *testing.T) {
	z, _ := start(t)
	if n := z.ObjectCount(); n != 250 {
		t.Fatalf("ObjectCount = %d", n)
	}
	if n := z.ObjectName(217); n != "troll" {
		t.Fatalf("object 217 = %q", n)
	}
	if p := z.ObjectParent(217); p != 102 {
		t.Fatalf("troll is in %d", p)
	}
	room, score, moves := z.StatusLine()
	if room != "West of House" || score != 0 || moves != 0 {
		t.Fatalf("status %q %d %d", room, score, moves)
	}
	if z.ObjectParent(0) != 0 || z.ObjectName(9999) != "" || z.Global(-1) != 0 || z.ObjectAttr(1, 99) {
		t.Fatal("out-of-range access should give zero values")
	}
	if err := z.MoveObject(217, 180); err != nil {
		t.Fatal(err)
	}
	if out := step(t, z, "look"); !strings.Contains(out, "troll") {
		t.Fatalf("moved troll not described: %q", out)
	}
}
