package main

import (
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// isPortAvailable 检查端口是否可用
func isPortAvailable(host string, port int) bool {
	address := fmt.Sprintf("%s: %d", host, port)
	conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
	if err != nil {
		return true
	}
	defer conn.Close()
	return false
}

// getAvailablePort 获取可用端口
func getAvailablePort(host string, minPort, maxPort int, usedPorts map[int]bool) (int, error) {
	// 首先尝试随机端口，增加找到可用端口的概率
	for i := 0; i < 100; i++ { // 尝试100次随机端口
		port := rand.Intn(maxPort-minPort+1) + minPort
		if !usedPorts[port] && isPortAvailable(host, port) {
			return port, nil
		}
	}

	// 如果随机失败，尝试顺序扫描
	for port := minPort; port <= maxPort; port++ {
		if !usedPorts[port] && isPortAvailable(host, port) {
			return port, nil
		}
	}

	return 0, fmt.Errorf("没有可用端口在范围 %d-%d 内", minPort, maxPort)
}

// GetCurrPath 获取当前执行文件的路径
func GetCurrPath() string {
	file, _ := exec.LookPath(os.Args[0])
	path, _ := filepath.Abs(file)
	splitstring := strings.Split(path, "\\")
	size := len(splitstring)
	splitstring = strings.Split(path, splitstring[size-1])
	ret := strings.Replace(splitstring[0], "\\", "/", size-1)
	return ret
}
