package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// Paths 保存所有运行时路径。所有路径均来自 TRIM_* / FNNAS_* 环境变量，
// 未设置时回退到应用安装目录下的默认位置（仅用于本地调试）。
type Paths struct {
	AppDest       string
	Etc           string
	Var           string
	Tmp           string
	Home          string
	GatewaySocket string
	GatewayPrefix string
	Arch          string
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// firstEnv 返回第一个非空环境变量。
func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// appName 为内置的应用名兜底（与 manifest 中保持一致）。
const appName = "MihomoProxy"

func NewPaths() *Paths {
	appdest := os.Getenv("TRIM_APPDEST")
	if appdest == "" {
		wd, err := os.Getwd()
		if err != nil {
			wd = "."
		}
		appdest = wd
	}
	p := &Paths{
		AppDest:       appdest,
		Etc:           envOr("TRIM_PKGETC", filepath.Join(appdest, "etc")),
		Var:           envOr("TRIM_PKGVAR", filepath.Join(appdest, "var")),
		Tmp:           envOr("TRIM_PKGTMP", filepath.Join(appdest, "tmp")),
		Home:          envOr("TRIM_PKGHOME", filepath.Join(appdest, "home")),
		// 网关环境变量：飞牛实际注入的是 TRIM_* 系列（GA 应用实测），FNNAS_* 作为兼容备选。
		GatewaySocket: firstEnv("TRIM_GATEWAY_SOCKET", "FNNAS_GATEWAY_SOCKET"),
		GatewayPrefix: firstEnv("TRIM_GATEWAY_PREFIX", "FNNAS_GATEWAY_PREFIX"),
	}
	if p.GatewayPrefix == "" {
		// 兜底：飞牛统一网关固定把应用挂在 /app/<appname>/ 下。
		p.GatewayPrefix = "/app/" + appName
	}
	switch runtime.GOARCH {
	case "amd64":
		p.Arch = "x86_64"
	case "arm64":
		p.Arch = "aarch64"
	default:
		p.Arch = runtime.GOARCH
	}
	p.GatewayPrefix = strings.TrimSuffix(p.GatewayPrefix, "/")
	return p
}

func (p *Paths) ConfigFile() string   { return filepath.Join(p.Etc, "config.yaml") }
func (p *Paths) TemplateFile() string { return filepath.Join(p.AppDest, "etc", "config.yaml.template") }
func (p *Paths) SubsFile() string     { return filepath.Join(p.Etc, "subscriptions.json") }
func (p *Paths) SettingsFile() string { return filepath.Join(p.Etc, "settings.json") }
func (p *Paths) MihomoSock() string   { return filepath.Join(p.Var, "mihomo.sock") }
func (p *Paths) MihomoPid() string    { return filepath.Join(p.Var, "mihomo.pid") }
func (p *Paths) MihomoLog() string    { return filepath.Join(p.Var, "mihomo.log") }
func (p *Paths) MihomoBin() string    { return filepath.Join(p.AppDest, "bin", p.Arch, "mihomo") }
func (p *Paths) PowerFile() string    { return filepath.Join(p.Var, "power.state") }

func (p *Paths) EnsureDirs() error {
	for _, d := range []string{p.Etc, p.Var, p.Tmp, p.Home} {
		if d == "" {
			continue
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// atomicWrite 原子写入：临时文件 + rename，避免半截文件被内核读取。
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// processAlive 通过信号 0 判定进程是否存在。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
