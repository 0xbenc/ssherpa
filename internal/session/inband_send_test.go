package session

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0xbenc/ssherpa/internal/inband"
)

// setInbandTimeouts shrinks the driver's wait windows for one test so
// the timeout paths resolve deterministically in milliseconds instead
// of the production 5s/5s/30s.
func setInbandTimeouts(t *testing.T, probe, ready, complete time.Duration) {
	t.Helper()
	origProbe, origReady, origComplete := inbandProbeTimeout, inbandReadyTimeout, inbandCompleteTimeout
	inbandProbeTimeout, inbandReadyTimeout, inbandCompleteTimeout = probe, ready, complete
	t.Cleanup(func() {
		inbandProbeTimeout, inbandReadyTimeout, inbandCompleteTimeout = origProbe, origReady, origComplete
	})
}

// fakeInbandRemote plays the far side of the in-band transfer with no
// ssh process or real PTY: it reads what the driver types into the PTY
// writer, replays each command line back through the output tap the way
// a remote tty in canonical echo mode would, and emits whatever
// scripted sentinel output the test calls for. The driver's two
// existing seams (ptmxRef and outputTap) carry everything.
type fakeInbandRemote struct {
	t   *testing.T
	tap *outputTap
	br  *bufio.Reader
}

func (rm *fakeInbandRemote) readLine() string {
	line, err := rm.br.ReadString('\n')
	if err != nil {
		rm.t.Errorf("fake remote: read typed command: %v (got %q)", err, line)
	}
	return strings.TrimSuffix(line, "\n")
}

func (rm *fakeInbandRemote) readExact(n int) []byte {
	buf := make([]byte, n)
	if _, err := io.ReadFull(rm.br, buf); err != nil {
		rm.t.Errorf("fake remote: read %d payload bytes: %v", n, err)
	}
	return buf
}

// readToEOF drains everything the driver writes until the test closes
// the PTY writer; timeout tests use it to capture the rollback bytes.
func (rm *fakeInbandRemote) readToEOF() []byte {
	data, err := io.ReadAll(rm.br)
	if err != nil {
		rm.t.Errorf("fake remote: drain remaining writes: %v", err)
	}
	return data
}

// echo replays a typed command the way a remote tty echoes it back
// before executing it.
func (rm *fakeInbandRemote) echo(line string) {
	rm.emit(line + "\r\n")
}

func (rm *fakeInbandRemote) emit(text string) {
	rm.tap.observe([]byte(text))
}

// startScriptedInband wires newInbandSendFunc to a pipe-backed fake
// remote running script in a goroutine. The returned finish func closes
// the driver's writer (EOF for the script) and waits for the script to
// end; reads of variables the script wrote are race-safe after finish
// returns.
func startScriptedInband(t *testing.T, script func(rm *fakeInbandRemote)) (InbandSendFunc, func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	ref := newPtmxRef()
	ref.set(w)
	tap := &outputTap{}
	rm := &fakeInbandRemote{t: t, tap: tap, br: bufio.NewReader(r)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		script(rm)
	}()
	var once sync.Once
	finish := func() {
		once.Do(func() {
			_ = w.Close()
			<-done
			_ = r.Close()
		})
	}
	t.Cleanup(finish)
	return newInbandSendFunc(ref, tap), finish
}

func writeInbandPayload(t *testing.T) (string, []byte) {
	t.Helper()
	// Larger than one 4096-byte write so the chunked streaming loop is
	// exercised.
	payload := bytes.Repeat([]byte("in-band driver payload\n"), 400)
	path := filepath.Join(t.TempDir(), "payload.bin")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	return path, payload
}

