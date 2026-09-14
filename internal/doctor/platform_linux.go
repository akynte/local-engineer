package doctor

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// filesystemTypes maps the magic numbers statfs reports to names. Only the
// ones §5.4 cares about need to be listed; anything else is reported by its
// hexadecimal magic so a bug report still carries the fact.
var filesystemTypes = map[int64]string{
	0x794c7630: "overlayfs",
	0x61756673: "aufs",
	0x6969:     "nfs",
	0xff534d42: "cifs",
	0x65735546: "fuse",
	0xef53:     "ext4",
	0x9123683e: "btrfs",
	0x58465342: "xfs",
	0x2fc12fc1: "zfs",
	0x01021994: "tmpfs",
	0x5346544e: "ntfs",
	0x4d44:     "vfat",
}

func filesystemType(path string) (string, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return "", err
	}
	magic := int64(st.Type)
	if name, ok := filesystemTypes[magic]; ok {
		return name, nil
	}
	return "0x" + strconv.FormatInt(magic, 16), nil
}

// queryVRAM asks the NVIDIA tooling for total VRAM. A machine without it is
// not an error: CPU-only hosts are a supported profile (§9.2).
func queryVRAM() int {
	out, err := exec.Command("nvidia-smi", "--query-gpu=memory.total", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return 0
	}
	first := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	mb, err := strconv.Atoi(first)
	if err != nil {
		return 0
	}
	return mb
}
