package panel

import (
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/nodeflow/nodeflow/internal/bootstrap"
)

type hostKeyScanInput struct {
	Address   string `json:"address"`
	SSHPort   int    `json:"ssh_port"`
	Algorithm string `json:"algorithm"`
}

func (a *API) hostKeyScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	var in hostKeyScanInput
	if !decode(w, r, &in) {
		return
	}
	in.Address, in.Algorithm = strings.TrimSpace(in.Address), strings.TrimSpace(in.Algorithm)
	if in.SSHPort == 0 {
		in.SSHPort = 22
	}
	if in.Algorithm == "" {
		in.Algorithm = "ssh-ed25519"
	}
	if net.ParseIP(in.Address) == nil || in.SSHPort < 1 || in.SSHPort > 65535 {
		writeError(w, http.StatusBadRequest, "validation_error", "valid IP address and SSH port are required")
		return
	}
	ctx, cancel := contextTimeout(r, 12*time.Second)
	defer cancel()
	result, err := bootstrap.ScanHostKey(ctx, in.Address, in.SSHPort, in.Algorithm)
	if err != nil {
		writeError(w, http.StatusBadGateway, "host_key_scan_failed", "SSH host key could not be read")
		return
	}
	writeJSON(w, http.StatusOK, result)
}
