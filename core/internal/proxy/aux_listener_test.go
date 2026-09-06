package proxy

import (
	"fmt"
	"net"
	"testing"

	"github.com/alpkeskin/rota/core/pkg/logger"
)

// freePorts returns n ports that were free a moment ago, by binding and
// releasing them. Racy in principle, fine in practice for a single test
// process and far better than hard-coding numbers another test might hold.
func freePorts(t *testing.T, n int) []int {
	t.Helper()
	out := make([]int, 0, n)
	held := make([]net.Listener, 0, n)
	for i := 0; i < n; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("reserve port: %v", err)
		}
		held = append(held, ln)
		out = append(out, ln.Addr().(*net.TCPAddr).Port)
	}
	for _, ln := range held {
		_ = ln.Close()
	}
	return out
}

func testRegistry(t *testing.T) *AuxRegistry {
	t.Helper()
	reg := NewAuxRegistry("main_machine", "127.0.0.1", 1, logger.New("error"))
	t.Cleanup(reg.Close)
	return reg
}

// canConnect reports whether something is accepting on the port right now.
func canConnect(port int) bool {
	c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func spec(machine, country string, port int, mode string) AuxListenerSpec {
	return AuxListenerSpec{MachineID: machine, Country: country, Port: port, Mode: mode}
}

func TestApplyBindsNewPortWithoutRestart(t *testing.T) {
	ports := freePorts(t, 1)
	reg := testRegistry(t)

	if canConnect(ports[0]) {
		t.Fatalf("port %d busy before test", ports[0])
	}

	delta := reg.Apply([]AuxListenerSpec{spec("main_machine", "Greece", ports[0], "")})

	if len(delta.Failed) != 0 {
		t.Fatalf("unexpected failures: %v", delta.Failed)
	}
	if len(delta.Added) != 1 || delta.Added[0] != ports[0] {
		t.Fatalf("Added = %v, want [%d]", delta.Added, ports[0])
	}
	if !canConnect(ports[0]) {
		t.Fatalf("port %d not accepting after Apply", ports[0])
	}
}

func TestApplyClosesRemovedPort(t *testing.T) {
	ports := freePorts(t, 1)
	reg := testRegistry(t)

	reg.Apply([]AuxListenerSpec{spec("main_machine", "Greece", ports[0], "")})
	if !canConnect(ports[0]) {
		t.Fatal("setup: port never opened")
	}

	delta := reg.Apply(nil)

	if len(delta.Removed) != 1 || delta.Removed[0] != ports[0] {
		t.Fatalf("Removed = %v, want [%d]", delta.Removed, ports[0])
	}
	if canConnect(ports[0]) {
		t.Fatalf("port %d still accepting after removal", ports[0])
	}
	if got := reg.ActivePorts(); len(got) != 0 {
		t.Fatalf("ActivePorts = %v, want empty", got)
	}
}

func TestApplyLeavesUnchangedListenerAlone(t *testing.T) {
	ports := freePorts(t, 2)
	reg := testRegistry(t)

	keep := spec("main_machine", "Greece", ports[0], "")
	reg.Apply([]AuxListenerSpec{keep})

	// Grab the live socket so we can prove it is the same object afterwards —
	// an unchanged listener must not drop its in-flight connections.
	before := reg.active[ports[0]].listener

	delta := reg.Apply([]AuxListenerSpec{keep, spec("main_machine", "France", ports[1], "")})

	if len(delta.Added) != 1 || delta.Added[0] != ports[1] {
		t.Fatalf("Added = %v, want [%d]", delta.Added, ports[1])
	}
	if len(delta.Removed) != 0 || len(delta.Rebound) != 0 {
		t.Fatalf("untouched listener disturbed: removed=%v rebound=%v", delta.Removed, delta.Rebound)
	}
	if reg.active[ports[0]].listener != before {
		t.Fatal("unchanged listener was rebound")
	}
}

func TestApplyRebindsWhenRoutingChanges(t *testing.T) {
	ports := freePorts(t, 1)
	reg := testRegistry(t)

	reg.Apply([]AuxListenerSpec{spec("main_machine", "Greece", ports[0], "")})
	before := reg.active[ports[0]].listener

	// Same port, different mode — the injected credentials change, so the
	// socket has to be rebound rather than left as-is.
	delta := reg.Apply([]AuxListenerSpec{spec("main_machine", "Greece", ports[0], "rotate")})

	if len(delta.Rebound) != 1 || delta.Rebound[0] != ports[0] {
		t.Fatalf("Rebound = %v, want [%d]", delta.Rebound, ports[0])
	}
	if len(delta.Added) != 0 || len(delta.Removed) != 0 {
		t.Fatalf("rebind reported as add/remove: added=%v removed=%v", delta.Added, delta.Removed)
	}
	if reg.active[ports[0]].listener == before {
		t.Fatal("listener was not actually rebound")
	}
	if reg.active[ports[0]].spec.Mode != "rotate" {
		t.Fatalf("mode = %q, want rotate", reg.active[ports[0]].spec.Mode)
	}
	if !canConnect(ports[0]) {
		t.Fatal("port stopped accepting after rebind")
	}
}

// Moving a listener from one port to another must close the old socket before
// binding the new one, otherwise a swap between two ports deadlocks on itself.
func TestApplySwapsPorts(t *testing.T) {
	ports := freePorts(t, 2)
	reg := testRegistry(t)

	reg.Apply([]AuxListenerSpec{
		spec("main_machine", "Greece", ports[0], ""),
		spec("main_machine", "France", ports[1], ""),
	})

	delta := reg.Apply([]AuxListenerSpec{
		spec("main_machine", "France", ports[0], ""),
		spec("main_machine", "Greece", ports[1], ""),
	})

	if len(delta.Failed) != 0 {
		t.Fatalf("swap failed: %v", delta.Failed)
	}
	if len(delta.Rebound) != 2 {
		t.Fatalf("Rebound = %v, want both ports", delta.Rebound)
	}
	if got := reg.active[ports[0]].spec.Country; got != "France" {
		t.Fatalf("port %d country = %q, want France", ports[0], got)
	}
	if got := reg.active[ports[1]].spec.Country; got != "Greece" {
		t.Fatalf("port %d country = %q, want Greece", ports[1], got)
	}
}

// A port held by another process must be reported, not silently dropped, and
// must not take the other listeners down with it.
func TestApplyReportsBindFailureAndKeepsRest(t *testing.T) {
	ports := freePorts(t, 2)
	reg := testRegistry(t)

	blocker, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", ports[1]))
	if err != nil {
		t.Fatalf("blocker bind: %v", err)
	}
	defer blocker.Close()

	delta := reg.Apply([]AuxListenerSpec{
		spec("main_machine", "Greece", ports[0], ""),
		spec("main_machine", "France", ports[1], ""),
	})

	if _, ok := delta.Failed[ports[1]]; !ok {
		t.Fatalf("Failed = %v, want an entry for %d", delta.Failed, ports[1])
	}
	if len(delta.Added) != 1 || delta.Added[0] != ports[0] {
		t.Fatalf("Added = %v, want [%d] — a bad port must not block a good one", delta.Added, ports[0])
	}
	if !canConnect(ports[0]) {
		t.Fatal("healthy listener did not come up")
	}
}

// An unknown machine_id is a config error, and must be reported without
// touching sockets that are working.
func TestApplyRejectsUnknownMachine(t *testing.T) {
	ports := freePorts(t, 2)
	reg := testRegistry(t)

	good := spec("main_machine", "Greece", ports[0], "")
	reg.Apply([]AuxListenerSpec{good})

	delta := reg.Apply([]AuxListenerSpec{good, spec("not_a_real_machine", "France", ports[1], "")})

	if _, ok := delta.Failed[ports[1]]; !ok {
		t.Fatalf("Failed = %v, want an entry for %d", delta.Failed, ports[1])
	}
	if !canConnect(ports[0]) {
		t.Fatal("valid listener was taken down by an invalid sibling")
	}
}

// An empty MachineID inherits ROUTING_DEFAULT_MACHINE, matching how the
// startup path resolves manual .env entries.
func TestApplyInheritsDefaultMachine(t *testing.T) {
	ports := freePorts(t, 1)
	reg := testRegistry(t)

	delta := reg.Apply([]AuxListenerSpec{spec("", "Greece", ports[0], "")})

	if len(delta.Failed) != 0 {
		t.Fatalf("unexpected failures: %v", delta.Failed)
	}
	if got := reg.active[ports[0]].spec.MachineID; got != "main_machine" {
		t.Fatalf("machine_id = %q, want main_machine", got)
	}
}

func TestStartFailsOnDuplicatePort(t *testing.T) {
	ports := freePorts(t, 1)
	reg := testRegistry(t)

	err := reg.Start([]AuxListenerSpec{
		spec("main_machine", "Greece", ports[0], ""),
		spec("main_machine", "France", ports[0], ""),
	})
	if err == nil {
		t.Fatal("want error for duplicate port, got nil")
	}
}
