package ui

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// Stopper is a run's stop request. The signal handler pulls it when Ctrl+C lands
// while plain lines are on screen, and the copy screen pulls it when Ctrl+C
// arrives as a key; either way every stage sees one closed channel.
type Stopper struct {
	ch   chan struct{}
	once sync.Once
}

func NewStopper() *Stopper { return &Stopper{ch: make(chan struct{})} }

// C is closed once a stop is requested.
func (s *Stopper) C() <-chan struct{} { return s.ch }

// Stop requests a stop. Calling it again does nothing.
func (s *Stopper) Stop() { s.once.Do(func() { close(s.ch) }) }

// Stopped reports whether a stop was requested.
func (s *Stopper) Stopped() bool {
	select {
	case <-s.ch:
		return true
	default:
		return false
	}
}

// Catch turns Ctrl+C (and SIGTERM) into a clean stop: the stage in flight
// abandons its file, removes any partial copy, and returns. A second Ctrl+C exits
// at once, for a stop that does not come quickly.
//
// Before this, the default handler killed the process mid-write and left a
// truncated file in the superdirectory under the source's own name. A Bubble Tea
// screen reads Ctrl+C as a key instead, and stops through the same Stopper.
func (s *Stopper) Catch() (release func()) {
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	quit := make(chan struct{})
	go func() {
		select {
		case <-sig:
			s.Stop()
		case <-quit:
			return
		}
		select {
		case <-sig:
			fmt.Println()
			os.Exit(130)
		case <-quit:
		}
	}()
	return func() {
		signal.Stop(sig)
		close(quit)
	}
}
