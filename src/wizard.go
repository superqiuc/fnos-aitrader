package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// applyWizard 把 fnOS 安装向导的 wizard_* 环境变量写入 config.json。
func applyWizard(appDir, dataDir string) error {
	if dataDir == "" {
		return errors.New("缺少数据目录（TRIM_PKGVAR）")
	}
	app, err := NewApp(appDir, dataDir, "0")
	if err != nil {
		return err
	}
	proxy, hasProxy := envValue("wizard_proxy_url")
	cashC, hasCashC := envValue("wizard_cash_crypto")
	cashA, hasCashA := envValue("wizard_cash_ashare")
	auto, hasAuto := envValue("wizard_auto")
	conf, hasConf := envValue("wizard_confidence")

	_, err = app.store.Update(func(s *Settings) error {
		if hasProxy {
			s.ProxyURL = proxy
		}
		if hasCashC && cashC != "" {
			v, err := strconv.ParseFloat(cashC, 64)
			if err != nil || v <= 0 {
				return fmt.Errorf("数字货币初始资金无效：%s", cashC)
			}
			s.InitialCash["crypto"] = v
		}
		if hasCashA && cashA != "" {
			v, err := strconv.ParseFloat(cashA, 64)
			if err != nil || v <= 0 {
				return fmt.Errorf("A 股初始资金无效：%s", cashA)
			}
			s.InitialCash["ashare"] = v
		}
		if hasAuto && auto != "" {
			s.Auto.Enabled = isTruthy(auto)
		}
		if hasConf && conf != "" {
			if v, err := strconv.ParseFloat(conf, 64); err == nil && v > 0 && v <= 100 {
				s.Auto.MinConfidence = v / 100
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Println("设置已保存")
	return nil
}

func envValue(key string) (string, bool) {
	v, ok := os.LookupEnv(key)
	return strings.TrimSpace(v), ok
}

func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on", "y":
		return true
	}
	return false
}
