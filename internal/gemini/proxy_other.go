//go:build !windows

package gemini

// readRegistryValue 非 Windows 平台没有系统代理注册表，直接返回空。
func readRegistryValue(path, name string) (string, error) {
	return "", nil
}
