package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

func main() {
	var (
		appDir  = flag.String("app", "", "应用目录")
		dataDir = flag.String("data", "", "数据目录")
		listen  = flag.String("listen", "", "监听端口")
		wizard  = flag.Bool("apply-wizard", false, "把安装向导变量写入配置")
		showVer = flag.Bool("version", false, "打印版本")
	)
	flag.Parse()
	if *showVer {
		fmt.Printf("aitrader %s\n", appVersion)
		return
	}
	if *wizard {
		resolved, err := resolveAppDir(*appDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "启动失败：%v\n", err)
			os.Exit(1)
		}
		data := *dataDir
		if data == "" {
			data = os.Getenv("TRIM_PKGVAR")
		}
		if err := applyWizard(resolved, data); err != nil {
			fmt.Fprintf(os.Stderr, "写入设置失败：%v\n", err)
			os.Exit(1)
		}
		return
	}
	if err := run(*appDir, *dataDir, *listen); err != nil {
		fmt.Fprintf(os.Stderr, "启动失败：%v\n", err)
		os.Exit(1)
	}
}

func run(appDir, dataDir, listen string) error {
	resolved, err := resolveAppDir(appDir)
	if err != nil {
		return err
	}
	if dataDir == "" {
		dataDir = os.Getenv("TRIM_PKGVAR")
	}
	if dataDir == "" {
		dataDir = filepath.Join(filepath.Dir(resolved), "var")
	}
	if listen == "" {
		listen = os.Getenv("TRIM_SERVICE_PORT")
	}
	app, err := NewApp(resolved, dataDir, listen)
	if err != nil {
		return err
	}
	app.Start()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	go func() { <-sig; app.Stop() }()
	if err := app.Run(); err != nil {
		app.Stop()
		return err
	}
	app.Stop()
	return nil
}

func resolveAppDir(explicit string) (string, error) {
	if explicit != "" {
		return filepath.Abs(explicit)
	}
	if env := os.Getenv("TRIM_APPDEST"); env != "" {
		return filepath.Abs(env)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(filepath.Dir(exe)), nil
}
