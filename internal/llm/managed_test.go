package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestManagedServerHelper(t *testing.T) {
	helper := false
	port := ""
	for i, arg := range os.Args {
		if arg == "--managed-helper" {
			helper = true
		}
		if arg == "--port" && i+1 < len(os.Args) {
			port = os.Args[i+1]
		}
	}
	if !helper {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"status":"ok"}`) })
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	})
	if err := http.ListenAndServe("127.0.0.1:"+port, mux); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestManagedProfilesUnloadBeforeSwap(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + listener.Addr().String()
	listener.Close()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(binary)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatal(err)
	}
	file.Close()
	makeProvider := func(label string) Provider {
		p, err := build(ProviderSpec{Name: label, Kind: KindLlamaCPP, BaseURL: endpoint, Process: &ServerProcess{Argv: []string{binary, "-test.run=TestManagedServerHelper", "--", "--managed-helper", label}, BinarySHA256: hex.EncodeToString(hash.Sum(nil)), ReadyTimeoutSeconds: 5}})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	daily, deep := makeProvider("daily"), makeProvider("deep")
	defer daily.Close()
	defer deep.Close()
	if _, err := daily.Chat(context.Background(), ChatRequest{}); err != nil {
		t.Fatal(err)
	}
	first := localSlot.cmd.Process.Pid
	if _, err := deep.Chat(context.Background(), ChatRequest{}); err != nil {
		t.Fatal(err)
	}
	second := localSlot.cmd.Process.Pid
	if first == second || syscall.Kill(first, 0) != syscall.ESRCH {
		t.Fatal("previous model still resident after swap")
	}
	if _, err := deep.Chat(context.Background(), ChatRequest{}); err != nil {
		t.Fatal(err)
	}
	if localSlot.cmd.Process.Pid != second {
		t.Fatal("unchanged profile restarted")
	}
	if err := deep.Close(); err != nil {
		t.Fatal(err)
	}
	if localSlot.cmd != nil {
		t.Fatal("gateway leaked its model process")
	}
}

func TestManagedProfileRejectsUnpinnedOrRemoteServer(t *testing.T) {
	for _, endpoint := range []string{"http://127.0.0.1:9000", "http://example.com:9000"} {
		_, err := build(ProviderSpec{Name: "bad", Kind: KindLlamaCPP, BaseURL: endpoint, Process: &ServerProcess{Argv: []string{"/bin/false"}, BinarySHA256: strings.Repeat("0", 1)}})
		if err == nil {
			t.Fatal("unsafe managed profile accepted")
		}
	}
}
