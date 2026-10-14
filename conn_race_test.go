package main

import (
	"sync"
	"testing"
)

// The webview hits the local HTTP API from several goroutines at once; an
// unlocked read of savedConnections alongside a locked append is a real data
// race (main.go Connect / savedDatabaseFilter). Run with -race.
func TestConnectionListReadersRaceWriters(t *testing.T) {
	a := &App{}
	a.savedConnections = []ConnectionConfig{{ID: "aabbccdd00112233", Type: "sqlite", Database: ":memory:"}}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				a.savedDatabaseFilter("aabbccdd00112233")
				a.GetConnections()
				_ = a.Connect("nonexistent-id") // invalid id: bounds check only, no dial
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 50; j++ {
			if err := a.AddConnection(ConnectionConfig{Type: "sqlite", Database: ":memory:"}); err != nil {
				t.Errorf("AddConnection: %v", err)
				return
			}
		}
	}()
	wg.Wait()
}