// TestInbandSendDriverHappyPathAgainstScriptedRemote replays a full
// successful transfer: probe, ready, payload, DONE. It pins the exact
// remote writes (probe command verbatim, structurally complete receiver
// command, base64 payload plus flush newline) and that no typed command
// ever carries a literal sentinel for the remote tty to echo back.
func TestInbandSendDriverHappyPathAgainstScriptedRemote(t *testing.T) {
	setInbandTimeouts(t, 2*time.Second, 2*time.Second, 5*time.Second)
	localPath, payload := writeInbandPayload(t)
	sum := sha256.Sum256(payload)
	wantHash := hex.EncodeToString(sum[:])
	encoded := base64.StdEncoding.EncodeToString(payload)

	var probeCmd, receiverCmd string
	var gotPayload []byte
	send, finish := startScriptedInband(t, func(rm *fakeInbandRemote) {
		probeCmd = rm.readLine()
		rm.echo(probeCmd)
		rm.emit(inband.ProbePrefix + " ok\n")
		receiverCmd = rm.readLine()
		rm.echo(receiverCmd)
		rm.emit(inband.ReadyPrefix + "\n")
		gotPayload = rm.readExact(len(encoded) + 1)
		rm.emit(inband.DonePrefix + " 0 " + wantHash + "\n")
	})

	res, err := send(InbandSendRequest{LocalPath: localPath, RemotePath: "/srv/app/out.bin"})
	finish()

	if err != nil {
		t.Fatalf("send returned error: %v", err)
	}
	if res.LocalPath != localPath || res.RemotePath != "/srv/app/out.bin" || res.Size != int64(len(payload)) || res.SHA256 != wantHash {
		t.Fatalf("result = %+v, want local=%q remote=/srv/app/out.bin size=%d sha=%s", res, localPath, len(payload), wantHash)
	}
	if probeCmd != inband.ProbeCommand() {
		t.Fatalf("probe command = %q, want %q", probeCmd, inband.ProbeCommand())
	}
	for _, want := range []string{
		fmt.Sprintf("head -c %d", len(encoded)),
		inband.ShellQuote("/srv/app/out.bin"),
		"'SSHERPA_''C_READY",
		"stty -echo -ixon -icanon",
	} {
		if !strings.Contains(receiverCmd, want) {
			t.Fatalf("receiver command = %q, want substring %q", receiverCmd, want)
		}
	}
	for _, sentinel := range []string{inband.ProbePrefix, inband.ReadyPrefix, inband.DonePrefix, inband.FailPrefix} {
		if strings.Contains(probeCmd, sentinel) || strings.Contains(receiverCmd, sentinel) {
			t.Fatalf("typed command contains literal sentinel %q; its echo would satisfy the driver's matcher", sentinel)
		}
	}
	if string(gotPayload) != encoded+"\n" {
		t.Fatalf("payload bytes = %d bytes, want base64 payload plus flush newline (%d bytes)", len(gotPayload), len(encoded)+1)
	}
}

// TestInbandSendDriverIgnoresTypedProbeEcho replays only the remote
// tty's echo of the typed probe command — including its quote-split
// sentinel construction text — and pins that the driver does not treat
// it as a probe result. Before the quote-split (HIGH-26) the echo
// carried the literal sentinel and turned the capability probe into a
// no-op.
func TestInbandSendDriverIgnoresTypedProbeEcho(t *testing.T) {
	setInbandTimeouts(t, 150*time.Millisecond, time.Second, time.Second)
	localPath, _ := writeInbandPayload(t)

	var probeCmd string
	send, finish := startScriptedInband(t, func(rm *fakeInbandRemote) {
		probeCmd = rm.readLine()
		rm.echo(probeCmd)
	})

	_, err := send(InbandSendRequest{LocalPath: localPath, RemotePath: "/srv/app/out.bin"})
	finish()

	if err == nil || !strings.Contains(err.Error(), "capability probe failed") || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want probe timeout because the echo must not satisfy the matcher", err)
	}
	if !strings.Contains(probeCmd, "'SSHERPA_''C_PROBE") {
		t.Fatalf("typed probe = %q, want quote-split sentinel construction text in the replayed echo", probeCmd)
	}
}

