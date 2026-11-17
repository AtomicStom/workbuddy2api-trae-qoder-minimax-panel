// device.go —— 读取本机 WorkBuddy 客户端的真实设备标识（用于事件指纹对齐）。
//
// 背景（2026-11-17 实测）：成长任务 skill_share 的判据事件在**真实客户端指纹**下
// 才被计分过一次（当时用的是本机客户端的 machineId + qimei36 + 真实客户端版本号）。
// 面板默认的桌面指纹（machineId 由 uid 派生）已在其它 17 个成长任务上验证可计分，
// 但为了对齐真实客户端，这里提供可选的真实设备标识读取。
//
// 数据来源（客户端本地数据目录）：
//   - %USERPROFILE%\.workbuddy\device-id       → machineId
//   - %USERPROFILE%\.workbuddy\qimei-cache.json → {"qimei36":"…"}
//   - %USERPROFILE%\.workbuddy-ai\device-id     → 国际版客户端的 machineId
//
// 读不到就返回空串（调用方按「用默认指纹」处理），不报错、不阻塞。
package upstream

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// RealDeviceIDs 返回本机客户端的真实 machineId 与 qimei36（可能为空串）。
func RealDeviceIDs() (machineID string, qimei36 string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	for _, dir := range []string{".workbuddy", ".workbuddy-ai"} {
		base := filepath.Join(home, dir)
		if machineID == "" {
			if b, err := os.ReadFile(filepath.Join(base, "device-id")); err == nil {
				machineID = strings.TrimSpace(string(b))
			}
		}
		if qimei36 == "" {
			if b, err := os.ReadFile(filepath.Join(base, "qimei-cache.json")); err == nil {
				var j struct {
					Qimei36 string `json:"qimei36"`
				}
				if json.Unmarshal(b, &j) == nil {
					qimei36 = strings.TrimSpace(j.Qimei36)
				}
			}
		}
	}
	return machineID, qimei36
}
