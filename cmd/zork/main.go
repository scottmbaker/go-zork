// Go-Zork, a Zork Z-Engine in Golang
// Scott Baker, https://github.com/scottmbaker/
//
// Based on MojoZork by Ryan C. Gordon, https://github.com/icculus/mojozork

package main

import (
	"bufio"
	"flag"
	"fmt"
	"go-zork/pkg/zork"
	"os"
	"sync"
)

func main() {
	seed     := flag.Int64("seed", 0, "Seed for random number generator (0 for current time)")
	saveFile := flag.String("save", "save.dat", "path to save file")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options] [story_file]\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	fname := "zork1.dat"
	args := flag.Args()
	if len(args) >= 1 {
		fname = args[0]
	}

	inputChan := make(chan string)
	outputChan := make(chan []byte, 64) // buffered to avoid blocking ZMachine on output

	z := &zork.ZMachine{}
	z.InputChan = inputChan
	z.OutputChan = outputChan
	z.SaveFile = *saveFile

	if *seed != 0 {
		z.RandomSeed = int32(*seed)
		z.InitialSeed = int32(*seed)
	} else {
		z.RandomSeed = int32(os.Getpid()) + int32(flag.NArg())
	}

	if err := z.LoadStory(fname); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	var wg sync.WaitGroup

	// Drain output to stdout
	wg.Add(1)
	go func() {
		defer wg.Done()
		for output := range outputChan {
			os.Stdout.Write(output) //nolint:errcheck
		}
	}()

	// Feed stdin lines to ZMachine
	go func() {
		reader := bufio.NewReader(os.Stdin)
		for {
			line, err := reader.ReadString('\n')
			if len(line) > 0 {
				inputChan <- line
			}
			if err != nil {
				close(inputChan)
				return
			}
		}
	}()

	done := z.Run()
	if err := <-done; err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	wg.Wait() // ensure all output is flushed before exit
}
