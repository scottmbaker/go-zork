package zork

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Status says why a Start or Step call returned.
type Status int

const (
	WaitingForInput Status = iota // the game wants another line
	Halted                        // the game has quit
)

// StepResult is what one turn produced.
type StepResult struct {
	Output []byte
	Status Status
}

// StepBudget caps the instructions one Start or Step may run, so that a damaged
// game state cannot spin forever. A normal turn takes a few thousand.
const StepBudget = 5_000_000

// ErrBudget is returned when a turn exceeds StepBudget.
var ErrBudget = errors.New("instruction budget exceeded")

const opRead = 228 // VAR:228, sread

// NewMachine creates a machine to be driven synchronously with Start and Step.
// No goroutines or channels are involved. The story image is not modified.
func NewMachine(story []byte, seed int32) (*ZMachine, error) {
	z := &ZMachine{RandomSeed: seed, InitialSeed: seed, stepping: true}
	if err := z.LoadStoryBytes(story); err != nil {
		return nil, err
	}
	return z, nil
}

// Start runs a new game up to its first request for input.
func (z *ZMachine) Start() (StepResult, error) { return z.run() }

// Step feeds one line of input and runs to the next request for input.
func (z *ZMachine) Step(line string) (StepResult, error) {
	if z.Quit {
		return StepResult{Status: Halted}, nil
	}
	if !z.AtInput() {
		return StepResult{}, errors.New("machine is not waiting for input")
	}
	z.pending = &line
	return z.run()
}

// AtInput reports whether the machine is stopped at a request for input.
func (z *ZMachine) AtInput() bool {
	return !z.Quit && int(z.PC) < len(z.Story) && z.Story[z.PC] == opRead
}

// run executes until the next instruction is a read with no line to give it.
// Stopping before the read, rather than inside it, leaves nothing half done:
// its operands have not been taken off the stack, so the state can be saved.
func (z *ZMachine) run() (res StepResult, err error) {
	if !z.stepping {
		return res, errors.New("machine was not created with NewMachine")
	}
	// A corrupt state can index outside memory; that must not kill a server.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("z-machine fault at %#x: %v", z.LogicalPC, r)
		}
		res.Output = z.out
		z.out = nil
	}()
	for n := 0; ; n++ {
		if z.Quit {
			res.Status = Halted
			return res, nil
		}
		if z.pending == nil && z.Story[z.PC] == opRead {
			res.Status = WaitingForInput
			return res, nil
		}
		if n >= StepBudget {
			return res, ErrBudget
		}
		if err := z.RunInstruction(); err != nil {
			return res, err
		}
	}
}

// --- snapshots ---------------------------------------------------------------

// A snapshot is a header followed by the deflated dynamic memory and the live
// part of the stack. The header identifies the story so that a snapshot is
// never loaded into the wrong game.
type snapKind uint8

const (
	snapInput snapKind = 1 // taken between turns, waiting for input
	snapSaved snapKind = 2 // taken inside the in-game save command
)

const snapVersion = 1

var snapMagic = [4]byte{'Z', 'S', 'N', 'P'}

type snapHeader struct {
	Magic       [4]byte
	Version     uint8
	Kind        uint8
	Release     uint16
	Serial      [6]byte
	Checksum    uint16
	PC          uint32
	SP, BP      uint32
	RandomSeed  int32
	InitialSeed int32
	DynLen      uint32
}

func (z *ZMachine) snapshot(kind snapKind) []byte {
	h := snapHeader{
		Magic: snapMagic, Version: snapVersion, Kind: uint8(kind),
		Release: z.Header.Release, Checksum: z.Header.StoryChecksum,
		PC: z.PC, SP: uint32(z.SP), BP: uint32(z.BP),
		RandomSeed: z.RandomSeed, InitialSeed: z.InitialSeed,
		DynLen: uint32(z.Header.StaticmemAddr),
	}
	copy(h.Serial[:], z.Header.SerialCode[:6])

	// Writing to a bytes.Buffer cannot fail, so the errors below are ignored.
	var buf bytes.Buffer
	binary.Write(&buf, binary.BigEndian, h) //nolint:errcheck
	w, _ := flate.NewWriter(&buf, flate.BestSpeed)
	w.Write(z.Story[:z.Header.StaticmemAddr])         //nolint:errcheck
	binary.Write(w, binary.BigEndian, z.Stack[:z.SP]) //nolint:errcheck
	w.Close()                                         //nolint:errcheck
	return buf.Bytes()
}

// restore replaces the machine's state. Nothing is changed unless the whole
// blob is valid.
func (z *ZMachine) restore(blob []byte, kind snapKind) error {
	r := bytes.NewReader(blob)
	var h snapHeader
	if err := binary.Read(r, binary.BigEndian, &h); err != nil {
		return fmt.Errorf("snapshot is too short: %w", err)
	}
	if h.Magic != snapMagic || h.Version != snapVersion {
		return errors.New("not a snapshot this version understands")
	}
	if snapKind(h.Kind) != kind {
		return errors.New("wrong kind of snapshot")
	}
	if h.Release != z.Header.Release || h.Checksum != z.Header.StoryChecksum ||
		!bytes.Equal(h.Serial[:], z.Header.SerialCode[:6]) ||
		h.DynLen != uint32(z.Header.StaticmemAddr) {
		return errors.New("snapshot belongs to a different story file")
	}
	if h.SP > uint32(len(z.Stack)) || h.BP > h.SP || h.PC >= uint32(len(z.Story)) {
		return errors.New("snapshot is corrupt")
	}
	fr := flate.NewReader(r)
	dyn := make([]byte, h.DynLen)
	if _, err := io.ReadFull(fr, dyn); err != nil {
		return fmt.Errorf("snapshot memory: %w", err)
	}
	stack := make([]uint16, h.SP)
	if err := binary.Read(fr, binary.BigEndian, stack); err != nil {
		return fmt.Errorf("snapshot stack: %w", err)
	}

	copy(z.Story, dyn)
	z.Stack = [2048]uint16{}
	copy(z.Stack[:], stack)
	z.PC, z.LogicalPC = h.PC, h.PC
	z.SP, z.BP = int(h.SP), int(h.BP)
	z.RandomSeed, z.InitialSeed = h.RandomSeed, h.InitialSeed
	z.Quit, z.StepCompleted = false, false
	return nil
}

// Snapshot captures a machine that is waiting for input, including its random
// number generator, so that Restore continues exactly where it left off.
func (z *ZMachine) Snapshot() ([]byte, error) {
	if !z.AtInput() {
		return nil, errors.New("can only snapshot while waiting for input")
	}
	return z.snapshot(snapInput), nil
}

// Restore loads a Snapshot of the same story.
func (z *ZMachine) Restore(blob []byte) error {
	if err := z.restore(blob, snapInput); err != nil {
		return err
	}
	z.pending = nil
	if !z.AtInput() {
		return errors.New("snapshot is corrupt: not at an input request")
	}
	return nil
}

// Clone returns an independent copy, for asking "what if" questions without
// disturbing the game. The copy has no save or restore handlers and refuses
// both commands.
func (z *ZMachine) Clone() *ZMachine {
	c := *z
	c.Story = append([]byte(nil), z.Story...)
	c.out, c.pending = nil, nil
	c.InputChan, c.OutputChan = nil, nil
	c.stepping = true
	c.SaveHandler = func([]byte) bool { return false }
	c.RestoreHandler = func() ([]byte, bool) { return nil, false }
	c.initOpcodeTable() // the table holds method values bound to the original
	return &c
}
