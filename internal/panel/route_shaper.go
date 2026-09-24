package panel

import (
	"fmt"
	"strconv"
	"strings"
)

// Route shaper modes. "haproxy" enforces client_*_mbps with HAProxy bwlim
// filters, which forces the buffered (non-splice) path for the whole
// frontend. "kernel" delegates enforcement to the Node Agent (nftables
// per-client token buckets) so HAProxy keeps splice(2).
const (
	ShaperModeHAProxy = "haproxy"
	ShaperModeKernel  = "kernel"

	// kernelShaperFailedCode is reported by the Agent through the config
	// report path when the nftables shaper cannot be reconciled.
	kernelShaperFailedCode = "kernel_shaper_failed"
	// kernelShaperFailureHold keeps an Agent-reported shaper failure visible
	// while heartbeats confirm the same HAProxy revision. The Agent clears it
	// with an "applied" report once the shaper recovers.
	kernelShaperFailureHold = `(node_config_state.state='failed' AND node_config_state.last_error='kernel_shaper_failed' AND node_config_state.actual_revision=$2)`

	kernelShaperAnnotationPrefix = "# nf-kernel-shaper "
)

func normalizeShaperMode(value string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(value))
	switch mode {
	case "":
		return ShaperModeHAProxy, nil
	case ShaperModeHAProxy, ShaperModeKernel:
		return mode, nil
	}
	return "", fmt.Errorf("shaper_mode must be haproxy or kernel")
}

func storedShaperMode(value string) string {
	if value == ShaperModeKernel {
		return ShaperModeKernel
	}
	return ShaperModeHAProxy
}

// fingerprintShaperMode omits the default so fingerprints of routes created
// before shaper_mode existed are unchanged.
func fingerprintShaperMode(value string) string {
	if value == ShaperModeKernel {
		return ShaperModeKernel
	}
	return ""
}

// routeUsesHAProxyBandwidth reports whether the renderer must emit bwlim
// filters, set-bandwidth-limit rules and stick-table backends for route.
func routeUsesHAProxyBandwidth(route renderRoute) bool {
	if route.ShaperMode == ShaperModeKernel {
		return false
	}
	return route.ClientDownloadMbps != nil || route.ClientUploadMbps != nil
}

// kernelShaperAnnotation is the machine-readable frontend comment consumed by
// the Node Agent (internal/agent/shaper_tc.go). Only emitted for kernel-mode
// routes that carry a limit. Rates are bytes/second (1 Mbps = 125000 B/s).
func kernelShaperAnnotation(listener renderListener, route renderRoute) string {
	if route.ShaperMode != ShaperModeKernel || (route.ClientDownloadMbps == nil && route.ClientUploadMbps == nil) {
		return ""
	}
	download, _ := bandwidthLimitBytes(route.ClientDownloadMbps)
	upload, _ := bandwidthLimitBytes(route.ClientUploadMbps)
	listen := listener.IP
	if listen == "" {
		listen = "*"
	}
	return "    " + kernelShaperAnnotationPrefix + "listen=" + listen + " port=" + strconv.Itoa(listener.Port) +
		" download_bps=" + strconv.FormatInt(download, 10) + " upload_bps=" + strconv.FormatInt(upload, 10) + "\n"
}

// validateKernelShaperListener rejects listeners where kernel shaping would be
// ambiguous. The kernel classifies by listener address/port and client IP; it
// cannot see SNI, so every kernel-shaped route sharing a listener must carry
// identical limits, and a listener cannot mix kernel and HAProxy shaping.
func validateKernelShaperListener(listener renderListener) error {
	var first *renderRoute
	haproxyLimited := false
	for i := range listener.Routes {
		route := &listener.Routes[i]
		if routeUsesHAProxyBandwidth(*route) {
			haproxyLimited = true
		}
		if kernelShaperAnnotation(listener, *route) == "" {
			continue
		}
		if first == nil {
			first = route
			continue
		}
		download1, _ := bandwidthLimitBytes(first.ClientDownloadMbps)
		download2, _ := bandwidthLimitBytes(route.ClientDownloadMbps)
		upload1, _ := bandwidthLimitBytes(first.ClientUploadMbps)
		upload2, _ := bandwidthLimitBytes(route.ClientUploadMbps)
		if download1 != download2 || upload1 != upload2 {
			return &RouteSetError{Reason: "listener " + listenerKey(listener.IP, listener.Port) +
				" has kernel-shaped routes " + first.ID + " and " + route.ID + " with different client limits"}
		}
	}
	if first != nil && haproxyLimited {
		return &RouteSetError{Reason: "listener " + listenerKey(listener.IP, listener.Port) +
			" mixes shaper_mode kernel and haproxy for client bandwidth limits"}
	}
	return nil
}
