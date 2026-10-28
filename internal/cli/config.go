package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ConfigDirName 是存放本地配置的目录名，位于**当前工作目录**下。
//
// 选择当前目录而不是 os.UserConfigDir()，是为了让工具「跟着项目走」：
// 把整个目录拷走就能连配置一起带走，多份数据各自独立互不干扰。
const ConfigDirName = ".chainrace"

// ConfigFileName 是配置文件名。
const ConfigFileName = "config.json"

// Config 是跨运行持久化的用户设置。
//
// 只存「每次都要重填会很烦」的东西，不存任何可推导的派生值。
type Config struct {
	// APIKey 以明文保存在 ./.chainrace/config.json，权限 0600。
	//
	// 取舍说明：这是一个本地单人工具，把 key 加密再存只是把密钥换个地方放，
	// 并不能真正提升安全性；真正有效的做法是「不落盘」——
	// 因此环境变量 TORN_API_KEY 与 -api 参数的优先级都高于这里，
	// 且 -no-save 可以完全关掉持久化。
	//
	// 注意：该目录已写入 .gitignore。**不要**把它移出忽略列表，
	// 否则 key 会被提交进仓库历史。
	APIKey string `json:"api_key,omitempty"`
	// IntervalSec 是默认统计间隔（秒）。
	IntervalSec int `json:"interval_sec,omitempty"`
	// Metric 是默认指标（respect / hits / both）。
	Metric string `json:"metric,omitempty"`
	// TimeFormat 是默认时间列头格式。
	TimeFormat string `json:"time_format,omitempty"`
	// OutDir 是默认输出目录。
	OutDir string `json:"out_dir,omitempty"`
}

// ConfigPath 返回配置文件的绝对路径：<当前工作目录>/.chainrace/config.json
func ConfigPath() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("无法定位当前工作目录: %w", err)
	}
	return filepath.Join(wd, ConfigDirName, ConfigFileName), nil
}

// LoadConfig 读取配置。文件不存在不是错误 —— 返回空配置即可。
func LoadConfig() (*Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return &Config{}, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return &Config{}, fmt.Errorf("读取配置失败: %w", err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		// 配置损坏不该让工具彻底跑不起来：退回默认值，下次保存时覆盖。
		return &Config{}, fmt.Errorf("配置文件 %s 格式损坏，已忽略: %w", path, err)
	}
	return &c, nil
}

// SaveConfig 写入配置（权限 0600，仅当前用户可读）。
func SaveConfig(c *Config) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	b = append(b, '\n')

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("写入配置失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("提交配置失败: %w", err)
	}
	return nil
}

// MaskKey 把 API key 脱敏成 "625P…BST" 形式，用于日志与界面展示。
//
// 任何情况下都不应把完整 key 打印出来 —— 终端记录、截图、issue 附件
// 都是密钥泄漏的常见途径。
func MaskKey(key string) string {
	if len(key) <= 8 {
		if len(key) <= 4 {
			return "****"
		}
		return key[:2] + "****"
	}
	return key[:4] + "…" + key[len(key)-3:]
}
