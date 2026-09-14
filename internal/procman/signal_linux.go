package procman

import (
	"os"
	"os/signal"
	"syscall"
)

func notifyChild(ch chan<- os.Signal) { signal.Notify(ch, syscall.SIGCHLD) }
