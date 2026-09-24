package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Kernel pipe limits that let HAProxy use 1 MiB splice pipes
// (tune.pipesize 1048576). With the default fs.pipe-user-pages-soft (16384
// pages per user) the kernel silently shrinks pipes of a user above the soft
// limit to one page, so the Panel only renders 1 MiB pipes once the Agent
// reports both limits as tuned.
const (
	TargetPipeMaxSize        = 1048576
	KernelPipesSysctlFile    = "90-nodeflow-pipes.conf"
	defaultKernelPipesProc   = "/proc"
	defaultKernelPipesSysctl = "/etc/sysctl.d"
	pipeMaxSizePath          = "sys/fs/pipe-max-size"
	pipeUserPagesSoftPath    = "sys/fs/pipe-user-pages-soft"
)

// KernelPipes is the heartbeat capability report (metrics.kernel_pipes).
// Tuned means the live kernel limits allow 1 MiB pipes for every user:
// pipe-max-size >= 1048576 and pipe-user-pages-soft == 0 (no soft limit).
type KernelPipes struct {
	PipeMaxSize       int64  `json:"pipe_max_size"`
	PipeUserPagesSoft int64  `json:"pipe_user_pages_soft"`
	Tuned             bool   `json:"tuned"`
	Managed           bool   `json:"managed"`
	Error             string `json:"error,omitempty"`
}

// KernelPipeTuner raises the kernel pipe limits at startup and on every
// heartbeat. It writes /proc/sys directly (no sysctl binary) and persists the
// same values in /etc/sysctl.d so they survive a reboot. It never lowers
// pipe-max-size. Disabled (NODE_AGENT_TUNE_SYSCTL=off) only reports.
type KernelPipeTuner struct {
	ProcRoot  string
	SysctlDir string
	Disabled  bool

	mu        sync.Mutex
	lastError string
}

// NewKernelPipeTunerFromEnv honours NODE_AGENT_TUNE_SYSCTL=off.
func NewKernelPipeTunerFromEnv() *KernelPipeTuner {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("NODE_AGENT_TUNE_SYSCTL")))
	return &KernelPipeTuner{Disabled: mode == "off" || mode == "false" || mode == "0"}
}

func (t *KernelPipeTuner) procRoot() string {
	if t.ProcRoot != "" {
		return t.ProcRoot
	}
	return defaultKernelPipesProc
}

func (t *KernelPipeTuner) sysctlDir() string {
	if t.SysctlDir != "" {
		return t.SysctlDir
	}
	return defaultKernelPipesSysctl
}

// Ensure applies the limits when needed (idempotent: no write when the live
// values and the persisted file already match) and returns the resulting
// state. Errors are reported in KernelPipes.Error, never fatal.
func (t *KernelPipeTuner) Ensure() KernelPipes {
	if t == nil {
		return KernelPipes{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	maxPath := filepath.Join(t.procRoot(), pipeMaxSizePath)
	softPath := filepath.Join(t.procRoot(), pipeUserPagesSoftPath)
	maxSize, maxErr := readKernelInt(maxPath)
	soft, softErr := readKernelInt(softPath)
	if maxErr != nil || softErr != nil {
		return KernelPipes{Managed: !t.Disabled, Error: "read kernel pipe limits: " + errors.Join(maxErr, softErr).Error()}
	}
	state := KernelPipes{PipeMaxSize: maxSize, PipeUserPagesSoft: soft, Managed: !t.Disabled}
	if !t.Disabled {
		var errs []error
		wantMax := maxSize
		if wantMax < TargetPipeMaxSize {
			wantMax = TargetPipeMaxSize
		}
		// Persist first so a reboot keeps the values even when a live write
		// is refused; never persist a value below the current one.
		if err := writeFileIfChanged(filepath.Join(t.sysctlDir(), KernelPipesSysctlFile), kernelPipesSysctlContent(wantMax)); err != nil {
			errs = append(errs, err)
		}
		if soft != 0 {
			if err := writeKernelInt(softPath, 0); err != nil {
				errs = append(errs, err)
			}
		}
		if maxSize < TargetPipeMaxSize {
			if err := writeKernelInt(maxPath, TargetPipeMaxSize); err != nil {
				errs = append(errs, err)
			}
		}
		if value, err := readKernelInt(maxPath); err == nil {
			state.PipeMaxSize = value
		}
		if value, err := readKernelInt(softPath); err == nil {
			state.PipeUserPagesSoft = value
		}
		if len(errs) > 0 {
			state.Error = errors.Join(errs...).Error()
		}
	}
	state.Tuned = state.PipeMaxSize >= TargetPipeMaxSize && state.PipeUserPagesSoft == 0
	t.lastError = state.Error
	return state
}

// LastError returns the error of the most recent Ensure (for logging).
func (t *KernelPipeTuner) LastError() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastError
}

func kernelPipesSysctlContent(pipeMaxSize int64) string {
	return "# Managed by NodeFlow Node Agent (NODE_AGENT_TUNE_SYSCTL=off disables).\n" +
		"# Lets HAProxy use 1 MiB splice pipes (tune.pipesize 1048576).\n" +
		"fs.pipe-user-pages-soft = 0\n" +
		"fs.pipe-max-size = " + strconv.FormatInt(pipeMaxSize, 10) + "\n"
}

func readKernelInt(path string) (int64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", path, err)
	}
	return value, nil
}

func writeKernelInt(path string, value int64) error {
	// One write(2) on the existing file, opened like sysctl(8) does
	// (fopen "w"); O_TRUNC is a no-op for /proc/sys and keeps fake roots exact.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString(strconv.FormatInt(value, 10) + "\n")
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("write %s: %w", path, writeErr)
	}
	return closeErr
}

func writeFileIfChanged(path, content string) error {
	if current, err := os.ReadFile(path); err == nil && string(current) == content {
		return nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
