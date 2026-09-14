package api

import "os"

func pid() int { return os.Getpid() }
