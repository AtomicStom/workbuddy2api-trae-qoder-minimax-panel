//go:build windows

package gemini

import (
	"golang.org/x/sys/windows/registry"
)

// readRegistryValue 读 HKCU\<path>\<name> 的字符串值。
func readRegistryValue(path, name string) (string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()
	v, _, err := k.GetStringValue(name)
	return v, err
}