// TestInbandSendDriverIgnoresReceiverEchoAndRollsBack replays the echo
// of the typed receiver command (quote-split READY construction text)
// with no genuine READY output: the driver must not start streaming the
// payload, and on the ready timeout it must roll the remote back with
// an interrupt plus stty reset.
func TestInbandSendDriverIgnoresReceiverEchoAndRollsBack(t *testing.T) {
	setInbandTimeouts(t, time.Second, 150*time.Millisecond, time.Second)
	localPath, _ := writeInbandPayload(t)

	var receiverCmd string
	var trailing []byte
	send, finish := startScriptedInband(t, func(rm *fakeInbandRemote) {
		probeCmd := rm.readLine()
		rm.echo(probeCmd)
		rm.emit(inband.ProbePrefix + " ok\n")
		receiverCmd = rm.readLine()
		rm.echo(receiverCmd)
		trailing = rm.readToEOF()
	})

	_, err := send(InbandSendRequest{LocalPath: localPath, RemotePath: "/srv/app/out.bin"})
	finish()

	if err == nil || !strings.Contains(err.Error(), "receiver did not become ready") {
		t.Fatalf("err = %v, want ready timeout because the echo must not satisfy the matcher", err)
	}
	if !strings.Contains(receiverCmd, "'SSHERPA_''C_READY") {
		t.Fatalf("typed receiver = %q, want quote-split sentinel construction text in the replayed echo", receiverCmd)
	}
	if want := "\x03" + inband.ResetCommand() + "\n"; string(trailing) != want {
		t.Fatalf("post-echo writes = %q, want interrupt plus reset %q (and no payload bytes)", trailing, want)
	}
}

// TestInbandSendDriverFailSentinelFailsFast pins that a FAIL sentinel
// surfaces as the specific remote error immediately instead of
// degrading into the completion timeout, and that the driver still
// resets the remote.
func TestInbandSendDriverFailSentinelFailsFast(t *testing.T) {
	completeTimeout := 5 * time.Second
	setInbandTimeouts(t, time.Second, time.Second, completeTimeout)
	localPath, payload := writeInbandPayload(t)
	encoded := base64.StdEncoding.EncodeToString(payload)

	var trailing []byte
	send, finish := startScriptedInband(t, func(rm *fakeInbandRemote) {
		probeCmd := rm.readLine()
		rm.echo(probeCmd)
		rm.emit(inband.ProbePrefix + " ok\n")
		receiverCmd := rm.readLine()
		rm.echo(receiverCmd)
		rm.emit(inband.ReadyPrefix + "\n")
		rm.readExact(len(encoded) + 1)
		rm.emit(inband.FailPrefix + " hash deadbeef\n")
		trailing = rm.readToEOF()
	})

	start := time.Now()
	_, err := send(InbandSendRequest{LocalPath: localPath, RemotePath: "/srv/app/out.bin"})
	elapsed := time.Since(start)
	finish()

	if err == nil || !strings.Contains(err.Error(), "remote sha256 deadbeef did not match local sha256") {
		t.Fatalf("err = %v, want the specific remote hash failure", err)
	}
	if strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, FAIL sentinel degraded into a timeout", err)
	}
	if elapsed >= completeTimeout {
		t.Fatalf("send took %s, want immediate failure well before the %s completion window", elapsed, completeTimeout)
	}
	if want := "\x03" + inband.ResetCommand() + "\n"; string(trailing) != want {
		t.Fatalf("post-failure writes = %q, want interrupt plus reset %q", trailing, want)
	}
}

// TestInbandSendDriverCompletionTimeoutRollsBack pins the timeout
// rollback: when the remote never emits DONE or FAIL, the driver writes
// an interrupt (0x03) followed by the stty reset command.
func TestInbandSendDriverCompletionTimeoutRollsBack(t *testing.T) {
	setInbandTimeouts(t, time.Second, time.Second, 150*time.Millisecond)
	localPath, payload := writeInbandPayload(t)
	encoded := base64.StdEncoding.EncodeToString(payload)

	var trailing []byte
	send, finish := startScriptedInband(t, func(rm *fakeInbandRemote) {
		probeCmd := rm.readLine()
		rm.echo(probeCmd)
		rm.emit(inband.ProbePrefix + " ok\n")
		receiverCmd := rm.readLine()
		rm.echo(receiverCmd)
		rm.emit(inband.ReadyPrefix + "\n")
		rm.readExact(len(encoded) + 1)
		trailing = rm.readToEOF()
	})

	_, err := send(InbandSendRequest{LocalPath: localPath, RemotePath: "/srv/app/out.bin"})
	finish()

	if err == nil || !strings.Contains(err.Error(), "in-band transfer failed") || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want completion timeout", err)
	}
	if want := "\x03" + inband.ResetCommand() + "\n"; string(trailing) != want {
		t.Fatalf("post-timeout writes = %q, want interrupt plus reset %q", trailing, want)
	}
}
