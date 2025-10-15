package main

import (
	"flag"
	"log"
)

var (
	config = &Config{
		Host:           "127.0.0.1",
		Port:           9000,
		WorkerCount:    20,
		MinPort:        9001,
		MaxPort:        65535,
		HealthCheckInt: 30,
		MaxRequests:    100,
		HideWindow:     true,
		MaxRequestTime: 60,
	}
)

func init() {
	flag.StringVar(&config.Host, "host", config.Host, "监听主机")
	flag.IntVar(&config.Port, "port", config.Port, "监听端口")
	flag.IntVar(&config.WorkerCount, "workers", config.WorkerCount, "工作进程数")
	flag.IntVar(&config.MinPort, "min-port", config.MinPort, "工作进程最小可用端口")
	flag.IntVar(&config.MaxPort, "max-port", config.MaxPort, "工作进程最大可用端口")
	flag.IntVar(&config.HealthCheckInt, "check-interval", config.HealthCheckInt, "健康检查间隔(秒)")
	flag.IntVar(&config.MaxRequests, "max-requests", config.MaxRequests, "每个进程最大请求数")
	flag.IntVar(&config.MaxRequestTime, "max-request-time", config.MaxRequestTime, "最大请求处理时间(秒)")
	flag.BoolVar(&config.HideWindow, "hide", config.HideWindow, "隐藏窗口")
}

func main() {
	flag.Parse()

	// 查找 php-cgi.exe
	if config.PHPCGIPath == "" {
		config.PHPCGIPath = findPHPCGI()
	}
	if config.PHPCGIPath == "" {
		log.Fatal("未找到 php-cgi.exe")
	}

	// 查找 php.ini
	config.PHPIniPath = findPHPIni()
	if config.PHPIniPath == "" {
		log.Println("警告: 未找到 php.ini")
	}

	log.Printf("启动 PHP-CGI 管理器: %s:%d", config.Host, config.Port)
	log.Printf("工作进程数: %d, 端口范围: %d-%d",
		config.WorkerCount, config.MinPort, config.MaxPort)
	log.Printf("PHP-CGI 路径: %s", config.PHPCGIPath)

	// 创建管理器
	manager := NewManager(config)

	// 处理退出信号
	go handleSignals(manager)

	// 启动管理器
	if err := manager.Start(); err != nil {
		log.Fatalf("启动失败: %v", err)
	}
}
