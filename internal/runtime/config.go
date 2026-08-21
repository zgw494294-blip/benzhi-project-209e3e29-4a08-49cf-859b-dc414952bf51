package runtime

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

const DefaultAddress = "127.0.0.1:19081"

func ResolveAddress(flagValue string, flagExplicit bool, portEnv string) (string, error) {
	address := DefaultAddress
	if flagExplicit {
		address = flagValue
	} else if portEnv != "" {
		address = "127.0.0.1:" + portEnv
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("监听地址格式无效: %w", err)
	}
	if host == "" {
		return "", fmt.Errorf("监听主机不能为空")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1024 || port > 65535 {
		return "", fmt.Errorf("端口必须是 1024 到 65535 的数字")
	}
	if port == 3000 || port == 8080 {
		return "", fmt.Errorf("端口 %d 禁止作为服务端口", port)
	}
	if !flagExplicit && host != "127.0.0.1" {
		return "", fmt.Errorf("默认监听必须使用 127.0.0.1")
	}
	return net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port)), nil
}
